// The advanced YAML editor of a task (FS-001 FR-002). It edits text only; validation is the daemon's, the same as the
// form's and the import's.
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { yaml } from "@codemirror/lang-yaml";
import { HighlightStyle, bracketMatching, indentOnInput, syntaxHighlighting } from "@codemirror/language";
import { Annotation, EditorState, StateEffect, StateField, type Extension } from "@codemirror/state";
import { Decoration, EditorView, highlightActiveLine, keymap, lineNumbers, type DecorationSet } from "@codemirror/view";
import { tags } from "@lezer/highlight";
import { useEffect, useRef } from "react";

const highlight = HighlightStyle.define([
  { tag: [tags.propertyName, tags.definition(tags.propertyName)], color: "var(--hf-color-cyan)" },
  { tag: [tags.string, tags.special(tags.string)], color: "var(--hf-color-success)" },
  { tag: [tags.number, tags.bool, tags.null], color: "var(--hf-color-magenta)" },
  { tag: [tags.comment, tags.lineComment], color: "var(--hf-color-text-faint)", fontStyle: "italic" },
  { tag: [tags.keyword, tags.atom], color: "var(--hf-color-violet)" },
  { tag: [tags.punctuation, tags.separator, tags.bracket], color: "var(--hf-color-text-muted)" },
  { tag: tags.meta, color: "var(--hf-color-violet)" },
]);

const theme = EditorView.theme(
  {
    "&": {
      height: "100%",
      backgroundColor: "var(--hf-color-surface-sunken)",
      color: "var(--hf-color-text)",
      fontSize: "var(--hf-font-size-sm)",
    },
    ".cm-content": { fontFamily: "var(--hf-font-mono)", caretColor: "var(--hf-color-cyan)", padding: "10px 0" },
    ".cm-scroller": { fontFamily: "var(--hf-font-mono)", lineHeight: "1.6" },
    ".cm-gutters": {
      backgroundColor: "var(--hf-color-surface-sunken)",
      color: "var(--hf-color-text-faint)",
      border: "none",
      borderRight: "1px solid var(--hf-color-border)",
    },
    ".cm-activeLine": { backgroundColor: "rgba(var(--hf-rgb-cyan), 0.05)" },
    ".cm-activeLineGutter": { backgroundColor: "rgba(var(--hf-rgb-cyan), 0.08)", color: "var(--hf-color-text-muted)" },
    "&.cm-focused": { outline: "none" },
    ".cm-cursor": { borderLeftColor: "var(--hf-color-cyan)" },
    ".cm-selectionBackground, &.cm-focused .cm-selectionBackground, ::selection": {
      backgroundColor: "rgba(var(--hf-rgb-violet), 0.28) !important",
    },
    ".cm-error-line": { backgroundColor: "var(--hf-color-danger-soft)" },
  },
  { dark: true },
);

const setErrorLines = StateEffect.define<number[]>();

/** Marks the text the `value` prop puts in place, which is no edit of the user's and reaches no `onChange`. */
const fromValue = Annotation.define<boolean>();

const errorLines = StateField.define<DecorationSet>({
  create: () => Decoration.none,
  update(deco, tr) {
    let next = deco.map(tr.changes);
    for (const effect of tr.effects) {
      if (effect.is(setErrorLines)) {
        const lineDeco = Decoration.line({ class: "cm-error-line" });
        const ranges = effect.value
          .filter((n) => n >= 1 && n <= tr.state.doc.lines)
          .sort((a, b) => a - b)
          .map((n) => lineDeco.range(tr.state.doc.line(n).from));
        next = Decoration.set(ranges, true);
      }
    }
    return next;
  },
  provide: (f) => EditorView.decorations.from(f),
});

export function YamlEditor({
  value,
  onChange,
  errorLineNumbers = [],
  readOnly,
}: {
  value: string;
  onChange: (value: string) => void;
  errorLineNumbers?: number[];
  readOnly?: boolean;
}) {
  const host = useRef<HTMLDivElement | null>(null);
  const view = useRef<EditorView | null>(null);
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;

  useEffect(() => {
    if (!host.current) return;
    const extensions: Extension[] = [
      lineNumbers(),
      highlightActiveLine(),
      history(),
      indentOnInput(),
      bracketMatching(),
      keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
      yaml(),
      syntaxHighlighting(highlight),
      theme,
      errorLines,
      EditorState.tabSize.of(2),
      EditorState.readOnly.of(!!readOnly),
      EditorView.updateListener.of((update) => {
        if (update.docChanged && !update.transactions.some((tr) => tr.annotation(fromValue))) onChangeRef.current(update.state.doc.toString());
      }),
    ];
    view.current = new EditorView({ state: EditorState.create({ doc: value, extensions }), parent: host.current });
    return () => {
      view.current?.destroy();
      view.current = null;
    };
    // The editor owns its text after mount; value changes from outside are applied below.
  }, [readOnly]);

  useEffect(() => {
    const v = view.current;
    if (v && v.state.doc.toString() !== value) {
      v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value }, annotations: fromValue.of(true) });
    }
  }, [value]);

  const errorKey = errorLineNumbers.join(",");
  useEffect(() => {
    const lines = errorKey ? errorKey.split(",").map(Number) : [];
    view.current?.dispatch({ effects: setErrorLines.of(lines) });
  }, [errorKey]);

  return <div className="yaml-editor" ref={host} />;
}
