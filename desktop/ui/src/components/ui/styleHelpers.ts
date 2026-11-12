// The class and status helpers the shared primitives, and the screens, draw with.
import type { ExecutionStatus } from "../../api/types";
import type { Tone } from "../../design/theme";
import type { IconName } from "../Icon";

export function cx(...classes: Array<string | false | null | undefined>): string {
  return classes.filter(Boolean).join(" ");
}

export function statusTone(status: ExecutionStatus | "scheduled"): Tone {
  switch (status) {
    case "preparing":
    case "running":
    case "testing":
    case "publishing":
      return "success";
    case "switching_harness":
      return "magenta";
    case "queued":
    case "scheduled":
      return "blue";
    case "completed":
      return "success";
    case "failed":
      return "danger";
    case "needs_attention":
    case "interrupted":
      return "warning";
    case "cancelled":
    case "no_work":
    default:
      return "neutral";
  }
}

/** The icon of a status, which StatusChip and StatusMark draw beside its text. */
export function statusIcon(status: ExecutionStatus | "scheduled"): IconName {
  switch (status) {
    case "completed":
      return "check";
    case "failed":
      return "x";
    case "cancelled":
      return "stop";
    case "needs_attention":
    case "interrupted":
      return "alert";
    case "queued":
    case "scheduled":
      return "clock";
    case "no_work":
      return "circle";
    case "switching_harness":
      return "switch";
    default:
      return "dot";
  }
}
