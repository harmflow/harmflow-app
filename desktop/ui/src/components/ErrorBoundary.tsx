// What the window shows when the interface fails to render, instead of an empty, dark window (harmflow-app#96).
import { Component, useEffect, type ErrorInfo, type ReactNode } from "react";
import type { RootOptions } from "react-dom/client";
import { useTranslation } from "react-i18next";
import { native } from "../api/native";
import { Icon } from "./Icon";
import { Button, CopyButton } from "./ui";

interface Failure {
  error: unknown;
  componentStack?: string;
}

/**
 * Logs every render error, caught or not: a release build has no console to open, but a development one does, and the
 * logs reach whatever collects the webview's output.
 */
export const rootOptions: RootOptions = {
  onUncaughtError: (error, info) => console.error("HarmFlow: uncaught render error", error, info.componentStack),
  onCaughtError: (error, info) => console.error("HarmFlow: render error", error, info.componentStack),
};

class Boundary extends Component<{ fallback: (failure: Failure) => ReactNode; children: ReactNode }, { failure: Failure | null }> {
  state: { failure: Failure | null } = { failure: null };

  static getDerivedStateFromError(error: unknown) {
    return { failure: { error } };
  }

  componentDidCatch(error: unknown, info: ErrorInfo) {
    this.setState({ failure: { error, componentStack: info.componentStack ?? undefined } });
  }

  render() {
    return this.state.failure ? this.props.fallback(this.state.failure) : this.props.children;
  }
}

function message(error: unknown): string {
  return error instanceof Error ? error.message || error.name : String(error);
}

/** The message, its stack, and React's component stack, as one text to copy into a report. */
function details({ error, componentStack }: Failure): string {
  const stack = error instanceof Error ? error.stack : undefined;
  return [message(error), stack, componentStack?.trim()].filter(Boolean).join("\n\n");
}

/** A screen that throws: the sidebar and the header, with the connection state, stay on screen. */
function ScreenFailure({ failure }: { failure: Failure }) {
  const { t } = useTranslation();
  const text = details(failure);
  return (
    <div className="screen">
      <div className="crash" role="alert">
        <Icon name="alert" size={26} className="crash__icon" />
        <h2>{t("crash.screenTitle")}</h2>
        <p className="crash__message mono">{message(failure.error)}</p>
        <p className="crash__hint">{t("crash.hint")}</p>
        <Button variant="primary" icon="refresh" onClick={() => window.location.reload()}>
          {t("common.reload")}
        </Button>
        <details className="crash__details">
          <summary>
            <Icon name="chevronRight" size={14} />
            {t("crash.details")}
          </summary>
          <div className="codebox">
            <div className="codebox__tools">
              <CopyButton text={text} />
            </div>
            <pre className="mono">{text}</pre>
          </div>
        </details>
      </div>
    </div>
  );
}

/** Anything the screen boundary does not catch: only the message and a reload, with nothing else that could throw. */
function RootFailure({ failure }: { failure: Failure }) {
  const { t } = useTranslation();
  // The window shows this at once rather than after the shell's fallback; a repeated signal changes nothing.
  useEffect(() => void native.interfaceReady(), []);
  return (
    <div className="crash-root">
      <div className="crash" role="alert">
        <Icon name="alert" size={26} className="crash__icon" />
        <h1>{t("crash.rootTitle")}</h1>
        <p className="crash__message mono">{message(failure.error)}</p>
        <Button variant="primary" icon="refresh" onClick={() => window.location.reload()}>
          {t("common.reload")}
        </Button>
      </div>
    </div>
  );
}

/** Wraps the screen below the header; give it the screen as its key, so navigating away leaves the error behind. */
export function ScreenBoundary({ children }: { children: ReactNode }) {
  return <Boundary fallback={(failure) => <ScreenFailure failure={failure} />}>{children}</Boundary>;
}

/** Wraps the whole interface: the root never ends up empty. */
export function RootBoundary({ children }: { children: ReactNode }) {
  return <Boundary fallback={(failure) => <RootFailure failure={failure} />}>{children}</Boundary>;
}
