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

/** The sources the catalog filters by, in its order: the templates that ship with HarmFlow, then the user's. */
export const templateSources = ["builtin", "user"] as const;

/** What the catalog keeps: the words searched for, and a source and a type; an empty one keeps every template. */
export interface TemplateFilter {
  query: string;
  source: string;
  type: string;
}

/** Text as the search compares it: lower case, accents dropped. */
function folded(text: string): string {
  return text
    .normalize("NFD")
    .replace(/[̀-ͯ]/g, "")
    .toLocaleLowerCase();
}

/**
 * The templates the filter keeps, in their order: those of its source and its type, whose name or description in the
 * interface's language holds every word searched for, whatever its case and accents.
 */
export function filterTemplates(templates: Template[], filter: TemplateFilter, language: string): Template[] {
  const words = folded(filter.query).split(/\s+/).filter(Boolean);
  return templates.filter((t) => {
    if ((filter.source && t.source !== filter.source) || (filter.type && t.type !== filter.type)) return false;
    const text = folded(`${localized(t.name, language)}\n${localized(t.description, language)}`);
    return words.every((w) => text.includes(w));
  });
}

/** The task types of the templates, each once, in their order. */
export function templateTypes(templates: Template[]): string[] {
  return [...new Set(templates.map((t) => t.type).filter((type): type is string => !!type))];
}
