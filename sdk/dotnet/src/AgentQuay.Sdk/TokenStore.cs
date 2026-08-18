using System.Runtime.InteropServices;
using System.Text.Json;

namespace AgentQuay;

/// <summary>
/// token 持久化（设计文档 §6.2）：优先 Windows Credential Manager
/// （advapi32 CredRead/CredWrite，零依赖），不可用时回退 ~/.agentquay/tokens.json。
/// macOS / Linux 当前使用文件回退。
/// </summary>
public sealed class TokenStore
{
    private const string CredentialTarget = "AgentQuay";

    private readonly string _appId;
    private readonly string _fallbackFile;

    public TokenStore(string appId)
    {
        _appId = appId;
        _fallbackFile = Path.Combine(
            Environment.GetFolderPath(Environment.SpecialFolder.UserProfile),
            ".agentquay", "tokens.json");
    }

    /// <summary>读取已持久化的 token（无则返回 null）。</summary>
    public string? Get()
    {
        if (OperatingSystem.IsWindows())
        {
            try
            {
                if (CredRead(CredentialTarget + "/" + _appId, 1 /* CRED_TYPE_GENERIC */, 0, out IntPtr credPtr)
                    && credPtr != IntPtr.Zero)
                {
                    try
                    {
                        var cred = Marshal.PtrToStructure<CREDENTIAL>(credPtr);
                        if (cred.CredentialBlob != IntPtr.Zero && cred.CredentialBlobSize > 0)
                        {
                            var bytes = new byte[cred.CredentialBlobSize];
                            Marshal.Copy(cred.CredentialBlob, bytes, 0, bytes.Length);
                            string? token = System.Text.Encoding.UTF8.GetString(bytes).TrimEnd('\0');
                            if (!string.IsNullOrEmpty(token))
                            {
                                return token;
                            }
                        }
                    }
                    finally
                    {
                        CredFree(credPtr);
                    }
                }
            }
            catch (Exception)
            {
                // 凭据管理器不可用（如非交互会话）→ 文件回退
            }
        }
        return ReadFile();
    }

    /// <summary>持久化 token。</summary>
    public void Set(string token)
    {
        if (OperatingSystem.IsWindows())
        {
            try
            {
                var bytes = System.Text.Encoding.UTF8.GetBytes(token + "\0");
                var cred = new CREDENTIAL
                {
                    Type = 1, // CRED_TYPE_GENERIC
                    TargetName = CredentialTarget + "/" + _appId,
                    CredentialBlob = Marshal.AllocHGlobal(bytes.Length),
                    CredentialBlobSize = (uint)bytes.Length,
                    Persist = 2, // CRED_PERSIST_LOCAL_MACHINE
                    UserName = _appId,
                };
                try
                {
                    Marshal.Copy(bytes, 0, cred.CredentialBlob, bytes.Length);
                    if (CredWrite(ref cred, 0))
                    {
                        return;
                    }
                }
                finally
                {
                    Marshal.FreeHGlobal(cred.CredentialBlob);
                }
            }
            catch (Exception)
            {
                // 写失败 → 文件回退
            }
        }
        WriteFile(token);
    }

    // ------------------------------------------------------------------
    // Windows Credential Manager（advapi32）
    // ------------------------------------------------------------------

    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct CREDENTIAL
    {
        public uint Flags;
        public int Type;
        public string TargetName;
        public string? Comment;
        public System.Runtime.InteropServices.ComTypes.FILETIME LastWritten;
        public uint CredentialBlobSize;
        public IntPtr CredentialBlob;
        public uint Persist;
        public uint AttributeCount;
        public IntPtr Attributes;
        public string? TargetAlias;
        public string UserName;
    }

    [DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool CredRead(string target, int type, int reservedFlag, out IntPtr credential);

    [DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool CredWrite(ref CREDENTIAL credential, uint flags);

    [DllImport("advapi32.dll", SetLastError = true)]
    private static extern void CredFree(IntPtr buffer);

    // ------------------------------------------------------------------
    // 文件回退：~/.agentquay/tokens.json
    // ------------------------------------------------------------------

    private string? ReadFile()
    {
        try
        {
            if (!File.Exists(_fallbackFile))
            {
                return null;
            }
            using var doc = JsonDocument.Parse(File.ReadAllText(_fallbackFile));
            if (doc.RootElement.TryGetProperty(_appId, out var token) && token.ValueKind == System.Text.Json.JsonValueKind.String)
            {
                return token.GetString();
            }
            return null;
        }
        catch (Exception)
        {
            return null;
        }
    }

    private void WriteFile(string token)
    {
        try
        {
            var dir = Path.GetDirectoryName(_fallbackFile)!;
            Directory.CreateDirectory(dir);
            Dictionary<string, string> data;
            if (File.Exists(_fallbackFile))
            {
                try
                {
                    data = JsonSerializer.Deserialize<Dictionary<string, string>>(File.ReadAllText(_fallbackFile))
                           ?? new Dictionary<string, string>();
                }
                catch (Exception)
                {
                    data = new Dictionary<string, string>(); // 文件损坏则重建
                }
            }
            else
            {
                data = new Dictionary<string, string>();
            }
            data[_appId] = token;
            File.WriteAllText(_fallbackFile, JsonSerializer.Serialize(data, new System.Text.Json.JsonSerializerOptions { WriteIndented = true }));
            if (!OperatingSystem.IsWindows())
            {
                try
                {
                    File.SetUnixFileMode(_fallbackFile, UnixFileMode.UserRead | UnixFileMode.UserWrite);
                }
                catch (Exception)
                {
                    // 非 POSIX 文件系统忽略
                }
            }
        }
        catch (Exception)
        {
            // token 写入失败不致命：下次注册会重新分配
        }
    }
}