# garbaj

[![CI](https://github.com/gokaykucuk/garbaj/actions/workflows/ci.yml/badge.svg)](https://github.com/gokaykucuk/garbaj/actions/workflows/ci.yml)

A disk garbage collector for machines that run coding agents.

Agents such as Claude Code and Codex create a git worktree per task, each with its
own dependencies and build output. They pile up quickly: dozens of worktrees at
2–4 GB each. garbaj runs on an interval, checks how much disk space is left, and
removes the worktrees and build caches that are provably safe to delete. It only
acts when space is low and stops as soon as there is enough.

## Install

```bash
go install github.com/gokaykucuk/garbaj@latest
```

Requires Go 1.25+ to build and `git` at runtime. No other dependencies.

## Usage

```bash
garbaj init              # write ~/.config/garbaj/config.json
garbaj scan              # list every managed worktree with its verdict
garbaj run -dry-run      # show what a run would delete right now
garbaj run               # clean up if space is low
garbaj install -every 6h # schedule with launchd; logs to ~/Library/Logs/garbaj.log
garbaj uninstall
```

`install` is macOS only. On Linux, schedule `garbaj run` with cron or a systemd timer.

Run `garbaj scan` and `garbaj run -dry-run` before scheduling it, and check that the
verdicts match what you expect.

## What it removes

Behaviour depends on free space on the configured volume:

| Free space | Action |
| --- | --- |
| At or above `soft_free_percent` (20%) | Nothing. |
| Below `soft_free_percent` | Remove safe worktrees, longest idle first, then orphan leftovers, until free space is back above the soft threshold. |
| Still below `hard_free_percent` (10%) | Also delete `cache_dirs` and run `cache_commands` (default `go clean -cache`). |

A worktree is **safe** only when all of these hold:

- it is a linked git worktree of a repo under `repo_roots`, at a path containing one
  of `worktree_markers` (`/.claude/worktrees/`, `/.codex/worktrees/`);
- it is not locked;
- it has no uncommitted changes (untracked `ignored_untracked` folders such as
  `node_modules` do not count);
- its `HEAD` commit is on at least one remote branch;
- nothing in it changed for `idle_hours` (12).

Branches are never deleted, so a removed worktree can be recreated with
`git worktree add`. A squash-merged branch whose remote branch was deleted counts as
unpushed and is kept.

An **orphan** is a folder in `<repo>/.claude/worktrees` that has no `.git` entry and
is not a registered worktree: build output left after git removed the checkout. It is
removed once idle for `orphan_idle_hours` (72).

## Configuration

`garbaj init` writes the defaults to `~/.config/garbaj/config.json`. Every key is
optional; unknown keys are rejected.

| Key | Default | Meaning |
| --- | --- | --- |
| `volume` | `~` | Path whose filesystem is measured for free space. |
| `soft_free_percent` | `20` | Below this, worktrees and orphans are removed. |
| `hard_free_percent` | `10` | Below this, caches are cleared too. |
| `idle_hours` | `12` | How long a worktree must be untouched to be safe. |
| `orphan_idle_hours` | `72` | How long an orphan folder must be untouched. |
| `repo_roots` | `["~/repos"]` | Folders whose direct children are scanned as git repos. |
| `worktree_markers` | `["/.claude/worktrees/", "/.codex/worktrees/"]` | Only worktrees whose path contains one of these are managed. |
| `orphan_parents` | `[".claude/worktrees"]` | Repo-relative folders checked for orphans. |
| `ignored_untracked` | `["node_modules", "output", "test-results", "playwright-report"]` | Untracked top-level entries that do not make a worktree dirty. |
| `cache_dirs` | `[]` | Folders deleted below the hard threshold, e.g. a Cargo `target`. |
| `cache_commands` | `[["go", "clean", "-cache"]]` | Commands run below the hard threshold. |

## Safety

garbaj deletes files without asking. The rules above are deliberately conservative:
when a worktree cannot be inspected, it is kept. Paths fewer than four levels deep
are never deleted, whatever the config says. Even so, it is provided without
warranty; see [LICENSE](LICENSE).

## Development

```bash
go vet ./... && go test ./...
```

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
