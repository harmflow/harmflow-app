//! The app menu in the screen's menu bar, on macOS: Tauri's default menu with HarmFlow's own entries in its app submenu,
//! Settings… (⌘,) and Quit (⌘Q), and in its Help submenu, Keyboard Shortcuts. Every menu event, the tray's included,
//! reaches one handler here, so none is handled twice.

use std::sync::Arc;

use tauri::menu::MenuItemKind;
use tauri::{AppHandle, Manager, Runtime};

use crate::state::AppState;
use crate::{tray, window};

/// The app menu's Settings… item. Its `app:` prefix keeps it apart from the tray's ids.
pub const SETTINGS: &str = "app:settings";
/// The app menu's Quit, on ⌘Q: apart from the tray's Quit interface, which always ends the desktop.
pub const QUIT: &str = "app:quit";
/// The Help menu's Keyboard Shortcuts item, with no key of its own.
pub const SHORTCUTS: &str = "app:shortcuts";

/// Settings…'s key. The dashboard's list of shortcuts shows it, and a test of the interface reads it here.
pub const SETTINGS_ACCELERATOR: &str = "CmdOrCtrl+,";
/// Quit's key. The dashboard's list of shortcuts shows it, and a test of the interface reads it here.
pub const QUIT_ACCELERATOR: &str = "CmdOrCtrl+Q";

/// What a menu item asks for, by its id.
#[derive(Debug, PartialEq, Eq)]
pub enum Action<'a> {
    /// The app menu's Settings…: the dashboard's Settings screen.
    Settings,
    /// The Help menu's Keyboard Shortcuts: the Settings screen on its Shortcuts section.
    Shortcuts,
    /// The app menu's Quit: it ends the desktop, or only hides the window when `quit_hides` says so.
    Quit,
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
        SHORTCUTS => Some(Action::Shortcuts),
        QUIT => Some(Action::Quit),
        tray::OPEN => Some(Action::OpenDashboard),
        tray::STOP => Some(Action::StopDaemon),
        tray::QUIT => Some(Action::QuitInterface),
        other => other.strip_prefix(tray::EXECUTION_PREFIX).map(Action::Execution),
    }
}

/// Whether ⌘Q, or Quit in the app menu, only hides the window, as closing it does: while the tray is shown and the user
/// chose to keep it there. Otherwise Quit ends the desktop, and the daemon keeps running.
pub fn quit_hides(tray_enabled: bool, keep_tray_on_quit: bool) -> bool {
    tray_enabled && keep_tray_on_quit
}

/// The one handler of every menu event, the app menu's and the tray's.
pub fn on_event<R: Runtime>(app: &AppHandle<R>, id: &str) {
    match action(id) {
        Some(Action::Settings) => window::show_screen(app, "settings", None),
        Some(Action::Shortcuts) => window::show_screen(app, "settings", Some("shortcuts")),
        Some(Action::Quit) => {
            let state = app.state::<Arc<AppState>>();
            if quit_hides(state.tray_enabled(), state.keep_tray_on_quit()) {
                window::hide_main(app);
            } else {
                app.exit(0);
            }
        }
        Some(Action::OpenDashboard) => window::show_main(app),
        Some(Action::StopDaemon) => tray::confirm_stop(app),
        Some(Action::QuitInterface) => app.exit(0),
        Some(Action::Execution(execution_id)) => window::show_execution(app, execution_id),
        None => {}
    }
}

/// Tauri's default menu with Settings… after About and its separator, followed by a separator, as macOS apps place it,
/// a Quit of HarmFlow's own, and Keyboard Shortcuts in the Help menu, which is empty on macOS. Starting from the default
/// menu keeps the Edit menu, which copy and paste in text fields need, and the Window menu.
#[cfg(target_os = "macos")]
pub fn build<R: Runtime>(app: &AppHandle<R>) -> tauri::Result<tauri::menu::Menu<R>> {
    use tauri::menu::{Menu, MenuItem, PredefinedMenuItem, HELP_SUBMENU_ID};

    let menu = Menu::default(app)?;
    if let Some(help_menu) = menu.get(HELP_SUBMENU_ID).as_ref().and_then(MenuItemKind::as_submenu) {
        let label = shortcuts_label(&app.state::<Arc<AppState>>());
        help_menu.append(&MenuItem::with_id(app, SHORTCUTS, label, true, None::<&str>)?)?;
    }
    let submenus = menu.items()?;
    if let Some(app_menu) = submenus.first().and_then(MenuItemKind::as_submenu) {
        let label = settings_label(&app.state::<Arc<AppState>>());
        let settings = MenuItem::with_id(app, SETTINGS, label, true, Some(SETTINGS_ACCELERATOR))?;
        app_menu.insert_items(&[&settings, &PredefinedMenuItem::separator(app)?], 2)?;

        // The default app menu ends with the predefined Quit, which sends terminate: to macOS: no handler of the app's
        // sees it, so it could not keep the tray. An item of HarmFlow's own takes its place, its text, and ⌘Q. Quit from
        // the Dock and logging out still terminate.
        let items = app_menu.items()?;
        if let Some(quit) = items.last().and_then(MenuItemKind::as_predefined_menuitem) {
            let text = quit.text()?;
            if text.starts_with("Quit") {
                app_menu.remove(quit)?;
                app_menu.append(&MenuItem::with_id(app, QUIT, text, true, Some(QUIT_ACCELERATOR))?)?;
            }
        }
    }
    Ok(menu)
}

/// Sets Settings… and Keyboard Shortcuts in the interface's language, which the webview sends with the tray's texts.
pub fn render<R: Runtime>(app: &AppHandle<R>) {
    let Some(menu) = app.menu() else { return };
    let state = app.state::<Arc<AppState>>();
    if let Ok(items) = menu.items() {
        let item = items.first().and_then(MenuItemKind::as_submenu).and_then(|m| m.get(SETTINGS));
        if let Some(settings) = item.as_ref().and_then(MenuItemKind::as_menuitem) {
            let _ = settings.set_text(settings_label(&state));
        }
    }
    let help_menu = menu.get(tauri::menu::HELP_SUBMENU_ID);
    let item = help_menu.as_ref().and_then(MenuItemKind::as_submenu).and_then(|m| m.get(SHORTCUTS));
    if let Some(shortcuts) = item.as_ref().and_then(MenuItemKind::as_menuitem) {
        let _ = shortcuts.set_text(shortcuts_label(&state));
    }
}

fn settings_label(state: &AppState) -> String {
    state.label("settings", "Settings…")
}

fn shortcuts_label(state: &AppState) -> String {
    state.label("shortcuts", "Keyboard Shortcuts")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn settings_opens_the_settings_screen() {
        assert_eq!(action(SETTINGS), Some(Action::Settings));
    }

    #[test]
    fn keyboard_shortcuts_opens_the_shortcuts_section() {
        assert_eq!(action(SHORTCUTS), Some(Action::Shortcuts));
    }

    #[test]
    fn keyboard_shortcuts_is_none_of_the_other_items() {
        // Its id reads as no other item, and no other item's id reads as it.
        for id in [SETTINGS, QUIT, "open", "stop", "quit", "header", "shortcuts", "exec:app:shortcuts"] {
            assert_ne!(action(id), Some(Action::Shortcuts), "{id}");
        }
        assert_ne!(action(SHORTCUTS), Some(Action::Settings));
    }

    #[test]
    fn the_app_menus_quit_is_not_the_trays_quit_interface() {
        assert_eq!(action(QUIT), Some(Action::Quit));
        assert_eq!(action("quit"), Some(Action::QuitInterface));
    }

    #[test]
    fn quit_hides_the_window_only_with_the_tray_and_the_choice_on() {
        // The arguments: tray_enabled, keep_tray_on_quit.
        assert!(quit_hides(true, true));
        assert!(!quit_hides(true, false));
        assert!(!quit_hides(false, true));
        assert!(!quit_hides(false, false));
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
