//! The dashboard window. With the tray enabled, closing it only hides it, and the app leaves the Dock while hidden;
//! the daemon never stops with it (FS-010 FR-004, FR-005).

use tauri::{AppHandle, Emitter, Manager, Runtime};

pub const MAIN: &str = "main";

pub fn show_main<R: Runtime>(app: &AppHandle<R>) {
    #[cfg(target_os = "macos")]
    let _ = app.set_activation_policy(tauri::ActivationPolicy::Regular);
    if let Some(window) = app.get_webview_window(MAIN) {
        let _ = window.unminimize();
        let _ = window.show();
        let _ = window.set_focus();
    }
}

/// Shows the window on an execution: from the tray's list of running executions, or a click on a native notice.
pub fn show_execution<R: Runtime>(app: &AppHandle<R>, execution_id: &str) {
    show_main(app);
    let _ = app.emit("harmflow://navigate", serde_json::json!({ "executionId": execution_id }));
}

pub fn hide_main<R: Runtime>(app: &AppHandle<R>) {
    if let Some(window) = app.get_webview_window(MAIN) {
        let _ = window.hide();
    }
    #[cfg(target_os = "macos")]
    let _ = app.set_activation_policy(tauri::ActivationPolicy::Accessory);
}
