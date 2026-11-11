//! The live event stream: one SSE connection to the daemon, forwarded to the webview as Tauri events. It reconnects
//! on its own and resumes after the last event id it delivered, so nothing is lost while the window is closed or the
//! daemon restarts.

use std::sync::Arc;
use std::time::Duration;

use futures_util::StreamExt;
use serde_json::Value;
use tauri::{AppHandle, Emitter, Manager};

use crate::state::AppState;

pub const EVENT: &str = "harmflow://event";
pub const CONNECTION: &str = "harmflow://connection";
pub const REPLAY_GAP: &str = "harmflow://replay-gap";

/// A stream that sends nothing, not even its 15-second heartbeat, for this long is considered dead.
const SILENCE_LIMIT: Duration = Duration::from_secs(45);

/// One message of the stream, assembled from its `id:`, `event:`, and `data:` lines.
#[derive(Debug, Default, PartialEq)]
pub struct SseMessage {
    pub id: Option<u64>,
    pub event: Option<String>,
    pub data: String,
}

/// Incremental parser of the text/event-stream format; comment lines (`: ping`) are skipped.
#[derive(Default)]
pub struct SseParser {
    buffer: String,
    current: SseMessage,
}

impl SseParser {
    pub fn push(&mut self, chunk: &str) -> Vec<SseMessage> {
        self.buffer.push_str(chunk);
        let mut out = Vec::new();
        while let Some(pos) = self.buffer.find('\n') {
            let line: String = self.buffer.drain(..=pos).collect();
            let line = line.trim_end_matches(['\n', '\r']);
            if line.is_empty() {
                let msg = std::mem::take(&mut self.current);
                if !msg.data.is_empty() || msg.event.is_some() {
                    out.push(msg);
                }
                continue;
            }
            if line.starts_with(':') {
                continue;
            }
            let (field, value) = match line.split_once(':') {
                Some((f, v)) => (f, v.strip_prefix(' ').unwrap_or(v)),
                None => (line, ""),
            };
            match field {
                "id" => self.current.id = value.parse().ok(),
                "event" => self.current.event = Some(value.to_string()),
                "data" => {
                    if !self.current.data.is_empty() {
                        self.current.data.push('\n');
                    }
                    self.current.data.push_str(value);
                }
                _ => {}
            }
        }
        out
    }
}

/// Where the stream's connection states and events go: the webview and the tray in the app, a record in the tests.
trait Sink: Send + Sync {
    /// The connection changed to `state`, which the app's state already holds.
    fn connection(&self, state: &str);
    fn event(&self, event: Value);
    fn replay_gap(&self);
}

/// The app's sink: Tauri events to the webview. A change of the connection also refreshes the tray, whose daemon item
/// follows a stop or a start from anywhere.
struct AppSink(AppHandle);

impl Sink for AppSink {
    fn connection(&self, state: &str) {
        let _ = self.0.emit(CONNECTION, serde_json::json!({ "state": state }));
        crate::tray::schedule_refresh(self.0.clone());
    }

    fn event(&self, event: Value) {
        crate::tray::observe_event(&self.0, &event);
        let _ = self.0.emit(EVENT, event);
    }

    fn replay_gap(&self) {
        let _ = self.0.emit(REPLAY_GAP, ());
    }
}

/// Records the connection state, and emits it only when it changed.
fn set_connection(sink: &dyn Sink, state: &AppState, value: &str) {
    if state.set_connection(value) {
        sink.connection(value);
    }
}

/// Runs for the life of the app.
pub fn spawn(app: AppHandle) {
    tauri::async_runtime::spawn(async move {
        let state: Arc<AppState> = app.state::<Arc<AppState>>().inner().clone();
        let sink = AppSink(app);
        run(&sink, &state, SILENCE_LIMIT).await;
    });
}

/// Streams, and connects again each time a stream ends, for ever. `silence` is `SILENCE_LIMIT` in the app; the tests
/// set a shorter one rather than wait for it.
async fn run(sink: &dyn Sink, state: &AppState, silence: Duration) {
    let mut backoff = Duration::from_millis(500);
    loop {
        match stream_once(sink, state, silence).await {
            Ok(()) => backoff = Duration::from_millis(500),
            Err(StreamEnd::DaemonStopped) => {
                set_connection(sink, state, "daemon_stopped");
                backoff = Duration::from_secs(2);
            }
            Err(StreamEnd::Lost) => {
                set_connection(sink, state, "reconnecting");
                backoff = (backoff * 2).min(Duration::from_secs(10));
            }
        }
        tokio::time::sleep(backoff).await;
    }
}

enum StreamEnd {
    DaemonStopped,
    Lost,
}

async fn stream_once(sink: &dyn Sink, state: &AppState, silence: Duration) -> Result<(), StreamEnd> {
    let client = &state.daemon;
    let conn = match client.connection() {
        Ok(c) => c,
        Err(_) => return Err(StreamEnd::DaemonStopped),
    };
    let last_id = state.last_event_id();
    let mut req = client
        .http()
        .get(format!("{}/api/v1/events/stream", conn.base_url()))
        .bearer_auth(&conn.token)
        .header("Accept", "text/event-stream");
    if last_id > 0 {
        req = req.header("Last-Event-ID", last_id.to_string());
    }
    // Only a refused connection or a 401 says the address or the token changed, and only they are read again. A
    // timeout is a busy daemon's, and anything else leaves both as they are (#908).
    let res = match req.send().await {
        Ok(r) => r,
        Err(e) => {
            if crate::daemon::connection_refused(&e) {
                client.invalidate();
            }
            return Err(if e.is_connect() { StreamEnd::DaemonStopped } else { StreamEnd::Lost });
        }
    };
    if res.status() == reqwest::StatusCode::UNAUTHORIZED {
        client.invalidate();
    }
    if !res.status().is_success() {
        return Err(StreamEnd::Lost);
    }
    set_connection(sink, state, "connected");

    let mut parser = SseParser::default();
    let mut body = res.bytes_stream();
    loop {
        let next = match tokio::time::timeout(silence, body.next()).await {
            Ok(n) => n,
            Err(_) => return Err(StreamEnd::Lost),
        };
        // A cut stream keeps the address and the token: the reconnect reads them again only if it is refused.
        let chunk = match next {
            Some(Ok(bytes)) => bytes,
            Some(Err(_)) | None => return Err(StreamEnd::Lost),
        };
        for msg in parser.push(&String::from_utf8_lossy(&chunk)) {
            if msg.event.as_deref() == Some("replay_gap") {
                sink.replay_gap();
                continue;
            }
            let Ok(event) = serde_json::from_str::<Value>(&msg.data) else { continue };
            if let Some(id) = msg.id.or_else(|| event.get("id").and_then(Value::as_u64)) {
                state.record_event_id(id);
            }
            sink.event(event);
        }
    }
}

#[cfg(test)]
mod tests {
    use std::io::{Read, Write};
    use std::net::{Shutdown, TcpListener, TcpStream};
    use std::path::{Path, PathBuf};
    use std::sync::atomic::{AtomicBool, Ordering};
    use std::sync::Mutex;

    use super::*;
    use crate::daemon::DaemonClient;

    #[test]
    fn parses_messages_split_across_chunks() {
        let mut p = SseParser::default();
        assert!(p.push("id: 7\nevent: execution.started\nda").is_empty());
        let msgs = p.push("ta: {\"id\":7}\n\n: ping\n\nid: 8\ndata: {\"id\":8}\n\n");
        assert_eq!(
            msgs,
            vec![
                SseMessage { id: Some(7), event: Some("execution.started".into()), data: "{\"id\":7}".into() },
                SseMessage { id: Some(8), event: None, data: "{\"id\":8}".into() },
            ]
        );
    }

    #[test]
    fn handles_crlf_and_events_without_data() {
        let mut p = SseParser::default();
        let msgs = p.push("event: replay_gap\r\n\r\n");
        assert_eq!(msgs, vec![SseMessage { id: None, event: Some("replay_gap".into()), data: String::new() }]);
    }

    /// What the stream emits, as the webview would receive it. Each connection state comes with the state the
    /// `connection_state` command returns at that moment, `AppState::connection`.
    struct Recorder {
        app: Arc<AppState>,
        states: Mutex<Vec<(String, String)>>,
        events: Mutex<Vec<Value>>,
    }

    impl Sink for Recorder {
        fn connection(&self, state: &str) {
            self.states.lock().unwrap().push((state.to_string(), self.app.connection()));
        }

        fn event(&self, event: Value) {
            self.events.lock().unwrap().push(event);
        }

        fn replay_gap(&self) {}
    }

    impl Recorder {
        /// The states emitted on `harmflow://connection`, in order, each checked against `connection_state`.
        fn states(&self) -> Vec<String> {
            let states = self.states.lock().unwrap().clone();
            for (emitted, read) in &states {
                assert_eq!(emitted, read, "connection_state read {read} when the stream emitted {emitted}");
            }
            states.into_iter().map(|(emitted, _)| emitted).collect()
        }

        /// The ids of the events forwarded on `harmflow://event`, in order.
        fn event_ids(&self) -> Vec<u64> {
            self.events.lock().unwrap().iter().filter_map(|e| e.get("id").and_then(Value::as_u64)).collect()
        }
    }

    /// The shell's stream client, run as the app runs it, against the HarmFlow folder `home`; stopped once dropped.
    struct Client {
        recorder: Arc<Recorder>,
        task: tokio::task::JoinHandle<()>,
    }

    impl Client {
        fn start(home: &Path, silence: Duration) -> Self {
            let daemon = DaemonClient::with(Some(home.to_path_buf()), Duration::from_secs(30));
            let app = Arc::new(AppState::with_daemon(daemon, false));
            let recorder = Arc::new(Recorder { app: app.clone(), states: Mutex::new(Vec::new()), events: Mutex::new(Vec::new()) });
            let sink = recorder.clone();
            let task = tokio::spawn(async move { run(&*sink, &app, silence).await });
            Client { recorder, task }
        }
    }

    impl Drop for Client {
        fn drop(&mut self) {
            self.task.abort();
        }
    }

    /// Waits up to `limit` for `done`, failing with `what` and the states seen so far.
    async fn until(what: &str, limit: Duration, client: &Client, done: impl Fn() -> bool) {
        let deadline = tokio::time::Instant::now() + limit;
        while !done() {
            assert!(
                tokio::time::Instant::now() < deadline,
                "{what}: not within {limit:?}; states {:?}, events {:?}",
                client.recorder.states(),
                client.recorder.event_ids()
            );
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
    }

    /// How the stand-in answers one connection to the stream.
    enum Reply {
        /// Writes this, then closes the connection.
        Close(String),
        /// Writes this, then holds the connection open and writes nothing more.
        Hold(String),
    }

    /// One request the stand-in read: its token and its `Last-Event-ID`.
    #[derive(Debug, Clone)]
    struct Seen {
        token: Option<String>,
        last_event_id: Option<String>,
    }

    /// A stand-in for the daemon's local API on a port of its own, which keeps accepting connections throughout. It
    /// answers the connections it accepts with `replies`, in turn; once they run out, it holds each one open and never
    /// answers.
    struct StandIn {
        address: String,
        seen: Arc<Mutex<Vec<Seen>>>,
    }

    impl StandIn {
        fn start(replies: Vec<Reply>) -> Self {
            let listener = TcpListener::bind("127.0.0.1:0").unwrap();
            let address = listener.local_addr().unwrap().to_string();
            let seen = Arc::new(Mutex::new(Vec::new()));
            let record = seen.clone();
            let mut replies = replies.into_iter();
            // The thread ends with the test process; the connections it holds stay open until then.
            std::thread::spawn(move || {
                let mut held = Vec::new();
                for stream in listener.incoming() {
                    let Ok(mut stream) = stream else { continue };
                    let mut request = Vec::new();
                    let mut buf = [0u8; 1024];
                    while !request.windows(4).any(|w| w == b"\r\n\r\n") {
                        match stream.read(&mut buf) {
                            Ok(0) | Err(_) => break,
                            Ok(n) => request.extend_from_slice(&buf[..n]),
                        }
                    }
                    let text = String::from_utf8_lossy(&request).into_owned();
                    let header = |name: &str| {
                        text.lines().find_map(|l| {
                            let (key, value) = l.split_once(':')?;
                            key.trim().eq_ignore_ascii_case(name).then(|| value.trim().to_string())
                        })
                    };
                    record.lock().unwrap().push(Seen { token: header("authorization"), last_event_id: header("last-event-id") });
                    match replies.next() {
                        Some(Reply::Close(text)) => {
                            let _ = stream.write_all(text.as_bytes());
                            let _ = stream.shutdown(Shutdown::Both);
                        }
                        Some(Reply::Hold(text)) => {
                            let _ = stream.write_all(text.as_bytes());
                            held.push(stream);
                        }
                        None => held.push(stream),
                    }
                }
            });
            StandIn { address, seen }
        }

        fn seen(&self) -> Vec<Seen> {
            self.seen.lock().unwrap().clone()
        }
    }

    /// The head of a stream the daemon accepted; its body runs until the connection closes.
    const STREAM: &str = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nCache-Control: no-cache\r\n\r\n";
    const UNAVAILABLE: &str = "HTTP/1.1 503 Service Unavailable\r\nContent-Length: 0\r\nConnection: close\r\n\r\n";
    const UNAUTHORIZED: &str = "HTTP/1.1 401 Unauthorized\r\nContent-Length: 0\r\nConnection: close\r\n\r\n";

    /// One event of the stream, as the daemon sends it.
    fn event(id: u64) -> String {
        format!("id: {id}\nevent: harness.message\ndata: {{\"id\":{id},\"type\":\"harness.message\"}}\n\n")
    }

    /// A stream answered with `events`, then left as the reply says.
    fn stream(events: &[u64]) -> String {
        events.iter().fold(STREAM.to_string(), |text, id| text + &event(*id))
    }

    /// A HarmFlow folder of its own, named for the test, with no runtime file yet.
    fn empty_home(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!("harmflow-sse-{name}-{}", std::process::id()));
        std::fs::remove_dir_all(&dir).ok();
        std::fs::create_dir_all(dir.join("run")).unwrap();
        dir
    }

    /// A HarmFlow folder of its own whose runtime file names `address` and whose file store holds `token`.
    fn home(name: &str, address: &str, token: &str) -> PathBuf {
        let dir = empty_home(name);
        write_runtime(&dir, address, token);
        dir
    }

    fn write_runtime(dir: &Path, address: &str, token: &str) {
        let runtime = serde_json::json!({ "address": address, "pid": std::process::id(), "tokenStore": "file" });
        std::fs::write(dir.join("run").join("daemon.json"), runtime.to_string()).unwrap();
        std::fs::write(dir.join("run").join("token"), format!("{token}\n")).unwrap();
    }

    /// An address nothing listens at, which refuses connections.
    fn refusing_address() -> String {
        TcpListener::bind("127.0.0.1:0").unwrap().local_addr().unwrap().to_string()
    }

    const WAIT: Duration = Duration::from_secs(15);

    /// A stream the daemon closes, its address still accepting connections, is a lost connection, never a stopped
    /// daemon, and the client connects again.
    #[tokio::test]
    async fn a_cut_stream_is_a_lost_connection() {
        let daemon = StandIn::start(vec![Reply::Close(stream(&[1])), Reply::Hold(stream(&[]))]);
        let home = home("cut", &daemon.address, "token");
        let client = Client::start(&home, SILENCE_LIMIT);

        until("connected again after the cut", WAIT, &client, || client.recorder.states().len() >= 3).await;
        let states = client.recorder.states();
        drop(client);
        std::fs::remove_dir_all(&home).ok();

        assert_eq!(states, ["connected", "reconnecting", "connected"]);
    }

    /// A stream that sends nothing, not even a heartbeat, ends as lost once the silence limit passes. The test sets a
    /// limit of its own; the app's stays 45 seconds.
    #[tokio::test]
    async fn a_silent_stream_is_a_lost_connection() {
        assert_eq!(SILENCE_LIMIT, Duration::from_secs(45));
        let daemon = StandIn::start(vec![Reply::Hold(stream(&[]))]);
        let home = home("silent", &daemon.address, "token");
        let client = Client::start(&home, Duration::from_millis(300));

        until("the silent stream ends", WAIT, &client, || client.recorder.states().len() >= 2).await;
        let states = client.recorder.states();
        drop(client);
        std::fs::remove_dir_all(&home).ok();

        assert_eq!(states, ["connected", "reconnecting"]);
    }

    /// With no runtime file, and with one whose address refuses connections, the daemon reads as stopped, never as a
    /// lost connection.
    #[tokio::test]
    async fn a_stopped_daemon_is_stopped() {
        let missing = empty_home("no-runtime");
        let refused = home("refused", &refusing_address(), "token");
        for home in [&missing, &refused] {
            let client = Client::start(home, SILENCE_LIMIT);
            until("the first attempt ends", WAIT, &client, || !client.recorder.states().is_empty()).await;
            // The next attempt, two seconds later, ends the same way.
            tokio::time::sleep(Duration::from_millis(2500)).await;
            let states = client.recorder.states();
            drop(client);
            std::fs::remove_dir_all(home).ok();
            assert_eq!(states, ["daemon_stopped"], "{}", home.display());
        }
    }

    /// A 503 is a lost connection that keeps the cached address and token; a 401 is one too, and the next attempt reads
    /// both again from the HarmFlow folder.
    #[tokio::test]
    async fn an_error_answer_is_a_lost_connection() {
        for (name, answer, tokens) in [
            ("unavailable", UNAVAILABLE, ["Bearer first", "Bearer first"]),
            ("unauthorized", UNAUTHORIZED, ["Bearer first", "Bearer second"]),
        ] {
            let daemon = StandIn::start(vec![Reply::Close(answer.into()), Reply::Hold(stream(&[]))]);
            let home = home(name, &daemon.address, "first");
            let client = Client::start(&home, SILENCE_LIMIT);

            until("the error answer ends the stream", WAIT, &client, || !client.recorder.states().is_empty()).await;
            // A token read again from now on is this one. The next attempt waits a second.
            write_runtime(&home, &daemon.address, "second");
            until("connected on the next attempt", WAIT, &client, || client.recorder.states().len() >= 2).await;
            let states = client.recorder.states();
            drop(client);
            std::fs::remove_dir_all(&home).ok();

            assert_eq!(states, ["reconnecting", "connected"], "{name}");
            let seen: Vec<String> = daemon.seen().into_iter().filter_map(|s| s.token).collect();
            assert_eq!(seen, tokens, "{name}: the tokens of the two attempts");
        }
    }

    /// Once the daemon serves the stream again after a cut, the client asks for what follows the last event it
    /// forwarded, and each event before and after the cut reaches the webview once, in order.
    #[tokio::test]
    async fn the_return_resumes_after_the_last_event() {
        let daemon = StandIn::start(vec![Reply::Close(stream(&[1, 2])), Reply::Hold(stream(&[3, 4]))]);
        let home = home("resume", &daemon.address, "token");
        let client = Client::start(&home, SILENCE_LIMIT);

        until("the events after the cut", WAIT, &client, || client.recorder.event_ids().len() >= 4).await;
        let (states, ids) = (client.recorder.states(), client.recorder.event_ids());
        drop(client);
        std::fs::remove_dir_all(&home).ok();

        assert_eq!(states, ["connected", "reconnecting", "connected"]);
        assert_eq!(ids, [1, 2, 3, 4]);
        let resumed: Vec<Option<String>> = daemon.seen().into_iter().map(|s| s.last_event_id).collect();
        assert_eq!(resumed, [None, Some("2".to_string())], "the Last-Event-ID of each request");
    }

    /// Failed attempts in a row that end the same way, with no success between them, emit their state once.
    #[tokio::test]
    async fn a_state_is_emitted_once() {
        let daemon = StandIn::start(vec![
            Reply::Close(UNAVAILABLE.into()),
            Reply::Close(UNAVAILABLE.into()),
            Reply::Close(UNAVAILABLE.into()),
        ]);
        let home = home("once", &daemon.address, "token");
        let client = Client::start(&home, SILENCE_LIMIT);

        // The third attempt comes a second, then two, after the first; it is held unanswered.
        until("three attempts", WAIT, &client, || daemon.seen().len() >= 3).await;
        tokio::time::sleep(Duration::from_millis(300)).await;
        let states = client.recorder.states();
        drop(client);
        std::fs::remove_dir_all(&home).ok();

        assert_eq!(states, ["reconnecting"]);
    }

    /// A TCP proxy the test controls, in front of the real daemon. While cut, it drops every open connection and closes
    /// each new one as soon as it takes it: the address keeps accepting connections, as a daemon that runs does, and a
    /// connection attempt is neither refused nor left waiting.
    struct Proxy {
        address: String,
        down: Arc<AtomicBool>,
        open: Arc<Mutex<Vec<TcpStream>>>,
    }

    impl Proxy {
        fn start(upstream: String) -> Self {
            let listener = TcpListener::bind("127.0.0.1:0").unwrap();
            let address = listener.local_addr().unwrap().to_string();
            let down = Arc::new(AtomicBool::new(false));
            let open = Arc::new(Mutex::new(Vec::new()));
            let (is_down, sockets) = (down.clone(), open.clone());
            std::thread::spawn(move || {
                for client in listener.incoming() {
                    let Ok(client) = client else { continue };
                    if is_down.load(Ordering::SeqCst) {
                        let _ = client.shutdown(Shutdown::Both);
                        continue;
                    }
                    let Ok(server) = TcpStream::connect(&upstream) else {
                        let _ = client.shutdown(Shutdown::Both);
                        continue;
                    };
                    let mut list = sockets.lock().unwrap();
                    for (mut from, mut to) in [
                        (client.try_clone().unwrap(), server.try_clone().unwrap()),
                        (server.try_clone().unwrap(), client.try_clone().unwrap()),
                    ] {
                        std::thread::spawn(move || {
                            let _ = std::io::copy(&mut from, &mut to);
                            let _ = to.shutdown(Shutdown::Both);
                        });
                    }
                    list.extend([client, server]);
                }
            });
            Proxy { address, down, open }
        }

        fn cut(&self) {
            self.down.store(true, Ordering::SeqCst);
            for socket in self.open.lock().unwrap().drain(..) {
                let _ = socket.shutdown(Shutdown::Both);
            }
        }

        fn restore(&self) {
            self.down.store(false, Ordering::SeqCst);
        }
    }

    /// A real harmflowd under a HarmFlow folder of its own, with the stand-in harnesses; killed once dropped.
    struct RealDaemon {
        home: PathBuf,
        base: String,
        token: String,
        process: std::process::Child,
        http: reqwest::Client,
    }

    impl RealDaemon {
        /// Starts the harmflowd `HARMFLOW_TEST_DAEMON` names, as the interface's tests against a real daemon do. Without
        /// it the test fails: a test that skipped proves nothing.
        async fn start() -> Self {
            let binary = std::env::var_os("HARMFLOW_TEST_DAEMON")
                .filter(|v| !v.is_empty())
                .expect("HARMFLOW_TEST_DAEMON names no harmflowd: run `make test-desktop-daemon`");
            let repo_root = Path::new(env!("CARGO_MANIFEST_DIR")).join("..").join("..");
            let home = empty_home("real-daemon");
            // Nothing of a HarmFlow session this test may run in reaches the daemon, which would record into it.
            let env = std::env::vars().filter(|(k, _)| !k.starts_with("HARMFLOW_"));
            let process = std::process::Command::new(binary)
                .arg("--home")
                .arg(&home)
                .arg("--builtins")
                .arg(&repo_root)
                .env_clear()
                .envs(env)
                .env("HARMFLOW_TOKEN_STORE", "file")
                .env("CODEX_HOME", home.join("codex"))
                .stdin(std::process::Stdio::null())
                .stdout(std::process::Stdio::null())
                .stderr(std::process::Stdio::null())
                .spawn()
                .expect("harmflowd did not start");
            let deadline = tokio::time::Instant::now() + Duration::from_secs(20);
            let address = loop {
                let runtime = std::fs::read_to_string(home.join("run").join("daemon.json")).ok();
                let address = runtime
                    .and_then(|t| serde_json::from_str::<Value>(&t).ok())
                    .and_then(|v| v.get("address").and_then(Value::as_str).map(str::to_string));
                if let Some(address) = address {
                    break address;
                }
                assert!(tokio::time::Instant::now() < deadline, "harmflowd wrote no address within 20 seconds");
                tokio::time::sleep(Duration::from_millis(50)).await;
            };
            let token = std::fs::read_to_string(home.join("run").join("token")).unwrap().trim().to_string();
            let http = reqwest::Client::builder().no_proxy().build().unwrap();
            let daemon = RealDaemon { home, base: format!("http://{address}/api/v1"), token, process, http };
            let fake = repo_root.join("tests").join("integration").join("testdata").join("fake-claude");
            daemon.call(reqwest::Method::PUT, "/harnesses/claude-code", Some(serde_json::json!({ "path": fake }))).await;
            daemon
        }

        fn address(&self) -> String {
            self.base.trim_start_matches("http://").trim_end_matches("/api/v1").to_string()
        }

        /// A request to the daemon's own API, past the proxy: the test's set-up and its independent reads.
        async fn call(&self, method: reqwest::Method, path: &str, body: Option<Value>) -> Value {
            let mut req = self.http.request(method.clone(), format!("{}{path}", self.base)).bearer_auth(&self.token);
            if let Some(body) = body {
                req = req.json(&body);
            }
            let res = req.send().await.unwrap_or_else(|e| panic!("{method} {path}: {e}"));
            let status = res.status();
            let text = res.text().await.unwrap_or_default();
            assert!(status.is_success(), "{method} {path}: {status} {text}");
            if text.is_empty() {
                Value::Null
            } else {
                serde_json::from_str(&text).unwrap()
            }
        }

        /// The execution's events the daemon stored.
        async fn stored_events(&self, id: &str) -> Vec<Value> {
            let res = self.call(reqwest::Method::GET, &format!("/executions/{id}/events?limit=5000"), None).await;
            res.get("events").and_then(Value::as_array).cloned().unwrap_or_default()
        }
    }

    impl Drop for RealDaemon {
        fn drop(&mut self) {
            let _ = self.process.kill();
            let _ = self.process.wait();
            let _ = std::fs::remove_dir_all(&self.home);
        }
    }

    const FAILURE_EVENTS: [&str; 3] = ["execution.failed", "execution.interrupted", "execution.cancelled"];

    fn id_of(event: &Value) -> u64 {
        event.get("id").and_then(Value::as_u64).unwrap_or(0)
    }

    fn type_of(event: &Value) -> &str {
        event.get("type").and_then(Value::as_str).unwrap_or("")
    }

    fn ids_and_types(events: &[Value]) -> Vec<String> {
        events.iter().map(|e| format!("{} {}", id_of(e), type_of(e))).collect()
    }

    /// The shell's stream client against a real harmflowd, each a real process, with a proxy between them that the test
    /// cuts while an execution runs. While the connection is down the client reports a loss, not a stopped daemon, and
    /// forwards no failure; once it returns, the client connects again and forwards the execution's events after the
    /// last one it forwarded, up to its completion. `make test-desktop-daemon` runs it by name, with `--ignored`; the
    /// unit run and its coverage never start a daemon.
    #[tokio::test(flavor = "multi_thread")]
    #[ignore = "needs a real harmflowd: make test-desktop-daemon"]
    async fn the_stream_survives_a_dropped_connection_to_the_real_daemon() {
        let daemon = RealDaemon::start().await;
        let proxy = Proxy::start(daemon.address());
        // The client's own HarmFlow folder names the proxy, with the daemon's token.
        let client_home = home("real-daemon-client", &proxy.address, &daemon.token);
        let client = Client::start(&client_home, SILENCE_LIMIT);
        let last_state = |client: &Client| client.recorder.states().last().cloned();
        until("the client connects", Duration::from_secs(20), &client, || last_state(&client).as_deref() == Some("connected")).await;

        // The stand-in emits until the test releases it, so the cut, the return, and the events after it all fall while
        // the execution runs, however slow the host.
        let yaml = [
            "apiVersion: harmflow/v1alpha1",
            "kind: Task",
            "metadata:",
            "  id: shell-stream",
            "  name: Task shell-stream",
            "spec:",
            "  type: general",
            "  prompt: \"SCENARIO=gated\"",
            "  harness:",
            "    preferred:",
            "      provider: claude-code",
            "  notifications:",
            "    native:",
            "      events: []",
            "",
        ]
        .join("\n");
        daemon.call(reqwest::Method::POST, "/tasks", Some(serde_json::json!({ "yaml": yaml }))).await;
        let run = daemon.call(reqwest::Method::POST, "/tasks/shell-stream/run", None).await;
        let id = run.get("executionId").and_then(Value::as_str).expect("the run gave no executionId").to_string();
        let of_execution = |client: &Client| -> Vec<Value> {
            let events = client.recorder.events.lock().unwrap();
            let found = events.iter().filter(|e| e.get("executionId").and_then(Value::as_str) == Some(id.as_str())).cloned().collect();
            found
        };
        let messages = |events: Vec<Value>| events.iter().filter(|e| type_of(e) == "harness.message").count();
        until("two messages of the execution", Duration::from_secs(20), &client, || messages(of_execution(&client)) >= 2).await;

        // The connection drops, not the daemon: the client reports it lost.
        proxy.cut();
        until("the client reports the loss", Duration::from_secs(30), &client, || {
            last_state(&client).as_deref() == Some("reconnecting")
        })
        .await;
        let noted = of_execution(&client).iter().map(id_of).max().unwrap_or(0);

        // It stays down until the daemon's own API shows a message the client missed, the execution still running.
        let deadline = tokio::time::Instant::now() + Duration::from_secs(20);
        loop {
            let missed: Vec<Value> = daemon.stored_events(&id).await.into_iter().filter(|e| id_of(e) > noted).collect();
            if missed.iter().any(|e| type_of(e) == "harness.message") {
                assert!(!missed.iter().any(|e| type_of(e) == "execution.completed"), "the execution ended while the client was away");
                break;
            }
            assert!(tokio::time::Instant::now() < deadline, "no message of the execution after event {noted} within 20 seconds");
            tokio::time::sleep(Duration::from_millis(100)).await;
        }
        let while_down = client.recorder.states();
        assert_eq!(while_down.last().map(String::as_str), Some("reconnecting"), "the state while the connection is down");
        assert!(!while_down.iter().any(|s| s == "daemon_stopped"), "a dropped connection read as a stopped daemon: {while_down:?}");
        let forwarded_while_down: Vec<Value> = of_execution(&client).into_iter().filter(|e| id_of(e) > noted).collect();
        assert_eq!(ids_and_types(&forwarded_while_down), Vec::<String>::new(), "events forwarded while down");

        // The connection returns while the execution still runs, and the client connects again on its own.
        proxy.restore();
        until("the client connects again", Duration::from_secs(30), &client, || last_state(&client).as_deref() == Some("connected")).await;

        // Released, the execution ends; the client forwards its events up to the completion.
        std::fs::write(daemon.home.join("runs").join(&id).join("release"), "").unwrap();
        until("the execution's completion", Duration::from_secs(180), &client, || {
            of_execution(&client).iter().any(|e| type_of(e) == "execution.completed")
        })
        .await;
        let execution = daemon.call(reqwest::Method::GET, &format!("/executions/{id}"), None).await;
        assert_eq!(execution.get("status").and_then(Value::as_str), Some("completed"), "the daemon's execution: {execution}");

        let stored = daemon.stored_events(&id).await;
        let forwarded = of_execution(&client);
        let states = client.recorder.states();
        drop(client);
        std::fs::remove_dir_all(&client_home).ok();

        assert!(!states.iter().any(|s| s == "daemon_stopped"), "a dropped connection read as a stopped daemon: {states:?}");
        // Each event once, in order, and after the return those the daemon stored after the last one forwarded.
        assert_eq!(ids_and_types(&forwarded), ids_and_types(&stored), "the execution's events the client forwarded");
        let failures: Vec<&str> = forwarded.iter().map(type_of).filter(|t| FAILURE_EVENTS.contains(t)).collect();
        assert_eq!(failures, Vec::<&str>::new(), "failure events forwarded");
    }
}
