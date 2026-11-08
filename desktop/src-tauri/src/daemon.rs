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
/// How long a request of the local API waits for the daemon's answer.
const REQUEST_TIMEOUT: Duration = Duration::from_secs(30);

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
    /// The daemon's process, which tells a daemon that does not answer from one that is gone.
    #[serde(default)]
    pub pid: Option<u32>,
    /// "keychain" or "file"; the version and start time it also holds are read from GET /health instead.
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
    /// The daemon runs but did not answer in time: GET /health timed out once its port took the connection, while the
    /// pid of the runtime file is alive. A busy or throttled daemon, not a stopped one (#908).
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub not_answering: bool,
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
        not_answering: false,
        address,
        pid: body.get("pid").and_then(Value::as_u64).map(|p| p as u32),
        version: get("version"),
        commit: get("commit"),
        built_at: get("builtAt"),
        started_at: get("startedAt"),
        error: None,
    }
}

/// One response of the local API, as the webview receives it. `total` is the response's `X-Total-Count`, how many
/// items a list's filters match, when the daemon sent one: the only header the webview needs.
#[derive(Debug, Clone, Serialize)]
pub struct ApiResponse {
    pub status: u16,
    pub body: Value,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub total: Option<u64>,
}

/// The `X-Total-Count` of a response, when it holds a number.
fn total_count(headers: &reqwest::header::HeaderMap) -> Option<u64> {
    headers.get("x-total-count")?.to_str().ok()?.trim().parse().ok()
}

/// Why a request got no answer from the daemon.
#[derive(Debug)]
enum Failure {
    /// The request never left: its path or method is not allowed, or the runtime file or the token could not be read.
    Setup(String),
    /// The request was sent and failed: refused, timed out, or cut.
    Http(reqwest::Error),
}

impl Failure {
    fn message(&self) -> String {
        match self {
            Failure::Setup(message) => message.clone(),
            Failure::Http(err) => err.to_string(),
        }
    }

    /// The daemon's port took the connection, and no answer came within the request's timeout.
    fn unanswered(&self) -> bool {
        matches!(self, Failure::Http(err) if err.is_timeout() && !err.is_connect())
    }

    /// The connection was refused: `connection_refused`.
    fn refused(&self) -> bool {
        matches!(self, Failure::Http(err) if connection_refused(err))
    }
}

/// The connection was refused, not left waiting: nothing listens at the address any longer, so the daemon stopped or
/// restarted on another port. A connection that timed out is a busy daemon's, and no reason to read it again.
pub fn connection_refused(err: &reqwest::Error) -> bool {
    err.is_connect() && !err.is_timeout()
}

/// The response the webview receives for a request that failed: status 0, and the reason as an internal error.
fn failed_response(failure: &Failure) -> ApiResponse {
    ApiResponse {
        status: 0,
        body: serde_json::json!({ "error": { "code": "internal", "message": failure.message() } }),
        total: None,
    }
}

async fn api_response(res: reqwest::Response) -> ApiResponse {
    let status = res.status().as_u16();
    let total = total_count(res.headers());
    let text = res.text().await.unwrap_or_default();
    let body = if text.is_empty() {
        Value::Null
    } else {
        serde_json::from_str(&text).unwrap_or(Value::String(text))
    };
    ApiResponse { status, body, total }
}

pub struct DaemonClient {
    http: reqwest::Client,
    conn: Mutex<Option<Connection>>,
    /// How long a request waits for the daemon's answer: `REQUEST_TIMEOUT`, shorter in the tests.
    timeout: Duration,
    /// The HarmFlow folder the runtime file and the token are read from; `None` is `harmflow_home()`, read each time.
    home: Option<PathBuf>,
}

impl DaemonClient {
    pub fn new() -> Self {
        Self::with(None, REQUEST_TIMEOUT)
    }

    fn with(home: Option<PathBuf>, timeout: Duration) -> Self {
        let http = reqwest::Client::builder()
            .no_proxy()
            .connect_timeout(Duration::from_secs(3))
            .build()
            .unwrap_or_else(|_| reqwest::Client::new());
        Self { http, conn: Mutex::new(None), timeout, home }
    }

    pub fn http(&self) -> &reqwest::Client {
        &self.http
    }

    fn home(&self) -> PathBuf {
        self.home.clone().unwrap_or_else(harmflow_home)
    }

    /// The cached connection, or a fresh one read from the runtime file and the Keychain.
    pub fn connection(&self) -> Result<Connection, String> {
        if let Ok(guard) = self.conn.lock() {
            if let Some(c) = guard.as_ref() {
                return Ok(c.clone());
            }
        }
        let home = self.home();
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

    async fn send_once(&self, method: &reqwest::Method, path: &str, body: &Option<Value>) -> Result<reqwest::Response, Failure> {
        let conn = self.connection().map_err(Failure::Setup)?;
        let mut req = self
            .http
            .request(method.clone(), format!("{}/api/v1{}", conn.base_url(), path))
            .bearer_auth(&conn.token)
            .timeout(self.timeout);
        if let Some(b) = body {
            req = req.json(b);
        }
        req.send().await.map_err(Failure::Http)
    }

    /// Sends a request. A 401 or a refused connection says the token or the address changed: it reads both again and
    /// tries once more. Any other failure, a timeout included, keeps them and is not sent again at once: a daemon too
    /// busy to answer would only queue the same request twice (#908).
    async fn exchange(&self, method: &str, path: &str, body: Option<Value>) -> Result<reqwest::Response, Failure> {
        validate_path(path).map_err(Failure::Setup)?;
        let method = parse_method(method).map_err(Failure::Setup)?;
        match self.send_once(&method, path, &body).await {
            Ok(res) if res.status() == reqwest::StatusCode::UNAUTHORIZED => {}
            Err(failure) if failure.refused() => {}
            done => return done,
        }
        self.invalidate();
        self.send_once(&method, path, &body).await
    }

    /// Sends a request, as `exchange` does, with the reason of a failure as text.
    pub async fn send(&self, method: &str, path: &str, body: Option<Value>) -> Result<reqwest::Response, String> {
        self.exchange(method, path, body).await.map_err(|failure| failure.message())
    }

    pub async fn request(&self, method: &str, path: &str, body: Option<Value>) -> ApiResponse {
        match self.exchange(method, path, body).await {
            Ok(res) => api_response(res).await,
            Err(failure) => failed_response(&failure),
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

    /// Running means the runtime file exists and GET /health answers with it. A check that timed out once the
    /// daemon's port took its connection, while the pid of the runtime file is alive, reads as not answering: the
    /// daemon runs, too busy to answer yet. Any other failure reads as stopped. Neither forgets the cached address and
    /// token, which `exchange` alone reads again.
    pub async fn status(&self) -> DaemonStatus {
        let res = match self.exchange("GET", "/health", None).await {
            Ok(res) => api_response(res).await,
            Err(failure) => {
                if failure.unanswered() {
                    if let Some(status) = self.alive_status(failure.message()).await {
                        return status;
                    }
                }
                failed_response(&failure)
            }
        };
        if res.status == 200 {
            let address = self.connection().ok().map(|c| c.runtime.address);
            return status_from_health(&res.body, address);
        }
        let error = res
            .body
            .pointer("/error/message")
            .and_then(Value::as_str)
            .map(str::to_string);
        DaemonStatus { running: false, error, ..Default::default() }
    }

    /// The not-answering status of the daemon the runtime file names, when its pid is alive; `None` otherwise.
    async fn alive_status(&self, error: String) -> Option<DaemonStatus> {
        let runtime = read_runtime(&self.home()).ok()?;
        let pid = runtime.pid?;
        let gone = tokio::task::spawn_blocking(move || process_gone(pid)).await.ok()?.ok()?;
        (!gone).then(|| DaemonStatus {
            running: false,
            not_answering: true,
            address: Some(runtime.address),
            pid: Some(pid),
            error: Some(error),
            ..Default::default()
        })
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

    #[test]
    fn a_lists_total_reaches_the_webview() {
        let mut headers = reqwest::header::HeaderMap::new();
        assert_eq!(total_count(&headers), None);
        headers.insert("X-Total-Count", "342".parse().unwrap());
        assert_eq!(total_count(&headers), Some(342));
        headers.insert("X-Total-Count", "many".parse().unwrap());
        assert_eq!(total_count(&headers), None);

        // The webview reads the total beside the status and the body, and finds none on a response that has none.
        let listed = ApiResponse { status: 200, body: serde_json::json!([]), total: Some(342) };
        assert_eq!(serde_json::to_value(&listed).unwrap(), serde_json::json!({ "status": 200, "body": [], "total": 342 }));
        let bare = ApiResponse { status: 204, body: Value::Null, total: None };
        assert_eq!(serde_json::to_value(&bare).unwrap(), serde_json::json!({ "status": 204, "body": null }));
    }

    /// A stand-in for the daemon's local API on a port of its own. It accepts every connection and answers the
    /// requests it reads with `answers`, in turn; once they run out, it keeps each connection open and never answers.
    struct StandIn {
        address: String,
        accepted: std::sync::Arc<std::sync::atomic::AtomicUsize>,
        tokens: std::sync::Arc<Mutex<Vec<String>>>,
    }

    impl StandIn {
        fn start(answers: &[&'static str]) -> Self {
            use std::io::{Read, Write};
            use std::sync::atomic::Ordering;
            let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
            let address = listener.local_addr().unwrap().to_string();
            let accepted = std::sync::Arc::new(std::sync::atomic::AtomicUsize::new(0));
            let tokens = std::sync::Arc::new(Mutex::new(Vec::new()));
            let (count, seen) = (accepted.clone(), tokens.clone());
            let mut answers = answers.to_vec().into_iter();
            // The thread ends with the test process; the connections it holds stay open until then.
            std::thread::spawn(move || {
                let mut held = Vec::new();
                for stream in listener.incoming() {
                    let Ok(mut stream) = stream else { continue };
                    count.fetch_add(1, Ordering::SeqCst);
                    let mut request = Vec::new();
                    let mut buf = [0u8; 1024];
                    while !request.windows(4).any(|w| w == b"\r\n\r\n") {
                        match stream.read(&mut buf) {
                            Ok(0) | Err(_) => break,
                            Ok(n) => request.extend_from_slice(&buf[..n]),
                        }
                    }
                    let text = String::from_utf8_lossy(&request);
                    let token = text
                        .lines()
                        .find_map(|l| l.to_ascii_lowercase().starts_with("authorization:").then(|| l[14..].trim().to_string()));
                    seen.lock().unwrap().extend(token);
                    match answers.next() {
                        Some(answer) => {
                            let _ = stream.write_all(answer.as_bytes());
                        }
                        None => held.push(stream),
                    }
                }
            });
            StandIn { address, accepted, tokens }
        }

        fn accepted(&self) -> usize {
            self.accepted.load(std::sync::atomic::Ordering::SeqCst)
        }

        fn tokens(&self) -> Vec<String> {
            self.tokens.lock().unwrap().clone()
        }
    }

    const UNAUTHORIZED: &str = "HTTP/1.1 401 Unauthorized\r\nContent-Length: 0\r\nConnection: close\r\n\r\n";
    const HEALTHY: &str = "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 27\r\nConnection: close\r\n\r\n{\"pid\":7,\"version\":\"0.1.0\"}";

    /// A HarmFlow folder of its own, whose runtime file names `address` and `pid` and whose file store holds `token`.
    fn runtime_home(name: &str, address: &str, pid: u32, token: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!("harmflow-{name}-{}", std::process::id()));
        std::fs::create_dir_all(dir.join("run")).unwrap();
        write_runtime(&dir, address, pid, token);
        dir
    }

    fn write_runtime(dir: &Path, address: &str, pid: u32, token: &str) {
        let runtime = serde_json::json!({ "address": address, "pid": pid, "tokenStore": "file" });
        std::fs::write(dir.join("run").join("daemon.json"), runtime.to_string()).unwrap();
        std::fs::write(dir.join("run").join("token"), format!("{token}\n")).unwrap();
    }

    /// harmflow-app#908: a daemon that takes the connection and never answers is asked once. The timeout neither sends
    /// the request again nor forgets the address and the token, and its status reads as running but not answering
    /// while the pid of the runtime file is alive.
    #[tokio::test]
    async fn a_timeout_keeps_the_token_and_reads_as_not_answering() {
        let daemon = StandIn::start(&[]);
        let home = runtime_home("not-answering", &daemon.address, std::process::id(), "first");
        let client = DaemonClient::with(Some(home.clone()), Duration::from_millis(300));

        let sent = client.send("GET", "/stats", None).await;
        // A token read again now would be this one.
        write_runtime(&home, &daemon.address, std::process::id(), "second");
        let kept = client.connection().map(|c| c.token);
        let status = client.status().await;
        std::fs::remove_dir_all(&home).ok();

        assert!(sent.is_err(), "a request that never got an answer succeeded");
        assert_eq!(kept.as_deref(), Ok("first"), "the timeout dropped the cached token");
        assert_eq!(daemon.accepted(), 2, "one connection for the request and one for the status, no retry");
        assert!(status.not_answering && !status.running, "status {status:?}, want not answering");
        assert_eq!(status.pid, Some(std::process::id()));
        assert_eq!(status.address.as_deref(), Some(daemon.address.as_str()));
        assert_eq!(serde_json::to_value(&status).unwrap()["notAnswering"], true);
    }

    /// harmflow-app#908: a timeout whose runtime file names a pid that is gone reads as a stopped daemon, as before.
    #[tokio::test]
    async fn a_timeout_with_its_pid_gone_reads_as_stopped() {
        let mut exited = Command::new("/usr/bin/true").spawn().unwrap();
        exited.wait().unwrap();
        let daemon = StandIn::start(&[]);
        let home = runtime_home("pid-gone", &daemon.address, exited.id(), "first");
        let client = DaemonClient::with(Some(home.clone()), Duration::from_millis(300));

        let status = client.status().await;
        std::fs::remove_dir_all(&home).ok();

        assert!(!status.running && !status.not_answering, "status {status:?}, want stopped");
        assert!(status.error.is_some(), "a stopped status gives no reason");
        let json = serde_json::to_value(&status).unwrap();
        assert!(json.get("notAnswering").is_none(), "a stopped status sends notAnswering: {json}");
    }

    /// A 401 says the token changed: the client reads it again and sends the request once more, with the new one.
    #[tokio::test]
    async fn a_401_reads_the_token_again_and_retries_once() {
        let daemon = StandIn::start(&[UNAUTHORIZED, HEALTHY]);
        let home = runtime_home("unauthorized", &daemon.address, std::process::id(), "first");
        let client = DaemonClient::with(Some(home.clone()), Duration::from_secs(5));
        client.connection().unwrap();
        write_runtime(&home, &daemon.address, std::process::id(), "second");

        let res = client.request("GET", "/health", None).await;
        std::fs::remove_dir_all(&home).ok();

        assert_eq!(res.status, 200);
        assert_eq!(daemon.tokens(), ["Bearer first", "Bearer second"]);
    }

    /// A refused connection says the address changed: the client reads the runtime file again and sends the request
    /// once more, to the daemon's new port.
    #[tokio::test]
    async fn a_refused_connection_reads_the_address_again_and_retries_once() {
        let closed = std::net::TcpListener::bind("127.0.0.1:0").unwrap().local_addr().unwrap().to_string();
        let daemon = StandIn::start(&[HEALTHY]);
        let home = runtime_home("refused", &closed, std::process::id(), "first");
        let client = DaemonClient::with(Some(home.clone()), Duration::from_secs(5));
        client.connection().unwrap();
        write_runtime(&home, &daemon.address, std::process::id(), "first");

        let status = client.status().await;
        std::fs::remove_dir_all(&home).ok();

        assert!(status.running, "status {status:?}, want running at the new address");
        assert_eq!(status.address.as_deref(), Some(daemon.address.as_str()));
        assert_eq!(daemon.accepted(), 1);
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
        let dir = std::env::temp_dir().join(format!("harmflow-spawn-test-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let bin = dir.join("harmflowd");
        // A shell of its own writes the stand-in and exits before the spawn, so this process never holds it open for
        // writing. Were it to, a fork another test makes beside this one would keep that descriptor until its exec, and
        // on Linux executing a file that any process holds open for writing fails with "Text file busy".
        let wrote = Command::new("/bin/sh")
            .args(["-c", r#"printf '#!/bin/sh\nexec sleep 60\n' > "$1" && chmod 755 "$1""#, "sh"])
            .arg(&bin)
            .status()
            .unwrap();
        assert!(wrote.success(), "the stand-in for harmflowd was not written");
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
