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

fn build_menu<R: Runtime>(app: &AppHandle<R>, running: &[ActiveExecution]) -> tauri::Result<Menu<R>> {
    let state = app.state::<Arc<AppState>>();
    let menu = Menu::new(app)?;
    let header = if running.is_empty() {
        state.label("idle", "Nothing running")
    } else {
        state.label("active", "{{count}} running").replace("{{count}}", &running.len().to_string())
    };
    menu.append(&MenuItem::with_id(app, "header", header, false, None::<&str>)?)?;
    for exec in running.iter().take(MAX_LISTED) {
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

async fn refresh(app: &AppHandle, state: &AppState) {
    let res = state.daemon.request("GET", "/executions?active=true&limit=50", None).await;
    let Some(list) = res.body.as_array() else { return };
    let running: Vec<ActiveExecution> = list
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
    if let Ok(mut current) = state.tray_running.lock() {
        *current = running;
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
