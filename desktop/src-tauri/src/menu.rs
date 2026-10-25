//! The app menu in the screen's menu bar, on macOS: Tauri's default menu with HarmFlow's own entry, Settings… (⌘,), in
//! its app submenu. Every menu event, the tray's included, reaches one handler here, so none is handled twice.

use std::sync::Arc;

use tauri::menu::MenuItemKind;
use tauri::{AppHandle, Manager, Runtime};

use crate::state::AppState;
use crate::{tray, window};

/// The app menu's Settings… item. Its `app:` prefix keeps it apart from the tray's ids.
pub const SETTINGS: &str = "app:settings";

/// What a menu item asks for, by its id.
#[derive(Debug, PartialEq, Eq)]
pub enum Action<'a> {
    /// The app menu's Settings…: the dashboard's Settings screen.
    Settings,
    /// The tray's Open dashboard.
    OpenDashboard,
    /// The tray's Stop daemon…, which asks first.
    StopDaemon,
    /// The tray's Quit interface: the desktop ends, and the daemon keeps running.
    QuitInterface,
    /// A running execution the tray lists.
    Execution(&'a str),
}

/// The action of a menu item, by its id. None for the items macOS handles itself (About, Edit, Window…) and for the
/// tray's header, which only reads.
pub fn action(id: &str) -> Option<Action<'_>> {
    match id {
        SETTINGS => Some(Action::Settings),
        tray::OPEN => Some(Action::OpenDashboard),
        tray::STOP => Some(Action::StopDaemon),
        tray::QUIT => Some(Action::QuitInterface),
        other => other.strip_prefix(tray::EXECUTION_PREFIX).map(Action::Execution),
    }
}

/// The one handler of every menu event, the app menu's and the tray's.
pub fn on_event<R: Runtime>(app: &AppHandle<R>, id: &str) {
    match action(id) {
        Some(Action::Settings) => window::show_screen(app, "settings"),
        Some(Action::OpenDashboard) => window::show_main(app),
        Some(Action::StopDaemon) => tray::confirm_stop(app),
        Some(Action::QuitInterface) => app.exit(0),
        Some(Action::Execution(execution_id)) => window::show_execution(app, execution_id),
        None => {}
    }
}

/// Tauri's default menu with Settings… after About and its separator, followed by a separator, as macOS apps place it.
/// Starting from the default menu keeps the Edit menu, which copy and paste in text fields need, and the Window menu.
#[cfg(target_os = "macos")]
pub fn build<R: Runtime>(app: &AppHandle<R>) -> tauri::Result<tauri::menu::Menu<R>> {
    use tauri::menu::{Menu, MenuItem, PredefinedMenuItem};

    let menu = Menu::default(app)?;
    let items = menu.items()?;
    if let Some(app_menu) = items.first().and_then(MenuItemKind::as_submenu) {
        let label = settings_label(&app.state::<Arc<AppState>>());
        let settings = MenuItem::with_id(app, SETTINGS, label, true, Some("CmdOrCtrl+,"))?;
        app_menu.insert_items(&[&settings, &PredefinedMenuItem::separator(app)?], 2)?;
    }
    Ok(menu)
}

/// Sets Settings… in the interface's language, which the webview sends with the tray's texts.
pub fn render<R: Runtime>(app: &AppHandle<R>) {
    let Some(menu) = app.menu() else { return };
    let Ok(items) = menu.items() else { return };
    let Some(item) = items.first().and_then(MenuItemKind::as_submenu).and_then(|m| m.get(SETTINGS)) else { return };
    if let Some(settings) = item.as_menuitem() {
        let _ = settings.set_text(settings_label(&app.state::<Arc<AppState>>()));
    }
}

fn settings_label(state: &AppState) -> String {
    state.label("settings", "Settings…")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn settings_opens_the_settings_screen() {
        assert_eq!(action(SETTINGS), Some(Action::Settings));
    }

    #[test]
    fn the_trays_items_keep_their_actions() {
        assert_eq!(action("open"), Some(Action::OpenDashboard));
        assert_eq!(action("stop"), Some(Action::StopDaemon));
        assert_eq!(action("quit"), Some(Action::QuitInterface));
        assert_eq!(action("exec:exec-8f3a2c"), Some(Action::Execution("exec-8f3a2c")));
    }

    #[test]
    fn the_app_menus_id_is_not_a_trays() {
        // Settings… is no tray item, and no tray id reads as Settings….
        for id in ["open", "stop", "quit", "header", "exec:settings"] {
            assert_ne!(action(id), Some(Action::Settings), "{id}");
        }
        assert_eq!(action("settings"), None);
    }

    #[test]
    fn items_macos_handles_and_the_header_do_nothing() {
        for id in ["header", "", "about", "quit:app", "app:unknown"] {
            assert_eq!(action(id), None, "{id}");
        }
    }
}
