# CLAUDE.md — harmflow-app

> Repo-specific guidance for the harmflow desktop app repository. Specifications, ADRs, and architecture live in a
> private repository, which this one never cites (see "Public repository").

---

## Public repository

This repository is **public**, history and issues included. Never write personal identifiers into a file here —
task-manager project or task IDs, account IDs, personal e-mail addresses — nor secrets or unannounced work.
Personal workflow guidance lives in an untracked `CLAUDE.md` outside the clone.

Never cite a record of the private documentation repository — a specification, requirement, acceptance criterion,
ADR, or decision — by ID, link, or path, and never reference its issues. This holds for code, comments, test names,
schemas, commits, issues, and pull requests. Describe the behavior in plain words; a neutral term such as "the
specification" or "a decision" is fine. The private documentation links the public issue or pull request, never the
reverse.

## Go code

Before you write or review Go code, read [`GO-CODE-GUIDE.md`](GO-CODE-GUIDE.md) and apply every rule in it.

## File names

Every file and folder the repository tracks is named by the row of its part. `make check-file-names` checks each
tracked path against the rules a pattern can test, and CI runs it on every pull request. The rules marked *(review)*
are held by review alone.

| Part | Rule |
|---|---|
| Any other file or folder | Lower case, words separated by hyphens, dots only before extensions (`coverage-floors.txt`, `pnpm-lock.yaml`, `src-tauri`). A name a tool or a format sets keeps it: `Makefile`, `LICENSE`, `Cargo.toml`, `Cargo.lock`, and the icons `tauri icon` writes into `desktop/src-tauri/icons/`. What a `testdata` folder holds is a fixture and keeps the name of what it stands in for |
| Markdown | A document at the root is upper case, words separated by hyphens (`GO-CODE-GUIDE.md`). `README.md` and `SKILL.md` keep those names in any folder |
| Go (`daemon/`, `tests/`) | A folder is one lower-case word, as its package is (`claudecode`, `httpapi`). A file is lower case, words separated by `_` (`path_rule.go`). *(review)* A file named after its package keeps the package's spelling (`claudecode.go`). *(review)* A `_<GOOS>` or `_<GOARCH>` suffix ends a name only to constrain the build (`tree_darwin.go`, beside `tree_other.go`). *(review)* A test is named after the file or the behavior it covers, with the same name in every package that tests that behavior (`detection_test.go`) |
| Interface (`desktop/ui/src/`) | A folder is one lower-case word (`screens`). A file of components is PascalCase `.tsx` (`TaskForm.tsx`, `TasksScreen.tsx`); any other module is camelCase, named for what it provides (`daemonText.ts`, `ui.tsx`, `toast.tsx`). *(review)* Which of the two a file is. A test is `<name>.test.ts` or `<name>.test.tsx` beside `<name>.ts` or `<name>.tsx`, or named after its folder when it covers the folder (`screens.test.tsx`). A locale file is named by its language tag (`pt-BR.ts`). `vite-env.d.ts` keeps Vite's name |
| Rust (`desktop/src-tauri/src/`) | Lower case, words separated by `_`, as Rust module names are (`sse.rs`) |
| Shell scripts | Lower case, words separated by hyphens (`update-app.sh`). A script's test is `<script>-test.sh` beside it |
| Built-in skills and agents | The daemon finds built-ins by the `hf-` prefix. A skill is a folder `skills/hf-<name>/` whose `SKILL.md` declares `name: hf-<name>`; an agent is `agents/hf-<name>.md` and declares `name: hf-<name>`. `<name>` is lower case with hyphens |

## Issues

Code work for this repo is an issue in `harmflow/harmflow-app`; pass `--repo harmflow/harmflow-app` to `gh`.
