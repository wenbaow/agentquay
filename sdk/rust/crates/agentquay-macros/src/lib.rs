//! agentquay-macros — `#[agent_tool]` 过程宏（设计文档 §4.8）。
//!
//! 编译期解析函数签名并生成注册代码：Tool 元数据（含 `schemars` 从参数类型
//! 生成的 JSON Schema）与调用分发（同步 / `async fn` 统一处理）。
//!
//! 两种用法：
//!
//! 1. **impl 块模式**（推荐，与 `register_tools(&MusicController)` 语义一致）：
//!
//! ```ignore
//! #[agent_tool]
//! impl MusicController {
//!     #[agent_tool(name = "search", description = "搜索音乐库")]
//!     fn search(&self, keyword: String) -> Vec<Song> { ... }
//! }
//! ```
//!
//! 2. **独立函数模式**：`#[agent_tool(...)] fn search(...)`，生成 `<fn名>_tool`
//!    单元结构体作为注册载体（`client.register_tools(search_tool)`）。
//!
//! 说明：设计文档中的 `client.register_tools(&MusicController)` 在 Rust 中无法
//! 把零散的自由函数与结构体关联，故 impl 块模式按值注册：
//! `client.register_tools(MusicController)`。

use proc_macro::TokenStream;
use proc_macro2::{Span, TokenStream as TokenStream2};
use quote::{format_ident, quote};
use syn::{
    parse::Parser, Attribute, FnArg, GenericArgument, ImplItem, ItemFn, ItemImpl, Lit, Meta,
    PatType, PathArguments, ReturnType, Signature, Type,
};

/// `#[agent_tool]` 属性宏：解析 fn / impl，编译期生成注册代码。
#[proc_macro_attribute]
pub fn agent_tool(attr: TokenStream, item: TokenStream) -> TokenStream {
    let attr = TokenStream2::from(attr);
    let item = TokenStream2::from(item);

    if let Ok(impl_block) = syn::parse2::<ItemImpl>(item.clone()) {
        return expand_impl(attr, impl_block).into();
    }
    if let Ok(item_fn) = syn::parse2::<ItemFn>(item.clone()) {
        return expand_fn(attr, item_fn).into();
    }
    syn::Error::new(Span::call_site(), "#[agent_tool] 只能用于 fn 或 impl 块")
        .to_compile_error()
        .into()
}

// ---------------------------------------------------------------------------
// 属性解析
// ---------------------------------------------------------------------------

/// 单个 Tool 的配置（由 `#[agent_tool(...)]` 解析得到）。
#[derive(Clone)]
struct ToolSpec {
    name: Option<String>,
    description: Option<String>,
    requires_confirmation: bool,
    timeout_seconds: u32,
    confirm_timeout_seconds: u32,
}

impl Default for ToolSpec {
    fn default() -> Self {
        Self {
            name: None,
            description: None,
            requires_confirmation: false,
            timeout_seconds: 30,
            confirm_timeout_seconds: 120,
        }
    }
}

/// 解析 `#[agent_tool(name = "...", description = "...", requires_confirmation = true, ...)]`。
fn parse_tool_spec(attr: &Attribute) -> syn::Result<ToolSpec> {
    let meta = attr.meta.require_list()?;
    parse_tool_spec_tokens(meta.tokens.clone())
}

/// 解析括号内的参数 token（自由函数模式直接使用，无需构造 Attribute）。
fn parse_tool_spec_tokens(tokens: TokenStream2) -> syn::Result<ToolSpec> {
    let mut spec = ToolSpec::default();
    let parser = syn::meta::parser(|meta| {
        if meta.path.is_ident("name") {
            spec.name = Some(meta.value()?.parse::<syn::LitStr>()?.value());
        } else if meta.path.is_ident("description") {
            spec.description = Some(meta.value()?.parse::<syn::LitStr>()?.value());
        } else if meta.path.is_ident("requires_confirmation") {
            spec.requires_confirmation = if meta.input.peek(syn::Token![=]) {
                meta.value()?.parse::<syn::LitBool>()?.value
            } else {
                true // 裸标记 `requires_confirmation` 等价于 true
            };
        } else if meta.path.is_ident("timeout_seconds") {
            spec.timeout_seconds = parse_u32(&meta)?;
        } else if meta.path.is_ident("confirm_timeout_seconds") {
            spec.confirm_timeout_seconds = parse_u32(&meta)?;
        } else {
            return Err(meta.error(format_args!(
                "未知参数 `{}`（支持 name / description / requires_confirmation / \
                 timeout_seconds / confirm_timeout_seconds）",
                meta.path.get_ident().map(|i| i.to_string()).unwrap_or_default()
            )));
        }
        Ok(())
    });
    parser.parse2(tokens)?;
    Ok(spec)
}

fn parse_u32(meta: &syn::meta::ParseNestedMeta<'_>) -> syn::Result<u32> {
    let lit: syn::LitInt = meta.value()?.parse()?;
    lit.base10_parse::<u32>()
}

/// 从函数的 `#[param(...)]` 参数属性解析（description / required）。
struct ParamAttr {
    description: Option<String>,
    required: Option<bool>,
}

fn parse_param_attr(attr: &Attribute) -> syn::Result<ParamAttr> {
    let mut out = ParamAttr { description: None, required: None };
    attr.parse_nested_meta(|meta| {
        if meta.path.is_ident("description") {
            out.description = Some(meta.value()?.parse::<syn::LitStr>()?.value());
        } else if meta.path.is_ident("required") {
            out.required = Some(meta.value()?.parse::<syn::LitBool>()?.value);
        } else {
            return Err(meta.error("未知参数 `param` 属性（支持 description / required）"));
        }
        Ok(())
    })?;
    Ok(out)
}

// ---------------------------------------------------------------------------
// 函数分析
// ---------------------------------------------------------------------------

/// 返回值形态：决定错误如何映射。
#[derive(Clone, Copy)]
enum ReturnKind {
    /// 普通返回值（任意 Serialize 类型；`()` → null）。
    Plain,
    /// `Result<T, E>`：E 为 agentquay::ToolError 时透传 code/details。
    ToolResult,
    /// `Result<T, E>`：其他错误按 Display 消息包装为 EXECUTION_ERROR。
    PlainResult,
}

/// 单个参数信息。
#[derive(Clone)]
struct ParamInfo {
    /// 参数名（JSON 属性名）。
    name: String,
    /// 参数类型（生成 Args 结构体字段与 schema）。
    ty: Type,
    /// 是否必填（schema required 列表；Option<T> 默认可选）。
    required: bool,
    /// `#[param(description = ...)]`。
    description: Option<String>,
}

/// 分析函数签名（方法或自由函数）。
#[derive(Clone)]
struct FnInfo {
    params: Vec<ParamInfo>,
    return_kind: ReturnKind,
    is_async: bool,
}

fn analyze_fn(sig: &Signature, fn_name: &str) -> syn::Result<FnInfo> {
    let mut params = Vec::new();
    for input in &sig.inputs {
        let FnArg::Typed(PatType { attrs, pat, ty, .. }) = input else {
            // 接收者：仅允许 `&self`（invoke 持有 Arc<Self>，无法调用 self / &mut self）
            let FnArg::Receiver(recv) = input else {
                return Err(syn::Error::new_spanned(
                    input,
                    "不支持的参数形式",
                ));
            };
            if recv.reference.is_none() {
                return Err(syn::Error::new_spanned(
                    recv,
                    "不支持 self 接收者（Agent 工具方法需声明为 `&self`）",
                ));
            }
            if recv.mutability.is_some() {
                return Err(syn::Error::new_spanned(
                    recv,
                    "不支持 &mut self 接收者（Agent 工具方法需声明为 `&self`）",
                ));
            }
            continue; // &self：跳过
        };
        // 参数名（仅支持普通标识符）
        let name = match pat.as_ref() {
            syn::Pat::Ident(ident) => ident.ident.to_string(),
            _ => {
                return Err(syn::Error::new_spanned(
                    pat,
                    "不支持的模式参数（仅支持 `name: Type` 形式的参数）",
                ))
            }
        };
        // 解析 #[param(...)] 属性
        let mut description = None;
        let mut required_override = None;
        for attr in attrs {
            if attr.path().is_ident("param") {
                let pa = parse_param_attr(attr)?;
                description = pa.description;
                required_override = pa.required;
            }
        }
        let is_option = type_is_option(ty.as_ref());
        let required = match required_override {
            Some(r) => {
                if !r && !is_option {
                    return Err(syn::Error::new_spanned(
                        ty,
                        format!(
                            "参数 `{name}` 声明为 required = false，但类型不是 Option<T>。\
                             可选参数必须声明为 `Option<T>` 类型",
                        ),
                    ));
                }
                r
            }
            None => !is_option,
        };
        params.push(ParamInfo { name, ty: ty.as_ref().clone(), required, description });
    }

    let return_kind = return_kind(&sig.output, fn_name)?;
    Ok(FnInfo { params, return_kind, is_async: sig.asyncness.is_some() })
}

/// 判断类型是否为 `Option<...>`（含 `std::option::Option<...>` 等路径）。
fn type_is_option(ty: &Type) -> bool {
    match ty {
        Type::Path(tp) => tp
            .path
            .segments
            .last()
            .map(|seg| seg.ident == "Option")
            .unwrap_or(false),
        _ => false,
    }
}

/// 判断返回值形态。
fn return_kind(ret: &ReturnType, fn_name: &str) -> syn::Result<ReturnKind> {
    let ReturnType::Type(_, ty) = ret else {
        return Ok(ReturnKind::Plain);
    };
    let Type::Path(tp) = ty.as_ref() else {
        return Ok(ReturnKind::Plain);
    };
    let Some(last) = tp.path.segments.last() else {
        return Ok(ReturnKind::Plain);
    };
    if last.ident != "Result" {
        return Ok(ReturnKind::Plain);
    }
    let PathArguments::AngleBracketed(args) = &last.arguments else {
        return Ok(ReturnKind::Plain);
    };
    if args.args.len() != 2 {
        return Err(syn::Error::new_spanned(
            ty,
            format!("`{fn_name}` 的 Result 缺少类型参数（应为 Result<T, E>）"),
        ));
    }
    let is_tool_error = match &args.args[1] {
        GenericArgument::Type(Type::Path(etp)) => etp
            .path
            .segments
            .last()
            .map(|seg| seg.ident == "ToolError")
            .unwrap_or(false),
        _ => false,
    };
    Ok(if is_tool_error { ReturnKind::ToolResult } else { ReturnKind::PlainResult })
}

/// 从 doc 注释（`/// ...`）取首行作为默认 description（与 Python SDK 的 docstring 回退一致）。
fn doc_first_line(attrs: &[Attribute]) -> Option<String> {
    for attr in attrs {
        if attr.path().is_ident("doc") {
            if let Meta::NameValue(nv) = &attr.meta {
                if let syn::Expr::Lit(expr_lit) = &nv.value {
                    if let Lit::Str(s) = &expr_lit.lit {
                        let value = s.value();
                        let first = value.lines().next().unwrap_or("").trim().to_string();
                        if !first.is_empty() {
                            return Some(first);
                        }
                    }
                }
            }
        }
    }
    None
}

/// 移除参数上的 `#[param(...)]` 属性（输出原函数时使用）。
fn strip_param_attrs(attrs: &mut Vec<Attribute>) {
    attrs.retain(|a| !a.path().is_ident("param"));
}

// ---------------------------------------------------------------------------
// 代码生成
// ---------------------------------------------------------------------------

/// 返回值包装表达式（在生成代码中引用 `__aq`）。
fn wrap_expr(kind: ReturnKind) -> TokenStream2 {
    match kind {
        ReturnKind::Plain => quote! { __aq::serialize(__r) },
        ReturnKind::ToolResult => quote! { __aq::finish_tool_result(__r) },
        ReturnKind::PlainResult => quote! { __aq::finish_result(__r) },
    }
}

/// 生成一个 Tool 的 invoke 分支。
///
/// `call_expr`：调用表达式模板（已含目标与函数名，参数由生成代码追加），
/// 如 `__aq_this.search`（方法）或 `search`（自由函数）。
///
/// `need_self = true` 时在分支开头注入 `let __aq_this = self.clone();`
/// （impl 模式：同步路径 spawn_blocking 与 async 路径都持有 Arc<Self>）。
fn invoke_arm(
    spec: &ToolSpec,
    fn_name: &str,
    info: &FnInfo,
    call_expr: TokenStream2,
    need_self: bool,
) -> TokenStream2 {
    let tool_name = spec.name.clone().unwrap_or_else(|| fn_name.to_string());
    let field_idents: Vec<_> = info.params.iter().map(|p| format_ident!("{}", p.name)).collect();
    let field_tys: Vec<_> = info.params.iter().map(|p| &p.ty).collect();

    // Args 反序列化结构体（按参数名绑定 JSON arguments，未知字段忽略）
    let args_struct = quote! {
        #[derive(::agentquay::serde::Deserialize)]
        #[allow(non_snake_case, dead_code)]
        struct __AgentQuayArgs {
            #( #field_idents: #field_tys, )*
        }
    };
    let deserialize = quote! {
        let __args: __AgentQuayArgs = ::agentquay::serde_json::from_value(args)
            .map_err(::agentquay::ToolError::invalid_arguments)?;
    };

    let wrap = wrap_expr(info.return_kind);
    let call_args: Vec<_> = info
        .params
        .iter()
        .map(|p| {
            let id = format_ident!("{}", p.name);
            quote! { __args.#id }
        })
        .collect();
    let call = quote! { #call_expr(#(#call_args),*) };

    // 同步方法：spawn_blocking 执行（不阻塞异步运行时；panic 由 JoinError 捕获）
    let sync_body = quote! {
        let __aq_result = ::agentquay::tokio::task::spawn_blocking(move || {
            let __r = #call;
            #wrap
        })
        .await
        .map_err(|e| ::agentquay::ToolError::execution_error(format!("tool 执行 panic: {e}")))?;
        ::std::result::Result::Ok(__aq_result?)
    };
    // async 方法：直接 await（panic 由客户端 CatchUnwind 兜底）
    let async_body = quote! {
        let __r = #call.await;
        ::std::result::Result::Ok(#wrap?)
    };
    let body = if info.is_async { async_body } else { sync_body };

    let self_stmt = if need_self {
        quote! { let __aq_this = self.clone(); }
    } else {
        TokenStream2::new()
    };

    quote! {
        #tool_name => {
            #self_stmt
            #args_struct
            #deserialize
            #body
        }
    }
}

/// 生成 ToolProvider 的 tools() 实现（元数据 + schemars schema）。
fn tools_impl(tools: &[(ToolSpec, String, FnInfo)]) -> TokenStream2 {
    let per_tool = tools.iter().map(|(spec, fn_name, info)| {
        let tool_name = spec.name.clone().unwrap_or_else(|| fn_name.to_string());
        let description = spec.description.clone().unwrap_or_default();
        let requires_confirmation = spec.requires_confirmation;
        let timeout_seconds = spec.timeout_seconds;
        let confirm_timeout_seconds = spec.confirm_timeout_seconds;
        // 每个参数 schema 先绑定到局部变量（避免对 __gen 的双重可变借用）
        let bindings = info.params.iter().enumerate().map(|(i, p)| {
            let bind = format_ident!("__aq_param_{i}");
            let name = &p.name;
            let ty = &p.ty;
            let required = p.required;
            // 注意：quote! 对 Option::None 的插值会输出空 token，必须显式写 None
            let desc = match &p.description {
                Some(d) => quote! { ::std::option::Option::Some(#d) },
                None => quote! { ::std::option::Option::None },
            };
            // subschema_for：复杂类型进 definitions 并返回 $ref（与嵌套类型去重一致）
            quote! {
                let #bind = __aq::param(#name, __aq::subschema_for::<#ty>(&mut __gen), #required, #desc);
            }
        });
        let param_ids: Vec<_> = (0..info.params.len())
            .map(|i| format_ident!("__aq_param_{i}"))
            .collect();
        quote! {
            __tools.push(::agentquay::ToolMetadata::new(
                #tool_name,
                #description,
                {
                    #( #bindings )*
                    __aq::param_schema(&mut __gen, &[ #(#param_ids),* ])
                },
                #requires_confirmation,
                #timeout_seconds,
                #confirm_timeout_seconds,
            ));
        }
    });
    quote! {
        fn tools(&self) -> ::std::vec::Vec<::agentquay::ToolMetadata> {
            use ::agentquay::__private as __aq;
            let mut __gen = __aq::schema_generator();
            let mut __tools: ::std::vec::Vec<::agentquay::ToolMetadata> = ::std::vec::Vec::new();
            #( #per_tool )*
            __tools
        }
    }
}

/// 生成 ToolProvider 的 invoke() 实现。
fn invoke_impl(arms: Vec<TokenStream2>) -> TokenStream2 {
    quote! {
        fn invoke(
            self: ::std::sync::Arc<Self>,
            tool: &str,
            args: ::agentquay::serde_json::Value,
        ) -> ::std::pin::Pin<Box<dyn ::std::future::Future<Output = ::std::result::Result<::agentquay::serde_json::Value, ::agentquay::ToolError>> + Send + 'static>> {
            let __tool = tool.to_string();
            Box::pin(async move {
                use ::agentquay::__private as __aq;
                match __tool.as_str() {
                    #( #arms )*
                    _ => ::std::result::Result::Err(::agentquay::ToolError::not_found(__tool.clone())),
                }
            })
        }
    }
}

/// 生成 `impl ToolProvider for #self_ty`。
///
/// `is_impl = true`：方法通过 `__aq_this.#fn(...)` 调用（`Arc<Self>` 自动解引用）；
/// `is_impl = false`：自由函数直接 `#fn(...)` 调用。
fn build_provider(self_ty: &syn::Type, tools: &[(ToolSpec, String, FnInfo)], is_impl: bool) -> TokenStream2 {
    let tools_impl = tools_impl(tools);
    let arms: Vec<_> = tools
        .iter()
        .map(|(spec, fn_name, info)| {
            let fn_ident = format_ident!("{}", fn_name);
            let call = if is_impl {
                quote! { __aq_this.#fn_ident }
            } else {
                quote! { #fn_ident }
            };
            invoke_arm(spec, fn_name, info, call, is_impl)
        })
        .collect();
    let invoke_impl = invoke_impl(arms);
    quote! {
        #[automatically_derived]
        impl ::agentquay::ToolProvider for #self_ty {
            #tools_impl
            #invoke_impl
        }
    }
}

// ---------------------------------------------------------------------------
// impl 块模式
// ---------------------------------------------------------------------------

/// `#[agent_tool] impl Foo { #[agent_tool(...)] fn ... }` →
/// 原 impl + `impl ToolProvider for Foo`。
fn expand_impl(attr: TokenStream2, mut impl_block: ItemImpl) -> TokenStream2 {
    if impl_block.trait_.is_some() {
        return err_impl("#[agent_tool] 不支持 trait impl 块（请标记普通 impl 块）");
    }
    if !impl_block.generics.params.is_empty() || impl_block.generics.where_clause.is_some() {
        return err_impl("#[agent_tool] 暂不支持泛型 / where 约束的 impl 块");
    }
    if !attr.is_empty() {
        return err_impl("impl 块上的 #[agent_tool] 不需要参数（参数写在方法上）");
    }

    let mut tools: Vec<(ToolSpec, String, FnInfo)> = Vec::new();
    let mut errors: Vec<syn::Error> = Vec::new();

    for item in &mut impl_block.items {
        let ImplItem::Fn(method) = item else { continue };
        let Some(idx) = method.attrs.iter().position(|a| a.path().is_ident("agent_tool")) else {
            continue;
        };
        let attr = method.attrs.remove(idx);
        let fn_name = method.sig.ident.to_string();
        match parse_tool_spec(&attr).and_then(|spec| {
            analyze_fn(&method.sig, &fn_name).map(|info| (spec, fn_name, info))
        }) {
            Ok(mut entry) => {
                // description 缺省回退到 doc 注释首行
                if entry.0.description.is_none() {
                    entry.0.description = doc_first_line(&method.attrs);
                }
                tools.push(entry);
            }
            Err(e) => errors.push(e),
        }
        // 参数上的 #[param(...)] 属性从原函数移除
        for input in &mut method.sig.inputs {
            if let FnArg::Typed(pat_type) = input {
                strip_param_attrs(&mut pat_type.attrs);
            }
        }
    }

    if let Some(e) = errors.pop() {
        return e.to_compile_error();
    }
    if tools.is_empty() {
        return err_impl("impl 块内没有找到标记 #[agent_tool(...)] 的方法");
    }

    let self_ty = impl_block.self_ty.clone();
    let provider = build_provider(&self_ty, &tools, true);
    quote! {
        #impl_block
        #provider
    }
}

// ---------------------------------------------------------------------------
// 自由函数模式
// ---------------------------------------------------------------------------

/// `#[agent_tool(...)] fn search(...)` → 原函数 + `search_tool` 注册载体。
fn expand_fn(attr: TokenStream2, mut item_fn: ItemFn) -> TokenStream2 {
    // 方法（带接收者）在非 #[agent_tool] impl 中使用 → 报错并给出指引
    if item_fn.sig.receiver().is_some() {
        return err_impl(
            "#[agent_tool] 方法需要放在 `#[agent_tool] impl` 块内（如 \
             `#[agent_tool] impl MusicController { #[agent_tool(...)] fn search(&self, ...) {} }`）",
        );
    }

    let fn_name = item_fn.sig.ident.to_string();
    let mut spec = match parse_tool_spec_tokens(attr) {
        Ok(spec) => spec,
        Err(e) => return e.to_compile_error(),
    };
    if spec.description.is_none() {
        spec.description = doc_first_line(&item_fn.attrs);
    }
    let info = match analyze_fn(&item_fn.sig, &fn_name) {
        Ok(info) => info,
        Err(e) => return e.to_compile_error(),
    };

    // 移除参数上的 #[param(...)] 属性
    for input in &mut item_fn.sig.inputs {
        if let FnArg::Typed(pat_type) = input {
            strip_param_attrs(&mut pat_type.attrs);
        }
    }

    let wrapper = format_ident!("{}_tool", fn_name);
    let tools_impl = tools_impl(&[(spec.clone(), fn_name.clone(), info.clone())]);
    let fn_ident = format_ident!("{}", fn_name);
    let arm = invoke_arm(&spec, &fn_name, &info, quote! { #fn_ident }, false);
    let invoke_impl = invoke_impl(vec![arm]);
    let tool_doc = format!("由 #[agent_tool] 生成的 Tool 注册载体（`fn {fn_name}` → `{wrapper}`）");

    quote! {
        #item_fn

        #[doc = #tool_doc]
        #[allow(non_camel_case_types, dead_code)]
        pub struct #wrapper;

        #[automatically_derived]
        impl ::agentquay::ToolProvider for #wrapper {
            #tools_impl
            #invoke_impl
        }
    }
}

fn err_impl(message: &str) -> TokenStream2 {
    syn::Error::new(Span::call_site(), message).to_compile_error()
}
