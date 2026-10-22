import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { Root } from "./App";
import { rootOptions } from "./components/ErrorBoundary";
import "./i18n";
import "./design/tokens.css";
import "./design/app.css";

const root = document.getElementById("root");
if (root) {
  // The first render replaces the placeholder of index.html.
  createRoot(root, rootOptions).render(
    <StrictMode>
      <Root />
    </StrictMode>,
  );
}
