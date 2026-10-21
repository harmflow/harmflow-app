// The daemon writes every text for the user in en-US, with a messageKey and params; the interface shows it in its own
// language, and keeps the daemon's text for a key it has no translation of (FS-012 FR-005).
import type { MessageKey, MessageParams } from "../api/types";
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
  // i18next picks a plural form only from a numeric count.
  const options = { ...params, count: params?.count === undefined ? undefined : Number(params.count) };
  return i18n.exists(key, options) ? String(i18n.t(key as never, options as never)) : message;
}

// A counted key exists only in its plural forms.
type Base<K> = K extends `${infer B}_${"one" | "other"}` ? B : K;
// Fails the type check, naming them, when a key of the contract has no text in en-US — and so in none of the
// languages, whose resources are typed against it.
const allTranslated: [Exclude<MessageKey, Base<keyof typeof enUS.messages>>] extends [never]
  ? true
  : Exclude<MessageKey, Base<keyof typeof enUS.messages>> = true;
void allTranslated;
