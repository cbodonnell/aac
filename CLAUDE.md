# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`aac` (assets as code) is a library for editors that edit Go source: a person edits data in
a tool, and the tool splices the change into the `.go` file it came from.

## Conventions

- Standard library only. No UI, rendering or domain code: that belongs to the editors
  built on it.
- Tests run without a display and cover mechanisms on planted source.
- Breaking changes are acceptable at this stage.
- Never commit changes to git. The user handles that.

## Commands

```sh
go vet ./...
go test ./...
```
