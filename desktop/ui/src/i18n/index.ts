import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import { enUS } from "./en-US";
import { esES } from "./es-ES";
import { ptBR } from "./pt-BR";

export const LANGUAGES = ["pt-BR", "en-US", "es-ES"] as const;
export type Language = (typeof LANGUAGES)[number];
export type LanguageSetting = Language | "system";

export const resources = {
  "en-US": { translation: enUS },
  "pt-BR": { translation: ptBR },
  "es-ES": { translation: esES },
} as const;

declare module "i18next" {
  interface CustomTypeOptions {
    defaultNS: "translation";
    resources: { translation: typeof enUS };
  }
}

/** The operating system's language when it is official, en-US otherwise (FS-012 FR-004). */
export function systemLanguage(candidates: readonly string[] = navigatorLanguages()): Language {
  for (const tag of candidates) {
    const lower = tag.toLowerCase();
    if (lower.startsWith("pt")) return "pt-BR";
    if (lower.startsWith("es")) return "es-ES";
    if (lower.startsWith("en")) return "en-US";
  }
  return "en-US";
}

function navigatorLanguages(): readonly string[] {
  if (typeof navigator === "undefined") return [];
  return navigator.languages?.length ? navigator.languages : [navigator.language];
}

export function resolveLanguage(setting: string | undefined): Language {
  if (setting && (LANGUAGES as readonly string[]).includes(setting)) return setting as Language;
  return systemLanguage();
}

const STORAGE_KEY = "harmflow.language";

/** Applies a language at once, with no restart (FS-012 FR-002). */
export function applyLanguage(setting: string | undefined): Language {
  const lang = resolveLanguage(setting);
  if (i18n.language !== lang) void i18n.changeLanguage(lang);
  document.documentElement.lang = lang;
  try {
    localStorage.setItem(STORAGE_KEY, setting ?? "system");
  } catch {
    // Storage is a convenience for the first paint only; the daemon's settings are the source.
  }
  return lang;
}

function initialLanguage(): Language {
  try {
    return resolveLanguage(localStorage.getItem(STORAGE_KEY) ?? undefined);
  } catch {
    return systemLanguage();
  }
}

void i18n.use(initReactI18next).init({
  resources,
  lng: initialLanguage(),
  fallbackLng: "en-US",
  interpolation: { escapeValue: false },
  returnNull: false,
});

export default i18n;
