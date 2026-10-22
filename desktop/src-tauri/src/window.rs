//! The dashboard window. With the tray enabled, closing it only hides it, and the app leaves the Dock while hidden;
//! the daemon never stops with it (FS-010 FR-004, FR-005).

use std::sync::atomic::Ordering;
use std::sync::Arc;
use std::time::Duration;

use tauri::{AppHandle, Emitter, Manager, Runtime};

use crate::state::AppState;

pub const MAIN: &str = "main";

/// How long a normal start waits for the interface's readiness before it shows the window anyway: the same wait as
/// `daemon_start`. A page that never paints still shows then, with its placeholder or the window's background.
pub const SHOW_FALLBACK: Duration = Duration::from_secs(10);

/// Whether the window shows now at start (harmflow-app#96): on the interface's readiness or once the fallback elapses,
/// whichever comes first. `decided` is whether one of them already came; the other then changes nothing, so a window
/// the user hid in between stays hidden. A `--hidden` start and a window already on screen are left alone.
pub fn should_show(start_hidden: bool, visible: bool, decided: bool, ready: bool, fallback_elapsed: bool) -> bool {
    !start_hidden && !visible && !decided && (ready || fallback_elapsed)
}

/// The interface's readiness or the fallback: shows the window when `should_show` says so.
pub fn show_at_start<R: Runtime>(app: &AppHandle<R>, ready: bool, fallback_elapsed: bool) {
    let state = app.state::<Arc<AppState>>();
    let decided = state.start_show_decided.swap(true, Ordering::SeqCst);
    let visible = app.get_webview_window(MAIN).and_then(|w| w.is_visible().ok()).unwrap_or(false);
    if should_show(state.start_hidden, visible, decided, ready, fallback_elapsed) {
        show_main(app);
    }
}

/// Shows the window after `SHOW_FALLBACK` when the interface has not said it is ready by then.
pub fn schedule_fallback_show(app: AppHandle) {
    tauri::async_runtime::spawn(async move {
        tokio::time::sleep(SHOW_FALLBACK).await;
        show_at_start(&app, false, true);
    });
}

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

#[cfg(test)]
mod tests {
    use super::*;

    // The arguments: start_hidden, visible, decided, ready, fallback_elapsed.

    #[test]
    fn a_normal_start_shows_on_the_interfaces_readiness() {
        assert!(should_show(false, false, false, true, false));
    }

    #[test]
    fn a_normal_start_shows_when_the_fallback_elapses() {
        assert!(should_show(false, false, false, false, true));
    }

    #[test]
    fn a_hidden_start_shows_on_neither() {
        assert!(!should_show(true, false, false, true, false));
        assert!(!should_show(true, false, false, false, true));
    }

    #[test]
    fn an_already_visible_window_is_left_alone() {
        assert!(!should_show(false, true, false, true, false));
        assert!(!should_show(false, true, false, false, true));
    }

    #[test]
    fn the_second_of_readiness_and_fallback_changes_nothing() {
        // The user hid the window after the first one showed it.
        assert!(!should_show(false, false, true, false, true));
        assert!(!should_show(false, false, true, true, false));
    }
}
