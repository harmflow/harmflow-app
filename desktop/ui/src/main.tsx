import { renderDashboard } from "./dashboard";

const root = document.getElementById("root");
// This build registers nothing: the dashboard is the one this repository builds.
if (root) renderDashboard(root);
