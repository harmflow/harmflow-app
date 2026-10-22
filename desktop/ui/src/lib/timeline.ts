// Turns normalized events into the steps of the execution timeline. The label of each type and the daemon's own
// messages are translated; the agent's text, commands, and tool output stay as the harness produced them
// (FS-012 FR-005).
import type { HarmflowEvent } from "../api/types";
import type { Tone } from "../design/tokens";
import { eventText } from "./daemonText";

export type TimelineIcon =
  | "play"
  | "check"
  | "x"
  | "stop"
  | "agent"
  | "terminal"
  | "file"
  | "pencil"
  | "search"
  | "branch"
  | "test"
  | "pr"
  | "switch"
  | "alert"
  | "shield"
  | "clock"
  | "message"
  | "dot";

export interface TimelineItem {
  id: number;
  event: HarmflowEvent;
  /** i18n key of the type's label, under `eventTypes`. */
  labelKey: string;
  /** Text that replaces the label when the event carries a better one, such as a step's own name. */
  title?: string;
  /** Monospaced detail: a command, a path, the agent's text, never translated; or the daemon's message, translated. */
  detail?: string;
  tone: Tone;
  icon: TimelineIcon;
  /** For a tool step: finished, with success or failure. Undefined while it runs. */
  completed?: { ok: boolean; at: string };
  attempt?: number;
}

/** Events that belong to the raw-output and output tabs rather than to the step timeline. */
const RAW_TYPES = new Set(["harness.output", "script.output"]);
/** Events with no place in an execution's own timeline. */
const HIDDEN_TYPES = new Set(["notification.created", "execution.status_changed"]);

type Payload = Record<string, unknown>;

function str(v: unknown): string | undefined {
  return typeof v === "string" && v.trim() !== "" ? v : undefined;
}

function payloadOf(e: HarmflowEvent): Payload {
  return (e.payload ?? {}) as Payload;
}

/** The identifier that pairs a tool.completed with its tool.started. */
export function toolKey(e: HarmflowEvent): string | undefined {
  const p = payloadOf(e);
  return str(p.toolUseId) ?? str(p.toolId) ?? str(p.id);
}

export function toolDetail(e: HarmflowEvent): string | undefined {
  const p = payloadOf(e);
  const input = (p.input ?? {}) as Payload;
  return (
    str(p.command) ??
    str(input.command) ??
    str(p.path) ??
    str(input.file_path) ??
    str(input.path) ??
    str(input.pattern) ??
    str(p.description) ??
    str(input.description) ??
    str(eventText(e))
  );
}

function toolIcon(e: HarmflowEvent): TimelineIcon {
  const p = payloadOf(e);
  const tool = (str(p.tool) ?? str(p.kind) ?? "").toLowerCase();
  if (/(bash|shell|command|exec)/.test(tool)) return "terminal";
  if (/(edit|write|patch|file_change|multiedit|notebook)/.test(tool)) return "pencil";
  if (/(read|view)/.test(tool)) return "file";
  if (/(grep|glob|search|find|web)/.test(tool)) return "search";
  return "terminal";
}

function toolTitle(e: HarmflowEvent): string | undefined {
  const p = payloadOf(e);
  return str(p.title) ?? str(p.tool);
}

interface Style {
  tone: Tone;
  icon: TimelineIcon;
}

function styleOf(e: HarmflowEvent): Style {
  switch (e.type) {
    case "execution.created":
    case "execution.started":
      return { tone: "cyan", icon: "play" };
    case "execution.completed":
      return { tone: "success", icon: "check" };
    case "execution.no_work":
      return { tone: "neutral", icon: "check" };
    case "execution.failed":
    case "harness.no_alternative":
      return { tone: "danger", icon: "x" };
    case "execution.cancelled":
    case "execution.cancel_requested":
      return { tone: "neutral", icon: "stop" };
    case "execution.interrupted":
    case "execution.needs_attention":
      return { tone: "warning", icon: "alert" };
    case "harness.started":
      return { tone: "violet", icon: "agent" };
    case "harness.message":
      return { tone: "violet", icon: "message" };
    case "harness.exited":
      return { tone: "violet", icon: "agent" };
    case "harness.switched":
      return { tone: "magenta", icon: "switch" };
    case "harness.quota_exhausted":
    case "harness.unavailable":
    case "harness.rate_limited":
    case "harness.version_untested":
      return { tone: "warning", icon: "alert" };
    case "script.started":
      return { tone: "cyan", icon: "terminal" };
    case "script.exited":
      return { tone: e.severity === "info" ? "success" : "danger", icon: "terminal" };
    case "tool.started":
    case "tool.completed":
      return { tone: "blue", icon: toolIcon(e) };
    case "step.started":
      return { tone: "cyan", icon: "dot" };
    case "work_item.selected":
      return { tone: "cyan", icon: "file" };
    case "work_item.conflicting_activity":
      return { tone: "warning", icon: "alert" };
    case "workspace.changed":
      return { tone: "cyan", icon: "branch" };
    case "test.started":
      return { tone: "magenta", icon: "test" };
    case "test.completed":
      return { tone: payloadOf(e).status === "failed" ? "danger" : "success", icon: "test" };
    case "test.failed":
      return { tone: "danger", icon: "test" };
    case "pull_request.created":
    case "pull_request.updated":
      return { tone: "success", icon: "pr" };
    case "policy.unenforced":
    case "repository.unprotected":
      return { tone: "warning", icon: "shield" };
    case "firing.queued":
    case "firing.coalesced":
    case "firing.missed":
    case "firing.skipped":
      return { tone: "neutral", icon: "clock" };
    default:
      if (e.severity === "error") return { tone: "danger", icon: "x" };
      if (e.severity === "warning") return { tone: "warning", icon: "alert" };
      return { tone: "neutral", icon: "dot" };
  }
}

export function labelKey(type: string): string {
  return `eventTypes.${type}`;
}

/**
 * Builds the timeline of one execution: one item per meaningful event, in order, with each tool.completed folded into
 * the tool.started it ends.
 */
export function buildTimeline(events: readonly HarmflowEvent[]): TimelineItem[] {
  const items: TimelineItem[] = [];
  const openTools = new Map<string, TimelineItem>();
  const seen = new Set<number>();

  for (const e of [...events].sort((a, b) => a.id - b.id)) {
    if (seen.has(e.id)) continue;
    seen.add(e.id);
    if (RAW_TYPES.has(e.type) || HIDDEN_TYPES.has(e.type)) continue;

    if (e.type === "tool.completed") {
      const key = toolKey(e);
      const open = key ? openTools.get(key) : undefined;
      if (open) {
        const p = payloadOf(e);
        const failed = p.isError === true || p.status === "failed" || (typeof p.exitCode === "number" && p.exitCode !== 0);
        open.completed = { ok: !failed, at: e.timestamp };
        if (key) openTools.delete(key);
        continue;
      }
    }

    const { tone, icon } = styleOf(e);
    const item: TimelineItem = {
      id: e.id,
      event: e,
      labelKey: labelKey(e.type),
      tone,
      icon,
      attempt: e.attempt,
    };

    if (e.type === "tool.started" || e.type === "tool.completed") {
      item.title = toolTitle(e);
      item.detail = toolDetail(e);
    } else if (e.type === "step.started") {
      item.title = str(eventText(e));
      item.detail = str(payloadOf(e).detail);
    } else {
      item.detail = str(eventText(e));
    }

    items.push(item);
    if (e.type === "tool.started") {
      const key = toolKey(e);
      if (key) openTools.set(key, item);
    }
  }
  return items;
}

/** Files the events say changed, deduplicated, in order of first mention. */
export function changedFilesFromEvents(events: readonly HarmflowEvent[]): string[] {
  const files: string[] = [];
  const add = (v: unknown) => {
    const s = str(v);
    if (s && !files.includes(s)) files.push(s);
  };
  for (const e of events) {
    const p = payloadOf(e);
    if (e.type === "workspace.changed") {
      add(p.path);
      if (Array.isArray(p.paths)) p.paths.forEach(add);
      if (Array.isArray(p.files)) p.files.forEach(add);
    }
    if (e.type === "tool.started" && toolIcon(e) === "pencil") {
      const input = (p.input ?? {}) as Payload;
      add(p.path ?? input.file_path ?? input.path);
    }
  }
  return files;
}

/** The latest step of an execution, for its card: never a made-up percentage (FS-008 FR-009). */
export function currentStepFrom(events: readonly HarmflowEvent[]): HarmflowEvent | undefined {
  const timeline = buildTimeline(events);
  return timeline.length ? timeline[timeline.length - 1].event : undefined;
}
