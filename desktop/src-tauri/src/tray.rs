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

use crate::daemon::ApiResponse;
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

pub fn create<R: Runtime>(app: &AppHandle<R>) -> tauri::Result<()> {
    let menu = build_menu(app, &[])?;
    TrayIconBuilder::with_id(TRAY_ID)
        .icon(Image::from_bytes(ICON_IDLE)?)
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
    let running = state.tray_running.lock().map(|r| r.clone()).unwrap_or_default();
    let Some(tray) = app.tray_by_id(TRAY_ID) else { return };
    if let Ok(menu) = build_menu(app, &running) {
        let _ = tray.set_menu(Some(menu));
    }
    let icon = if running.is_empty() { ICON_IDLE } else { ICON_EXECUTING };
    if let Ok(image) = Image::from_bytes(icon) {
        let _ = tray.set_icon(Some(image));
        let _ = tray.set_icon_as_template(true);
    }
    #[cfg(target_os = "macos")]
    let _ = tray.set_title(if running.is_empty() { None } else { Some(running.len().to_string()) });
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
    let Some(running) = running_of(&res.body) else { return };
    if let Ok(mut current) = state.tray_running.lock() {
        *current = running;
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
    ask_stop(app, state, count);
}

/// Asks to stop the daemon with the dialog for `count` running executions, and sends the stop once the user confirms.
fn ask_stop<R: Runtime>(app: &AppHandle<R>, state: Arc<AppState>, count: usize) {
    let dialog = stop_dialog(count, |key, fallback| state.label(key, fallback));
    let handle = app.clone();
    app.dialog()
        .message(dialog.message)
        .title(dialog.title)
        .kind(MessageDialogKind::Warning)
        .buttons(MessageDialogButtons::OkCancelCustom(dialog.ok, dialog.cancel))
        .show(move |confirmed| {
            let Some(request) = stop_request(count, confirmed) else { return };
            tauri::async_runtime::spawn(stop(handle, state, request));
        });
}

/// Sends the stop and follows the daemon's answer: a refusal is never dropped (harmflow-app#630).
async fn stop<R: Runtime>(app: AppHandle<R>, state: Arc<AppState>, request: Request) {
    let forced = request.body.get("force").and_then(Value::as_bool).unwrap_or(false);
    let res = state.daemon.request(request.method, request.path, Some(request.body)).await;
    match after_stop(&res, forced) {
        AfterStop::Stopping => {}
        AfterStop::ConfirmRunning(count) => ask_stop(&app, state, count),
        AfterStop::Failed(message) => {
            let ok = state.label("stopFailedOk", "OK");
            app.dialog()
                .message(message)
                .title(state.label("stopFailedTitle", "The daemon did not stop"))
                .kind(MessageDialogKind::Warning)
                .buttons(MessageDialogButtons::OkCustom(ok))
                .show(|_| {});
        }
    }
}

/// What the tray does after the daemon answers a stop.
#[derive(Debug, PartialEq)]
enum AfterStop {
    /// The daemon accepted the stop.
    Stopping,
    /// The daemon refused an unforced stop while executions run: ask again with its count, then force the stop.
    ConfirmRunning(usize),
    /// Any other answer, or no answer: show the daemon's message, or the request's error.
    Failed(String),
}

fn after_stop(res: &ApiResponse, forced: bool) -> AfterStop {
    if (200..300).contains(&res.status) {
        return AfterStop::Stopping;
    }
    let error = res.body.get("error");
    let code = error.and_then(|e| e.get("code")).and_then(Value::as_str);
    let count = error
        .and_then(|e| e.get("params"))
        .and_then(|p| p.get("count"))
        .and_then(Value::as_str)
        .and_then(|count| count.parse::<usize>().ok())
        .filter(|count| *count > 0);
    if let (false, Some("executions_running"), Some(count)) = (forced, code, count) {
        return AfterStop::ConfirmRunning(count);
    }
    let message = match error.and_then(|e| e.get("message")).and_then(Value::as_str) {
        Some(message) if !message.is_empty() => message.to_string(),
        _ => match &res.body {
            Value::String(text) if !text.is_empty() => text.clone(),
            _ => format!("The daemon answered HTTP {}.", res.status),
        },
    };
    AfterStop::Failed(message)
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

    fn answer(status: u16, body: Value) -> ApiResponse {
        ApiResponse { status, body }
    }

    fn refused_running(count: &str) -> ApiResponse {
        answer(
            409,
            json!({ "error": {
                "code": "executions_running",
                "message": format!("{count} executions are running; confirm to stop the daemon and cancel them"),
                "messageKey": "executions_running",
                "params": { "count": count },
            } }),
        )
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
    fn an_accepted_stop_shows_nothing() {
        assert_eq!(after_stop(&answer(202, Value::Null), false), AfterStop::Stopping);
        assert_eq!(after_stop(&answer(202, Value::Null), true), AfterStop::Stopping);
    }

    #[test]
    fn a_stop_refused_for_running_executions_asks_again_with_the_daemons_count() {
        // The tray's own list read no execution, so it sent no force; the daemon counted two.
        assert_eq!(after_stop(&refused_running("2"), false), AfterStop::ConfirmRunning(2));
        // The dialog asked again names the daemon's count and, confirmed, forces the stop.
        assert_eq!(
            stop_dialog(2, defaults).message,
            "2 executions are running. Stopping the daemon now interrupts them."
        );
        assert_eq!(stop_request(2, true).map(|r| r.body), Some(json!({ "force": true })));
    }

    #[test]
    fn a_forced_stop_that_is_still_refused_shows_the_daemons_message() {
        // A confirmed stop is never asked again in a loop.
        assert_eq!(
            after_stop(&refused_running("1"), true),
            AfterStop::Failed("1 executions are running; confirm to stop the daemon and cancel them".into())
        );
    }

    #[test]
    fn a_refusal_for_another_reason_shows_the_daemons_message() {
        let res = answer(500, json!({ "error": { "code": "internal", "message": "the event log is full" } }));
        assert_eq!(after_stop(&res, false), AfterStop::Failed("the event log is full".into()));
    }

    #[test]
    fn a_daemon_that_cannot_be_reached_shows_the_requests_error() {
        // DaemonClient::request answers status 0 with the request's error when nothing answers.
        let res = answer(0, json!({ "error": { "code": "internal", "message": "error sending request" } }));
        assert_eq!(after_stop(&res, false), AfterStop::Failed("error sending request".into()));
    }

    #[test]
    fn an_answer_with_no_message_still_says_what_happened() {
        assert_eq!(after_stop(&answer(502, json!("Bad Gateway")), false), AfterStop::Failed("Bad Gateway".into()));
        assert_eq!(
            after_stop(&answer(409, json!({ "error": { "code": "executions_running" } })), false),
            AfterStop::Failed("The daemon answered HTTP 409.".into())
        );
    }
}
