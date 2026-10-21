// PROTOTYPE — throwaway, not production code. Three variants of a "Costs" tab in the execution detail, switchable via
// `?variant=A|B|C` (stub data via `?scenario=`), on the existing Executions and History screens.
// Question: what should the estimated token spend of an execution look like?
// The stub scenarios are invented and their prices illustrative; the "real" scenario parses the selected execution's
// raw output in the browser (realUsage.prototype.ts), because the daemon does not record token usage yet.
import { useEffect, useMemo, useState } from "react";
import { api } from "../../../api/client";
import type { Execution, ExecutionOutput } from "../../../api/types";
import { HarnessLabel } from "../../Shell";
import { Icon } from "../../Icon";
import { Banner, Button, Chip, CopyButton, EmptyState, KeyValue, cx } from "../../ui";
import { PrototypeSwitcher, useSearchParam } from "../PrototypeSwitcher";
import { REAL_PRICES, parseClaude, parseCodex } from "./realUsage.prototype";
import "./costs.prototype.css";

// ---------- Stub model: what the daemon would have to record per attempt ----------

type Harness = "claude-code" | "codex";
type Outcome = string;
/**
 * reported: the harness printed its own cost (Claude Code's `total_cost_usd`); estimated: tokens × price table;
 * partial: no result line, so the output tokens are a lower bound; unpriced: tokens without a price; none: no usage.
 */
type Source = "reported" | "estimated" | "partial" | "unpriced" | "none";

interface Tokens {
  input: number;
  cacheWrite: number;
  cacheRead: number;
  output: number;
}

/** USD per million tokens. */
interface Price extends Tokens {
  name: string;
}

interface Turn extends Tokens {
  /** Seconds since the execution started. */
  at: number;
  label: string;
  /** The output count is a lower bound (Claude Code's per-message usage, before the result line). */
  provisional?: boolean;
}

interface AttemptUsage {
  attempt: number;
  harness: Harness;
  model: string;
  outcome: Outcome;
  source: Source;
  price?: Price;
  /** Seconds since the execution started, when it differs from the first step. */
  startedAt?: number;
  turns: Turn[];
  /** What the harness itself printed as the cost, to check the price table against. */
  reportedCostUsd?: number;
  /** Why there is no price, when there is none. */
  unpricedReason?: string;
  apiKeySource?: string;
}

interface Scenario {
  key: string;
  name: string;
  running: boolean;
  pricesAsOf: string;
  attempts: AttemptUsage[];
  note?: string;
}

type RecordAttempt = NonNullable<Execution["attempts"]>[number];

/** The "real" scenario: the selected execution's own attempts, parsed from its raw output. */
function realScenario(out: ExecutionOutput, attempts: RecordAttempt[], start: number, running: boolean): Scenario {
  const parsed = attempts.map((a): AttemptUsage => {
    const text = out.attempts.find((c) => c.attempt === a.number)?.text ?? "";
    const p = a.harness === "codex" ? parseCodex(text) : parseClaude(text);
    const model = p.models[0];
    const rp = model ? REAL_PRICES[model] : undefined;
    const price = rp
      ? { name: rp.name, input: rp.input, cacheWrite: rp.cacheWrite1h * p.cacheWrite1hShare + rp.cacheWrite5m * (1 - p.cacheWrite1hShare), cacheRead: rp.cacheRead, output: rp.output }
      : undefined;
    const source: Source = p.kind === "none" ? "none" : !price ? "unpriced" : p.kind === "reported" ? "reported" : p.kind === "partial" ? "partial" : "estimated";
    const s0 = a.startedAt ? Math.max(0, (Date.parse(a.startedAt) - start) / 1000) : 0;
    const s1 = a.endedAt ? (Date.parse(a.endedAt) - start) / 1000 : s0 + 60;
    return {
      attempt: a.number,
      harness: a.harness as Harness,
      model: model ?? a.model ?? "default",
      outcome: a.outcome ?? "running",
      source,
      price,
      startedAt: s0,
      // Raw lines carry no timestamps: spread the steps over the attempt (the daemon would stamp them on arrival).
      turns: p.steps.map((st, i) => ({ ...st, at: Math.round(s0 + ((i + 1) / p.steps.length) * Math.max(1, s1 - s0)) })),
      reportedCostUsd: p.reportedCostUsd,
      unpricedReason:
        a.harness === "codex"
          ? `Codex does not name its model in --json output; the task asked for "${a.model ?? "default"}"`
          : model
            ? `no confirmed price for ${model}`
            : undefined,
      apiKeySource: p.apiKeySource,
    };
  });
  const cut = out.attempts.some((c) => c.offset > 0);
  return {
    key: "real",
    name: "Real data (this execution)",
    running,
    pricesAsOf: "2026-10-21",
    attempts: parsed,
    note: `Parsed in the browser from this execution's raw output (${out.attempts.length} capture${out.attempts.length === 1 ? "" : "s"}${cut ? ", beginning cut" : ""}). Step times are spread evenly across each attempt.`,
  };
}

const PRICES: Record<string, Price> = {
  haiku: { name: "Claude Haiku", input: 1, cacheWrite: 1.25, cacheRead: 0.1, output: 5 },
  sonnet: { name: "Claude Sonnet", input: 3, cacheWrite: 3.75, cacheRead: 0.3, output: 15 },
  "gpt-5-codex": { name: "GPT-5 Codex", input: 1.25, cacheWrite: 0, cacheRead: 0.125, output: 10 },
};

const t = (at: number, label: string, input: number, cacheWrite: number, cacheRead: number, output: number): Turn => ({
  at,
  label,
  input,
  cacheWrite,
  cacheRead,
  output,
});

const SONNET_TURNS: Turn[] = [
  t(6, "Read CLAUDE.md", 12, 18_400, 0, 210),
  t(21, "Bash gh issue view 42", 8, 2_100, 18_400, 180),
  t(48, "Read daemon/internal/app/executor.go", 10, 9_800, 20_500, 240),
  t(95, 'Grep "attempt"', 6, 1_200, 30_300, 150),
  t(140, "Edit executor.go", 14, 3_600, 31_500, 1_900),
  t(201, "Bash go test ./...", 8, 4_900, 35_100, 320),
  t(260, "Edit executor_test.go", 12, 2_300, 40_000, 2_400),
  t(372, "Usage limit reached", 4, 0, 42_300, 60),
];

const SCENARIOS: Scenario[] = [
  {
    key: "claude",
    name: "Claude Code, 1 attempt",
    running: false,
    pricesAsOf: "2026-10-01",
    attempts: [
      {
        attempt: 1,
        harness: "claude-code",
        model: "haiku",
        outcome: "succeeded",
        source: "reported",
        price: PRICES.haiku,
        turns: [
          t(9, "Read …/napoleon.yaml", 8, 14_200, 12_900, 160),
          t(10, "Bash ls -la …", 6, 1_100, 27_100, 95),
          t(23, "Bash ls -la …", 6, 2_300, 28_200, 120),
          t(28, "Read …", 8, 3_400, 30_500, 140),
          t(35, "Agent message", 14, 800, 33_900, 870),
        ],
      },
    ],
  },
  {
    key: "failover",
    name: "Failover Claude → Codex",
    running: false,
    pricesAsOf: "2026-10-01",
    attempts: [
      { attempt: 1, harness: "claude-code", model: "sonnet", outcome: "quota_exhausted", source: "reported", price: PRICES.sonnet, turns: SONNET_TURNS },
      {
        attempt: 2,
        harness: "codex",
        model: "gpt-5-codex",
        outcome: "succeeded",
        source: "estimated",
        price: PRICES["gpt-5-codex"],
        startedAt: 380,
        // `codex exec --json` reports usage once, on turn.completed: one step for the whole attempt.
        turns: [t(838, "turn.completed", 41_200, 0, 186_000, 14_300)],
      },
    ],
  },
  {
    key: "running",
    name: "Running (live)",
    running: true,
    pricesAsOf: "2026-10-01",
    attempts: [{ attempt: 1, harness: "claude-code", model: "sonnet", outcome: "running", source: "reported", price: PRICES.sonnet, turns: SONNET_TURNS.slice(0, 7) }],
  },
  {
    key: "unpriced",
    name: "Model without a price",
    running: false,
    pricesAsOf: "2026-10-01",
    attempts: [{ attempt: 1, harness: "codex", model: "o5-preview", outcome: "succeeded", source: "unpriced", turns: [t(412, "turn.completed", 22_800, 0, 96_400, 7_900)] }],
  },
  {
    key: "none",
    name: "No usage reported",
    running: false,
    pricesAsOf: "2026-10-01",
    attempts: [{ attempt: 1, harness: "claude-code", model: "sonnet", outcome: "succeeded", source: "none", turns: [] }],
  },
];

const VARIANTS = [
  { key: "AB", name: "Chosen: Summary → Receipt" },
  { key: "A", name: "Summary" },
  { key: "B", name: "Receipt" },
  { key: "C", name: "Over time" },
];

// ---------- Derived numbers ----------

const ZERO: Tokens = { input: 0, cacheWrite: 0, cacheRead: 0, output: 0 };
const CLASSES = ["input", "cacheWrite", "cacheRead", "output"] as const;
type TokenClass = (typeof CLASSES)[number];
const CLASS_LABEL: Record<TokenClass, string> = {
  input: "Input",
  cacheWrite: "Cache write",
  cacheRead: "Cache read",
  output: "Output",
};

function sum(list: Tokens[]): Tokens {
  return list.reduce((a, b) => ({ input: a.input + b.input, cacheWrite: a.cacheWrite + b.cacheWrite, cacheRead: a.cacheRead + b.cacheRead, output: a.output + b.output }), ZERO);
}

const total = (x: Tokens) => x.input + x.cacheWrite + x.cacheRead + x.output;

function costByClass(x: Tokens, p?: Price): Tokens | undefined {
  if (!p) return undefined;
  return { input: (x.input * p.input) / 1e6, cacheWrite: (x.cacheWrite * p.cacheWrite) / 1e6, cacheRead: (x.cacheRead * p.cacheRead) / 1e6, output: (x.output * p.output) / 1e6 };
}

interface AttemptView extends AttemptUsage {
  tokens: Tokens;
  cost?: number;
  costs?: Tokens;
}

function view(a: AttemptUsage): AttemptView {
  const tokens = sum(a.turns);
  const costs = a.source === "unpriced" ? undefined : costByClass(tokens, a.price);
  return { ...a, tokens, costs, cost: costs ? total(costs) : undefined };
}

// ---------- Formatting ----------

const usd = (n: number | undefined, locale: string): string => {
  if (n === undefined) return "—";
  const f = (v: number) => new Intl.NumberFormat(locale, { style: "currency", currency: "USD", minimumFractionDigits: v < 1 ? 4 : 2, maximumFractionDigits: v < 1 ? 4 : 2 }).format(v);
  return n > 0 && n < 0.0001 ? `< ${f(0.0001)}` : f(n);
};
const compact = (n: number, locale: string) => new Intl.NumberFormat(locale, { notation: "compact", maximumFractionDigits: 1 }).format(n);
const int = (n: number, locale: string) => new Intl.NumberFormat(locale).format(n);
const pct = (n: number, locale: string) => new Intl.NumberFormat(locale, { style: "percent", maximumFractionDigits: 0 }).format(n);
const clock = (start: number, at: number, locale: string) => new Date(start + at * 1000).toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit", second: "2-digit" });
const duration = (s: number) => (s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, "0")}s`);

const HARNESS_NAME: Record<Harness, string> = { "claude-code": "Claude Code", codex: "Codex" };

function SourceChip({ a }: { a: AttemptView }) {
  if (a.source === "reported") return <Chip tone="success" icon="check">reported by {HARNESS_NAME[a.harness]}</Chip>;
  if (a.source === "estimated") return <Chip tone="violet" icon="sparkle">estimated · price table</Chip>;
  if (a.source === "partial") return <Chip tone="warning" icon="alert">partial · no result line, output is a lower bound</Chip>;
  if (a.source === "unpriced")
    return (
      <Chip tone="neutral" icon="alert" title={a.unpricedReason}>
        no price for {a.model}
      </Chip>
    );
  return <Chip tone="neutral">no usage reported</Chip>;
}

function BillingNote({ asOf, attempts }: { asOf: string; attempts?: AttemptView[] }) {
  const subscription = attempts?.some((a) => a.apiKeySource === "none");
  const reasons = (attempts ?? []).flatMap((a) => (a.source === "unpriced" && a.unpricedReason ? [`#${a.attempt}: ${a.unpricedReason}.`] : []));
  return (
    <p className="pc-note">
      <Icon name="shield" size={13} />
      <span>
        An estimate at API list prices of {asOf}. On a subscription plan the tokens are not billed one by one; this is what they would cost on
        the API.
        {subscription ? " This run used a subscription login: Claude Code reported no API key (apiKeySource: none)." : null}
        {reasons.length ? ` ${reasons.join(" ")}` : null}
      </span>
    </p>
  );
}

// ---------- The tab ----------

export function prototypeCostsRequested(): boolean {
  return import.meta.env.DEV && new URLSearchParams(window.location.search).has("variant");
}

export function CostsTabPrototype({
  executionId,
  recordAttempts,
  active,
  startedAt,
  locale,
}: {
  executionId: string;
  recordAttempts: RecordAttempt[];
  active: boolean;
  startedAt?: string;
  locale: string;
}) {
  const [variant, setVariant] = useSearchParam("variant", "AB");
  const [scenarioKey, setScenario] = useSearchParam("scenario", "real");
  const start = startedAt ? Date.parse(startedAt) : Date.now() - 60_000;

  // The real scenario reads the raw output through the existing API, and again every 2 s while the execution runs.
  const [real, setReal] = useState<Scenario | "loading" | "error">("loading");
  const attemptsKey = recordAttempts.map((a) => `${a.number}:${a.outcome ?? ""}`).join(",");
  useEffect(() => {
    if (scenarioKey !== "real") return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const load = async () => {
      try {
        const out = await api.executionOutput(executionId);
        if (!stopped) setReal(realScenario(out, recordAttempts, start, active));
      } catch {
        if (!stopped) setReal("error");
      }
      if (active && !stopped) timer = setTimeout(() => void load(), 2000);
    };
    setReal("loading");
    void load();
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- reload per execution, attempt set, or run state
  }, [scenarioKey, executionId, attemptsKey, active]);

  const base = scenarioKey === "real" ? (typeof real === "object" ? real : undefined) : (SCENARIOS.find((s) => s.key === scenarioKey) ?? SCENARIOS[0]);

  // The stub running execution grows: reveal one more step every 2.5 s, then start over.
  const [tick, setTick] = useState(0);
  useEffect(() => {
    setTick(0);
    if (base?.key !== "running") return;
    const id = setInterval(() => setTick((n) => n + 1), 2500);
    return () => clearInterval(id);
  }, [base]);
  const scenario: Scenario | undefined = useMemo(() => {
    if (!base || base.key !== "running") return base;
    const shown = SONNET_TURNS.slice(0, 1 + (tick % 7));
    return { ...base, attempts: base.attempts.map((a) => ({ ...a, turns: shown })) };
  }, [base, tick]);

  const switcher = (
    <PrototypeSwitcher
      variants={VARIANTS}
      variant={variant}
      onVariant={setVariant}
      scenarios={[{ key: "real", name: "Real data (this execution)" }, ...SCENARIOS.map((s) => ({ key: s.key, name: s.name }))]}
      scenario={scenarioKey}
      onScenario={setScenario}
      state={
        scenario
          ? { variant, scenario: scenario.key, note: scenario.note, attempts: scenario.attempts.map(view).map(({ turns, ...a }) => ({ ...a, steps: turns.length })) }
          : { variant, scenario: scenarioKey, real }
      }
    />
  );

  if (!scenario) {
    return (
      <div className="pc">
        {real === "error" ? <EmptyState icon="alert" title="Could not read this execution's raw output" /> : <EmptyState icon="coin" title="Reading the raw output…" />}
        {switcher}
      </div>
    );
  }

  const attempts = scenario.attempts.map(view);
  const noUsage = attempts.length === 0 || attempts.every((a) => a.source === "none");

  return (
    <div className="pc">
      {scenario.note ? (
        <Banner tone="cyan" icon="sparkle">
          <strong>Real data</strong> <span className="mono">— {scenario.note}</span>
        </Banner>
      ) : null}
      {noUsage ? (
        <EmptyState
          icon="coin"
          title="No token usage for this execution"
          hint="The harness did not report usage — an older CLI version, or the attempt ended before its final result line."
        />
      ) : variant === "AB" ? (
        <SummaryWithReceipt key={scenario.key} attempts={attempts} scenario={scenario} locale={locale} />
      ) : variant === "B" ? (
        <VariantB attempts={attempts} scenario={scenario} locale={locale} />
      ) : variant === "C" ? (
        <VariantC attempts={attempts} scenario={scenario} locale={locale} start={start} />
      ) : (
        <VariantA attempts={attempts} scenario={scenario} locale={locale} />
      )}
      {switcher}
    </div>
  );
}

interface VariantProps {
  attempts: AttemptView[];
  scenario: Scenario;
  locale: string;
}

function totals(attempts: AttemptView[]) {
  const tokens = sum(attempts.map((a) => a.tokens));
  const priced = attempts.filter((a) => a.cost !== undefined);
  const cost = priced.length ? priced.reduce((n, a) => n + (a.cost ?? 0), 0) : undefined;
  const costs = priced.length ? sum(priced.map((a) => a.costs ?? ZERO)) : undefined;
  const incomplete = priced.length < attempts.filter((a) => a.source !== "none").length;
  return { tokens, cost, costs, incomplete };
}

// ---------- Variant A: summary tiles, token mix, per-attempt table ----------

// ---------- Chosen: the summary, with a button to the receipt and back ----------

function SummaryWithReceipt(props: VariantProps) {
  const [view, setView] = useState<"summary" | "receipt">("summary");
  return view === "receipt" ? <VariantB {...props} onBack={() => setView("summary")} /> : <VariantA {...props} onReceipt={() => setView("receipt")} />;
}

function VariantA({ attempts, scenario, locale, onReceipt }: VariantProps & { onReceipt?: () => void }) {
  const [by, setBy] = useState<"tokens" | "cost">("cost");
  const { tokens, cost, costs, incomplete } = totals(attempts);
  const inputSide = tokens.input + tokens.cacheWrite + tokens.cacheRead;
  const end = Math.max(...attempts.flatMap((a) => a.turns.map((x) => x.at)), 1);
  const mix = by === "cost" && costs ? costs : tokens;
  const mixTotal = total(mix) || 1;

  return (
    <div className="pc-a">
      <div className="pc-a__tiles">
        <div className="pc-tile pc-tile--hero">
          <span className="pc-tile__label">
            {scenario.running ? (
              <>
                <span className="pc-live" /> Estimated cost so far
              </>
            ) : (
              "Estimated cost"
            )}
          </span>
          <span className="pc-tile__value">
            {usd(cost, locale)}
            {incomplete && cost !== undefined ? <span className="pc-tile__plus">+ ?</span> : null}
          </span>
          <span className="pc-tile__sub">{attempts.length > 1 ? `${attempts.length} attempts, ${attempts.map((a) => HARNESS_NAME[a.harness]).join(" → ")}` : HARNESS_NAME[attempts[0].harness]}</span>
        </div>
        <div className="pc-tile">
          <span className="pc-tile__label">Tokens</span>
          <span className="pc-tile__value">{compact(total(tokens), locale)}</span>
          <span className="pc-tile__sub">
            {compact(inputSide, locale)} in · {compact(tokens.output, locale)} out
          </span>
        </div>
        <div className="pc-tile">
          <span className="pc-tile__label">Read from cache</span>
          <span className="pc-tile__value">{pct(inputSide ? tokens.cacheRead / inputSide : 0, locale)}</span>
          <span className="pc-tile__sub">of the input tokens</span>
        </div>
        <div className="pc-tile">
          <span className="pc-tile__label">Per minute</span>
          <span className="pc-tile__value">{cost === undefined ? "—" : usd((cost / end) * 60, locale)}</span>
          <span className="pc-tile__sub">over {duration(end)}</span>
        </div>
      </div>

      <section className="pc-block">
        <header className="pc-block__header">
          <h4>Where it went</h4>
          <div className="pc-seg" role="group" aria-label="Measure">
            <button type="button" className={cx(by === "cost" && "is-on")} onClick={() => setBy("cost")} disabled={!costs}>
              by cost
            </button>
            <button type="button" className={cx(by === "tokens" && "is-on")} onClick={() => setBy("tokens")}>
              by tokens
            </button>
          </div>
        </header>
        <div className="pc-mix" aria-hidden="true">
          {CLASSES.map((c) => (mix[c] > 0 ? <span key={c} className={`pc-mix__seg pc-c--${c}`} style={{ flexGrow: mix[c] / mixTotal }} /> : null))}
        </div>
        <ul className="pc-legend">
          {CLASSES.map((c) => (
            <li key={c}>
              <span className="pc-legend__top">
                <span className={`pc-dot pc-c--${c}`} />
                <span className="pc-legend__name">{CLASS_LABEL[c]}</span>
                <span className="mono pc-legend__share">{pct(mix[c] / mixTotal, locale)}</span>
              </span>
              <span className="mono muted">
                {int(tokens[c], locale)} tok · {costs ? usd(costs[c], locale) : "—"}
              </span>
            </li>
          ))}
        </ul>
      </section>

      <section className="pc-block">
        <header className="pc-block__header">
          <h4>By attempt</h4>
          {onReceipt ? (
            <Button size="sm" icon="list" onClick={onReceipt}>
              Receipt
            </Button>
          ) : null}
        </header>
        <table className="table">
          <thead>
            <tr>
              <th>Attempt</th>
              <th>Harness</th>
              <th className="num">Input</th>
              <th className="num">Cached</th>
              <th className="num">Output</th>
              <th className="num">Cost</th>
              <th>Source</th>
            </tr>
          </thead>
          <tbody>
            {attempts.map((a) => (
              <tr key={a.attempt}>
                <td className="mono">#{a.attempt}</td>
                <td>
                  <HarnessLabel harness={a.harness} model={a.model} />
                </td>
                <td className="num mono">{int(a.tokens.input + a.tokens.cacheWrite, locale)}</td>
                <td className="num mono">{int(a.tokens.cacheRead, locale)}</td>
                <td className="num mono">{int(a.tokens.output, locale)}</td>
                <td className="num mono">
                  <strong>{usd(a.cost, locale)}</strong>
                </td>
                <td>
                  <SourceChip a={a} />
                  {a.outcome === "quota_exhausted" ? (
                    <Chip tone="magenta" icon="switch">
                      usage limit
                    </Chip>
                  ) : null}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>
      <BillingNote asOf={scenario.pricesAsOf} attempts={attempts} />
    </div>
  );
}

// ---------- Variant B: an itemized receipt that shows the arithmetic ----------

function VariantB({ attempts, scenario, locale, onBack }: VariantProps & { onBack?: () => void }) {
  const { cost, incomplete } = totals(attempts);
  const lines: string[] = [];
  const rate = (n: number) => `${new Intl.NumberFormat(locale, { style: "currency", currency: "USD", minimumFractionDigits: 2, maximumFractionDigits: 3 }).format(n)}/M`;

  const sections = attempts.map((a) => {
    const rows = CLASSES.filter((c) => a.tokens[c] > 0).map((c) => ({
      c,
      qty: int(a.tokens[c], locale),
      rate: a.price ? rate(a.price[c]) : "—",
      amount: a.costs ? usd(a.costs[c], locale) : "—",
    }));
    lines.push(`Attempt ${a.attempt} · ${HARNESS_NAME[a.harness]} · ${a.model}`);
    for (const r of rows) lines.push(`  ${CLASS_LABEL[r.c].padEnd(12)} ${r.qty.padStart(10)} × ${r.rate.padStart(10)} = ${r.amount.padStart(9)}`);
    lines.push(`  ${"Subtotal".padEnd(37)} ${usd(a.cost, locale).padStart(9)}`);
    return { a, rows };
  });
  lines.push(`TOTAL (estimate) ${usd(cost, locale)}${incomplete ? " + unpriced" : ""}`);

  return (
    <div className={cx("pc-b", onBack && "pc-b--sub")}>
      {onBack ? (
        <div className="pc-b__back">
          <Button size="sm" variant="ghost" onClick={onBack}>
            ← Summary
          </Button>
        </div>
      ) : null}
      <div className="pc-receipt">
        <header className="pc-receipt__head">
          <span className="pc-receipt__brand">HARMFLOW · TOKEN ESTIMATE</span>
          <span className="mono muted">prices as of {scenario.pricesAsOf}</span>
          <CopyButton text={lines.join("\n")} />
        </header>
        {sections.map(({ a, rows }) => (
          <section key={a.attempt} className="pc-receipt__section">
            <div className="pc-receipt__attempt">
              <span className="mono">ATTEMPT {a.attempt}</span>
              <HarnessLabel harness={a.harness} model={a.model} />
              <SourceChip a={a} />
            </div>
            <table className="pc-receipt__lines">
              <tbody>
                {rows.map((r) => (
                  <tr key={r.c}>
                    <td>{CLASS_LABEL[r.c]}</td>
                    <td className="num">{r.qty}</td>
                    <td className="pc-receipt__op">×</td>
                    <td className="num muted">{r.rate}</td>
                    <td className="pc-receipt__op">=</td>
                    <td className="num">{r.amount}</td>
                  </tr>
                ))}
                <tr className="pc-receipt__subtotal">
                  <td colSpan={5}>
                    Subtotal
                    {a.source === "reported" ? (
                      <span className="muted">
                        {" "}
                        · {a.reportedCostUsd === undefined || a.cost === undefined || Math.abs(a.reportedCostUsd - a.cost) < 1e-6 ? "matches" : "differs from"} the{" "}
                        {usd(a.reportedCostUsd ?? a.cost, locale)} {HARNESS_NAME[a.harness]} reported
                      </span>
                    ) : null}
                    {a.outcome === "quota_exhausted" ? <span className="pc-receipt__flag"> · stopped at a usage limit</span> : null}
                  </td>
                  <td className="num">{usd(a.cost, locale)}</td>
                </tr>
              </tbody>
            </table>
          </section>
        ))}
        <div className="pc-receipt__total">
          <span>{scenario.running ? "RUNNING TOTAL" : "TOTAL"} (estimate)</span>
          <span className="mono">
            {usd(cost, locale)}
            {incomplete ? <span className="pc-receipt__flag"> + unpriced</span> : null}
          </span>
        </div>
        <BillingNote asOf={scenario.pricesAsOf} attempts={attempts} />
      </div>
    </div>
  );
}

// ---------- Variant C: cumulative cost over the execution's timeline ----------

interface Step {
  key: string;
  attempt: AttemptView;
  turn: Turn;
  cost?: number;
  cumulative: number;
}

function VariantC({ attempts, scenario, locale, start }: VariantProps & { start: number }) {
  const steps: Step[] = [];
  let running = 0;
  for (const a of attempts) {
    for (const [i, turn] of a.turns.entries()) {
      const c = costByClass(turn, a.source === "unpriced" ? undefined : a.price);
      const cost = c ? total(c) : undefined;
      running += cost ?? 0;
      steps.push({ key: `${a.attempt}-${i}`, attempt: a, turn, cost, cumulative: running });
    }
  }
  const [selectedKey, setSelected] = useState<string | null>(null);
  const selected = steps.find((s) => s.key === selectedKey) ?? steps[steps.length - 1];
  const { cost } = totals(attempts);

  const W = 920;
  const H = 170;
  const pad = { l: 56, r: 16, t: 14, b: 26 };
  const end = Math.max(...steps.map((s) => s.turn.at), 1) * 1.04;
  const top = Math.max(running, 0.0001) * 1.12;
  const x = (s: number) => pad.l + (s / end) * (W - pad.l - pad.r);
  const y = (v: number) => H - pad.b - (v / top) * (H - pad.t - pad.b);

  let d = `M ${x(0)} ${y(0)}`;
  let prev = 0;
  for (const s of steps) {
    d += ` H ${x(s.turn.at)} V ${y(s.cumulative)}`;
    prev = s.cumulative;
  }
  d += ` H ${x(end)}`;
  const area = `${d} V ${y(0)} H ${x(0)} Z`;
  const boundaries = attempts.slice(1).map((a) => ({ a, at: a.startedAt ?? a.turns[0]?.at ?? 0 }));
  const yTicks = [0, 0.5, 1].map((f) => f * top);
  const xTicks = [0, 0.25, 0.5, 0.75, 1].map((f) => Math.round(f * end));

  return (
    <div className="pc-c">
      <div className="pc-c__head">
        <div>
          <span className="pc-tile__label">{scenario.running ? "So far" : "Total"} (estimate)</span>
          <span className="pc-c__total">
            {usd(cost, locale)} {scenario.running ? <span className="pc-live" /> : null}
          </span>
        </div>
        <ul className="pc-legend pc-legend--inline">
          {attempts.map((a) => (
            <li key={a.attempt}>
              <span className={cx("pc-dot", a.attempt > 1 ? "pc-c--switch" : "pc-c--line")} />#{a.attempt} <HarnessLabel harness={a.harness} model={a.model} /> <span className="mono">{usd(a.cost, locale)}</span>
            </li>
          ))}
        </ul>
      </div>
      <svg className="pc-chart" viewBox={`0 0 ${W} ${H}`} role="img" aria-label="Cumulative estimated cost over time">
        <defs>
          <linearGradient id="pc-area" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" stopColor="var(--hf-color-cyan)" stopOpacity="0.28" />
            <stop offset="1" stopColor="var(--hf-color-cyan)" stopOpacity="0" />
          </linearGradient>
        </defs>
        {yTicks.map((v) => (
          <g key={v}>
            <line x1={pad.l} x2={W - pad.r} y1={y(v)} y2={y(v)} className="pc-chart__grid" />
            <text x={pad.l - 8} y={y(v) + 4} textAnchor="end" className="pc-chart__tick">
              {usd(v, locale)}
            </text>
          </g>
        ))}
        {xTicks.map((s) => (
          <text key={s} x={x(s)} y={H - 6} textAnchor="middle" className="pc-chart__tick">
            {duration(s)}
          </text>
        ))}
        {boundaries.map(({ a, at }) => (
          <g key={a.attempt}>
            <line x1={x(at) - 6} x2={x(at) - 6} y1={pad.t} y2={H - pad.b} className="pc-chart__switch" />
            <text x={x(at) - 10} y={pad.t + 10} textAnchor="end" className="pc-chart__switch-label">
              switched to {HARNESS_NAME[a.harness]}
            </text>
          </g>
        ))}
        <path d={area} fill="url(#pc-area)" />
        <path d={d} className="pc-chart__line" />
        {steps.map((s) => (
          <circle
            key={s.key}
            cx={x(s.turn.at)}
            cy={y(s.cumulative)}
            r={s.key === selected?.key ? 6 : 4}
            className={cx("pc-chart__dot", s.attempt.attempt > 1 && "pc-chart__dot--switch", s.key === selected?.key && "is-selected")}
            onClick={() => setSelected(s.key)}
          />
        ))}
        <text x={x(end) - 2} y={y(prev) - 8} textAnchor="end" className="pc-chart__end">
          {usd(prev, locale)}
        </text>
      </svg>

      <div className="pc-c__split">
        <ol className="pc-steps">
          {steps.map((s) => (
            <li key={s.key}>
              <button type="button" className={cx("pc-step", s.key === selected?.key && "is-selected")} onClick={() => setSelected(s.key)}>
                <span className="mono muted">{clock(start, s.turn.at, locale)}</span>
                <span className="pc-step__label">{s.turn.label}</span>
                <span className="mono muted">+{compact(total(s.turn), locale)}</span>
                <span className="mono">{s.cost === undefined ? "—" : `+${usd(s.cost, locale)}`}</span>
              </button>
            </li>
          ))}
        </ol>
        {selected ? (
          <aside className="pc-c__detail">
            <h4>
              {selected.turn.label} <span className="mono muted">#{selected.attempt.attempt}</span>
            </h4>
            <KeyValue
              items={[
                ...CLASSES.map((c): [string, string] => [
                  CLASS_LABEL[c],
                  `${int(selected.turn[c], locale)} tokens${c === "output" && selected.turn.provisional ? " (provisional — settled on the result line)" : ""}`,
                ]),
                ["Step cost", usd(selected.cost, locale)],
                ["Running total", usd(selected.cumulative, locale)],
                ["Price", selected.attempt.price && selected.attempt.source !== "unpriced" ? selected.attempt.price.name : "—"],
              ]}
            />
            <SourceChip a={selected.attempt} />
          </aside>
        ) : null}
      </div>
      <BillingNote asOf={scenario.pricesAsOf} attempts={attempts} />
    </div>
  );
}
