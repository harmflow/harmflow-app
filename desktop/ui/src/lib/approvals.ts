import type { HarmflowEvent } from "../api/types";

/** The kinds of request a harness asks the owner to approve, as an approval event's payload.kind names them. */
export const APPROVAL_KINDS = ["command", "file_change", "permissions"] as const;

/** One approval request a running execution's harness waits on, as its approval.requested event's payload carries it. */
export interface PendingApproval {
  requestId: string;
  kind: "command" | "file_change" | "permissions" | string;
  command?: string;
  cwd?: string;
  reason?: string;
  networkHost?: string;
  paths?: string[];
  network?: boolean;
  /** The attempt that asked, whose session alone an answer for the session covers. */
  attempt?: number;
  harness?: string;
}

/**
 * The approval requests that still wait for the owner: each requested in the attempt and neither answered nor
 * withdrawn since. A request belongs to its attempt, so one of an earlier attempt never waits in a later one.
 */
export function pendingApprovals(events: HarmflowEvent[]): PendingApproval[] {
  const waiting = new Map<string, PendingApproval>();
  const key = (attempt: number | undefined, id: unknown) => `${attempt ?? 0}:${String(id)}`;
  let lastAttempt = 0;
  for (const e of events) {
    if (e.attempt && e.attempt > lastAttempt) {
      lastAttempt = e.attempt;
      for (const [k, q] of waiting) if ((q.attempt ?? 0) < lastAttempt) waiting.delete(k);
    }
    const p = (e.payload ?? {}) as Record<string, unknown>;
    if (e.type === "approval.requested" && typeof p.requestId === "string") {
      waiting.set(key(e.attempt, p.requestId), {
        requestId: p.requestId,
        kind: typeof p.kind === "string" ? p.kind : "command",
        command: typeof p.command === "string" ? p.command : undefined,
        cwd: typeof p.cwd === "string" ? p.cwd : undefined,
        reason: typeof p.reason === "string" ? p.reason : undefined,
        networkHost: typeof p.networkHost === "string" ? p.networkHost : undefined,
        paths: Array.isArray(p.paths) ? p.paths.filter((x): x is string => typeof x === "string") : undefined,
        network: p.network === true,
        attempt: e.attempt,
        harness: e.harness,
      });
    } else if (e.type === "approval.answered" || e.type === "approval.withdrawn") {
      waiting.delete(key(e.attempt, p.requestId));
    }
  }
  return [...waiting.values()];
}
