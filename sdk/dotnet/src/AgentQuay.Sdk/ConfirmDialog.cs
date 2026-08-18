using System.Reflection;

namespace AgentQuay;

/// <summary>
/// 默认确认对话框：Windows 桌面宿主优先弹原生观感对话框（System.Windows.Forms，反射加载、
/// 零编译期依赖），带超时自动取消；无图形环境 / 加载失败时回退控制台输入（同样带超时）。
/// 桌面应用（WPF / WinForms）可通过 <see cref="AgentQuayClient.ConnectAsync"/> 的
/// confirmationHandler 参数自定义确认 UI。
/// </summary>
public static class ConfirmDialog
{
    /// <summary>询问用户（阻塞当前线程直到决策或超时）。</summary>
    public static bool Ask(string message, IReadOnlyDictionary<string, object?>? arguments, int timeoutSeconds)
    {
        string full = Format(message, arguments);

        if (Environment.UserInteractive)
        {
            try
            {
                if (WinFormsAsk(full, timeoutSeconds, out bool result))
                {
                    return result;
                }
            }
            catch (Exception)
            {
                // 弹窗失败 → 控制台回退
            }
        }
        return ConsoleAsk(full, timeoutSeconds);
    }

    // ------------------------------------------------------------------

    private static string Format(string message, IReadOnlyDictionary<string, object?>? arguments)
    {
        if (arguments == null || arguments.Count == 0)
        {
            return message;
        }
        string args = string.Join(", ", arguments.Select(kv => $"{kv.Key}: {kv.Value ?? "null"}"));
        return message + "\n\n参数: {" + args + "}";
    }

    /// <summary>
    /// 反射驱动的 WinForms 对话框（专用 STA 线程 + 独立消息泵，从任意调用上下文可用）。
    /// </summary>
    private static bool WinFormsAsk(string message, int timeoutSeconds, out bool result)
    {
        result = false;
        if (!OperatingSystem.IsWindows())
        {
            return false; // WinForms 仅 Windows
        }
        Assembly forms;
        try
        {
            forms = Assembly.Load("System.Windows.Forms");
        }
        catch (Exception)
        {
            return false; // 当前运行时无 WinForms（如纯控制台/MAUI）→ 控制台回退
        }
        if (forms.GetType("System.Windows.Forms.Form") == null)
        {
            return false;
        }

        bool? dialogResult = null;
        Exception? threadError = null;
        var done = new ManualResetEventSlim(false);

        var thread = new Thread(() =>
        {
            try
            {
                dialogResult = ShowDialogOnSta(forms, message, timeoutSeconds);
            }
            catch (Exception e)
            {
                threadError = e;
            }
            finally
            {
                done.Set();
            }
        });
        thread.SetApartmentState(ApartmentState.STA);
        thread.IsBackground = true;
        thread.Start();
        done.Wait();
        if (threadError != null)
        {
            throw threadError;
        }
        result = dialogResult == true;
        return true;
    }

    private static bool ShowDialogOnSta(Assembly forms, string message, int timeoutSeconds)
    {
        Type formType = forms.GetType("System.Windows.Forms.Form", throwOnError: true)!;
        Type labelType = forms.GetType("System.Windows.Forms.Label", throwOnError: true)!;
        Type buttonType = forms.GetType("System.Windows.Forms.Button", throwOnError: true)!;
        Type dialogResultType = forms.GetType("System.Windows.Forms.DialogResult", throwOnError: true)!;
        object ok = Enum.Parse(dialogResultType, "OK");

        object form = Activator.CreateInstance(formType)!;
        try
        {
            SetProperty(form, "Text", "AgentQuay 确认");
            SetProperty(form, "StartPosition", "CenterScreen");
            SetProperty(form, "FormBorderStyle", "FixedDialog");
            SetProperty(form, "MaximizeBox", false);
            SetProperty(form, "MinimizeBox", false);
            SetProperty(form, "Width", 480);
            SetProperty(form, "Height", 220);

            object label = Activator.CreateInstance(labelType)!;
            SetProperty(label, "Text", message);
            SetProperty(label, "Width", 440);
            SetProperty(label, "Height", 110);
            SetProperty(label, "Left", 20);
            SetProperty(label, "Top", 20);
            SetProperty(label, "AutoSize", false);
            Invoke(form, "Controls.Add", label);

            object confirm = Activator.CreateInstance(buttonType)!;
            SetProperty(confirm, "Text", "确认执行");
            SetProperty(confirm, "Left", 260);
            SetProperty(confirm, "Top", 150);
            SetProperty(confirm, "Width", 100);
            AddClick(confirm, () => SetProperty(form, "DialogResult", ok));

            object cancel = Activator.CreateInstance(buttonType)!;
            SetProperty(cancel, "Text", "取消");
            SetProperty(cancel, "Left", 370);
            SetProperty(cancel, "Top", 150);
            SetProperty(cancel, "Width", 90);
            SetProperty(form, "CancelButton", cancel);
            AddClick(cancel, () => SetProperty(form, "DialogResult", "Cancel"));

            Invoke(form, "Controls.Add", confirm);
            Invoke(form, "Controls.Add", cancel);

            // 超时自动取消（确认超时视为取消 → confirm_result(false) → Bridge 返回 -32005）
            if (timeoutSeconds > 0)
            {
                object timer = Activator.CreateInstance(
                    forms.GetType("System.Windows.Forms.Timer", throwOnError: true)!)!;
                SetProperty(timer, "Interval", timeoutSeconds * 1000);
                AddTick(timer, () => SetProperty(form, "DialogResult", "Cancel"));
                SetProperty(timer, "Enabled", true);
            }

            object? shown = Invoke(form, "ShowDialog");
            return shown?.ToString() == ok.ToString();
        }
        finally
        {
            Invoke(form, "Dispose");
        }
    }

    // ------------------------------------------------------------------
    // 反射辅助
    // ------------------------------------------------------------------

    private static void SetProperty(object target, string name, object value)
    {
        var prop = target.GetType().GetProperty(name)
                   ?? throw new InvalidOperationException($"属性不存在: {target.GetType().Name}.{name}");
        if (value is string enumName && prop.PropertyType.IsEnum)
        {
            value = Enum.Parse(prop.PropertyType, enumName);
        }
        prop.SetValue(target, value);
    }

    private static object? Invoke(object target, string name, params object?[] args)
    {
        var method = target.GetType().GetMethod(name)
                     ?? throw new InvalidOperationException($"方法不存在: {target.GetType().Name}.{name}");
        return method.Invoke(target, args);
    }

    private static void AddClick(object button, Action action)
    {
        var evt = button.GetType().GetEvent("Click")
                  ?? throw new InvalidOperationException("Button 无 Click 事件");
        var handler = Delegate.CreateDelegate(evt.EventHandlerType!, action.Target, action.Method);
        evt.AddEventHandler(button, handler);
    }

    private static void AddTick(object timer, Action action)
    {
        var evt = timer.GetType().GetEvent("Tick")
                  ?? throw new InvalidOperationException("Timer 无 Tick 事件");
        var handler = Delegate.CreateDelegate(evt.EventHandlerType!, action.Target, action.Method);
        evt.AddEventHandler(timer, handler);
    }

    // ------------------------------------------------------------------
    // 控制台回退（带超时）
    // ------------------------------------------------------------------

    private static bool ConsoleAsk(string message, int timeoutSeconds)
    {
        Console.WriteLine(message);
        var read = Task.Run(() => Console.ReadLine()?.Trim().ToLowerInvariant());
        if (timeoutSeconds > 0 && Task.WaitAny(read, Task.Delay(timeoutSeconds * 1000)) == 1)
        {
            Console.WriteLine("(确认超时，视为取消)");
            return false;
        }
        string? line = read.GetAwaiter().GetResult();
        return line is "y" or "yes";
    }
}