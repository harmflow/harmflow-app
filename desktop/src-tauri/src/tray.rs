//! The menu-bar tray. Its icon changes only while an execution actually runs — queued work and idleness never change
//! it — and it lists the running executions and a protected daemon stop.

use std::sync::atomic::Ordering;
use std::sync::Arc;
use std::time::Duration;

use serde_json::Value;
use tauri::image::Image;
use tauri::menu::{Menu, MenuItem, PredefinedMenuItem};
use tauri::tray::TrayIconBuilder;
use tauri::{AppHandle, Manager, Runtime};
use tauri_plugin_dialog::{DialogExt, MessageDialogButtons, MessageDialogKind};

use crate::state::{ActiveExecution, AppState};

const TRAY_ID: &str = "main";
/// The ids of the tray's items, which `menu::on_event` handles with the app menu's.
pub const OPEN: &str = "open";
pub const STOP: &str = "stop";
pub const QUIT: &str = "quit";
pub const EXECUTION_PREFIX: &str = "exec:";
const ACTIVE_STATUSES: [&str; 5] = ["preparing", "running", "switching_harness", "testing", "publishing"];
const MAX_LISTED: usize = 5;

static ICON_IDLE: &[u8] = include_bytes!("../icons/tray-idle.png");
static ICON_EXECUTING: &[u8] = include_bytes!("../icons/tray-executing.png");

/// The tray's icon.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Icon {
    Idle,
    Executing,
}

impl Icon {
    fn bytes(self) -> &'static [u8] {
        match self {
            Icon::Idle => ICON_IDLE,
            Icon::Executing => ICON_EXECUTING,
        }
    }
}

/// What the tray shows: its icon, the count beside it, and the executions its menu lists. Only the executions that
/// actually run count; queued and ended ones change nothing.
#[derive(Debug)]
struct TrayState {
    icon: Icon,
    /// The count the macOS build sets as the tray's title; `None` while nothing runs.
    #[cfg_attr(not(target_os = "macos"), allow(dead_code))]
    title: Option<String>,
    running: Vec<ActiveExecution>,
}

impl TrayState {
    /// The state for executions already known to run, as `AppState::tray_running` keeps them.
    fn of_running(running: Vec<ActiveExecution>) -> Self {
        let (icon, title) =
            if running.is_empty() { (Icon::Idle, None) } else { (Icon::Executing, Some(running.len().to_string())) };
        Self { icon, title, running }
    }

    /// The state for the daemon's answer to `GET /executions?active=true`; `None` when the answer holds no list.
    fn from_answer(answer: &Value) -> Option<Self> {
        let running = answer
            .as_array()?
            .iter()
            .filter(|e| e.get("status").and_then(Value::as_str).is_some_and(|s| ACTIVE_STATUSES.contains(&s)))
            .map(|e| {
                let task = e.get("taskName").and_then(Value::as_str).unwrap_or("—");
                let label = match e.get("workItem").and_then(Value::as_str) {
                    Some(item) => format!("{task} — {item}"),
                    None => task.to_string(),
                };
                ActiveExecution { id: e.get("id").and_then(Value::as_str).unwrap_or_default().to_string(), label }
            })
            .collect();
        Some(Self::of_running(running))
    }

    /// The executions the menu lists by name: the first `MAX_LISTED` of those that run.
    fn listed(&self) -> &[ActiveExecution] {
        &self.running[..self.running.len().min(MAX_LISTED)]
    }
}

pub fn create<R: Runtime>(app: &AppHandle<R>) -> tauri::Result<()> {
    let menu = build_menu(app, &TrayState::of_running(Vec::new()))?;
    TrayIconBuilder::with_id(TRAY_ID)
        .icon(Image::from_bytes(Icon::Idle.bytes())?)
        .icon_as_template(true)
        .tooltip("HarmFlow")
        .menu(&menu)
        .show_menu_on_left_click(true)
        .build(app)?;
    Ok(())
}

fn build_menu<R: Runtime>(app: &AppHandle<R>, shown: &TrayState) -> tauri::Result<Menu<R>> {
    let state = app.state::<Arc<AppState>>();
    let menu = Menu::new(app)?;
    let header = if shown.running.is_empty() {
        state.label("idle", "Nothing running")
    } else {
        state.label("active", "{{count}} running").replace("{{count}}", &shown.running.len().to_string())
    };
    menu.append(&MenuItem::with_id(app, "header", header, false, None::<&str>)?)?;
    for exec in shown.listed() {
        let id = format!("{EXECUTION_PREFIX}{}", exec.id);
        menu.append(&MenuItem::with_id(app, id, format!("  {}", exec.label), true, None::<&str>)?)?;
    }
    menu.append(&PredefinedMenuItem::separator(app)?)?;
    menu.append(&MenuItem::with_id(app, OPEN, state.label("open", "Open dashboard"), true, None::<&str>)?)?;
    menu.append(&PredefinedMenuItem::separator(app)?)?;
    menu.append(&MenuItem::with_id(app, STOP, state.label("stop", "Stop daemon…"), true, None::<&str>)?)?;
    menu.append(&MenuItem::with_id(app, QUIT, state.label("quit", "Quit interface"), true, None::<&str>)?)?;
    Ok(menu)
}

/// Rebuilds the menu and the icon from the executions the state holds.
pub fn render<R: Runtime>(app: &AppHandle<R>) {
    let state = app.state::<Arc<AppState>>();
    let shown = TrayState::of_running(state.tray_running.lock().map(|r| r.clone()).unwrap_or_default());
    let Some(tray) = app.tray_by_id(TRAY_ID) else { return };
    if let Ok(menu) = build_menu(app, &shown) {
        let _ = tray.set_menu(Some(menu));
    }
    if let Ok(image) = Image::from_bytes(shown.icon.bytes()) {
        let _ = tray.set_icon(Some(image));
        let _ = tray.set_icon_as_template(true);
    }
    #[cfg(target_os = "macos")]
    let _ = tray.set_title(shown.title.as_deref());
    let _ = tray.set_visible(state.tray_enabled());
}

pub fn set_visible<R: Runtime>(app: &AppHandle<R>, visible: bool) {
    if let Some(tray) = app.tray_by_id(TRAY_ID) {
        let _ = tray.set_visible(visible);
    }
}

/// Called for every event of the stream: execution changes refresh the tray from the daemon, so it stays right even
/// with no window open.
pub fn observe_event(app: &AppHandle, event: &Value) {
    let ty = event.get("type").and_then(Value::as_str).unwrap_or_default();
    if ty.starts_with("execution.") || ty == "daemon.started" {
        schedule_refresh(app.clone());
    }
}

pub fn schedule_refresh(app: AppHandle) {
    let state = app.state::<Arc<AppState>>().inner().clone();
    if state.tray_refresh_pending.swap(true, Ordering::SeqCst) {
        return;
    }
    tauri::async_runtime::spawn(async move {
        tokio::time::sleep(Duration::from_millis(300)).await;
        state.tray_refresh_pending.store(false, Ordering::SeqCst);
        refresh(&app, &state).await;
    });
}

async fn refresh(app: &AppHandle, state: &AppState) {
    let res = state.daemon.request("GET", "/executions?active=true&limit=50", None).await;
    let Some(shown) = TrayState::from_answer(&res.body) else { return };
    if let Ok(mut current) = state.tray_running.lock() {
        *current = shown.running;
    }
    render(app);
}

/// Stopping the daemon is its own command, and asks first when executions run.
pub fn confirm_stop<R: Runtime>(app: &AppHandle<R>) {
    let state = app.state::<Arc<AppState>>().inner().clone();
    let count = state.tray_running.lock().map(|r| r.len()).unwrap_or(0);
    let message = if count > 0 {
        state
            .label("stopConfirmRunning", "{{count}} executions are running. Stopping the daemon now interrupts them.")
            .replace("{{count}}", &count.to_string())
    } else {
        state.label("stopConfirm", "No schedule fires while the daemon is stopped.")
    };
    let ok = state.label("stopOk", "Stop daemon");
    let cancel = state.label("cancel", "Cancel");
    let app = app.clone();
    app.dialog()
        .message(message)
        .title(state.label("stopTitle", "Stop the daemon?"))
        .kind(MessageDialogKind::Warning)
        .buttons(MessageDialogButtons::OkCancelCustom(ok, cancel))
        .show(move |confirmed| {
            if !confirmed {
                return;
            }
            let state = state.clone();
            tauri::async_runtime::spawn(async move {
                let _ = state
                    .daemon
                    .request("POST", "/daemon/stop", Some(serde_json::json!({ "force": count > 0 })))
                    .await;
            });
        });
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    /// An execution as `GET /executions?active=true` lists it.
    fn execution(id: &str, status: &str) -> Value {
        json!({ "id": id, "status": status, "taskName": "Nightly", "workItem": null })
    }

    fn state_of(executions: Vec<Value>) -> TrayState {
        TrayState::from_answer(&Value::Array(executions)).expect("a list is an answer")
    }

    fn assert_idle(shown: &TrayState, case: &str) {
        assert_eq!(shown.icon, Icon::Idle, "{case}");
        assert_eq!(shown.title, None, "{case}");
        assert!(shown.listed().is_empty(), "{case}");
    }

    #[test]
    fn queued_work_and_an_empty_list_keep_the_idle_icon() {
        assert_idle(&state_of(vec![]), "empty");
        assert_idle(&state_of(vec![execution("exec-1", "queued"), execution("exec-2", "queued")]), "queued");
    }

    #[test]
    fn each_active_status_changes_the_icon() {
        for status in ["preparing", "running", "switching_harness", "testing", "publishing"] {
            let shown = state_of(vec![execution("exec-1", status)]);
            assert_eq!(shown.icon, Icon::Executing, "{status}");
            assert_eq!(shown.title.as_deref(), Some("1"), "{status}");
            assert_eq!(shown.listed().len(), 1, "{status}");
        }
    }

    #[test]
    fn only_the_running_executions_count_and_are_listed() {
        let shown =
            state_of(vec![execution("exec-1", "running"), execution("exec-2", "queued"), execution("exec-3", "running")]);
        assert_eq!(shown.icon, Icon::Executing);
        assert_eq!(shown.title.as_deref(), Some("2"));
        let listed: Vec<&str> = shown.listed().iter().map(|e| e.id.as_str()).collect();
        assert_eq!(listed, ["exec-1", "exec-3"]);
    }

    #[test]
    fn ended_executions_leave_the_icon_idle() {
        for status in ["completed", "failed", "cancelled", "interrupted", "no_work", "needs_attention"] {
            assert_idle(&state_of(vec![execution("exec-1", status)]), status);
        }
    }

    #[test]
    fn an_answer_without_a_list_changes_nothing() {
        assert!(TrayState::from_answer(&json!({ "error": "unavailable" })).is_none());
        assert!(TrayState::from_answer(&Value::Null).is_none());
    }

    #[test]
    fn the_kept_executions_give_the_same_state_as_the_answer() {
        // `render` rebuilds the state from what `refresh` kept, so it shows what the answer gave.
        let answer = state_of(vec![execution("exec-1", "testing"), execution("exec-2", "queued")]);
        let kept = TrayState::of_running(answer.running.clone());
        assert_eq!(kept.icon, answer.icon);
        assert_eq!(kept.title, answer.title);
        assert_eq!(kept.running.len(), answer.running.len());
        assert_idle(&TrayState::of_running(Vec::new()), "nothing kept");
    }

    #[test]
    fn the_menu_lists_at_most_five_by_name() {
        let shown = state_of((1..=7).map(|n| execution(&format!("exec-{n}"), "running")).collect());
        assert_eq!(shown.title.as_deref(), Some("7"));
        assert_eq!(shown.listed().len(), MAX_LISTED);
    }

    #[test]
    fn a_listed_execution_reads_as_its_task_and_work_item() {
        let shown = state_of(vec![
            json!({ "id": "exec-1", "status": "running", "taskName": "Queue", "workItem": "owner/repo#12" }),
            json!({ "id": "exec-2", "status": "running", "taskName": "Nightly" }),
        ]);
        let labels: Vec<&str> = shown.listed().iter().map(|e| e.label.as_str()).collect();
        assert_eq!(labels, ["Queue — owner/repo#12", "Nightly"]);
    }
}
