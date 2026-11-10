// What the task form's modules share: the row type of its lists, the setter they write with, the closed sets it offers
// in a select, and the cleaning of a list row.
import type { Path } from "../../lib/taskForm";

export type Item = Record<string, unknown>;

export type SetFn = (path: Path, value: unknown) => void;

/** The closed sets the form offers in a select: the key of their labels under tasks.options, and where they are written. */
export const OPTION_FIELDS = {
  ordering: ["spec", "github", "queue", "ordering"],
  resumeStrategy: ["spec", "github", "queue", "resumeStrategy"],
  profile: ["spec", "permissions", "profile"],
  network: ["spec", "permissions", "network"],
  missedRun: ["spec", "schedule", "missedRun", "policy"],
  overlap: ["spec", "concurrency", "overlappingRuns"],
} as const satisfies Record<string, Path>;

/** Removes undefined and empty-string keys from a list row, keeping the row itself. */
export function cleanRow(row: Item): Item {
  const out: Item = {};
  for (const [k, v] of Object.entries(row)) if (v !== undefined && v !== "") out[k] = v;
  return out;
}
