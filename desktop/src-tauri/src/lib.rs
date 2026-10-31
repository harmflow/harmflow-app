//! The HarmFlow desktop: the dashboard window, the tray, and native notifications, as a client of the daemon's
//! local API. It never drives a harness and never decides an execution's state.

mod daemon;
mod menu;
mod notice;
mod sse;
mod state;
#[cfg(all(debug_assertions, target_os = "macos"))]
mod test_control;
mod tray;
mod window;

use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

use serde::Serialize;
use serde_json::Value;
use tauri::{Manager, RunEvent, State, WindowEvent};
use tauri_plugin_autostart::MacosLauncher;

use daemon::{ApiResponse, DaemonStatus};
use state::AppState;

type Shared<'a> = State<'a, Arc<AppState>>;

#[tauri::command]
async fn api_request(state: Shared<'_>, method: String, path: String, body: Option<Value>) -> Result<ApiResponse, String> {
    Ok(state.daemon.request(&method, &path, body).await)
}

#[derive(Serialize)]
struct TextResponse {
    status: u16,
    text: String,
}

#[tauri::command]
async fn api_request_text(state: Shared<'_>, path: String) -> Result<TextResponse, String> {
    let (status, text) = state.daemon.request_text(&path).await;
    Ok(TextResponse { status, text })
}

#[tauri::command]
fn connection_state(state: Shared<'_>) -> String {
    state.connection()
}

#[tauri::command]
async fn daemon_status(state: Shared<'_>) -> Result<DaemonStatus, String> {
    Ok(state.daemon.status().await)
}

/// Starts the bundled daemon when none answers, then waits until it does. `replacing` is the pid of a daemon asked to
/// stop, whose process exits first.
#[tauri::command]
async fn daemon_start(state: Shared<'_>, replacing: Option<u32>) -> Result<DaemonStatus, String> {
    if let Some(pid) = replacing {
        daemon::wait_gone(pid, Duration::from_secs(30)).await?;
    }
    let current = state.daemon.status().await;
    if current.running {
        return Ok(current);
    }
    daemon::spawn_daemon()?;
    for _ in 0..40 {
        tokio::time::sleep(Duration::from_millis(250)).await;
        let status = state.daemon.status().await;
        if status.running {
            return Ok(status);
        }
    }
    Err("the daemon did not answer within 10 seconds; see logs/daemon.log in the HarmFlow folder".into())
}

#[tauri::command]
fn set_tray_enabled(app: tauri::AppHandle, state: Shared<'_>, enabled: bool) {
    state.set_tray_enabled(enabled);
    tray::set_visible(&app, enabled);
}

/// Whether ⌘Q keeps HarmFlow in the menu bar; the interface sends it with the tray's after each settings refresh.
#[tauri::command]
fn set_keep_tray_on_quit(state: Shared<'_>, keep: bool) {
    state.set_keep_tray_on_quit(keep);
}

/// Reads the stored tray choices once at start. The window can show on the interface's readiness before its first
/// settings refresh, and a ⌘Q pressed then already follows them. A daemon not running yet leaves them to the interface.
fn read_tray_settings(app: tauri::AppHandle) {
    tauri::async_runtime::spawn(async move {
        let state = app.state::<Arc<AppState>>().inner().clone();
        let res = state.daemon.request("GET", "/settings", None).await;
        if res.status != 200 {
            return;
        }
        if let Some(enabled) = res.body.get("trayEnabled").and_then(Value::as_bool) {
            state.set_tray_enabled(enabled);
            tray::set_visible(&app, enabled);
        }
        if let Some(keep) = res.body.get("keepTrayOnQuit").and_then(Value::as_bool) {
            state.set_keep_tray_on_quit(keep);
        }
    });
}

/// Tray texts in the interface's language: idle, active ({{count}}), open, quit, stop, stopTitle, stopConfirm,
/// stopConfirmRunning ({{count}}), stopOk, cancel; and the app menu's settings.
#[tauri::command]
fn set_tray_labels(app: tauri::AppHandle, state: Shared<'_>, labels: HashMap<String, String>) {
    if let Ok(mut current) = state.tray_labels.lock() {
        *current = labels;
    }
    tray::render(&app);
    menu::render(&app);
}

/// The interface committed its first render: a normal start shows the window now, unless the fallback already did
/// (harmflow-app#96). The interface only signals; whether to show stays the shell's call.
#[tauri::command]
fn interface_ready(app: tauri::AppHandle) {
    window::show_at_start(&app, true, false);
}

#[tauri::command]
fn write_text_file(path: String, content: String) -> Result<(), String> {
    std::fs::write(&path, content).map_err(|e| format!("{path}: {e}"))
}

#[tauri::command]
fn open_path(path: String) -> Result<(), String> {
    let p = std::path::Path::new(&path);
    if !p.is_absolute() || !p.exists() {
        return Err(format!("not an existing absolute path: {path}"));
    }
    // A file is revealed in its folder; a folder is opened.
    let mut cmd = std::process::Command::new("/usr/bin/open");
    if p.is_file() {
        cmd.arg("-R");
    }
    cmd.arg(p).spawn().map(|_| ()).map_err(|e| e.to_string())
}

#[tauri::command]
fn open_url(url: String) -> Result<(), String> {
    if !url.starts_with("https://") {
        return Err(format!("only https links are opened: {url}"));
    }
    std::process::Command::new("/usr/bin/open").arg(&url).spawn().map(|_| ()).map_err(|e| e.to_string())
}

pub fn run() {
    let start_hidden = std::env::args().any(|a| a == "--hidden");
    let state = Arc::new(AppState::new(start_hidden));

    let app = tauri::Builder::default()
        .plugin(tauri_plugin_notification::init())
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_autostart::init(MacosLauncher::LaunchAgent, Some(vec!["--hidden"])))
        .manage(state.clone())
        // Every menu event, the tray's included, has this one handler.
        .on_menu_event(|app, event| menu::on_event(app, event.id().as_ref()))
        .setup(move |app| {
            #[cfg(target_os = "macos")]
            app.set_menu(menu::build(app.handle())?)?;
            tray::create(app.handle())?;
            read_tray_settings(app.handle().clone());
            notice::listen(app.handle())?;
            sse::spawn(app.handle().clone());
            tray::schedule_refresh(app.handle().clone());
            #[cfg(all(debug_assertions, target_os = "macos"))]
            test_control::listen(app.handle());
            // A normal start shows the window once the interface has painted, not before (harmflow-app#96).
            if start_hidden {
                window::hide_main(app.handle());
            } else {
                window::schedule_fallback_show(app.handle().clone());
            }
            Ok(())
        })
        .on_window_event(|window, event| {
            if let WindowEvent::CloseRequested { api, .. } = event {
                let state = window.app_handle().state::<Arc<AppState>>();
                if state.tray_enabled() {
                    api.prevent_close();
                    window::hide_main(window.app_handle());
                }
            }
        })
        .invoke_handler(tauri::generate_handler![
            api_request,
            api_request_text,
            connection_state,
            daemon_status,
            daemon_start,
            set_tray_enabled,
            set_keep_tray_on_quit,
            set_tray_labels,
            interface_ready,
            write_text_file,
            open_path,
            open_url,
        ])
        .build(tauri::generate_context!());

    let app = match app {
        Ok(app) => app,
        Err(err) => {
            eprintln!("HarmFlow could not start: {err}");
            std::process::exit(1);
        }
    };

    app.run(|app, event| match event {
        // The Dock icon or a reopen brings the hidden window back.
        #[cfg(target_os = "macos")]
        RunEvent::Reopen { .. } => window::show_main(app),
        // Closing the last window ends only the interface when the tray is disabled; with the tray, the app stays. The
        // app menu's Quit and the tray's Quit interface exit with a code, which this lets through.
        RunEvent::ExitRequested { api, code, .. } => {
            if code.is_none() && app.state::<Arc<AppState>>().tray_enabled() {
                api.prevent_exit();
            }
        }
        _ => {}
    });
}
