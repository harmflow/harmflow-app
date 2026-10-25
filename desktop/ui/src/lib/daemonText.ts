// The daemon writes every text for the user in en-US, with a messageKey and params; the interface shows it in its own
// language, and keeps the daemon's text for a key it has no translation of.
import type { HarmflowEvent, MessageKey, MessageParams } from "../api/types";
import i18n from "../i18n";
import type { enUS } from "../i18n/en-US";

export interface DaemonText {
  message: string;
  messageKey?: MessageKey;
  params?: MessageParams;
}

export function daemonText({ message, messageKey, params }: DaemonText): string {
  if (!messageKey) return message;
  const key = `messages.${messageKey}`;
  const values = translateNested(params);
  // i18next picks a plural form only from a numeric count.
  const options = { ...values, count: values?.count === undefined ? undefined : Number(values.count) };
  return i18n.exists(key, options) ? String(i18n.t(key as never, options as never)) : message;
}

/** An event's message: the daemon's in the interface's language, the agent's or a tool's as it is. */
export function eventText(e: Pick<HarmflowEvent, "message" | "messageKey" | "params">): string {
  return daemonText({ message: e.message ?? "", messageKey: e.messageKey, params: e.params });
}

/** A field the daemon writes as a text, such as errorMessage beside errorKey and errorParams. */
export function fieldText(message: string | undefined, messageKey?: MessageKey, params?: MessageParams): string | undefined {
  return message === undefined ? undefined : daemonText({ message, messageKey, params });
}

// A param beside a `<name>Key` param quotes another of the daemon's texts, whose own params are named `<name>.<param>`:
// it is translated in place. A notice's params quote the event's message this way.
export function translateNested(params?: MessageParams): MessageParams | undefined {
  if (!params) return params;
  const out: MessageParams = { ...params };
  for (const name of Object.keys(params)) {
    const innerKey = params[`${name}Key`];
    if (name.includes(".") || !innerKey) continue;
    const prefix = `${name}.`;
    const inner = Object.fromEntries(
      Object.entries(params)
        .filter(([k]) => k.startsWith(prefix))
        .map(([k, v]) => [k.slice(prefix.length), v]),
    );
    out[name] = daemonText({ message: params[name], messageKey: innerKey as MessageKey, params: inner });
  }
  return out;
}

// A counted key exists only in its plural forms.
type Base<K> = K extends `${infer B}_${"one" | "other"}` ? B : K;
// Fails the type check, naming them, when a key of the contract has no text in en-US — and so in none of the
// languages, whose resources are typed against it.
const allTranslated: [Exclude<MessageKey, Base<keyof typeof enUS.messages>>] extends [never]
  ? true
  : Exclude<MessageKey, Base<keyof typeof enUS.messages>> = true;
void allTranslated;
