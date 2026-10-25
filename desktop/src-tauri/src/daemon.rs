//! Finding, reaching, and starting the HarmFlow daemon.
//!
//! The daemon writes its address to a runtime file only the user can read and keeps the API token in the
//! Keychain. The desktop reads both and holds the token here, in the Rust core, so it never reaches the webview.

use std::ffi::OsStr;
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::sync::Mutex;
use std::time::Duration;

use serde::{Deserialize, Serialize};
use serde_json::Value;

const KEYCHAIN_SERVICE: &str = "com.harmflow.local-api";
const KEYCHAIN_ACCOUNT: &str = "token";
const DAEMON_BINARY: &str = "harmflowd";

/// `$HARMFLOW_HOME`, or `~/Library/Application Support/HarmFlow`.
pub fn harmflow_home() -> PathBuf {
    if let Some(home) = std::env::var_os("HARMFLOW_HOME").filter(|v| !v.is_empty()) {
        return PathBuf::from(home);
    }
    dirs::home_dir()
        .unwrap_or_else(|| PathBuf::from("/"))
        .join("Library")
        .join("Application Support")
        .join("HarmFlow")
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Runtime {
    pub address: String,
    /// "keychain" or "file"; the pid, version, and start time it also holds are read from GET /health instead.
    #[serde(default)]
    pub token_store: Option<String>,
}

#[derive(Debug, Clone)]
pub struct Connection {
    pub runtime: Runtime,
    pub token: String,
}

impl Connection {
    pub fn base_url(&self) -> String {
        format!("http://{}", self.runtime.address)
    }
}

fn read_runtime(home: &Path) -> Result<Runtime, String> {
    let path = home.join("run").join("daemon.json");
    let text = std::fs::read_to_string(&path).map_err(|e| format!("{}: {e}", path.display()))?;
    serde_json::from_str(&text).map_err(|e| format!("{}: {e}", path.display()))
}

fn read_token(home: &Path, store: Option<&str>) -> Result<String, String> {
    if store == Some("file") {
        let path = home.join("run").join("token");
        return std::fs::read_to_string(&path)
            .map(|t| t.trim().to_string())
            .map_err(|e| format!("{}: {e}", path.display()));
    }
    // The security CLI created the item, so reading it through the same CLI shows no Keychain prompt.
    let mut security = Command::new("/usr/bin/security");
    security.args(["find-generic-password", "-s", KEYCHAIN_SERVICE, "-a", KEYCHAIN_ACCOUNT, "-w"]);
    // `$HARMFLOW_KEYCHAIN`, when set, is the keychain file the daemon keeps the token in, as a test points both at a
    // throwaway keychain (#79); unset, security searches the user's keychains.
    if let Some(keychain) = std::env::var_os("HARMFLOW_KEYCHAIN").filter(|v| !v.is_empty()) {
        security.arg(keychain);
    }
    let out = security
        .stdin(Stdio::null())
        .output()
        .map_err(|e| format!("security: {e}"))?;
    if !out.status.success() {
        return Err("the API token is not in the Keychain".into());
    }
    Ok(String::from_utf8_lossy(&out.stdout).trim().to_string())
}

#[derive(Debug, Clone, Serialize, Default)]
#[serde(rename_all = "camelCase")]
pub struct DaemonStatus {
    pub running: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub address: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub pid: Option<u32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub version: Option<String>,
    /// The commit the daemon was built from, as GET /health gives it; absent for a build without git information.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub commit: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub built_at: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub started_at: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

/// The status of a daemon that answered GET /health with `body`.
fn status_from_health(body: &Value, address: Option<String>) -> DaemonStatus {
    let get = |k: &str| body.get(k).and_then(Value::as_str).map(str::to_string);
    DaemonStatus {
        running: true,
        address,
        pid: body.get("pid").and_then(Value::as_u64).map(|p| p as u32),
        version: get("version"),
        commit: get("commit"),
        built_at: get("builtAt"),
        started_at: get("startedAt"),
        error: None,
    }
}

/// One response of the local API, as the webview receives it.
#[derive(Debug, Clone, Serialize)]
pub struct ApiResponse {
    pub status: u16,
    pub body: Value,
}

pub struct DaemonClient {
    http: reqwest::Client,
    conn: Mutex<Option<Connection>>,
}

impl DaemonClient {
    pub fn new() -> Self {
        let http = reqwest::Client::builder()
            .no_proxy()
            .connect_timeout(Duration::from_secs(3))
            .build()
            .unwrap_or_else(|_| reqwest::Client::new());
        Self { http, conn: Mutex::new(None) }
    }

    pub fn http(&self) -> &reqwest::Client {
        &self.http
    }

    /// The cached connection, or a fresh one read from the runtime file and the Keychain.
    pub fn connection(&self) -> Result<Connection, String> {
        if let Ok(guard) = self.conn.lock() {
            if let Some(c) = guard.as_ref() {
                return Ok(c.clone());
            }
        }
        let home = harmflow_home();
        let runtime = read_runtime(&home)?;
        let token = read_token(&home, runtime.token_store.as_deref())?;
        let conn = Connection { runtime, token };
        if let Ok(mut guard) = self.conn.lock() {
            *guard = Some(conn.clone());
        }
        Ok(conn)
    }

    /// Forgets the cached address and token: the daemon may have restarted on another port or rotated the token.
    pub fn invalidate(&self) {
        if let Ok(mut guard) = self.conn.lock() {
            *guard = None;
        }
    }

    async fn send_once(&self, method: &reqwest::Method, path: &str, body: &Option<Value>) -> Result<reqwest::Response, String> {
        let conn = self.connection()?;
        let mut req = self
            .http
            .request(method.clone(), format!("{}/api/v1{}", conn.base_url(), path))
            .bearer_auth(&conn.token)
            .timeout(Duration::from_secs(30));
        if let Some(b) = body {
            req = req.json(b);
        }
        req.send().await.map_err(|e| e.to_string())
    }

    /// Sends a request; after a 401 or a refused connection it rereads the token and address and tries once more.
    pub async fn send(&self, method: &str, path: &str, body: Option<Value>) -> Result<reqwest::Response, String> {
        validate_path(path)?;
        let method = parse_method(method)?;
        match self.send_once(&method, path, &body).await {
            Ok(res) if res.status() != reqwest::StatusCode::UNAUTHORIZED => Ok(res),
            _ => {
                self.invalidate();
                self.send_once(&method, path, &body).await
            }
        }
    }

    pub async fn request(&self, method: &str, path: &str, body: Option<Value>) -> ApiResponse {
        match self.send(method, path, body).await {
            Ok(res) => {
                let status = res.status().as_u16();
                let text = res.text().await.unwrap_or_default();
                let body = if text.is_empty() {
                    Value::Null
                } else {
                    serde_json::from_str(&text).unwrap_or(Value::String(text))
                };
                ApiResponse { status, body }
            }
            Err(message) => ApiResponse {
                status: 0,
                body: serde_json::json!({ "error": { "code": "internal", "message": message } }),
            },
        }
    }

    pub async fn request_text(&self, path: &str) -> (u16, String) {
        match self.send("GET", path, None).await {
            Ok(res) => {
                let status = res.status().as_u16();
                (status, res.text().await.unwrap_or_default())
            }
            Err(message) => (0, message),
        }
    }

    /// Running means the runtime file exists and GET /health answers with it.
    pub async fn status(&self) -> DaemonStatus {
        let res = self.request("GET", "/health", None).await;
        if res.status == 200 {
            let address = self.connection().ok().map(|c| c.runtime.address);
            return status_from_health(&res.body, address);
        }
        self.invalidate();
        let error = res
            .body
            .pointer("/error/message")
            .and_then(Value::as_str)
            .map(str::to_string);
        DaemonStatus { running: false, error, ..Default::default() }
    }
}

fn parse_method(method: &str) -> Result<reqwest::Method, String> {
    match method.to_ascii_uppercase().as_str() {
        "GET" => Ok(reqwest::Method::GET),
        "POST" => Ok(reqwest::Method::POST),
        "PUT" => Ok(reqwest::Method::PUT),
        "PATCH" => Ok(reqwest::Method::PATCH),
        "DELETE" => Ok(reqwest::Method::DELETE),
        other => Err(format!("method not allowed: {other}")),
    }
}

/// Only paths of the local API, relative to /api/v1, may be forwarded.
fn validate_path(path: &str) -> Result<(), String> {
    let ok = path.starts_with('/')
        && !path.contains("..")
        && !path.contains("://")
        && !path.contains('\\')
        && path.chars().all(|c| !c.is_control());
    if ok {
        Ok(())
    } else {
        Err(format!("path not allowed: {path}"))
    }
}

/// The daemon binary: `$HARMFLOW_DAEMON_BIN`, the sidecar next to this executable, or the development build.
pub fn daemon_binary() -> Option<PathBuf> {
    if let Some(p) = std::env::var_os("HARMFLOW_DAEMON_BIN").map(PathBuf::from) {
        if p.is_file() {
            return Some(p);
        }
    }
    if let Some(dir) = std::env::current_exe().ok().and_then(|p| p.parent().map(Path::to_path_buf)) {
        let sidecar = dir.join(DAEMON_BINARY);
        if sidecar.is_file() {
            return Some(sidecar);
        }
    }
    let dev = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("binaries")
        .join(format!("{DAEMON_BINARY}-{}", target_triple()));
    dev.is_file().then_some(dev)
}

fn target_triple() -> &'static str {
    if cfg!(all(target_os = "macos", target_arch = "aarch64")) {
        "aarch64-apple-darwin"
    } else if cfg!(all(target_os = "macos", target_arch = "x86_64")) {
        "x86_64-apple-darwin"
    } else if cfg!(all(target_os = "linux", target_arch = "x86_64")) {
        "x86_64-unknown-linux-gnu"
    } else if cfg!(all(target_os = "linux", target_arch = "aarch64")) {
        "aarch64-unknown-linux-gnu"
    } else {
        "x86_64-pc-windows-msvc"
    }
}

/// Starts the daemon detached in its own process group, so it outlives the window and the app.
pub fn spawn_daemon() -> Result<(), String> {
    let bin = daemon_binary().ok_or_else(|| "the harmflowd binary was not found".to_string())?;
    let home = std::env::var_os("HARMFLOW_HOME").filter(|v| !v.is_empty());
    spawn_detached(&bin, home.as_deref()).map(|_| ())
}

/// Starts `bin` in a process group of its own, with its standard streams closed, so neither the app's exit nor a
/// signal to the app's group reaches it.
fn spawn_detached(bin: &Path, home: Option<&OsStr>) -> Result<Child, String> {
    let mut cmd = Command::new(bin);
    if let Some(home) = home {
        cmd.arg("--home").arg(home);
    }
    cmd.stdin(Stdio::null()).stdout(Stdio::null()).stderr(Stdio::null());
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt;
        cmd.process_group(0);
    }
    cmd.spawn().map_err(|e| format!("{}: {e}", bin.display()))
}

/// Waits up to `limit` for process `pid` to exit. A daemon asked to stop closes its API before it exits and frees its
/// data directory, so a harmflowd started in between finds the directory taken and exits.
pub async fn wait_gone(pid: u32, limit: Duration) -> Result<(), String> {
    let deadline = tokio::time::Instant::now() + limit;
    while !process_gone(pid)? {
        if tokio::time::Instant::now() >= deadline {
            return Err(format!(
                "daemon pid {pid} did not exit within {} seconds; see logs/daemon.log in the HarmFlow folder",
                limit.as_secs()
            ));
        }
        tokio::time::sleep(Duration::from_millis(200)).await;
    }
    Ok(())
}

/// Whether process `pid` has exited: `ps` lists no such process, or lists a zombie, which an exited daemon stays while
/// the app that started it never waits for it.
fn process_gone(pid: u32) -> Result<bool, String> {
    let out = Command::new("/bin/ps")
        .args(["-o", "stat=", "-p", &pid.to_string()])
        .stdin(Stdio::null())
        .stderr(Stdio::null())
        .output()
        .map_err(|e| format!("ps: {e}"))?;
    let stat = String::from_utf8_lossy(&out.stdout);
    let stat = stat.trim();
    Ok(stat.is_empty() || stat.starts_with('Z'))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn only_api_paths_are_forwarded() {
        assert!(validate_path("/tasks/implement-issues/run").is_ok());
        assert!(validate_path("/executions?active=true&limit=50").is_ok());
        assert!(validate_path("tasks").is_err());
        assert!(validate_path("/../../etc/passwd").is_err());
        assert!(validate_path("/http://example.com").is_err());
    }

    #[test]
    fn only_known_methods_are_forwarded() {
        assert!(parse_method("patch").is_ok());
        assert!(parse_method("TRACE").is_err());
    }

    #[test]
    fn runtime_file_is_read() {
        let dir = std::env::temp_dir().join(format!("harmflow-runtime-test-{}", std::process::id()));
        std::fs::create_dir_all(dir.join("run")).unwrap();
        std::fs::write(
            dir.join("run").join("daemon.json"),
            r#"{"address":"127.0.0.1:4321","port":4321,"pid":7,"version":"0.1.0","startedAt":"2026-10-20T10:00:00Z","tokenStore":"file"}"#,
        )
        .unwrap();
        std::fs::write(dir.join("run").join("token"), "secret\n").unwrap();
        let rt = read_runtime(&dir).unwrap();
        assert_eq!(rt.address, "127.0.0.1:4321");
        assert_eq!(read_token(&dir, rt.token_store.as_deref()).unwrap(), "secret");
        std::fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn health_status_keeps_the_build_identity() {
        let body = serde_json::json!({
            "status": "ok",
            "pid": 7,
            "version": "0.1.0",
            "commit": "13bd5f9-dirty",
            "builtAt": "2026-10-23T10:00:00Z",
            "startedAt": "2026-10-23T10:00:05Z"
        });
        let status = status_from_health(&body, Some("127.0.0.1:4321".into()));
        assert!(status.running);
        assert_eq!(status.pid, Some(7));
        assert_eq!(status.commit.as_deref(), Some("13bd5f9-dirty"));
        assert_eq!(status.built_at.as_deref(), Some("2026-10-23T10:00:00Z"));
        let json = serde_json::to_value(&status).unwrap();
        assert_eq!(json["commit"], "13bd5f9-dirty");
        assert_eq!(json["builtAt"], "2026-10-23T10:00:00Z");

        // A daemon built without git information or a build time sends neither, and the status leaves both out.
        let bare = status_from_health(&serde_json::json!({ "pid": 7, "version": "0.1.0" }), None);
        assert_eq!((bare.commit, bare.built_at), (None, None));
    }

    /// The wait for a stopped daemon ends once its process exits, also while it is a zombie its parent never waited
    /// for, and gives up on one that keeps running.
    #[cfg(unix)]
    #[tokio::test]
    async fn the_wait_for_a_daemon_ends_when_its_process_exits() {
        let mut child = Command::new("/bin/sleep").arg("60").spawn().unwrap();
        let pid = child.id();
        let refused = wait_gone(pid, Duration::from_millis(300)).await;
        child.kill().unwrap();
        let after_kill = wait_gone(pid, Duration::from_secs(5)).await;
        let zombie = process_gone(pid);
        child.wait().unwrap();
        assert!(refused.is_err_and(|e| e.contains(&format!("daemon pid {pid} did not exit"))));
        assert_eq!(after_kill, Ok(()));
        assert_eq!(zombie, Ok(true), "an unreaped process counts as gone");
        assert_eq!(process_gone(pid), Ok(true));
    }

    /// The daemon `daemon_start` launches leads a process group of its own, apart from the app's. The stand-in for
    /// harmflowd comes as a path, so no process-wide variable is touched.
    #[cfg(unix)]
    #[test]
    fn daemon_starts_in_its_own_process_group() {
        use std::os::unix::fs::PermissionsExt;
        let dir = std::env::temp_dir().join(format!("harmflow-spawn-test-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let bin = dir.join("harmflowd");
        std::fs::write(&bin, "#!/bin/sh\nexec sleep 60\n").unwrap();
        std::fs::set_permissions(&bin, std::fs::Permissions::from_mode(0o755)).unwrap();
        let pgid = |pid: u32| -> Option<u32> {
            let out = Command::new("/bin/ps").args(["-o", "pgid=", "-p", &pid.to_string()]).output().ok()?;
            String::from_utf8_lossy(&out.stdout).trim().parse().ok()
        };

        let mut child = spawn_detached(&bin, Some(dir.as_os_str())).unwrap();
        let (group, own) = (pgid(child.id()), pgid(std::process::id()));
        child.kill().ok();
        child.wait().ok();
        std::fs::remove_dir_all(&dir).ok();
        assert_eq!(group, Some(child.id()), "the child's process group is not its own");
        assert!(own.is_some() && own != group, "the child shares the test's process group {own:?}");
    }

    /// A keychain file made for one test, unlocked, and named by `$HARMFLOW_KEYCHAIN` once `point_here` runs. Dropping
    /// it, also after a failed assertion, deletes it with `security delete-keychain`, which also takes it off the search
    /// list, and unsets the variable.
    #[cfg(target_os = "macos")]
    struct ThrowawayKeychain {
        dir: PathBuf,
        path: PathBuf,
    }

    #[cfg(target_os = "macos")]
    impl ThrowawayKeychain {
        fn new() -> Self {
            let dir = std::env::temp_dir().join(format!("harmflow-keychain-test-{}", std::process::id()));
            std::fs::create_dir_all(&dir).unwrap();
            let keychain = ThrowawayKeychain { path: dir.join("throwaway.keychain-db"), dir };
            let path = keychain.path.to_str().unwrap();
            security(&["create-keychain", "-p", "harmflow-test", path]);
            security(&["unlock-keychain", "-p", "harmflow-test", path]);
            keychain
        }

        fn point_here(&self) {
            std::env::set_var("HARMFLOW_KEYCHAIN", &self.path);
        }
    }

    #[cfg(target_os = "macos")]
    impl Drop for ThrowawayKeychain {
        fn drop(&mut self) {
            std::env::remove_var("HARMFLOW_KEYCHAIN");
            let _ = Command::new("/usr/bin/security").arg("delete-keychain").arg(&self.path).status();
            let _ = std::fs::remove_dir_all(&self.dir);
        }
    }

    #[cfg(target_os = "macos")]
    fn security(args: &[&str]) {
        let out = Command::new("/usr/bin/security").args(args).output().unwrap();
        assert!(out.status.success(), "security {}: {}", args[0], String::from_utf8_lossy(&out.stderr));
    }

    /// The desktop reads the token the daemon keeps in the Keychain, the item of service com.harmflow.local-api and
    /// account token, written by the same security CLI. A throwaway keychain holds it, never the login keychain (#79).
    #[cfg(target_os = "macos")]
    #[test]
    fn token_is_read_from_the_keychain() {
        let keychain = ThrowawayKeychain::new();
        let path = keychain.path.to_str().unwrap();
        // The literal service and account the daemon writes, not this file's constants, so both sides must agree.
        security(&["add-generic-password", "-s", "com.harmflow.local-api", "-a", "token", "-w", "kc-token", path]);
        keychain.point_here();
        assert_eq!(read_token(&keychain.dir, Some("keychain")).unwrap(), "kc-token");
    }
}
