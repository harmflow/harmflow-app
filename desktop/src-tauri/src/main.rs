// Keeps a console window from opening on Windows release builds.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    harmflow_desktop_lib::run();
}
