//! A test's control of the shell, in a debug build on macOS only: nothing of it reaches a release build. With
//! `HARMFLOW_TEST_SHELL_CONTROL` set to `1`, the shell reads one command per line on its standard input and answers each
//! with one JSON line on its standard output. A test closes the window as its close button does, reads the window's and
//! the tray's state, and opens the dashboard as the tray's item does, with no Accessibility permission
//! (harmflow-app#464). It reads the app menu macOS shows, chooses one of its items or one of the tray's, and asks the
//! app to quit as the Dock's Quit does (harmflow-app#655). It reads the notices macOS delivered for the shell
//! (harmflow-app#515), and the title and the image of the tray's status item macOS shows (harmflow-app#880). It reads
//! what the window's page shows and clicks in it, as a user's click does (harmflow-app#873). A test of what else the
//! shell does adds its own command here.

use std::io::{BufRead, Write};
use std::sync::mpsc;
use std::sync::Arc;
use std::time::Duration;

use objc2::{sel, AllocAnyThread, MainThreadMarker};
use objc2_app_kit::{NSApplication, NSBitmapImageRep, NSEventModifierFlags, NSImage, NSMenu};
use objc2_foundation::{NSData, NSObjectNSDelayedPerforming};
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
    /// Sends the app `terminate:`, the request macOS sends when the user quits it from the Dock, from the run loop as
    /// macOS does.
    Terminate,
    /// The notices macOS's notification center holds as delivered for the shell, with the identity they were sent under.
    Notices,
    /// The title of the tray's status item macOS shows, and which of the tray's two images it shows.
    Tray,
    /// The text and the tooltip of each element of the window's page that this CSS selector matches, as the page shows
    /// them, in document order.
    Read(&'a str),
    /// Clicks the first element of the window's page that this CSS selector matches, as a user's click does: the page
    /// gets the same click event, and a checkbox or a button acts on it.
    Click(&'a str),
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
        ("notices", "") => Some(Command::Notices),
        ("tray", "") => Some(Command::Tray),
        ("read", selector) if !selector.is_empty() => Some(Command::Read(selector)),
        ("click", selector) if !selector.is_empty() => Some(Command::Click(selector)),
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
                // Not terminate: itself: this closure runs inside the event loop's handler, which holds the event
                // loop's lock, and terminate: ends that loop under the same lock on the same thread, so the shell
                // hangs (harmflow-app#805). The Dock's Quit reaches the app from the run loop, outside the handler;
                // the next turn of the run loop sends terminate: from there too.
                let ns_app = NSApplication::sharedApplication(mtm);
                // SAFETY: terminate: is NSApplication's, and takes its sender, which may be nil.
                unsafe { ns_app.performSelector_withObject_afterDelay(sel!(terminate:), None, 0.0) };
            });
            json!({ "ok": true })
        }
        Command::Notices => delivered(),
        Command::Tray => shown_tray(app),
        Command::Read(selector) => in_page(
            app,
            &format!(
                "({{ elements: Array.from(document.querySelectorAll({}), (e) => ({{ text: e.innerText, title: e.title }})) }})",
                js_string(selector)
            ),
        ),
        Command::Click(selector) => {
            let selector = js_string(selector);
            in_page(
                app,
                &format!(
                    "(() => {{ const e = document.querySelector({selector}); if (!e) return {{ error: \"nothing matches \" + {selector} }}; e.click(); return {{ ok: true }}; }})()"
                ),
            )
        }
    }
}

/// The tray as macOS shows it, read on the main thread from the status item Tauri's tray holds: the title of the
/// item's button, `null` when it shows none, and which of the tray's two images the button shows, told by its pixels
/// (`tray_image`). It reads neither the state the tray decided nor what `tray::render` asked for.
fn shown_tray<R: Runtime>(app: &AppHandle<R>) -> Value {
    let Some(tray) = app.tray_by_id(tray::TRAY_ID) else { return json!({ "error": "the app has no tray" }) };
    let shown = tray.with_inner_tray_icon(|inner| {
        let mtm = MainThreadMarker::new().expect("with_inner_tray_icon runs on the main thread");
        // No status item while the tray is hidden.
        let button = inner.ns_status_item()?.button(mtm)?;
        // A tray with no title sets an empty one on the button.
        let title = button.title().to_string();
        let image = button.image().map_or("none", |image| tray_image(&image));
        Some(json!({ "title": (!title.is_empty()).then_some(title), "image": image }))
    });
    match shown {
        Ok(Some(answer)) => answer,
        Ok(None) => json!({ "error": "macOS shows no status item for the tray" }),
        Err(err) => json!({ "error": err.to_string() }),
    }
}

/// An image's pixels as AppKit holds them: its width and height in pixels, the bytes of a row, and the bytes of every
/// row, read back from its TIFF representation.
type Pixels = (isize, isize, isize, Vec<u8>);

/// The pixels of `image`, or `None` when AppKit gives them in no single plane.
fn pixels(image: &NSImage) -> Option<Pixels> {
    let tiff = image.TIFFRepresentation()?;
    let rep = NSBitmapImageRep::imageRepWithData(&tiff)?;
    let (wide, high, row) = (rep.pixelsWide(), rep.pixelsHigh(), rep.bytesPerRow());
    let data = rep.bitmapData();
    if rep.isPlanar() || data.is_null() || high <= 0 || row <= 0 {
        return None;
    }
    // SAFETY: a meshed bitmap holds `bytesPerRow` bytes for each of its rows at `bitmapData`, which `rep` keeps alive.
    let bytes = unsafe { std::slice::from_raw_parts(data, (row * high) as usize) }.to_vec();
    Some((wide, high, row, bytes))
}

/// The pixels of a PNG as AppKit decodes it, as it decodes the PNG the tray-icon crate hands macOS for the tray.
fn decoded(png: &[u8]) -> Option<Pixels> {
    let image = NSImage::initWithData(NSImage::alloc(), &NSData::with_bytes(png))?;
    pixels(&image)
}

/// Which of the tray's two images `shown` is: `idle` or `executing` when its pixels are those of `tray::ICON_IDLE` or
/// `tray::ICON_EXECUTING` decoded the same way, `unknown` when they are neither's.
fn tray_image(shown: &NSImage) -> &'static str {
    let Some(shown) = pixels(shown) else { return "unknown" };
    [("idle", tray::ICON_IDLE), ("executing", tray::ICON_EXECUTING)]
        .into_iter()
        .find(|(_, png)| decoded(png).as_ref() == Some(&shown))
        .map_or("unknown", |(name, _)| name)
}

/// A string as a JavaScript literal: JSON's string syntax is JavaScript's.
fn js_string(s: &str) -> String {
    Value::from(s).to_string()
}

/// Evaluates `expression` in the main window's page and returns its value, which must be one JSON can hold. The page
/// answers through WebKit's completion handler, on the main thread, so the control thread waits for it with a timeout.
fn in_page<R: Runtime>(app: &AppHandle<R>, expression: &str) -> Value {
    let Some(window) = app.get_webview_window(window::MAIN) else {
        return json!({ "error": "no main window" });
    };
    // WebKit answers a script that throws with no value, and the conversion of a value JSON cannot hold, such as an
    // element, would fail in wry: the page itself turns either into a string, the error included.
    let script = format!(
        "(() => {{ try {{ return JSON.stringify({expression}); }} catch (e) {{ return JSON.stringify({{ error: String(e) }}); }} }})()"
    );
    let (tx, rx) = mpsc::channel();
    if let Err(err) = window.eval_with_callback(script, move |answer| {
        let _ = tx.send(answer);
    }) {
        return json!({ "error": err.to_string() });
    }
    let answer = match rx.recv_timeout(Duration::from_secs(5)) {
        Ok(answer) => answer,
        Err(err) => return json!({ "error": format!("the page did not answer: {err}") }),
    };
    // wry hands back the script's value as JSON, here a string, which holds the JSON of the expression's value.
    serde_json::from_str::<String>(&answer)
        .ok()
        .and_then(|text| serde_json::from_str(&text).ok())
        .unwrap_or_else(|| json!({ "error": format!("the page answered {answer:?}") }))
}

/// The notices delivered for the shell, read back from macOS's notification center: the notification plugin hands
/// macOS each notice through that center, under the bundle identifier it set for the process. Until the first notice
/// sets one, an unbundled debug build has none and no center to read, which the answer says with `bundle: null`.
#[allow(deprecated)] // The plugin delivers through NSUserNotificationCenter, so its notices are read there too.
fn delivered() -> Value {
    use objc2_foundation::{NSBundle, NSUserNotificationCenter};

    let Some(bundle) = NSBundle::mainBundle().bundleIdentifier() else {
        return json!({ "bundle": null, "notices": [] });
    };
    let notices: Vec<Value> = NSUserNotificationCenter::defaultUserNotificationCenter()
        .deliveredNotifications()
        .iter()
        .map(|n| {
            json!({
                "title": n.title().map(|s| s.to_string()).unwrap_or_default(),
                "body": n.informativeText().map(|s| s.to_string()).unwrap_or_default(),
            })
        })
        .collect();
    json!({ "bundle": bundle.to_string(), "notices": notices })
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
        assert_eq!(parse("notices\n"), Some(Command::Notices));
        assert_eq!(parse(" tray\n"), Some(Command::Tray));
        assert_eq!(parse("read .detail__alerts .banner\n"), Some(Command::Read(".detail__alerts .banner")));
        assert_eq!(parse("click  #settings-restriction-warnings "), Some(Command::Click("#settings-restriction-warnings")));
    }

    #[test]
    fn an_unknown_line_is_no_command() {
        for line in [
            "", "quit", "Close", "open dashboard", "state now", "choose-app-menu", "choose-tray ", "terminate x", "notice", "notices x", "tray x",
            "Tray", "read", "read ", "click", "Click #x",
        ] {
            assert_eq!(parse(line), None, "{line:?}");
        }
    }

    /// An image as the tray-icon crate hands macOS the tray's: the RGBA pixels of Tauri's `Image`, encoded again as
    /// an 8-bit RGBA PNG and decoded by AppKit.
    fn as_the_tray_holds(width: u32, height: u32, rgba: &[u8]) -> objc2::rc::Retained<NSImage> {
        let mut encoded = Vec::new();
        let mut encoder = png::Encoder::new(&mut encoded, width, height);
        encoder.set_color(png::ColorType::Rgba);
        encoder.set_depth(png::BitDepth::Eight);
        encoder.write_header().and_then(|mut writer| writer.write_image_data(rgba)).expect("the pixels encode");
        NSImage::initWithData(NSImage::alloc(), &NSData::with_bytes(&encoded)).expect("AppKit decodes the PNG")
    }

    fn tray_holds(icon: &[u8]) -> objc2::rc::Retained<NSImage> {
        let image = tauri::image::Image::from_bytes(icon).expect("a tray image decodes");
        as_the_tray_holds(image.width(), image.height(), image.rgba())
    }

    #[test]
    fn each_tray_image_is_told_by_its_pixels() {
        assert_eq!(tray_image(&tray_holds(tray::ICON_IDLE)), "idle");
        assert_eq!(tray_image(&tray_holds(tray::ICON_EXECUTING)), "executing");
        assert!(decoded(tray::ICON_IDLE).is_some());
        assert_ne!(decoded(tray::ICON_IDLE), decoded(tray::ICON_EXECUTING));
    }

    #[test]
    fn an_image_of_neither_is_unknown() {
        let image = tauri::image::Image::from_bytes(tray::ICON_IDLE).expect("a tray image decodes");
        let mut rgba = image.rgba().to_vec();
        // One pixel's alpha differs from the idle image's.
        rgba[3] ^= 0xff;
        assert_eq!(tray_image(&as_the_tray_holds(image.width(), image.height(), &rgba)), "unknown");
    }

    #[test]
    fn a_selector_reaches_the_page_as_a_string_literal() {
        assert_eq!(js_string(r#"[title*="a \"b\""]"#), r#""[title*=\"a \\\"b\\\"\"]""#);
        assert_eq!(js_string("`${x}`\n"), r#""`${x}`\n""#);
    }
}
