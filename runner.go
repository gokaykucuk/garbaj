package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const minDeleteDepth = 4

type Runner struct {
	cfg    Config
	now    time.Time
	dryRun bool
	free   func() (float64, error)
	out    io.Writer
}

type Survey struct {
	Verdicts []Verdict
	Orphans  []string
}

func newRunner(cfg Config, dryRun bool, out io.Writer) *Runner {
	return &Runner{
		cfg:    cfg,
		now:    time.Now(),
		dryRun: dryRun,
		free:   func() (float64, error) { return freePercent(cfg.Volume) },
		out:    out,
	}
}

func (r *Runner) survey() Survey {
	var s Survey
	for _, repo := range listRepos(r.cfg.RepoRoots) {
		r.surveyRepo(repo, &s)
	}
	sort.SliceStable(s.Verdicts, func(i, j int) bool {
		return s.Verdicts[i].LastActive.Before(s.Verdicts[j].LastActive)
	})
	return s
}

func (r *Runner) surveyRepo(repo string, s *Survey) {
	out, err := git(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return
	}
	registered := map[string]bool{}
	var managed []Worktree
	for _, wt := range parseWorktrees(repo, out) {
		registered[wt.Path] = true
		if r.cfg.managed(wt.Path) {
			managed = append(managed, wt)
		}
	}
	if len(managed) > 0 {
		_, _ = git(repo, "fetch", "--quiet", "--all")
	}
	for _, wt := range managed {
		s.Verdicts = append(s.Verdicts, classify(r.cfg, wt, r.now))
	}
	s.Orphans = append(s.Orphans, findOrphans(r.cfg, repo, registered, r.now)...)
}

func (r *Runner) Scan() error {
	if err := r.printFree(); err != nil {
		return err
	}
	s := r.survey()
	for _, v := range s.Verdicts {
		label := "KEEP"
		if v.Safe {
			label = "SAFE"
		}
		fmt.Fprintf(r.out, "%s  %-26s %s\n", label, v.Reason, v.Path)
	}
	for _, orphan := range s.Orphans {
		fmt.Fprintf(r.out, "SAFE  %-26s %s\n", "orphan leftovers", orphan)
	}
	return nil
}

// Run removes the longest-idle safe worktrees first and stops as soon as
// free space is back above the soft threshold. Caches are only cleared
// when that still leaves the disk below the hard threshold.
func (r *Runner) Run() error {
	fmt.Fprintln(r.out, r.now.Format(time.RFC3339))
	if err := r.printFree(); err != nil {
		return err
	}
	if !r.below(r.cfg.SoftFreePercent) {
		fmt.Fprintln(r.out, "enough free space, nothing to do")
		return nil
	}
	r.reclaim(r.survey())
	if r.below(r.cfg.HardFreePercent) {
		r.clearCaches()
	}
	return r.printFree()
}

func (r *Runner) printFree() error {
	pct, err := r.free()
	if err != nil {
		return err
	}
	fmt.Fprintf(r.out, "free space %.1f%% (soft %.0f%%, hard %.0f%%)\n",
		pct, r.cfg.SoftFreePercent, r.cfg.HardFreePercent)
	return nil
}

func (r *Runner) below(threshold float64) bool {
	pct, err := r.free()
	return err == nil && pct < threshold
}

func (r *Runner) reclaim(s Survey) {
	for _, v := range s.Verdicts {
		if !r.below(r.cfg.SoftFreePercent) {
			return
		}
		if v.Safe {
			r.removeWorktree(v.Worktree)
		}
	}
	for _, orphan := range s.Orphans {
		if !r.below(r.cfg.SoftFreePercent) {
			return
		}
		r.removeDir("orphan", orphan)
	}
}

func (r *Runner) removeWorktree(wt Worktree) {
	fmt.Fprintln(r.out, r.verb("remove worktree"), wt.Path)
	if r.dryRun {
		return
	}
	if _, err := git(wt.Repo, "worktree", "remove", "--force", wt.Path); err != nil {
		fmt.Fprintln(r.out, "  failed:", err)
	}
}

func (r *Runner) removeDir(kind, path string) {
	if !deletable(path) {
		fmt.Fprintln(r.out, "refusing to delete shallow path", path)
		return
	}
	if _, err := os.Lstat(path); err != nil {
		return
	}
	fmt.Fprintln(r.out, r.verb("remove "+kind), path)
	if r.dryRun {
		return
	}
	if err := os.RemoveAll(path); err != nil {
		fmt.Fprintln(r.out, "  failed:", err)
	}
}

func deletable(path string) bool {
	clean := filepath.Clean(path)
	return filepath.IsAbs(clean) && strings.Count(clean, string(filepath.Separator)) >= minDeleteDepth
}

func (r *Runner) clearCaches() {
	for _, dir := range r.cfg.CacheDirs {
		r.removeDir("cache", dir)
	}
	for _, command := range r.cfg.CacheCommands {
		if len(command) == 0 {
			continue
		}
		fmt.Fprintln(r.out, r.verb("run"), strings.Join(command, " "))
		if r.dryRun {
			continue
		}
		if err := exec.Command(command[0], command[1:]...).Run(); err != nil {
			fmt.Fprintln(r.out, "  failed:", err)
		}
	}
}

func (r *Runner) verb(action string) string {
	if r.dryRun {
		return "would " + action
	}
	return action
}
