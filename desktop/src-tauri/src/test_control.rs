//! A test's control of the shell, in a debug build on macOS only: nothing of it reaches a release build. With
//! `HARMFLOW_TEST_SHELL_CONTROL` set to `1`, the shell reads one command per line on its standard input and answers each
//! with one JSON line on its standard output. A test closes the window as its close button does, reads the window's and
//! the tray's state, and opens the dashboard as the tray's item does, with no Accessibility permission
//! (harmflow-app#464). A test of what else the shell does adds its own command here.

use std::io::{BufRead, Write};
use std::sync::Arc;

use serde_json::{json, Value};
use tauri::{AppHandle, Manager, Runtime};

use crate::state::AppState;
use crate::{menu, tray, window};

/// The variable that turns the channel on, set by the test that starts the shell.
pub const VARIABLE: &str = "HARMFLOW_TEST_SHELL_CONTROL";

#[derive(Debug, PartialEq, Eq)]
enum Command {
    /// Whether the window is visible, and whether the tray is enabled.
    State,
    /// Closes the window through `close()`, which raises the same `CloseRequested` its close button does.
    Close,
    /// The tray's Open dashboard, through the handler of every menu event.
    OpenDashboard,
}

fn parse(line: &str) -> Option<Command> {
    match line.trim() {
        "state" => Some(Command::State),
        "close" => Some(Command::Close),
        "open-dashboard" => Some(Command::OpenDashboard),
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
            json!({ "visible": visible, "trayEnabled": app.state::<Arc<AppState>>().tray_enabled() })
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
    }

    #[test]
    fn an_unknown_line_is_no_command() {
        for line in ["", "quit", "Close", "open dashboard"] {
            assert_eq!(parse(line), None, "{line:?}");
        }
    }
}
