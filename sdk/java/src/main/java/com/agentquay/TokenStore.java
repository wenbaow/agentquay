package com.agentquay;

import com.fasterxml.jackson.databind.ObjectMapper;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.logging.Level;
import java.util.logging.Logger;

/**
 * token 持久化（设计文档 §6.2）：优先 keyring（Windows Credential Manager /
 * macOS Keychain / libsecret，可选依赖），不可用时回退 ~/.agentquay/tokens.json。
 */
public final class TokenStore {

    private static final Logger LOG = Logger.getLogger(TokenStore.class.getName());

    private static final String KEYRING_SERVICE = "agentquay";
    private static final ObjectMapper MAPPER = new ObjectMapper();

    private final String appId;
    private final boolean keyringAvailable;

    public TokenStore(String appId) {
        this.appId = appId;
        this.keyringAvailable = detectKeyring();
    }

    /** 读取已持久化的 token（无则返回 null）。 */
    public String get() {
        if (keyringAvailable) {
            try {
                Object keyring = keyringInstance();
                String token = (String) keyring.getClass()
                        .getMethod("getPassword", String.class, String.class)
                        .invoke(keyring, KEYRING_SERVICE, appId);
                if (token != null && !token.isEmpty()) {
                    return token;
                }
            } catch (Exception e) {
                LOG.log(Level.FINE, "keyring 读取失败，回退文件存储", e);
            }
        }
        return readFile();
    }

    /** 持久化 token。 */
    public void set(String token) {
        if (keyringAvailable) {
            try {
                Object keyring = keyringInstance();
                keyring.getClass()
                        .getMethod("setPassword", String.class, String.class, String.class)
                        .invoke(keyring, KEYRING_SERVICE, appId, token);
                return;
            } catch (Exception e) {
                LOG.log(Level.FINE, "keyring 写入失败，回退文件存储", e);
            }
        }
        writeFile(token);
    }

    // ------------------------------------------------------------------
    // keyring（反射加载可选依赖，避免强绑定）
    // ------------------------------------------------------------------

    private static boolean detectKeyring() {
        try {
            Class.forName("io.github.javakeyring.Keyring");
            return true;
        } catch (ClassNotFoundException e) {
            return false;
        }
    }

    private static Object keyringInstance() throws Exception {
        Class<?> cls = Class.forName("io.github.javakeyring.Keyring");
        Object keyring = cls.getMethod("createDefault").invoke(null);
        if (keyring == null) {
            throw new IllegalStateException("Keyring.createDefault() 返回 null");
        }
        return keyring;
    }

    // ------------------------------------------------------------------
    // 文件回退：~/.agentquay/tokens.json
    // ------------------------------------------------------------------

    private static Path fallbackFile() {
        return Paths.get(System.getProperty("user.home"), ".agentquay", "tokens.json");
    }

    @SuppressWarnings("unchecked")
    private String readFile() {
        try {
            Path path = fallbackFile();
            if (!Files.exists(path)) {
                return null;
            }
            Map<String, Object> data = MAPPER.readValue(path.toFile(), Map.class);
            Object token = data.get(appId);
            return token == null ? null : token.toString();
        } catch (IOException e) {
            return null;
        }
    }

    private void writeFile(String token) {
        try {
            Path path = fallbackFile();
            Files.createDirectories(path.getParent());
            Map<String, Object> data = new LinkedHashMap<>();
            if (Files.exists(path)) {
                try {
                    data.putAll(MAPPER.readValue(path.toFile(), Map.class));
                } catch (IOException ignored) {
                    // 文件损坏则重建
                }
            }
            data.put(appId, token);
            Files.write(path, MAPPER.writerWithDefaultPrettyPrinter()
                    .writeValueAsBytes(data), java.nio.file.StandardOpenOption.CREATE,
                    java.nio.file.StandardOpenOption.TRUNCATE_EXISTING);
            try {
                Files.setPosixFilePermissions(path,
                        java.nio.file.attribute.PosixFilePermissions.fromString("rw-------"));
            } catch (UnsupportedOperationException ignored) {
                // Windows 无 POSIX 权限
            }
        } catch (IOException e) {
            LOG.log(Level.WARNING, "写入 token 文件失败", e);
        }
    }
}
