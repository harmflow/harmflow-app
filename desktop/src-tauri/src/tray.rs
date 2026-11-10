//! The menu-bar tray. Its icon changes only while an execution actually runs — queued work and idleness never change
//! it — and it lists the running executions and a daemon item that follows the daemon's state: a protected stop while
//! it runs, a start while it is stopped, and a disabled item while it is alive but not answering.

use std::sync::atomic::Ordering;
use std::sync::Arc;
use std::time::Duration;

use serde_json::Value;
use tauri::image::Image;
use tauri::menu::{Menu, MenuItem, PredefinedMenuItem};
use tauri::tray::TrayIconBuilder;
use tauri::{AppHandle, Manager, Runtime};
use tauri_plugin_dialog::{DialogExt, MessageDialogButtons, MessageDialogKind, MessageDialogResult};

use crate::daemon::{ApiResponse, DaemonStatus, Sent};
use crate::state::{ActiveExecution, AppState, DaemonState};

pub(crate) const TRAY_ID: &str = "main";
/// The ids of the tray's items, which `menu::on_event` handles with the app menu's.
pub const OPEN: &str = "open";
pub const STOP: &str = "stop";
pub const START: &str = "start";
/// The disabled daemon item while the daemon is not answering, which no action takes.
pub const NOT_ANSWERING: &str = "not-answering";
pub const QUIT: &str = "quit";
pub const EXECUTION_PREFIX: &str = "exec:";
const ACTIVE_STATUSES: [&str; 5] = ["preparing", "running", "switching_harness", "testing", "publishing"];
const MAX_LISTED: usize = 5;

pub(crate) static ICON_IDLE: &[u8] = include_bytes!("../icons/tray-idle.png");
pub(crate) static ICON_EXECUTING: &[u8] = include_bytes!("../icons/tray-executing.png");

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

/// The menu the tray holds, as Tauri built it, kept for the debug build's test control channel: it names each item
/// macOS shows by the id Tauri gave it, as it names the app menu's (src/test_control.rs). Nothing else reads it.
#[cfg(all(debug_assertions, target_os = "macos"))]
pub(crate) struct HeldMenu<R: Runtime>(pub std::sync::Mutex<Option<Menu<R>>>);

#[cfg(all(debug_assertions, target_os = "macos"))]
fn hold<R: Runtime>(app: &AppHandle<R>, menu: &Menu<R>) {
    match app.try_state::<HeldMenu<R>>() {
        Some(held) => {
            if let Ok(mut current) = held.0.lock() {
                *current = Some(menu.clone());
            }
        }
        None => {
            app.manage(HeldMenu(std::sync::Mutex::new(Some(menu.clone()))));
        }
    }
}

pub fn create<R: Runtime>(app: &AppHandle<R>) -> tauri::Result<()> {
    let menu = build_menu(app, &[])?;
    #[cfg(all(debug_assertions, target_os = "macos"))]
    hold(app, &menu);
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
/// of them, "Open dashboard", then the daemon item and "Quit interface". The daemon item follows `daemon`: "Stop
/// daemon…" while it runs, "Start daemon" while it is stopped, and a disabled "Not answering" while it is alive but not
/// answering. `label` gives a text by its key, or the fallback when the interface has sent none, as `AppState::label`
/// does.
fn menu_entries(running: &[ActiveExecution], daemon: DaemonState, label: impl Fn(&str, &str) -> String) -> Vec<Entry> {
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
    let daemon_item = match daemon {
        DaemonState::Running => item(STOP, label("stop", "Stop daemon…"), true),
        DaemonState::Stopped => item(START, label("start", "Start daemon"), true),
        DaemonState::NotAnswering => item(NOT_ANSWERING, label("notAnswering", "Not answering"), false),
    };
    entries.extend([
        Entry::Separator,
        item(OPEN, label("open", "Open dashboard"), true),
        Entry::Separator,
        daemon_item,
        item(QUIT, label("quit", "Quit interface"), true),
    ]);
    entries
}

/// The state the tray's daemon item shows for the desktop's daemon status, the one the dashboard's banner reads.
fn daemon_state(status: &DaemonStatus) -> DaemonState {
    if status.running {
        DaemonState::Running
    } else if status.not_answering {
        DaemonState::NotAnswering
    } else {
        DaemonState::Stopped
    }
}

fn tray_daemon(state: &AppState) -> DaemonState {
    state.tray_daemon.lock().map(|d| *d).unwrap_or(DaemonState::Running)
}

fn set_tray_daemon(state: &AppState, daemon: DaemonState) {
    if let Ok(mut current) = state.tray_daemon.lock() {
        *current = daemon;
    }
}

fn build_menu<R: Runtime>(app: &AppHandle<R>, running: &[ActiveExecution]) -> tauri::Result<Menu<R>> {
    let state = app.state::<Arc<AppState>>();
    let menu = Menu::new(app)?;
    for entry in menu_entries(running, tray_daemon(&state), |key, fallback| state.label(key, fallback)) {
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
        #[cfg(all(debug_assertions, target_os = "macos"))]
        hold(app, &menu);
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

/// How often the shell reads the daemon's status on its own, as the dashboard's health poll does.
const STATUS_POLL: Duration = Duration::from_secs(10);

/// Reads the daemon's status every `STATUS_POLL`, for the life of the app, and refreshes the tray when it reads a
/// state the daemon item does not show. A daemon kept busy stops answering its health check while its event stream,
/// whose heartbeat takes no lock, stays connected: no change of the connection and no event follow, so only this read
/// sees it, and sees it answer again. It runs with no window open, as the tray does.
pub fn follow_status(app: AppHandle) {
    tauri::async_runtime::spawn(async move {
        let state = app.state::<Arc<AppState>>().inner().clone();
        loop {
            tokio::time::sleep(STATUS_POLL).await;
            let status = state.daemon.status().await;
            if polled(tray_daemon(&state), &status).is_some() {
                schedule_refresh(app.clone());
            }
        }
    });
}

/// The state a status read moves the daemon item to, from the one it shows; `None` when the read shows no change.
fn polled(shown: DaemonState, status: &DaemonStatus) -> Option<DaemonState> {
    let read = daemon_state(status);
    (read != shown).then_some(read)
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

/// The executions the tray keeps for `daemon`, from the daemon's answer to `GET /executions?active=true`, which only a
/// running daemon is asked for: none while it is stopped, since nothing runs then, and `None`, the list kept as it
/// was, while it is not answering or when the answer holds no list.
fn kept_running(daemon: DaemonState, answer: Option<&Value>) -> Option<Vec<ActiveExecution>> {
    match daemon {
        DaemonState::Running => answer.and_then(TrayState::from_answer).map(|shown| shown.running),
        DaemonState::Stopped => Some(Vec::new()),
        DaemonState::NotAnswering => None,
    }
}

/// Reads the daemon's status, the one the dashboard reads, then, from a running daemon, its running executions.
async fn refresh(app: &AppHandle, state: &AppState) {
    let daemon = daemon_state(&state.daemon.status().await);
    let answer = match daemon {
        DaemonState::Running => Some(state.daemon.request("GET", "/executions?active=true&limit=50", None).await.body),
        _ => None,
    };
    set_tray_daemon(state, daemon);
    if let Some(running) = kept_running(daemon, answer.as_ref()) {
        if let Ok(mut current) = state.tray_running.lock() {
            *current = running;
        }
    }
    render(app);
}

/// The dialog a stop shows before anything is sent: with executions running, a choice between waiting for them and
/// cancelling them; with none, a confirmation.
#[derive(Debug, PartialEq, Eq)]
struct StopDialog {
    title: String,
    message: String,
    /// "Wait for them", offered only while executions run.
    wait: Option<String>,
    /// "Cancel them" while executions run, "Stop daemon" otherwise.
    stop: String,
    cancel: String,
}

/// The user's answer to the stop dialog.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum StopAnswer {
    /// Wait for the running executions: a draining stop, which the daemon ends once they have finished or its deadline
    /// has passed.
    Wait,
    /// Stop now, cancelling what runs.
    Stop,
    /// Keep the daemon running.
    Cancel,
}

impl StopDialog {
    fn buttons(&self) -> MessageDialogButtons {
        match &self.wait {
            Some(wait) => MessageDialogButtons::YesNoCancelCustom(wait.clone(), self.stop.clone(), self.cancel.clone()),
            None => MessageDialogButtons::OkCancelCustom(self.stop.clone(), self.cancel.clone()),
        }
    }

    /// The answer the dialog's result gives. A custom button answers its label; a dialog that answers the platform's
    /// own buttons answers Yes for the first and No for the second, as the window's `native.choose` reads them.
    fn answer(&self, result: &MessageDialogResult) -> StopAnswer {
        match result {
            MessageDialogResult::Custom(text) if self.wait.as_ref() == Some(text) => StopAnswer::Wait,
            MessageDialogResult::Custom(text) if *text == self.stop => StopAnswer::Stop,
            MessageDialogResult::Yes if self.wait.is_some() => StopAnswer::Wait,
            MessageDialogResult::Yes | MessageDialogResult::No | MessageDialogResult::Ok => StopAnswer::Stop,
            _ => StopAnswer::Cancel,
        }
    }
}

/// The dialog for `count` running executions: it names them when there are some, in the singular for one, and offers
/// to wait for them or cancel them; when there are none, it says no schedule fires while the daemon is stopped. `label`
/// is as for `menu_entries`.
fn stop_dialog(count: usize, label: impl Fn(&str, &str) -> String) -> StopDialog {
    let (message, wait, stop) = match count {
        0 => (
            label("stopConfirm", "No schedule fires while the daemon is stopped."),
            None,
            label("stopOk", "Stop daemon"),
        ),
        1 => (
            label(
                "stopChoiceRunning_one",
                "{{count}} execution is running. Wait for it to finish, and the daemon stops then; or cancel it now, \
                 and it will be recorded as interrupted.",
            ),
            Some(label("stopWait_one", "Wait for it")),
            label("stopCancelRunning_one", "Cancel it"),
        ),
        _ => (
            label(
                "stopChoiceRunning_other",
                "{{count}} executions are running. Wait for them to finish, and the daemon stops then; or cancel them \
                 now, and they will be recorded as interrupted.",
            ),
            Some(label("stopWait_other", "Wait for them")),
            label("stopCancelRunning_other", "Cancel them"),
        ),
    };
    StopDialog {
        title: label("stopTitle", "Stop the daemon?"),
        message: message.replace("{{count}}", &count.to_string()),
        wait,
        stop,
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

/// The request the user's answer sends for `count` running executions: none when declined, a draining stop when the
/// user waits for them, and otherwise a stop, forced when executions run.
fn stop_request(count: usize, answer: StopAnswer) -> Option<Request> {
    let body = match answer {
        StopAnswer::Cancel => return None,
        StopAnswer::Wait => serde_json::json!({ "drain": true }),
        StopAnswer::Stop => serde_json::json!({ "force": count > 0 }),
    };
    Some(Request { method: "POST", path: "/daemon/stop", body })
}

/// Stopping the daemon is its own command, and always asks first.
pub fn confirm_stop<R: Runtime>(app: &AppHandle<R>) {
    let state = app.state::<Arc<AppState>>().inner().clone();
    let count = state.tray_running.lock().map(|r| r.len()).unwrap_or(0);
    ask_stop(app, state, count);
}

/// Asks to stop the daemon with the dialog for `count` running executions, and sends the stop the user chose.
fn ask_stop<R: Runtime>(app: &AppHandle<R>, state: Arc<AppState>, count: usize) {
    let dialog = stop_dialog(count, |key, fallback| state.label(key, fallback));
    let handle = app.clone();
    app.dialog()
        .message(dialog.message.clone())
        .title(dialog.title.clone())
        .kind(MessageDialogKind::Warning)
        .buttons(dialog.buttons())
        .show_with_result(move |result| {
            let Some(request) = stop_request(count, dialog.answer(&result)) else { return };
            tauri::async_runtime::spawn(stop(handle, state, request));
        });
}

/// How long the tray follows a stop the daemon took and left unanswered: longer than the daemon's own stop takes at
/// most, its waits for the detection and the executions and its local API's shutdown included.
const UNANSWERED_STOP_WAIT: Duration = Duration::from_secs(120);

/// Sends the stop and follows the daemon's answer: a refusal is never dropped (harmflow-app#630), and a stop left
/// unanswered is followed until the daemon is gone (harmflow-app#995).
async fn stop<R: Runtime>(app: AppHandle<R>, state: Arc<AppState>, request: Request) {
    let chosen = ["force", "drain"].iter().any(|key| request.body.get(*key).and_then(Value::as_bool).unwrap_or(false));
    let sent = state.daemon.request_sent(request.method, request.path, Some(request.body)).await;
    match after_sent(&sent, chosen) {
        AfterStop::Stopping => {}
        AfterStop::ConfirmRunning(count) => ask_stop(&app, state, count),
        AfterStop::Failed(message) => stop_failed(&app, &state, message),
        AfterStop::Unanswered(cause) => follow_unanswered(&app, &state, cause).await,
    }
}

/// Shows that the daemon did not stop, and why.
fn stop_failed<R: Runtime>(app: &AppHandle<R>, state: &AppState, message: String) {
    notice(app, state, state.label("stopFailedTitle", "The daemon did not stop"), message);
}

/// Shows a dialog with one OK button.
fn notice<R: Runtime>(app: &AppHandle<R>, state: &AppState, title: String, message: String) {
    let ok = state.label("stopFailedOk", "OK");
    app.dialog()
        .message(message)
        .title(title)
        .kind(MessageDialogKind::Warning)
        .buttons(MessageDialogButtons::OkCustom(ok))
        .show(|_| {});
}

/// Says that the stop was sent and the daemon is not answering yet, as the status polls do (harmflow-app#908), then
/// waits for the pid of the runtime file to be gone and says so, or shows the failure, with the request's cause, when
/// the daemon answers that it is not stopping or the pid outlives the wait.
async fn follow_unanswered<R: Runtime>(app: &AppHandle<R>, state: &AppState, cause: String) {
    notice(
        app,
        state,
        state.label("stopUnansweredTitle", "The daemon is not answering yet"),
        state.label(
            "stopUnanswered",
            "The stop was sent, and the daemon has not answered yet. HarmFlow tells you once it has stopped.",
        ),
    );
    let pid = state.daemon.connection().ok().and_then(|c| c.runtime.pid);
    let waited = match pid {
        Some(pid) => state.daemon.wait_stopped(pid, UNANSWERED_STOP_WAIT).await,
        None => Err("the runtime file names no daemon pid".to_string()),
    };
    match waited {
        Ok(()) => notice(
            app,
            state,
            state.label("stopDoneTitle", "The daemon stopped"),
            state.label("stopDone", "The daemon stopped after the stop you sent."),
        ),
        Err(reason) => stop_failed(app, state, format!("{cause}; {reason}")),
    }
}

/// What the tray does after a stop was sent: `after_stop` for an answer, and a stop left unanswered is followed.
fn after_sent(sent: &Sent, chosen: bool) -> AfterStop {
    match sent {
        Sent::Answered(res) => after_stop(res, chosen),
        Sent::Unanswered(cause) => AfterStop::Unanswered(cause.clone()),
    }
}

/// What the tray does after the daemon answers a stop.
#[derive(Debug, PartialEq)]
enum AfterStop {
    /// The daemon accepted the stop.
    Stopping,
    /// The daemon refused a plain stop while executions run: ask again with its count, whether to wait for them or
    /// cancel them.
    ConfirmRunning(usize),
    /// Any other answer, or a request that failed: show the daemon's message, or the request's error.
    Failed(String),
    /// The daemon took the stop and did not answer within the request's timeout, which is no sign that it did not
    /// stop: say so, and follow the daemon until it is gone. Holds the request's error, its cause included.
    Unanswered(String),
}

/// `chosen` is true for a stop that already answered for running executions, forced or draining: a refusal of it is
/// shown, never asked again.
fn after_stop(res: &ApiResponse, chosen: bool) -> AfterStop {
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
    if let (false, Some("executions_running"), Some(count)) = (chosen, code, count) {
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

/// Starting the daemon asks nothing, since it interrupts nothing: it takes the path of the dashboard's Start daemon.
pub fn start<R: Runtime>(app: &AppHandle<R>) {
    let state = app.state::<Arc<AppState>>().inner().clone();
    if state.tray_starting.swap(true, Ordering::SeqCst) {
        return;
    }
    let handle = app.clone();
    tauri::async_runtime::spawn(async move {
        let result = crate::start_daemon(&state, None).await;
        state.tray_starting.store(false, Ordering::SeqCst);
        let (daemon, failure) = after_start(&result, |key, fallback| state.label(key, fallback));
        set_tray_daemon(&state, daemon);
        render(&handle);
        if let Some(message) = failure {
            let ok = state.label("stopFailedOk", "OK");
            handle
                .dialog()
                .message(message)
                .title(state.label("startFailedTitle", "The HarmFlow daemon is not running"))
                .kind(MessageDialogKind::Warning)
                .buttons(MessageDialogButtons::OkCustom(ok))
                .show(|_| {});
        }
    });
}

/// The daemon item's state after a start, and the message that says why it failed, as the dashboard words it: a start
/// that failed leaves the daemon stopped. `label` is as for `menu_entries`.
fn after_start(
    result: &Result<DaemonStatus, String>,
    label: impl Fn(&str, &str) -> String,
) -> (DaemonState, Option<String>) {
    match result {
        Ok(status) => (daemon_state(status), None),
        Err(error) => {
            let message = label("startFailed", "The daemon did not start: {{message}}").replace("{{message}}", error);
            (DaemonState::Stopped, Some(message))
        }
    }
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
        ApiResponse { status, body, total: None }
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

    /// An execution as `GET /executions?active=true` lists it.
    fn execution(id: &str, status: &str) -> Value {
        json!({ "id": id, "status": status, "taskName": "Nightly", "workItem": null })
    }

    fn state_of(executions: Vec<Value>) -> TrayState {
        TrayState::from_answer(&Value::Array(executions)).expect("a list is an answer")
    }

    /// The ids of the executions the menu lists by name for `running`.
    fn listed_ids(running: &[ActiveExecution]) -> Vec<String> {
        items(&menu_entries(running, DaemonState::Running, defaults))
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
            items(&menu_entries(&[], DaemonState::Running, defaults)),
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
            items(&menu_entries(&running, DaemonState::Running, defaults)),
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
        let entries = menu_entries(&[], DaemonState::Running, defaults);
        let ids: Vec<&str> = items(&entries).iter().map(|(id, _, _)| *id).collect();
        assert_ne!(STOP, QUIT);
        assert_eq!(ids.iter().filter(|id| **id == STOP).count(), 1);
        assert_eq!(ids.iter().filter(|id| **id == QUIT).count(), 1);
    }

    #[test]
    fn the_menu_lists_the_first_five_of_seven() {
        let entries = menu_entries(&running(7), DaemonState::Running, defaults);
        let items = items(&entries);
        assert_eq!(items[0], ("header", "7 running", false));
        let listed: Vec<&str> =
            items.iter().filter_map(|(id, _, _)| id.strip_prefix(EXECUTION_PREFIX)).collect();
        assert_eq!(listed, ["exec-1", "exec-2", "exec-3", "exec-4", "exec-5"]);
    }

    /// The daemon item: the one item between "Open dashboard" and "Quit interface".
    fn daemon_item(daemon: DaemonState) -> (String, String, bool) {
        let entries = menu_entries(&running(1), daemon, defaults);
        let items = items(&entries);
        let at = items.iter().position(|(id, _, _)| *id == OPEN).expect("the menu opens the dashboard") + 1;
        assert_eq!(items[at + 1].0, QUIT, "{daemon:?}");
        assert_eq!(items.len(), at + 2, "{daemon:?}");
        let (id, text, enabled) = items[at];
        (id.to_string(), text.to_string(), enabled)
    }

    #[test]
    fn the_daemon_item_follows_the_daemons_state() {
        assert_eq!(daemon_item(DaemonState::Running), (STOP.into(), "Stop daemon…".into(), true));
        assert_eq!(daemon_item(DaemonState::Stopped), (START.into(), "Start daemon".into(), true));
        assert_eq!(daemon_item(DaemonState::NotAnswering), (NOT_ANSWERING.into(), "Not answering".into(), false));
    }

    #[test]
    fn each_state_shows_one_daemon_item_and_keeps_the_rest_of_the_menu() {
        for daemon in [DaemonState::Running, DaemonState::Stopped, DaemonState::NotAnswering] {
            let entries = menu_entries(&running(2), daemon, defaults);
            let ids: Vec<&str> = items(&entries).iter().map(|(id, _, _)| *id).collect();
            let daemon_items = ids.iter().filter(|id| [STOP, START, NOT_ANSWERING].contains(*id)).count();
            assert_eq!(daemon_items, 1, "{daemon:?}");
            assert_eq!(ids[..3], ["header", "exec:exec-1", "exec:exec-2"], "{daemon:?}");
            assert!(ids.contains(&OPEN) && ids.contains(&QUIT), "{daemon:?}");
        }
    }

    #[test]
    fn each_daemon_item_has_its_action() {
        use crate::menu::{action, Action};
        // Stop daemon… keeps its confirmation, Start daemon asks nothing, and the not-answering item does nothing.
        assert_eq!(action(STOP), Some(Action::StopDaemon));
        assert_eq!(action(START), Some(Action::StartDaemon));
        assert_eq!(action(NOT_ANSWERING), None);
    }

    #[test]
    fn the_daemon_status_gives_the_items_state() {
        let running = DaemonStatus { running: true, pid: Some(4242), ..Default::default() };
        let not_answering = DaemonStatus {
            not_answering: true,
            pid: Some(4242),
            error: Some("operation timed out".into()),
            ..Default::default()
        };
        let stopped = DaemonStatus { error: Some("connection refused".into()), ..Default::default() };
        assert_eq!(daemon_state(&running), DaemonState::Running);
        assert_eq!(daemon_state(&not_answering), DaemonState::NotAnswering);
        assert_eq!(daemon_state(&stopped), DaemonState::Stopped);
        assert_eq!(daemon_state(&DaemonStatus::default()), DaemonState::Stopped);
    }

    #[test]
    fn a_status_read_moves_the_item_while_the_connection_stays() {
        // The stream stays connected and sends no event, so only the status read sees the daemon stop answering, and
        // answer again.
        let running = DaemonStatus { running: true, pid: Some(4242), ..Default::default() };
        let not_answering = DaemonStatus {
            not_answering: true,
            pid: Some(4242),
            error: Some("operation timed out".into()),
            ..Default::default()
        };
        let stopped = DaemonStatus { error: Some("connection refused".into()), ..Default::default() };
        assert_eq!(polled(DaemonState::Running, &not_answering), Some(DaemonState::NotAnswering));
        assert_eq!(polled(DaemonState::NotAnswering, &running), Some(DaemonState::Running));
        assert_eq!(polled(DaemonState::Running, &stopped), Some(DaemonState::Stopped));
        assert_eq!(polled(DaemonState::Stopped, &running), Some(DaemonState::Running));
        // A read that shows the state the item shows refreshes nothing.
        assert_eq!(polled(DaemonState::Running, &running), None);
        assert_eq!(polled(DaemonState::NotAnswering, &not_answering), None);
        assert_eq!(polled(DaemonState::Stopped, &stopped), None);
    }

    #[test]
    fn a_start_that_succeeds_shows_stop_daemon_again() {
        let started = Ok(DaemonStatus { running: true, ..Default::default() });
        assert_eq!(after_start(&started, defaults), (DaemonState::Running, None));
        assert_eq!(daemon_item(after_start(&started, defaults).0).0, STOP);
        // A daemon that runs but does not answer yet is not started again: the item says so.
        let busy = Ok(DaemonStatus { not_answering: true, ..Default::default() });
        assert_eq!(after_start(&busy, defaults), (DaemonState::NotAnswering, None));
    }

    #[test]
    fn a_start_that_fails_says_why_and_keeps_start_daemon() {
        let failed = Err("the daemon did not answer within 10 seconds".to_string());
        let (daemon, message) = after_start(&failed, defaults);
        assert_eq!(daemon, DaemonState::Stopped);
        assert_eq!(daemon_item(daemon).0, START);
        assert_eq!(message.as_deref(), Some("The daemon did not start: the daemon did not answer within 10 seconds"));
        let label = |key: &str, fallback: &str| match key {
            "startFailed" => "O daemon não iniciou: {{message}}".to_string(),
            _ => fallback.to_string(),
        };
        assert_eq!(
            after_start(&failed, label).1.as_deref(),
            Some("O daemon não iniciou: the daemon did not answer within 10 seconds")
        );
    }

    #[test]
    fn the_interfaces_texts_name_the_daemon_item() {
        let label = |key: &str, fallback: &str| match key {
            "start" => "Iniciar o daemon".to_string(),
            "notAnswering" => "Sem resposta".to_string(),
            _ => fallback.to_string(),
        };
        let texts = |daemon| {
            items(&menu_entries(&[], daemon, label)).iter().map(|(_, text, _)| text.to_string()).collect::<Vec<_>>()
        };
        assert!(texts(DaemonState::Stopped).contains(&"Iniciar o daemon".to_string()));
        assert!(texts(DaemonState::NotAnswering).contains(&"Sem resposta".to_string()));
    }

    #[test]
    fn a_stopped_daemon_clears_the_executions_and_one_not_answering_keeps_them() {
        let answer = json!([{ "id": "exec-1", "status": "running", "taskName": "Nightly" }]);
        let ids = |kept: Option<Vec<ActiveExecution>>| kept.map(|r| r.into_iter().map(|e| e.id).collect::<Vec<_>>());
        assert_eq!(ids(kept_running(DaemonState::Running, Some(&answer))), Some(vec!["exec-1".to_string()]));
        assert_eq!(ids(kept_running(DaemonState::Running, Some(&json!({ "error": "unavailable" })))), None);
        assert_eq!(ids(kept_running(DaemonState::Running, None)), None);
        assert_eq!(ids(kept_running(DaemonState::Stopped, None)), Some(vec![]));
        assert_eq!(ids(kept_running(DaemonState::NotAnswering, None)), None);
    }

    fn stop_with(body: Value) -> Option<Request> {
        Some(Request { method: "POST", path: "/daemon/stop", body })
    }

    #[test]
    fn a_stop_with_an_execution_running_asks_and_names_it() {
        let dialog = stop_dialog(1, defaults);
        assert_eq!(dialog.title, "Stop the daemon?");
        assert_eq!(
            dialog.message,
            "1 execution is running. Wait for it to finish, and the daemon stops then; or cancel it now, and it will be \
             recorded as interrupted."
        );
        assert_eq!(dialog.wait.as_deref(), Some("Wait for it"));
        assert_eq!((dialog.stop.as_str(), dialog.cancel.as_str()), ("Cancel it", "Cancel"));
        assert_eq!(stop_request(1, StopAnswer::Cancel), None);
        assert_eq!(stop_request(1, StopAnswer::Stop), stop_with(json!({ "force": true })));
    }

    #[test]
    fn waiting_for_the_running_executions_asks_for_a_draining_stop() {
        for count in [1, 2] {
            let dialog = stop_dialog(count, defaults);
            let wait = dialog.wait.clone().expect("a stop with executions running offers to wait");
            assert_eq!(dialog.answer(&MessageDialogResult::Custom(wait)), StopAnswer::Wait, "{count}");
            assert_eq!(stop_request(count, StopAnswer::Wait), stop_with(json!({ "drain": true })), "{count}");
        }
    }

    #[test]
    fn a_stop_with_nothing_running_still_asks() {
        let dialog = stop_dialog(0, defaults);
        assert_eq!(dialog.message, "No schedule fires while the daemon is stopped.");
        assert_eq!(dialog.wait, None);
        assert_eq!((dialog.stop.as_str(), dialog.cancel.as_str()), ("Stop daemon", "Cancel"));
        assert_eq!(stop_request(0, StopAnswer::Cancel), None);
        assert_eq!(stop_request(0, StopAnswer::Stop), stop_with(json!({ "force": false })));
    }

    #[test]
    fn each_button_answers_its_choice() {
        use MessageDialogResult::{Cancel, Custom, No, Ok, Yes};
        let running = stop_dialog(2, defaults);
        assert!(matches!(
            running.buttons(),
            MessageDialogButtons::YesNoCancelCustom(wait, stop, cancel)
                if wait == "Wait for them" && stop == "Cancel them" && cancel == "Cancel"
        ));
        for (result, answer) in [
            (Custom("Wait for them".into()), StopAnswer::Wait),
            (Custom("Cancel them".into()), StopAnswer::Stop),
            (Custom("Cancel".into()), StopAnswer::Cancel),
            (Yes, StopAnswer::Wait),
            (No, StopAnswer::Stop),
            (Cancel, StopAnswer::Cancel),
        ] {
            assert_eq!(running.answer(&result), answer, "{result:?}");
        }
        let idle = stop_dialog(0, defaults);
        assert!(matches!(
            idle.buttons(),
            MessageDialogButtons::OkCancelCustom(stop, cancel) if stop == "Stop daemon" && cancel == "Cancel"
        ));
        for (result, answer) in [
            (Custom("Stop daemon".into()), StopAnswer::Stop),
            (Custom("Cancel".into()), StopAnswer::Cancel),
            (Ok, StopAnswer::Stop),
            (Yes, StopAnswer::Stop),
            (Cancel, StopAnswer::Cancel),
        ] {
            assert_eq!(idle.answer(&result), answer, "{result:?}");
        }
    }

    #[test]
    fn the_stop_dialog_names_one_execution_in_the_singular_and_two_in_the_plural() {
        let one = stop_dialog(1, defaults);
        assert!(one.message.starts_with("1 execution is running. Wait for it to finish"), "{}", one.message);
        assert_eq!((one.wait.as_deref(), one.stop.as_str()), (Some("Wait for it"), "Cancel it"));
        let two = stop_dialog(2, defaults);
        assert_eq!(
            two.message,
            "2 executions are running. Wait for them to finish, and the daemon stops then; or cancel them now, and \
             they will be recorded as interrupted."
        );
        assert_eq!((two.wait.as_deref(), two.stop.as_str()), (Some("Wait for them"), "Cancel them"));
    }

    #[test]
    fn the_interfaces_texts_replace_the_defaults() {
        let label = |key: &str, fallback: &str| match key {
            "active" => "{{count}} em execução".to_string(),
            "stopChoiceRunning_one" => "{{count}} execução em andamento.".to_string(),
            "stopChoiceRunning_other" => "{{count}} execuções em andamento.".to_string(),
            "stopWait_one" => "Aguardar".to_string(),
            "stopWait_other" => "Aguardar todas".to_string(),
            "stopCancelRunning_other" => "Cancelar todas".to_string(),
            _ => fallback.to_string(),
        };
        assert_eq!(items(&menu_entries(&running(3), DaemonState::Running, label))[0].1, "3 em execução");
        assert_eq!(stop_dialog(1, label).message, "1 execução em andamento.");
        assert_eq!(stop_dialog(1, label).wait.as_deref(), Some("Aguardar"));
        assert_eq!(stop_dialog(2, label).message, "2 execuções em andamento.");
        let three = stop_dialog(3, label);
        assert_eq!(three.message, "3 execuções em andamento.");
        assert_eq!((three.wait.as_deref(), three.stop.as_str()), (Some("Aguardar todas"), "Cancelar todas"));
    }

    #[test]
    fn an_accepted_stop_shows_nothing() {
        assert_eq!(after_stop(&answer(202, Value::Null), false), AfterStop::Stopping);
        assert_eq!(after_stop(&answer(202, Value::Null), true), AfterStop::Stopping);
    }

    #[test]
    fn a_stop_refused_for_running_executions_asks_again_with_the_daemons_count() {
        // The tray's own list read no execution, so it sent a plain stop; the daemon counted two.
        assert_eq!(after_stop(&refused_running("2"), false), AfterStop::ConfirmRunning(2));
        // The dialog asked again names the daemon's count and offers to wait for them or cancel them.
        let dialog = stop_dialog(2, defaults);
        assert!(dialog.message.starts_with("2 executions are running."), "{}", dialog.message);
        assert_eq!(dialog.wait.as_deref(), Some("Wait for them"));
        assert_eq!(stop_request(2, StopAnswer::Wait), stop_with(json!({ "drain": true })));
        assert_eq!(stop_request(2, StopAnswer::Stop), stop_with(json!({ "force": true })));
    }

    #[test]
    fn a_forced_stop_that_is_still_refused_shows_the_daemons_message() {
        // A stop that already chose, forced or draining, is never asked again in a loop.
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

    /// harmflow-app#995: a stop the daemon took and left unanswered is not reported as one that did not stop: the tray
    /// says the daemon is not answering yet and follows it, keeping the request's cause for a failure it may show later.
    #[test]
    fn an_unanswered_stop_is_followed_not_reported_as_failed() {
        let cause = "error sending request for url (http://127.0.0.1:7000/api/v1/daemon/stop): operation timed out";
        assert_eq!(after_sent(&Sent::Unanswered(cause.into()), true), AfterStop::Unanswered(cause.into()));
        assert_eq!(after_sent(&Sent::Unanswered(cause.into()), false), AfterStop::Unanswered(cause.into()));
    }

    /// An answered stop, a refusal included, keeps what `after_stop` makes of it (harmflow-app#630).
    #[test]
    fn an_answered_stop_keeps_its_reading() {
        assert_eq!(after_sent(&Sent::Answered(answer(202, Value::Null)), true), AfterStop::Stopping);
        assert_eq!(after_sent(&Sent::Answered(refused_running("2")), false), AfterStop::ConfirmRunning(2));
        let refused = answer(0, json!({ "error": { "code": "internal", "message": "error sending request: connection refused" } }));
        assert_eq!(
            after_sent(&Sent::Answered(refused), true),
            AfterStop::Failed("error sending request: connection refused".into())
        );
    }

    #[test]
    fn an_answer_with_no_message_still_says_what_happened() {
        assert_eq!(after_stop(&answer(502, json!("Bad Gateway")), false), AfterStop::Failed("Bad Gateway".into()));
        assert_eq!(
            after_stop(&answer(409, json!({ "error": { "code": "executions_running" } })), false),
            AfterStop::Failed("The daemon answered HTTP 409.".into())
        );
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
