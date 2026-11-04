// The dashboard's entry file: its registration point. `main.tsx` renders the dashboard through it with nothing
// registered; another build renders it with registrations, each adding screens, Settings sections, and their texts.
// Changing what it exports, or the shape of a registration, is a breaking change.
import { StrictMode } from "react";
import { createRoot, type Root as ReactRoot } from "react-dom/client";
import { Root } from "./App";
import { RootBoundary, rootOptions } from "./components/ErrorBoundary";
import "./i18n";
import { register, type Registered, type Registration } from "./registrations";
import "./design/fonts.css";
import "./design/tokens.css";
import "./design/app.css";

export type { RegisteredScreen, RegisteredSettingsSection, Registration, SettingsGroup } from "./registrations";

/** Throws the failure it is given while rendering, so the root boundary shows it as it shows any other. */
function Failed({ error }: { error: unknown }): never {
  throw error;
}

/**
 * Renders the dashboard into element, with what each registration adds, and returns the root it rendered into. The
 * registrations are checked before anything renders: one that fails shows the dashboard's failure screen with its
 * message, as a render error does, and nothing of the dashboard starts — no screen, and no connection to the daemon.
 */
export function renderDashboard(element: HTMLElement, ...registrations: Registration[]): ReactRoot {
  let registered: Registered | undefined;
  let failure: unknown;
  try {
    registered = register(registrations);
  } catch (error) {
    failure = error;
  }
  const root = createRoot(element, rootOptions);
  // The first render replaces the placeholder of index.html.
  root.render(
    <StrictMode>
      {registered ? (
        <Root registered={registered} />
      ) : (
        <RootBoundary>
          <Failed error={failure} />
        </RootBoundary>
      )}
    </StrictMode>,
  );
  return root;
}
