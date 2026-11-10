//! A test's control of the shell, in a debug build on macOS only: nothing of it reaches a release build. With
//! `HARMFLOW_TEST_SHELL_CONTROL` set to `1`, the shell reads one command per line on its standard input and answers each
//! with one JSON line on its standard output. A test closes the window as its close button does, reads the window's and
//! the tray's state, and opens the dashboard as the tray's item does, with no Accessibility permission
//! (harmflow-app#464). It reads the app menu macOS shows, chooses one of its items or one of the tray's, and asks the
//! app to quit as the Dock's Quit does (harmflow-app#655). It reads the notices macOS delivered for the shell
//! (harmflow-app#515), and the title and the image of the tray's status item macOS shows (harmflow-app#880). It reads
//! what the window's page shows and clicks in it, as a user's click does (harmflow-app#873), with the height each
//! element renders at (harmflow-app#912). It reads whether the start's show is decided and whether macOS shows the
//! tray's status item, and calls what the start's fallback calls (harmflow-app#508). It opens the tray's menu as a click
//! does and reads it as macOS shows it, and reads which windows of the system's alert process macOS shows
//! (harmflow-app#903). A test of what else the shell does adds its own command here.

use std::io::{BufRead, Write};
use std::sync::atomic::Ordering;
use std::sync::mpsc;
use std::sync::Arc;
use std::time::Duration;

use block2::RcBlock;
use objc2::runtime::AnyObject;
use objc2::{sel, AllocAnyThread, MainThreadMarker};
use objc2_app_kit::{NSApplication, NSBitmapImageRep, NSEventModifierFlags, NSEventTrackingRunLoopMode, NSImage, NSMenu};
use objc2_core_graphics::{kCGNullWindowID, CGWindowListCopyWindowInfo, CGWindowListOption};
use objc2_foundation::{ns_string, NSArray, NSData, NSDictionary, NSNumber, NSObjectNSDelayedPerforming, NSRunLoop, NSString};
use serde_json::{json, Value};
use tauri::menu::MenuItemKind;
use tauri::{AppHandle, Manager, Runtime};

use crate::state::AppState;
use crate::{menu, tray, window};

/// The variable that turns the channel on, set by the test that starts the shell.
pub const VARIABLE: &str = "HARMFLOW_TEST_SHELL_CONTROL";

#[derive(Debug, PartialEq, Eq)]
enum Command<'a> {
    /// Whether the window is visible, whether the tray is enabled, whether ⌘Q keeps the tray, and whether the
    /// interface's readiness or the fallback already decided the start's show.
    State,
    /// What the start's fallback calls once it elapses, `window::show_at_start`, at once: a hidden start schedules no
    /// fallback, and a normal one waits `window::SHOW_FALLBACK` for it.
    FallbackShow,
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
    /// The title of the tray's status item macOS shows, which of the tray's two images it shows, and whether macOS
    /// shows the item.
    Tray,
    /// The items of the tray's menu macOS shows, opened as a click on the tray's status item opens it, in order: each
    /// with its id, its text, and whether it is enabled, and the separators.
    TrayMenu,
    /// The windows macOS shows on screen for the system's alert process, which draws an alert that has no parent
    /// window, as the tray's stop dialog has none.
    SystemAlerts,
    /// The text, the tooltip, and the rendered height in CSS pixels of each element of the window's page that this CSS
    /// selector matches, as the page shows them, in document order.
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
        ("fallback-show", "") => Some(Command::FallbackShow),
        ("close", "") => Some(Command::Close),
        ("open-dashboard", "") => Some(Command::OpenDashboard),
        ("app-menu", "") => Some(Command::AppMenu),
        ("choose-app-menu", id) if !id.is_empty() => Some(Command::ChooseAppMenu(id)),
        ("choose-tray", id) if !id.is_empty() => Some(Command::ChooseTray(id)),
        ("terminate", "") => Some(Command::Terminate),
        ("notices", "") => Some(Command::Notices),
        ("tray", "") => Some(Command::Tray),
        ("tray-menu", "") => Some(Command::TrayMenu),
        ("system-alerts", "") => Some(Command::SystemAlerts),
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
                "startShowDecided": state.start_show_decided.load(Ordering::SeqCst),
            })
        }
        Command::FallbackShow => {
            window::show_at_start(app, false, true);
            json!({ "ok": true })
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
        Command::TrayMenu => tray_menu(app),
        Command::SystemAlerts => system_alerts(),
        Command::Read(selector) => in_page(
            app,
            &format!(
                "({{ elements: Array.from(document.querySelectorAll({}), (e) => ({{ text: e.innerText, title: e.title, height: e.getBoundingClientRect().height }})) }})",
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
/// item's button, `null` when it shows none, which of the tray's two images the button shows, told by its pixels
/// (`tray_image`), and whether macOS shows the item (`NSStatusItem`'s `visible`). It reads neither the state the tray
/// decided nor what `tray::render` asked for.
fn shown_tray<R: Runtime>(app: &AppHandle<R>) -> Value {
    let Some(tray) = app.tray_by_id(tray::TRAY_ID) else { return json!({ "error": "the app has no tray" }) };
    let shown = tray.with_inner_tray_icon(|inner| {
        let mtm = MainThreadMarker::new().expect("with_inner_tray_icon runs on the main thread");
        // No status item while the tray is hidden: tray-icon removes it from the status bar.
        let item = inner.ns_status_item()?;
        let button = item.button(mtm)?;
        // A tray with no title sets an empty one on the button.
        let title = button.title().to_string();
        let image = button.image().map_or("none", |image| tray_image(&image));
        Some(json!({ "title": (!title.is_empty()).then_some(title), "image": image, "visible": item.isVisible() }))
    });
    match shown {
        Ok(Some(answer)) => answer,
        Ok(None) => json!({ "error": "macOS shows no status item for the tray" }),
        Err(err) => json!({ "error": err.to_string() }),
    }
}

/// An item of a menu as macOS shows it: its text, whether it is enabled, and whether it is a separator.
type Shown = (String, bool, bool);

/// The tray's menu as macOS shows it. The command opens it as a click on the tray's status item opens it — tray-icon
/// sets the menu on the item only while a click opens it, and `show_menu` takes the same steps as its click — reads the
/// items from the item's `NSMenu` while macOS tracks it, and closes it. Each item is named by the id Tauri gave it, from
/// the menu the tray holds (`tray::HeldMenu`), matched by position as `app_menu` matches the app menu's; its text, its
/// enabled state, the separators, and the order are those macOS shows, not what `tray::menu_entries` asked for.
fn tray_menu<R: Runtime>(app: &AppHandle<R>) -> Value {
    let ids = match tray_menu_ids(app) {
        Ok(ids) => ids,
        Err(err) => return json!({ "error": err }),
    };
    let Some(tray) = app.tray_by_id(tray::TRAY_ID) else { return json!({ "error": "the app has no tray" }) };
    let (tx, rx) = mpsc::channel::<Result<Vec<Shown>, String>>();
    let scheduled = app.run_on_main_thread(move || {
        // A click reaches the status item from the run loop, outside the event loop's handler this closure runs in
        // (harmflow-app#805), and the menu it opens runs the run loop until it closes: the menu opens on the next turn
        // of the run loop, as a click's does.
        let open = RcBlock::new(move || {
            let tx = tx.clone();
            let failed = tx.clone();
            let opened = tray.with_inner_tray_icon(move |inner| {
                let mtm = MainThreadMarker::new().expect("the run loop's block runs on the main thread");
                let Some(item) = inner.ns_status_item() else {
                    let _ = tx.send(Err("macOS shows no status item for the tray".into()));
                    return;
                };
                // macOS runs the run loop in its event-tracking mode while the menu is open: the block reads the menu
                // then, and closes it, which ends the click.
                let read = RcBlock::new(move || {
                    let shown = item.menu(mtm).map(|menu| {
                        let shown = shown_menu(&menu);
                        menu.cancelTracking();
                        shown
                    });
                    let _ = tx.send(shown.ok_or_else(|| "the tray's status item holds no menu while it is open".into()));
                });
                // SAFETY: the mode is AppKit's constant, and the main run loop runs the block on the main thread, where
                // it was made.
                unsafe {
                    let modes = NSArray::from_slice(&[NSEventTrackingRunLoopMode]);
                    NSRunLoop::mainRunLoop().performInModes_block(&modes, &read);
                }
                inner.show_menu();
            });
            if let Err(err) = opened {
                let _ = failed.send(Err(err.to_string()));
            }
        });
        // SAFETY: as above, with Foundation's constant.
        unsafe {
            let modes = NSArray::from_slice(&[objc2_foundation::NSDefaultRunLoopMode]);
            NSRunLoop::mainRunLoop().performInModes_block(&modes, &open);
        }
    });
    if let Err(err) = scheduled {
        return json!({ "error": err.to_string() });
    }
    let shown = match rx.recv_timeout(Duration::from_secs(5)) {
        Ok(Ok(shown)) => shown,
        Ok(Err(err)) => return json!({ "error": err }),
        Err(err) => return json!({ "error": format!("the tray's menu did not open: {err}") }),
    };
    if shown.len() != ids.len() {
        return json!({ "error": format!("macOS shows {} items, the tray holds {}", shown.len(), ids.len()) });
    }
    let items: Vec<Value> = shown
        .into_iter()
        .zip(ids)
        .map(|((text, enabled, separator), id)| {
            if separator {
                json!({ "separator": true })
            } else {
                json!({ "id": id, "text": text, "enabled": enabled })
            }
        })
        .collect();
    json!({ "items": items })
}

/// The ids of the tray menu's items, from the menu Tauri built and the tray holds, in the order macOS lists them.
fn tray_menu_ids<R: Runtime>(app: &AppHandle<R>) -> Result<Vec<String>, String> {
    let held = app.try_state::<tray::HeldMenu<R>>().ok_or("the tray holds no menu")?;
    let menu = held.0.lock().map_err(|e| e.to_string())?.clone().ok_or("the tray holds no menu")?;
    let items = menu.items().map_err(|e| e.to_string())?;
    Ok(items.iter().map(|item| item.id().as_ref().to_string()).collect())
}

/// Each item of `menu` as macOS shows it, in order.
fn shown_menu(menu: &NSMenu) -> Vec<Shown> {
    (0..menu.numberOfItems())
        .filter_map(|i| menu.itemAtIndex(i))
        .map(|item| (item.title().to_string(), item.isEnabled(), item.isSeparatorItem()))
        .collect()
}

/// The process that draws an alert with no parent window. The dialog plugin hands macOS such a dialog, the tray's
/// stop dialog among them, as a user notification's alert, which this agent of the system shows in windows of its own.
const ALERT_PROCESS: &str = "UserNotificationCenter";

/// The windows macOS shows on screen for `ALERT_PROCESS`, from the window list macOS gives any process: each with its
/// process's pid and its window layer. It reads only who owns each window, never a window's title or what it shows,
/// which would need a permission.
fn system_alerts() -> Value {
    let options = CGWindowListOption::OptionOnScreenOnly | CGWindowListOption::ExcludeDesktopElements;
    let Some(list) = CGWindowListCopyWindowInfo(options, kCGNullWindowID) else {
        return json!({ "error": "macOS gives no window list" });
    };
    // SAFETY: a CFArray is toll-free bridged with an NSArray, and each window of the list is a CFDictionary with
    // CFString keys, toll-free bridged with an NSDictionary; `list` keeps them alive while `windows` is used.
    let windows = unsafe { &*std::ptr::from_ref(&*list).cast::<NSArray<NSDictionary<NSString, AnyObject>>>() };
    // The window list's keys are CGWindow's constants, each of which holds its own name.
    let alerts: Vec<Value> = windows
        .iter()
        .filter(|window| {
            let owner = window.objectForKey(ns_string!("kCGWindowOwnerName"));
            owner.and_then(|o| o.downcast::<NSString>().ok()).is_some_and(|name| name.to_string() == ALERT_PROCESS)
        })
        .map(|window| {
            let number = |key: &NSString| window.objectForKey(key).and_then(|o| o.downcast::<NSNumber>().ok()).map(|n| n.as_i64());
            json!({ "pid": number(ns_string!("kCGWindowOwnerPID")), "layer": number(ns_string!("kCGWindowLayer")) })
        })
        .collect();
    json!({ "alerts": alerts })
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
        assert_eq!(parse(" fallback-show\n"), Some(Command::FallbackShow));
        assert_eq!(parse("close"), Some(Command::Close));
        assert_eq!(parse(" open-dashboard "), Some(Command::OpenDashboard));
        assert_eq!(parse("app-menu\n"), Some(Command::AppMenu));
        assert_eq!(parse("choose-app-menu app:quit\n"), Some(Command::ChooseAppMenu("app:quit")));
        assert_eq!(parse("choose-tray  quit"), Some(Command::ChooseTray("quit")));
        assert_eq!(parse("terminate"), Some(Command::Terminate));
        assert_eq!(parse("notices\n"), Some(Command::Notices));
        assert_eq!(parse(" tray\n"), Some(Command::Tray));
        assert_eq!(parse("tray-menu\n"), Some(Command::TrayMenu));
        assert_eq!(parse(" system-alerts "), Some(Command::SystemAlerts));
        assert_eq!(parse("read .detail__alerts .banner\n"), Some(Command::Read(".detail__alerts .banner")));
        assert_eq!(parse("click  #settings-restriction-warnings "), Some(Command::Click("#settings-restriction-warnings")));
    }

    #[test]
    fn an_unknown_line_is_no_command() {
        for line in [
            "", "quit", "Close", "open dashboard", "state now", "choose-app-menu", "choose-tray ", "terminate x", "notice", "notices x", "tray x",
            "Tray", "read", "read ", "click", "Click #x", "fallback", "fallback-show now", "Fallback-show", "tray-menu x",
            "tray menu", "Tray-menu", "system-alerts now", "system-alert", "alerts",
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
