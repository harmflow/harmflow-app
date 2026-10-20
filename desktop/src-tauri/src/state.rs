//! State shared by the commands, the event stream, and the tray.

use std::collections::HashMap;
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::Mutex;

use serde::Deserialize;

use crate::daemon::DaemonClient;

/// An execution the tray lists while it runs.
#[derive(Debug, Clone, Deserialize)]
pub struct ActiveExecution {
    pub id: String,
    pub label: String,
}

pub struct AppState {
    pub daemon: DaemonClient,
    connection: Mutex<String>,
    last_event_id: AtomicU64,
    tray_enabled: AtomicBool,
    pub tray_running: Mutex<Vec<ActiveExecution>>,
    /// Tray texts in the interface's language, sent by the webview; English until it does.
    pub tray_labels: Mutex<HashMap<String, String>>,
    pub tray_refresh_pending: AtomicBool,
}

impl AppState {
    pub fn new() -> Self {
        Self {
            daemon: DaemonClient::new(),
            connection: Mutex::new("connecting".into()),
            last_event_id: AtomicU64::new(0),
            tray_enabled: AtomicBool::new(true),
            tray_running: Mutex::new(Vec::new()),
            tray_labels: Mutex::new(HashMap::new()),
            tray_refresh_pending: AtomicBool::new(false),
        }
    }

    /// Records the connection state; true when it changed.
    pub fn set_connection(&self, value: &str) -> bool {
        match self.connection.lock() {
            Ok(mut current) if *current != value => {
                *current = value.to_string();
                true
            }
            _ => false,
        }
    }

    pub fn connection(&self) -> String {
        self.connection.lock().map(|c| c.clone()).unwrap_or_else(|_| "connecting".into())
    }

    pub fn last_event_id(&self) -> u64 {
        self.last_event_id.load(Ordering::SeqCst)
    }

    pub fn record_event_id(&self, id: u64) {
        self.last_event_id.fetch_max(id, Ordering::SeqCst);
    }

    pub fn tray_enabled(&self) -> bool {
        self.tray_enabled.load(Ordering::SeqCst)
    }

    pub fn set_tray_enabled(&self, enabled: bool) {
        self.tray_enabled.store(enabled, Ordering::SeqCst);
    }

    pub fn label(&self, key: &str, fallback: &str) -> String {
        self.tray_labels
            .lock()
            .ok()
            .and_then(|l| l.get(key).cloned())
            .unwrap_or_else(|| fallback.to_string())
    }
}
