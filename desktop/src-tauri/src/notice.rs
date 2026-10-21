//! Clicks on the native notices (FS-011 FR-003). The interface sends the notices, naming the execution each concerns;
//! the click is handled here, since only the shell can bring back the window while it is hidden.

use tauri::{AppHandle, Runtime};
use tauri_plugin_notification::{ActionPerformed, NotificationExt};

use crate::window;

/// The key of a notice's `extra` payload that names its execution, as `native.notify` sets it.
const EXECUTION_ID: &str = "executionId";

/// Where a click on a notice leads.
#[derive(Debug, PartialEq)]
enum Target {
    Dashboard,
    Execution(String),
}

/// Reports the clicks of every notice shown from now on. The plugin reports nothing for a notice shown before.
pub fn listen<R: Runtime>(app: &AppHandle<R>) -> tauri_plugin_notification::Result<()> {
    let handle = app.clone();
    app.notification().on_action(move |performed| match target(performed) {
        Some(Target::Execution(id)) => window::show_execution(&handle, &id),
        Some(Target::Dashboard) => window::show_main(&handle),
        None => {}
    })
}

/// Only a click on the notice itself (`tap`) leads anywhere: the notices offer no action buttons.
fn target(performed: &ActionPerformed) -> Option<Target> {
    if performed.action_id() != "tap" {
        return None;
    }
    let id = performed
        .notification()
        .and_then(|n| n.extra().get(EXECUTION_ID))
        .and_then(|v| v.as_str())
        .filter(|id| !id.is_empty());
    Some(id.map_or(Target::Dashboard, |id| Target::Execution(id.to_owned())))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::{json, Value};

    /// An action as the desktop plugin reports it: the notification is the payload the interface sent.
    fn performed(action_id: &str, notification: Value) -> ActionPerformed {
        serde_json::from_value(json!({ "actionId": action_id, "inputValue": null, "notification": notification })).unwrap()
    }

    #[test]
    fn a_click_opens_the_execution_the_notice_names() {
        let p = performed("tap", json!({ "id": 7, "title": "Failed", "extra": { "executionId": "exec-8f3a2c" } }));
        assert_eq!(target(&p), Some(Target::Execution("exec-8f3a2c".into())));
    }

    #[test]
    fn a_click_on_a_notice_without_an_execution_opens_the_dashboard() {
        for notification in [json!({ "id": 7 }), json!({ "id": 7, "extra": { "executionId": "" } }), json!({ "id": "?" })] {
            assert_eq!(target(&performed("tap", notification)), Some(Target::Dashboard));
        }
    }

    #[test]
    fn other_actions_lead_nowhere() {
        let p = performed("dismiss", json!({ "id": 7, "extra": { "executionId": "exec-8f3a2c" } }));
        assert_eq!(target(&p), None);
    }
}
