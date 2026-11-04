//! The desktop crate's registration point: what a build that links this crate as a library adds to the app before it
//! runs. [`crate::run_with`] takes the registrations; [`crate::run`] passes none.
//!
//! This is public API: changing what a registration can add, or how, is a breaking change.

use std::collections::HashMap;

use tauri::ipc::Invoke;
use tauri::plugin::Plugin;
use tauri::{Builder, Runtime, Wry};

/// A change a registration makes to the app's builder, such as adding a plugin.
type Apply<R> = Box<dyn FnOnce(Builder<R>) -> Builder<R> + Send>;

/// An invoke handler, as `tauri::generate_handler!` makes one: it answers the commands it knows, and returns false for
/// any other.
type Handler<R> = Box<dyn Fn(Invoke<R>) -> bool + Send + Sync>;

/// What one registration adds to the app: Tauri plugins, and commands the dashboard calls as it calls the app's own.
///
/// ```no_run
/// use harmflow_desktop_lib::{run_with, Registration};
///
/// #[tauri::command]
/// fn my_command() -> &'static str {
///     "answered"
/// }
///
/// let registration = Registration::new()
///     .plugin(tauri::plugin::Builder::<tauri::Wry>::new("my-plugin").build())
///     .commands(&["my_command"], tauri::generate_handler![my_command]);
/// run_with(vec![registration]);
/// ```
pub struct Registration<R: Runtime = Wry> {
    plugins: Vec<Apply<R>>,
    commands: Vec<(Vec<&'static str>, Handler<R>)>,
}

impl<R: Runtime> Default for Registration<R> {
    fn default() -> Self {
        Self::new()
    }
}

impl<R: Runtime> Registration<R> {
    /// A registration that adds nothing yet.
    pub fn new() -> Self {
        Self {
            plugins: Vec::new(),
            commands: Vec::new(),
        }
    }

    /// Adds a Tauri plugin to the app, after the app's own plugins. A plugin whose setup fails stops the start, as the
    /// app's own do.
    pub fn plugin<P: Plugin<R> + 'static>(mut self, plugin: P) -> Self {
        self.plugins.push(Box::new(move |builder| builder.plugin(plugin)));
        self
    }

    /// Adds commands the dashboard can call, as it calls the app's own: `names` are the commands `handler` answers,
    /// such as the handler `tauri::generate_handler!` makes of them. Each call to a name goes to `handler`; a name the
    /// app or another registration already has stops the start.
    pub fn commands<F>(mut self, names: &[&'static str], handler: F) -> Self
    where
        F: Fn(Invoke<R>) -> bool + Send + Sync + 'static,
    {
        self.commands.push((names.to_vec(), Box::new(handler)));
        self
    }
}

/// The app's own commands, from one list: the handler `tauri::generate_handler!` makes of them, and their names, which
/// [`assemble`] keeps a registration from taking.
macro_rules! app_commands {
    ($($command:ident),* $(,)?) => {
        $crate::registration::app_handler(tauri::generate_handler![$($command),*], &[$(stringify!($command)),*])
    };
}
pub(crate) use app_commands;

/// Pairs the app's handler with its commands' names, as [`assemble`] takes them. The closure `generate_handler!` makes
/// learns from this bound that it handles an `Invoke`, which a closure bound to a plain `let` cannot infer.
pub(crate) fn app_handler<R, F>(handler: F, names: &'static [&'static str]) -> (F, &'static [&'static str])
where
    R: Runtime,
    F: Fn(Invoke<R>) -> bool + Send + Sync + 'static,
{
    (handler, names)
}

/// Applies each registration to `builder`, then gives it its one invoke handler: a command a registration names goes
/// to that registration's handler, and any other to the app's, `app.0`, whose names are `app.1`. A command name that
/// the app already has, that two registrations add, or that only a plugin's commands take is an error, returned
/// before any registration is applied: the app is then never built, and nothing of it runs.
pub(crate) fn assemble<R, F>(
    mut builder: Builder<R>,
    app: (F, &[&'static str]),
    registrations: Vec<Registration<R>>,
) -> Result<Builder<R>, String>
where
    R: Runtime,
    F: Fn(Invoke<R>) -> bool + Send + Sync + 'static,
{
    let (app_handler, app_names) = app;
    // Each registered name and the index of its handler, in the order the handlers are collected below.
    let mut routes: HashMap<&'static str, usize> = HashMap::new();
    for (index, (names, _)) in registrations.iter().flat_map(|r| r.commands.iter()).enumerate() {
        for &name in names {
            if name.starts_with("plugin:") {
                return Err(format!(
                    "a registration adds the command {name}, a name only a plugin's commands take"
                ));
            }
            if app_names.contains(&name) {
                return Err(format!(
                    "a registration adds the command {name}, which the app already has"
                ));
            }
            if routes.insert(name, index).is_some() {
                return Err(format!("the command {name} is registered twice"));
            }
        }
    }

    let mut handlers: Vec<Handler<R>> = Vec::new();
    for registration in registrations {
        for apply in registration.plugins {
            builder = apply(builder);
        }
        handlers.extend(registration.commands.into_iter().map(|(_, handler)| handler));
    }
    Ok(builder.invoke_handler(
        move |invoke: Invoke<R>| match routes.get(invoke.message.command()).copied() {
            Some(index) => handlers[index](invoke),
            None => app_handler(invoke),
        },
    ))
}

#[cfg(test)]
mod tests {
    use std::sync::atomic::{AtomicBool, Ordering};

    use tauri::ipc::{CallbackFn, InvokeBody};
    use tauri::test::{get_ipc_response, mock_builder, mock_context, noop_assets, MockRuntime, INVOKE_KEY};
    use tauri::webview::InvokeRequest;
    use tauri::{App, Manager, WebviewWindowBuilder};

    use super::*;

    /// Stands in for one of the app's own commands.
    #[tauri::command]
    fn app_ping() -> &'static str {
        "app"
    }

    /// Stands in for a command a registration adds.
    #[tauri::command]
    fn added_ping() -> &'static str {
        "added"
    }

    /// Stands in for a second registration's command.
    #[tauri::command]
    fn other_ping() -> &'static str {
        "other"
    }

    /// The app as `run_with` assembles it, on the mock runtime, with `app_ping` as the app's only command.
    fn build(registrations: Vec<Registration<MockRuntime>>) -> Result<App<MockRuntime>, String> {
        assemble(mock_builder(), app_commands![app_ping], registrations)?
            .build(mock_context(noop_assets()))
            .map_err(|e| e.to_string())
    }

    /// Calls `command` from the app's main window, as the dashboard's `invoke` does, and gives back its answer.
    fn call(app: &App<MockRuntime>, command: &str) -> Result<String, String> {
        let window = match app.get_webview_window("main") {
            Some(window) => window,
            None => WebviewWindowBuilder::new(app, "main", Default::default())
                .build()
                .expect("the main window"),
        };
        let request = InvokeRequest {
            cmd: command.into(),
            callback: CallbackFn(0),
            error: CallbackFn(1),
            url: "tauri://localhost".parse().expect("the dashboard's URL"),
            body: InvokeBody::default(),
            headers: Default::default(),
            invoke_key: INVOKE_KEY.to_string(),
        };
        get_ipc_response(&window, request)
            .map(|body| body.deserialize::<String>().expect("a string answer"))
            .map_err(|error| error.to_string())
    }

    #[test]
    fn a_registered_command_answers_beside_the_apps() {
        let registration = Registration::new().commands(&["added_ping"], tauri::generate_handler![added_ping]);
        let app = build(vec![registration]).expect("the app builds");
        assert_eq!(call(&app, "added_ping"), Ok("added".into()));
        assert_eq!(call(&app, "app_ping"), Ok("app".into()));
    }

    #[test]
    fn each_registrations_commands_go_to_its_own_handler() {
        let first = Registration::new().commands(&["added_ping"], tauri::generate_handler![added_ping]);
        let second = Registration::new().commands(&["other_ping"], tauri::generate_handler![other_ping]);
        let app = build(vec![first, second]).expect("the app builds");
        assert_eq!(call(&app, "added_ping"), Ok("added".into()));
        assert_eq!(call(&app, "other_ping"), Ok("other".into()));
        assert_eq!(call(&app, "app_ping"), Ok("app".into()));
    }

    /// What a stub plugin's setup leaves in the app's state.
    struct SetUp(&'static str);

    #[test]
    fn a_registered_plugin_is_set_up_with_the_app() {
        let plugin = tauri::plugin::Builder::<MockRuntime>::new("stub")
            .setup(|app, _api| {
                app.manage(SetUp("by the stub plugin"));
                Ok(())
            })
            .build();
        let app = build(vec![Registration::new().plugin(plugin)]).expect("the app builds");
        assert_eq!(app.state::<SetUp>().0, "by the stub plugin");
        assert_eq!(call(&app, "app_ping"), Ok("app".into()));
    }

    #[test]
    fn with_nothing_registered_only_the_apps_commands_answer() {
        let app = build(Vec::new()).expect("the app builds");
        assert_eq!(call(&app, "app_ping"), Ok("app".into()));
        assert_eq!(call(&app, "added_ping"), Err("\"Command added_ping not found\"".into()));
    }

    #[test]
    fn a_default_registration_adds_nothing() {
        let app = build(vec![Registration::default()]).expect("the app builds");
        assert_eq!(call(&app, "app_ping"), Ok("app".into()));
        assert_eq!(call(&app, "added_ping"), Err("\"Command added_ping not found\"".into()));
    }

    /// Set by the plugin of a registration the app refuses, which must never be set up.
    static REFUSED_SET_UP: AtomicBool = AtomicBool::new(false);

    #[test]
    fn a_command_the_app_has_is_refused_before_anything_is_built() {
        let plugin = tauri::plugin::Builder::<MockRuntime>::new("refused")
            .setup(|_app, _api| {
                REFUSED_SET_UP.store(true, Ordering::SeqCst);
                Ok(())
            })
            .build();
        let registration = Registration::new()
            .plugin(plugin)
            .commands(&["app_ping"], tauri::generate_handler![added_ping]);
        assert_eq!(
            build(vec![registration]).err(),
            Some("a registration adds the command app_ping, which the app already has".into())
        );
        assert!(!REFUSED_SET_UP.load(Ordering::SeqCst));
    }

    #[test]
    fn a_command_two_registrations_add_is_refused() {
        let first = Registration::new().commands(&["added_ping"], tauri::generate_handler![added_ping]);
        let second = Registration::new().commands(&["added_ping"], tauri::generate_handler![other_ping]);
        assert_eq!(
            build(vec![first, second]).err(),
            Some("the command added_ping is registered twice".into())
        );
    }

    #[test]
    fn a_plugin_command_name_is_refused() {
        let registration = Registration::new().commands(&["plugin:stub|ping"], tauri::generate_handler![added_ping]);
        assert_eq!(
            build(vec![registration]).err(),
            Some("a registration adds the command plugin:stub|ping, a name only a plugin's commands take".into())
        );
    }

    #[test]
    fn a_plugin_whose_setup_fails_stops_the_build() {
        let plugin = tauri::plugin::Builder::<MockRuntime>::new("failing")
            .setup(|_app, _api| Err("the stub plugin cannot start".into()))
            .build();
        let Err(err) = build(vec![Registration::new().plugin(plugin)]) else {
            panic!("build() = Ok, want the plugin's error");
        };
        assert!(
            err.contains("the stub plugin cannot start"),
            "build() = Err({err:?}), want the plugin's error"
        );
    }
}
