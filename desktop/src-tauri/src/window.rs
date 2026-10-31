//! The dashboard window. With the tray enabled, closing it only hides it, and the app leaves the Dock while hidden;
//! the daemon never stops with it.

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

/// Shows the window on one of the dashboard's screens: from the app menu's Settings…. The interface decides whether
/// the screen may change now, as it does for ⌘B and ⌘N.
pub fn show_screen<R: Runtime>(app: &AppHandle<R>, screen: &str) {
    show_main(app);
    let _ = app.emit("harmflow://navigate", serde_json::json!({ "screen": screen }));
}

pub fn hide_main<R: Runtime>(app: &AppHandle<R>) {
    if let Some(window) = app.get_webview_window(MAIN) {
        let _ = window.hide();
    }
    #[cfg(target_os = "macos")]
    let _ = app.set_activation_policy(tauri::ActivationPolicy::Accessory);
}

/// Whether closing the window only hides it, as `menu::quit_hides` decides for ⌘Q: while the tray is shown. Otherwise
/// the close goes through. The daemon runs on either way (harmflow-app#464).
pub fn close_hides(tray_enabled: bool) -> bool {
    tray_enabled
}

/// Whether the app stays when it is asked to exit: when the last window closed, with no exit code, while the tray is
/// shown. Without the tray only the interface ends. The app menu's Quit and the tray's Quit interface exit with a code,
/// which always goes through.
pub fn exit_prevented(tray_enabled: bool, code: Option<i32>) -> bool {
    tray_enabled && code.is_none()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn with_the_tray_a_close_only_hides_the_window_and_the_app_stays() {
        assert!(close_hides(true));
        assert!(exit_prevented(true, None));
    }

    #[test]
    fn without_the_tray_a_close_and_its_exit_go_through() {
        assert!(!close_hides(false));
        assert!(!exit_prevented(false, None));
    }

    #[test]
    fn an_exit_with_a_code_always_goes_through() {
        for tray_enabled in [true, false] {
            assert!(!exit_prevented(tray_enabled, Some(0)), "tray {tray_enabled}");
            assert!(!exit_prevented(tray_enabled, Some(1)), "tray {tray_enabled}");
        }
    }

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
