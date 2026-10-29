// The task templates as the dashboard offers them, read from the daemon: what each is called and what it is for come
// with it, in every language of the interface.
import type { LocalizedText, Template } from "../api/types";

/** The templates a task can be created from: the valid ones, in the daemon's order. */
export function offeredTemplates(templates: Template[] | undefined): Template[] {
  return (templates ?? []).filter((t) => t.validation.valid);
}

/** A template's text in the interface's language, or in en-US for a language the text lacks. */
export function localized(text: LocalizedText | undefined, language: string): string {
  if (!text) return "";
  return (text as Record<string, string>)[language] ?? text["en-US"];
}
