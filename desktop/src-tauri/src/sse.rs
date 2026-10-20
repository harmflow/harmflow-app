//! The live event stream (ADR-0004): one SSE connection to the daemon, forwarded to the webview as Tauri events.
//! It reconnects on its own and resumes after the last event id it delivered, so nothing is lost while the window is
//! closed or the daemon restarts (FS-008 FR-003, FR-007).

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

fn set_connection(app: &AppHandle, state: &AppState, value: &str) {
    if state.set_connection(value) {
        let _ = app.emit(CONNECTION, serde_json::json!({ "state": value }));
    }
}

/// Runs for the life of the app.
pub fn spawn(app: AppHandle) {
    tauri::async_runtime::spawn(async move {
        let state: Arc<AppState> = app.state::<Arc<AppState>>().inner().clone();
        let mut backoff = Duration::from_millis(500);
        loop {
            match stream_once(&app, &state).await {
                Ok(()) => backoff = Duration::from_millis(500),
                Err(StreamEnd::DaemonStopped) => {
                    set_connection(&app, &state, "daemon_stopped");
                    backoff = Duration::from_secs(2);
                }
                Err(StreamEnd::Lost) => {
                    set_connection(&app, &state, "reconnecting");
                    backoff = (backoff * 2).min(Duration::from_secs(10));
                }
            }
            tokio::time::sleep(backoff).await;
        }
    });
}

enum StreamEnd {
    DaemonStopped,
    Lost,
}

async fn stream_once(app: &AppHandle, state: &AppState) -> Result<(), StreamEnd> {
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
    let res = match req.send().await {
        Ok(r) => r,
        Err(e) => {
            client.invalidate();
            return Err(if e.is_connect() { StreamEnd::DaemonStopped } else { StreamEnd::Lost });
        }
    };
    if !res.status().is_success() {
        client.invalidate();
        return Err(StreamEnd::Lost);
    }
    set_connection(app, state, "connected");

    let mut parser = SseParser::default();
    let mut body = res.bytes_stream();
    loop {
        let next = match tokio::time::timeout(SILENCE_LIMIT, body.next()).await {
            Ok(n) => n,
            Err(_) => return Err(StreamEnd::Lost),
        };
        let chunk = match next {
            Some(Ok(bytes)) => bytes,
            Some(Err(_)) | None => {
                client.invalidate();
                return Err(StreamEnd::Lost);
            }
        };
        for msg in parser.push(&String::from_utf8_lossy(&chunk)) {
            if msg.event.as_deref() == Some("replay_gap") {
                let _ = app.emit(REPLAY_GAP, ());
                continue;
            }
            let Ok(event) = serde_json::from_str::<Value>(&msg.data) else { continue };
            if let Some(id) = msg.id.or_else(|| event.get("id").and_then(Value::as_u64)) {
                state.record_event_id(id);
            }
            crate::tray::observe_event(app, &event);
            let _ = app.emit(EVENT, event);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

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
}
