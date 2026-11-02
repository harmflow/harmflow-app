//! A test's control of the shell, in a debug build on macOS only: nothing of it reaches a release build. With
//! `HARMFLOW_TEST_SHELL_CONTROL` set to `1`, the shell reads one command per line on its standard input and answers each
//! with one JSON line on its standard output. A test closes the window as its close button does, reads the window's and
//! the tray's state, and opens the dashboard as the tray's item does, with no Accessibility permission
//! (harmflow-app#464). It reads the app menu macOS shows, chooses one of its items or one of the tray's, and asks the
//! app to quit as the Dock's Quit does (harmflow-app#655). A test of what else the shell does adds its own command here.

use std::io::{BufRead, Write};
use std::sync::mpsc;
use std::sync::Arc;
use std::time::Duration;

use objc2::MainThreadMarker;
use objc2_app_kit::{NSApplication, NSEventModifierFlags, NSMenu};
use serde_json::{json, Value};
use tauri::menu::MenuItemKind;
use tauri::{AppHandle, Manager, Runtime};

use crate::state::AppState;
use crate::{menu, tray, window};

/// The variable that turns the channel on, set by the test that starts the shell.
pub const VARIABLE: &str = "HARMFLOW_TEST_SHELL_CONTROL";

#[derive(Debug, PartialEq, Eq)]
enum Command<'a> {
    /// Whether the window is visible, whether the tray is enabled, and whether ⌘Q keeps the tray.
    State,
    /// Closes the window through `close()`, which raises the same `CloseRequested` its close button does.
    Close,
    /// The tray's Open dashboard, through the handler of every menu event.
    OpenDashboard,
    /// The items of the app menu macOS shows, in order: each with its id, its text, its key equivalent and modifiers,
    /// and the selector macOS sends when it is chosen.
    AppMenu,
    /// Chooses the app menu's item with this id: macOS performs the item's action, as a click or its shortcut does.
    ChooseAppMenu(&'a str),
    /// Chooses the tray menu's item with this id, through the handler of every menu event.
    ChooseTray(&'a str),
    /// Sends the app `terminate:`, the request macOS sends when the user quits it from the Dock.
    Terminate,
}

fn parse(line: &str) -> Option<Command<'_>> {
    let line = line.trim();
    let (name, arg) = line.split_once(' ').map_or((line, ""), |(name, arg)| (name, arg.trim()));
    match (name, arg) {
        ("state", "") => Some(Command::State),
        ("close", "") => Some(Command::Close),
        ("open-dashboard", "") => Some(Command::OpenDashboard),
        ("app-menu", "") => Some(Command::AppMenu),
        ("choose-app-menu", id) if !id.is_empty() => Some(Command::ChooseAppMenu(id)),
        ("choose-tray", id) if !id.is_empty() => Some(Command::ChooseTray(id)),
        ("terminate", "") => Some(Command::Terminate),
        _ => None,
    }
}

/// Starts reading commands when the variable says so; otherwise does nothing.
pub fn listen<R: Runtime>(app: &AppHandle<R>) {
    if std::env::var(VARIABLE).as_deref() != Ok("1") {
        return;
    }
    let app = app.clone();
    std::thread::spawn(move || {
        for line in std::io::stdin().lock().lines() {
            let Ok(line) = line else { break };
            let answer = match parse(&line) {
                Some(command) => run(&app, command),
                None => json!({ "error": format!("unknown command: {}", line.trim()) }),
            };
            let mut out = std::io::stdout().lock();
            let _ = writeln!(out, "{answer}");
            let _ = out.flush();
        }
    });
}

fn run<R: Runtime>(app: &AppHandle<R>, command: Command) -> Value {
    match command {
        Command::State => {
            let visible = app.get_webview_window(window::MAIN).and_then(|w| w.is_visible().ok()).unwrap_or(false);
            let state = app.state::<Arc<AppState>>();
            json!({
                "visible": visible,
                "trayEnabled": state.tray_enabled(),
                "keepTrayOnQuit": state.keep_tray_on_quit(),
            })
        }
        Command::Close => match app.get_webview_window(window::MAIN).map(|w| w.close()) {
            Some(Ok(())) => json!({ "ok": true }),
            Some(Err(err)) => json!({ "error": err.to_string() }),
            None => json!({ "error": "no main window" }),
        },
        Command::OpenDashboard => {
            menu::on_event(app, tray::OPEN);
            json!({ "ok": true })
        }
        Command::AppMenu => app_menu(app),
        Command::ChooseAppMenu(id) => choose_app_menu(app, id),
        Command::ChooseTray(id) => {
            if ![tray::OPEN, tray::STOP, tray::QUIT].contains(&id) {
                return json!({ "error": format!("no tray item {id}") });
            }
            menu::on_event(app, id);
            json!({ "ok": true })
        }
        Command::Terminate => {
            // The answer goes out before macOS ends the app, which may then exit before the test reads it.
            let _ = app.run_on_main_thread(|| {
                let mtm = MainThreadMarker::new().expect("run_on_main_thread runs on the main thread");
                NSApplication::sharedApplication(mtm).terminate(None);
            });
            json!({ "ok": true })
        }
    }
}

/// The ids of the app menu's items, from the menu Tauri built, in the order macOS lists them.
fn app_menu_ids<R: Runtime>(app: &AppHandle<R>) -> Result<Vec<(String, &'static str)>, String> {
    let menu = app.menu().ok_or("the app has no menu")?;
    let submenus = menu.items().map_err(|e| e.to_string())?;
    let app_menu = submenus.first().and_then(MenuItemKind::as_submenu).ok_or("the menu has no app submenu")?;
    let items = app_menu.items().map_err(|e| e.to_string())?;
    Ok(items
        .iter()
        .map(|item| {
            let kind = match item {
                MenuItemKind::MenuItem(_) => "item",
                MenuItemKind::Submenu(_) => "submenu",
                MenuItemKind::Predefined(_) => "predefined",
                MenuItemKind::Check(_) => "check",
                MenuItemKind::Icon(_) => "icon",
            };
            (item.id().as_ref().to_string(), kind)
        })
        .collect())
}

/// Runs `f` with the app submenu macOS shows, on the main thread, as AppKit asks, and returns what it returns.
fn with_app_submenu<R: Runtime, T: Send + 'static>(
    app: &AppHandle<R>,
    f: impl FnOnce(&NSMenu) -> T + Send + 'static,
) -> Result<T, String> {
    let (tx, rx) = mpsc::channel();
    app.run_on_main_thread(move || {
        let mtm = MainThreadMarker::new().expect("run_on_main_thread runs on the main thread");
        let submenu = NSApplication::sharedApplication(mtm).mainMenu().and_then(|m| m.itemAtIndex(0)).and_then(|i| i.submenu());
        let _ = tx.send(submenu.map(|menu| f(&menu)));
    })
    .map_err(|e| e.to_string())?;
    rx.recv_timeout(Duration::from_secs(5))
        .map_err(|e| format!("the main thread did not answer: {e}"))?
        .ok_or_else(|| "macOS shows no app menu".to_string())
}

/// The app menu as macOS shows it, each item beside the id Tauri gave it.
fn app_menu<R: Runtime>(app: &AppHandle<R>) -> Value {
    let ids = match app_menu_ids(app) {
        Ok(ids) => ids,
        Err(err) => return json!({ "error": err }),
    };
    let shown = with_app_submenu(app, |menu| {
        (0..menu.numberOfItems())
            .filter_map(|i| menu.itemAtIndex(i))
            .map(|item| {
                let flags = item.keyEquivalentModifierMask();
                let modifiers: Vec<&str> = [
                    (NSEventModifierFlags::Command, "command"),
                    (NSEventModifierFlags::Shift, "shift"),
                    (NSEventModifierFlags::Option, "option"),
                    (NSEventModifierFlags::Control, "control"),
                ]
                .into_iter()
                .filter(|(flag, _)| flags.contains(*flag))
                .map(|(_, name)| name)
                .collect();
                json!({
                    "text": item.title().to_string(),
                    "separator": item.isSeparatorItem(),
                    "key": item.keyEquivalent().to_string(),
                    "modifiers": modifiers,
                    "action": item.action().map(|sel| sel.name().to_string_lossy().into_owned()),
                })
            })
            .collect::<Vec<Value>>()
    });
    let mut shown = match shown {
        Ok(shown) => shown,
        Err(err) => return json!({ "error": err }),
    };
    if shown.len() != ids.len() {
        return json!({ "error": format!("macOS shows {} items, the app built {}", shown.len(), ids.len()) });
    }
    for (item, (id, kind)) in shown.iter_mut().zip(ids) {
        item["id"] = json!(id);
        item["kind"] = json!(kind);
    }
    json!({ "items": shown })
}

/// Chooses the app menu's item with this id in the menu macOS shows: `performActionForItemAtIndex:` sends the item's
/// action as a click on it or its shortcut does, which reaches `menu::on_event` through Tauri's menu event.
fn choose_app_menu<R: Runtime>(app: &AppHandle<R>, id: &str) -> Value {
    let index = match app_menu_ids(app) {
        Ok(ids) => ids.iter().position(|(item, _)| item == id),
        Err(err) => return json!({ "error": err }),
    };
    let Some(index) = index else { return json!({ "error": format!("no app menu item {id}") }) };
    // The answer goes out once the action ran; an action that quits may end the app before the test reads it.
    match with_app_submenu(app, move |menu| menu.performActionForItemAtIndex(index as isize)) {
        Ok(()) => json!({ "ok": true }),
        Err(err) => json!({ "error": err }),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn each_command_is_read_from_its_line() {
        assert_eq!(parse("state\n"), Some(Command::State));
        assert_eq!(parse("close"), Some(Command::Close));
        assert_eq!(parse(" open-dashboard "), Some(Command::OpenDashboard));
        assert_eq!(parse("app-menu\n"), Some(Command::AppMenu));
        assert_eq!(parse("choose-app-menu app:quit\n"), Some(Command::ChooseAppMenu("app:quit")));
        assert_eq!(parse("choose-tray  quit"), Some(Command::ChooseTray("quit")));
        assert_eq!(parse("terminate"), Some(Command::Terminate));
    }

    #[test]
    fn an_unknown_line_is_no_command() {
        for line in ["", "quit", "Close", "open dashboard", "state now", "choose-app-menu", "choose-tray ", "terminate x"] {
            assert_eq!(parse(line), None, "{line:?}");
        }
    }
}
