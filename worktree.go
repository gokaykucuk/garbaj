package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	reasonSafe     = "clean, pushed, idle"
	reasonLocked   = "locked"
	reasonDirty    = "uncommitted changes"
	reasonUnpushed = "commits not on any remote"
	reasonActive   = "recently active"
	reasonUnknown  = "could not inspect"

	gitTimeout = 2 * time.Minute
)

type Worktree struct {
	Repo   string
	Path   string
	Locked bool
}

type Verdict struct {
	Worktree
	Safe       bool
	Reason     string
	LastActive time.Time
}

// --no-optional-locks keeps `git status` from rewriting the index, which
// would reset the idle clock of every worktree on each scan.
func git(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	return string(out), err
}

func listRepos(roots []string) []string {
	var repos []string
	for _, root := range roots {
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		entries, _ := os.ReadDir(resolved)
		for _, e := range entries {
			repo := filepath.Join(resolved, e.Name())
			if info, err := os.Stat(filepath.Join(repo, ".git")); err == nil && info.IsDir() {
				repos = append(repos, repo)
			}
		}
	}
	return repos
}

// The first block of `git worktree list --porcelain` is the main checkout,
// which is never a removal candidate.
func parseWorktrees(repo, porcelain string) []Worktree {
	var all []Worktree
	for _, block := range strings.Split(strings.TrimSpace(porcelain), "\n\n") {
		wt := Worktree{Repo: repo}
		for _, line := range strings.Split(block, "\n") {
			key, value, _ := strings.Cut(line, " ")
			switch key {
			case "worktree":
				wt.Path = value
			case "locked":
				wt.Locked = true
			}
		}
		if wt.Path != "" {
			all = append(all, wt)
		}
	}
	if len(all) == 0 {
		return nil
	}
	return all[1:]
}

func classify(cfg Config, wt Worktree, now time.Time) Verdict {
	v := Verdict{Worktree: wt, LastActive: lastActivity(wt.Path, gitDirFiles(wt.Path)...)}
	v.Reason = verdictReason(cfg, wt, now.Sub(v.LastActive))
	v.Safe = v.Reason == reasonSafe
	return v
}

func verdictReason(cfg Config, wt Worktree, idle time.Duration) string {
	if wt.Locked {
		return reasonLocked
	}
	status, err := git(wt.Path, "status", "--porcelain")
	if err != nil {
		return reasonUnknown
	}
	if isDirty(status, cfg.IgnoredUntracked) {
		return reasonDirty
	}
	remote, err := git(wt.Path, "for-each-ref", "--count=1", "--contains", "HEAD", "refs/remotes")
	if err != nil {
		return reasonUnknown
	}
	if strings.TrimSpace(remote) == "" {
		return reasonUnpushed
	}
	if idle < cfg.idle() {
		return reasonActive
	}
	return reasonSafe
}

func isDirty(porcelain string, ignoredUntracked []string) bool {
	for _, line := range strings.Split(porcelain, "\n") {
		if line == "" || ignorableUntracked(line, ignoredUntracked) {
			continue
		}
		return true
	}
	return false
}

func ignorableUntracked(line string, ignored []string) bool {
	path, ok := strings.CutPrefix(line, "?? ")
	if !ok {
		return false
	}
	first, _, _ := strings.Cut(strings.Trim(path, `"`), "/")
	return slices.Contains(ignored, first)
}

func gitDirFiles(worktree string) []string {
	out, err := git(worktree, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil
	}
	dir := strings.TrimSpace(out)
	return []string{filepath.Join(dir, "HEAD"), filepath.Join(dir, "index")}
}

// lastActivity looks two levels deep: enough to see edits and checkouts
// without walking dependency trees.
func lastActivity(root string, extra ...string) time.Time {
	latest := modTime(root)
	for _, path := range append(shallowEntries(root), extra...) {
		if t := modTime(path); t.After(latest) {
			latest = t
		}
	}
	return latest
}

func shallowEntries(root string) []string {
	var paths []string
	for _, first := range children(root) {
		paths = append(paths, first)
		paths = append(paths, children(first)...)
	}
	return paths
}

func children(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var paths []string
	for _, e := range entries {
		if e.Name() == "node_modules" || e.Name() == ".git" {
			continue
		}
		paths = append(paths, filepath.Join(dir, e.Name()))
	}
	return paths
}

func modTime(path string) time.Time {
	info, err := os.Lstat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// Orphans are folders left behind in a worktree parent after git already
// removed the checkout: no .git entry and not registered with the repo.
func findOrphans(cfg Config, repo string, registered map[string]bool, now time.Time) []string {
	var orphans []string
	for _, parent := range cfg.OrphanParents {
		dir := filepath.Join(repo, parent)
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			path := filepath.Join(dir, e.Name())
			if e.IsDir() && isOrphan(path, registered) && now.Sub(lastActivity(path)) >= cfg.orphanIdle() {
				orphans = append(orphans, path)
			}
		}
	}
	return orphans
}

func isOrphan(path string, registered map[string]bool) bool {
	if registered[path] {
		return false
	}
	_, err := os.Lstat(filepath.Join(path, ".git"))
	return os.IsNotExist(err)
}
