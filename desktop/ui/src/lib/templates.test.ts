// The template catalog's search and filters: the search reads the name and description in the interface's language,
// whatever their case and accents, and the filters by source and type narrow the list and combine with it.
import { describe, expect, it } from "vitest";
import type { LocalizedText, Template } from "../api/types";
import { filterTemplates, templateTypes } from "./templates";

const text = (en: string, pt: string, es: string): LocalizedText => ({ "en-US": en, "pt-BR": pt, "es-ES": es });

const template = (id: string, source: string, type: string, name: LocalizedText, description: LocalizedText): Template =>
  ({ id, source, path: `/templates/${id}.yaml`, type, name, description, validation: { valid: true, errors: [], warnings: [] } }) as Template;

const templates: Template[] = [
  template("hf-general", "builtin", "general", text("General task", "Tarefa geral", "Tarea general"),
    text("A prompt that runs on a schedule.", "Um prompt que roda num horário.", "Un prompt que corre según un horario.")),
  template("hf-github-issues", "builtin", "github", text("Implement GitHub issues", "Implementar issues do GitHub", "Implementar issues de GitHub"),
    text("Opens a draft pull request per issue.", "Abre um pull request em rascunho por issue.", "Abre un pull request en borrador por issue.")),
  template("meeting-notes", "user", "general", text("Meeting notes", "Notas da reunião", "Notas de la reunión"),
    text("Sums up the meeting.", "Resume a reunião.", "Resume la reunión.")),
];

const ids = (list: Template[]) => list.map((t) => t.id);
const all = { query: "", source: "", type: "" };

describe("filterTemplates", () => {
  it("keeps every template, in its order, when nothing is searched or filtered", () => {
    expect(ids(filterTemplates(templates, all, "en-US"))).toEqual(["hf-general", "hf-github-issues", "meeting-notes"]);
  });

  it("searches the name and the description in the interface's language, whatever their case and accents", () => {
    expect(ids(filterTemplates(templates, { ...all, query: "GITHUB" }, "en-US"))).toEqual(["hf-github-issues"]);
    expect(ids(filterTemplates(templates, { ...all, query: "schedule" }, "en-US"))).toEqual(["hf-general"]);
    expect(ids(filterTemplates(templates, { ...all, query: "reuniao" }, "pt-BR"))).toEqual(["meeting-notes"]);
    expect(ids(filterTemplates(templates, { ...all, query: "reunião" }, "en-US"))).toEqual([]);
    expect(ids(filterTemplates(templates, { ...all, query: "borrador" }, "es-ES"))).toEqual(["hf-github-issues"]);
  });

  it("keeps a template only when it holds every word searched for", () => {
    expect(ids(filterTemplates(templates, { ...all, query: "  draft   issue " }, "en-US"))).toEqual(["hf-github-issues"]);
    expect(ids(filterTemplates(templates, { ...all, query: "draft meeting" }, "en-US"))).toEqual([]);
  });

  it("filters by source and by type, each alone and combined with the search", () => {
    expect(ids(filterTemplates(templates, { ...all, source: "user" }, "en-US"))).toEqual(["meeting-notes"]);
    expect(ids(filterTemplates(templates, { ...all, source: "builtin" }, "en-US"))).toEqual(["hf-general", "hf-github-issues"]);
    expect(ids(filterTemplates(templates, { ...all, type: "general" }, "en-US"))).toEqual(["hf-general", "meeting-notes"]);
    expect(ids(filterTemplates(templates, { ...all, source: "builtin", type: "general" }, "en-US"))).toEqual(["hf-general"]);
    expect(ids(filterTemplates(templates, { query: "notes", source: "", type: "general" }, "en-US"))).toEqual(["meeting-notes"]);
    expect(ids(filterTemplates(templates, { query: "notes", source: "builtin", type: "" }, "en-US"))).toEqual([]);
  });
});

describe("templateTypes", () => {
  it("names each type once, in the templates' order", () => {
    expect(templateTypes(templates)).toEqual(["general", "github"]);
  });
});
