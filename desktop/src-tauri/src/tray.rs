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

/// What the tray shows: its icon, the count beside it, and the executions its menu lists, as `menu_entries` lists
/// them. Only the executions that actually run count; queued and ended ones change nothing.
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

    /// The state for the daemon's answer to `GET /executions?active=true`, with the executions `running_of` reads
    /// from it; `None` when the answer holds no list.
    fn from_answer(answer: &Value) -> Option<Self> {
        running_of(answer).map(Self::of_running)
    }
}

pub fn create<R: Runtime>(app: &AppHandle<R>) -> tauri::Result<()> {
    let menu = build_menu(app, &[])?;
    TrayIconBuilder::with_id(TRAY_ID)
        .icon(Image::from_bytes(Icon::Idle.bytes())?)
        .icon_as_template(true)
        .tooltip("HarmFlow")
        .menu(&menu)
        .show_menu_on_left_click(true)
        .build(app)?;
    Ok(())
}

/// One entry of the tray's menu.
#[derive(Debug, Clone, PartialEq, Eq)]
enum Entry {
    Item { id: String, text: String, enabled: bool },
    Separator,
}

/// The tray's menu for the running executions, in order: a disabled header with their count, the first `MAX_LISTED`
/// of them, "Open dashboard", then "Stop daemon…" and "Quit interface". `label` gives a text by its key, or the
/// fallback when the interface has sent none, as `AppState::label` does.
fn menu_entries(running: &[ActiveExecution], label: impl Fn(&str, &str) -> String) -> Vec<Entry> {
    let item = |id: &str, text: String, enabled: bool| Entry::Item { id: id.to_string(), text, enabled };
    let header = if running.is_empty() {
        label("idle", "Nothing running")
    } else {
        label("active", "{{count}} running").replace("{{count}}", &running.len().to_string())
    };
    let mut entries = vec![item("header", header, false)];
    for exec in running.iter().take(MAX_LISTED) {
        entries.push(item(&format!("{EXECUTION_PREFIX}{}", exec.id), format!("  {}", exec.label), true));
    }
    entries.extend([
        Entry::Separator,
        item(OPEN, label("open", "Open dashboard"), true),
        Entry::Separator,
        item(STOP, label("stop", "Stop daemon…"), true),
        item(QUIT, label("quit", "Quit interface"), true),
    ]);
    entries
}

fn build_menu<R: Runtime>(app: &AppHandle<R>, running: &[ActiveExecution]) -> tauri::Result<Menu<R>> {
    let state = app.state::<Arc<AppState>>();
    let menu = Menu::new(app)?;
    for entry in menu_entries(running, |key, fallback| state.label(key, fallback)) {
        match entry {
            Entry::Item { id, text, enabled } => {
                menu.append(&MenuItem::with_id(app, id, text, enabled, None::<&str>)?)?
            }
            Entry::Separator => menu.append(&PredefinedMenuItem::separator(app)?)?,
        }
    }
    Ok(menu)
}

/// Rebuilds the menu and the icon from the executions the state holds.
pub fn render<R: Runtime>(app: &AppHandle<R>) {
    let state = app.state::<Arc<AppState>>();
    let shown = TrayState::of_running(state.tray_running.lock().map(|r| r.clone()).unwrap_or_default());
    let Some(tray) = app.tray_by_id(TRAY_ID) else { return };
    if let Ok(menu) = build_menu(app, &shown.running) {
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

/// The running executions in the daemon's answer to `GET /executions?active=true`, each labeled "task — work item",
/// or the task alone without a work item; `None` when the answer holds no list.
fn running_of(answer: &Value) -> Option<Vec<ActiveExecution>> {
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
    Some(running)
}

async fn refresh(app: &AppHandle, state: &AppState) {
    let res = state.daemon.request("GET", "/executions?active=true&limit=50", None).await;
    let Some(shown) = TrayState::from_answer(&res.body) else { return };
    if let Ok(mut current) = state.tray_running.lock() {
        *current = shown.running;
    }
    render(app);
}

/// The dialog a stop shows before anything is sent.
#[derive(Debug, PartialEq, Eq)]
struct StopDialog {
    title: String,
    message: String,
    ok: String,
    cancel: String,
}

/// The dialog for `count` running executions: it names them when there are some, and says no schedule fires while
/// the daemon is stopped when there are none. `label` is as for `menu_entries`.
fn stop_dialog(count: usize, label: impl Fn(&str, &str) -> String) -> StopDialog {
    let message = if count > 0 {
        label("stopConfirmRunning", "{{count}} executions are running. Stopping the daemon now interrupts them.")
            .replace("{{count}}", &count.to_string())
    } else {
        label("stopConfirm", "No schedule fires while the daemon is stopped.")
    };
    StopDialog {
        title: label("stopTitle", "Stop the daemon?"),
        message,
        ok: label("stopOk", "Stop daemon"),
        cancel: label("cancel", "Cancel"),
    }
}

/// A request to the daemon.
#[derive(Debug, PartialEq)]
struct Request {
    method: &'static str,
    path: &'static str,
    body: Value,
}

/// The request the user's answer sends: none when declined, a stop forced when executions run when confirmed.
fn stop_request(count: usize, confirmed: bool) -> Option<Request> {
    confirmed.then(|| Request { method: "POST", path: "/daemon/stop", body: serde_json::json!({ "force": count > 0 }) })
}

/// Stopping the daemon is its own command, and always asks first.
pub fn confirm_stop<R: Runtime>(app: &AppHandle<R>) {
    let state = app.state::<Arc<AppState>>().inner().clone();
    let count = state.tray_running.lock().map(|r| r.len()).unwrap_or(0);
    let dialog = stop_dialog(count, |key, fallback| state.label(key, fallback));
    let app = app.clone();
    app.dialog()
        .message(dialog.message)
        .title(dialog.title)
        .kind(MessageDialogKind::Warning)
        .buttons(MessageDialogButtons::OkCancelCustom(dialog.ok, dialog.cancel))
        .show(move |confirmed| {
            let Some(request) = stop_request(count, confirmed) else { return };
            let state = state.clone();
            tauri::async_runtime::spawn(async move {
                let _ = state.daemon.request(request.method, request.path, Some(request.body)).await;
            });
        });
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    /// The texts when the interface has sent none, as `AppState::label` gives them.
    fn defaults(_key: &str, fallback: &str) -> String {
        fallback.to_string()
    }

    /// The menu's items, separators aside: their id, text, and whether they are enabled.
    fn items(entries: &[Entry]) -> Vec<(&str, &str, bool)> {
        entries
            .iter()
            .filter_map(|e| match e {
                Entry::Item { id, text, enabled } => Some((id.as_str(), text.as_str(), *enabled)),
                Entry::Separator => None,
            })
            .collect()
    }

    fn running(n: usize) -> Vec<ActiveExecution> {
        (1..=n).map(|i| ActiveExecution { id: format!("exec-{i}"), label: format!("Task {i}") }).collect()
    }

    /// An execution as `GET /executions?active=true` lists it.
    fn execution(id: &str, status: &str) -> Value {
        json!({ "id": id, "status": status, "taskName": "Nightly", "workItem": null })
    }

    fn state_of(executions: Vec<Value>) -> TrayState {
        TrayState::from_answer(&Value::Array(executions)).expect("a list is an answer")
    }

    /// The ids of the executions the menu lists by name for `running`.
    fn listed_ids(running: &[ActiveExecution]) -> Vec<String> {
        items(&menu_entries(running, defaults))
            .iter()
            .filter_map(|(id, _, _)| id.strip_prefix(EXECUTION_PREFIX))
            .map(str::to_string)
            .collect()
    }

    fn assert_idle(shown: &TrayState, case: &str) {
        assert_eq!(shown.icon, Icon::Idle, "{case}");
        assert_eq!(shown.title, None, "{case}");
        assert!(listed_ids(&shown.running).is_empty(), "{case}");
    }

    #[test]
    fn with_nothing_running_the_menu_holds_its_fixed_items() {
        assert_eq!(
            items(&menu_entries(&[], defaults)),
            [
                ("header", "Nothing running", false),
                (OPEN, "Open dashboard", true),
                (STOP, "Stop daemon…", true),
                (QUIT, "Quit interface", true),
            ]
        );
    }

    #[test]
    fn running_executions_follow_the_header_before_open_dashboard() {
        let answer = json!([
            { "id": "exec-1", "status": "running", "taskName": "Queue", "workItem": "owner/repo#12" },
            { "id": "exec-2", "status": "testing", "taskName": "Nightly", "workItem": null },
        ]);
        let running = running_of(&answer).expect("a list is an answer");
        assert_eq!(
            items(&menu_entries(&running, defaults)),
            [
                ("header", "2 running", false),
                ("exec:exec-1", "  Queue — owner/repo#12", true),
                ("exec:exec-2", "  Nightly", true),
                (OPEN, "Open dashboard", true),
                (STOP, "Stop daemon…", true),
                (QUIT, "Quit interface", true),
            ]
        );
    }

    #[test]
    fn stop_daemon_and_quit_interface_are_two_items() {
        let entries = menu_entries(&[], defaults);
        let ids: Vec<&str> = items(&entries).iter().map(|(id, _, _)| *id).collect();
        assert_ne!(STOP, QUIT);
        assert_eq!(ids.iter().filter(|id| **id == STOP).count(), 1);
        assert_eq!(ids.iter().filter(|id| **id == QUIT).count(), 1);
    }

    #[test]
    fn the_menu_lists_the_first_five_of_seven() {
        let entries = menu_entries(&running(7), defaults);
        let items = items(&entries);
        assert_eq!(items[0], ("header", "7 running", false));
        let listed: Vec<&str> =
            items.iter().filter_map(|(id, _, _)| id.strip_prefix(EXECUTION_PREFIX)).collect();
        assert_eq!(listed, ["exec-1", "exec-2", "exec-3", "exec-4", "exec-5"]);
    }

    #[test]
    fn a_stop_with_an_execution_running_asks_and_names_it() {
        let dialog = stop_dialog(1, defaults);
        assert_eq!(dialog.title, "Stop the daemon?");
        assert_eq!(dialog.message, "1 executions are running. Stopping the daemon now interrupts them.");
        assert_eq!((dialog.ok.as_str(), dialog.cancel.as_str()), ("Stop daemon", "Cancel"));
        assert_eq!(stop_request(1, false), None);
        assert_eq!(
            stop_request(1, true),
            Some(Request { method: "POST", path: "/daemon/stop", body: json!({ "force": true }) })
        );
    }

    #[test]
    fn a_stop_with_nothing_running_still_asks() {
        let dialog = stop_dialog(0, defaults);
        assert_eq!(dialog.message, "No schedule fires while the daemon is stopped.");
        assert_eq!(stop_request(0, false), None);
        assert_eq!(
            stop_request(0, true),
            Some(Request { method: "POST", path: "/daemon/stop", body: json!({ "force": false }) })
        );
    }

    #[test]
    fn the_interfaces_texts_replace_the_defaults() {
        let label = |key: &str, fallback: &str| match key {
            "active" => "{{count}} em execução".to_string(),
            "stopConfirmRunning" => "{{count}} execuções em andamento.".to_string(),
            _ => fallback.to_string(),
        };
        assert_eq!(items(&menu_entries(&running(3), label))[0].1, "3 em execução");
        assert_eq!(stop_dialog(3, label).message, "3 execuções em andamento.");
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
            assert_eq!(listed_ids(&shown.running).len(), 1, "{status}");
        }
    }

    #[test]
    fn only_the_running_executions_count_and_are_listed() {
        let shown =
            state_of(vec![execution("exec-1", "running"), execution("exec-2", "queued"), execution("exec-3", "running")]);
        assert_eq!(shown.icon, Icon::Executing);
        assert_eq!(shown.title.as_deref(), Some("2"));
        assert_eq!(listed_ids(&shown.running), ["exec-1", "exec-3"]);
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
        assert_eq!(listed_ids(&shown.running).len(), MAX_LISTED);
    }

    #[test]
    fn a_listed_execution_reads_as_its_task_and_work_item() {
        let shown = state_of(vec![
            json!({ "id": "exec-1", "status": "running", "taskName": "Queue", "workItem": "owner/repo#12" }),
            json!({ "id": "exec-2", "status": "running", "taskName": "Nightly" }),
        ]);
        let labels: Vec<&str> = shown.running.iter().map(|e| e.label.as_str()).collect();
        assert_eq!(labels, ["Queue — owner/repo#12", "Nightly"]);
    }
}
