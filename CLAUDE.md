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
reverse. Older code still carries such IDs until they are removed: never add one, and drop any in lines you change.

## Go code

Before you write or review Go code, read [`GO-CODE-GUIDE.md`](GO-CODE-GUIDE.md) and apply every rule in it.

## Issues

Code work for this repo is an issue in `harmflow/harmflow-app`; pass `--repo harmflow/harmflow-app` to `gh`.
