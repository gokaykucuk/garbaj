package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func newRepo(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitConfig := filepath.Join(root, "gitconfig")
	mustWrite(t, gitConfig, "")
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(key, "garbaj test")
	}
	for _, key := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(key, "test@garbaj.invalid")
	}
	remote := filepath.Join(root, "remote.git")
	repo := filepath.Join(root, "repos", "app")
	mustGit(t, root, "init", "--quiet", "--bare", "-b", "main", remote)
	mustWrite(t, filepath.Join(repo, "README.md"), "app\n")
	mustGit(t, repo, "init", "--quiet", "-b", "main")
	mustGit(t, repo, "add", "README.md")
	mustGit(t, repo, "commit", "--quiet", "-m", "init")
	mustGit(t, repo, "remote", "add", "origin", remote)
	mustGit(t, repo, "push", "--quiet", "-u", "origin", "main")
	return repo
}

func addWorktree(t *testing.T, repo, name string) Worktree {
	t.Helper()
	path := filepath.Join(repo, ".claude", "worktrees", name)
	mustGit(t, repo, "worktree", "add", "--quiet", "-b", name, path)
	return Worktree{Repo: repo, Path: path}
}

func testConfig(repo string) Config {
	cfg := defaultConfig()
	cfg.RepoRoots = []string{filepath.Dir(repo)}
	cfg.CacheCommands = nil
	return cfg
}

func idleNow() time.Time {
	return time.Now().Add(48 * time.Hour)
}

func TestParseWorktreesSkipsMainAndReadsLocked(t *testing.T) {
	porcelain := "worktree /r\nHEAD abc\nbranch refs/heads/main\n\n" +
		"worktree /r/.claude/worktrees/a\nHEAD abc\ndetached\nlocked busy\n\n" +
		"worktree /r/.claude/worktrees/b\nHEAD def\nbranch refs/heads/b\n"
	got := parseWorktrees("/r", porcelain)
	if len(got) != 2 || !got[0].Locked || got[1].Locked || got[1].Path != "/r/.claude/worktrees/b" {
		t.Fatalf("unexpected worktrees: %+v", got)
	}
}

func TestIsDirtyIgnoresConfiguredUntracked(t *testing.T) {
	ignored := []string{"node_modules"}
	cases := map[string]bool{
		"":                                 false,
		"?? node_modules/\n":               false,
		"?? node_modules/\n?? notes.txt\n": true,
		" M README.md\n":                   true,
		"?? src/node_modules/\n":           true,
	}
	for porcelain, want := range cases {
		if got := isDirty(porcelain, ignored); got != want {
			t.Errorf("isDirty(%q) = %v, want %v", porcelain, got, want)
		}
	}
}

func TestClassifyPushedCleanIdleIsSafe(t *testing.T) {
	repo := newRepo(t)
	wt := addWorktree(t, repo, "done")
	mustWrite(t, filepath.Join(wt.Path, "node_modules", "pkg", "index.js"), "")
	if v := classify(testConfig(repo), wt, idleNow()); !v.Safe {
		t.Fatalf("want safe, got %q", v.Reason)
	}
}

func TestClassifyKeepsRecentlyActive(t *testing.T) {
	repo := newRepo(t)
	wt := addWorktree(t, repo, "fresh")
	if v := classify(testConfig(repo), wt, time.Now()); v.Reason != reasonActive {
		t.Fatalf("want %q, got %q", reasonActive, v.Reason)
	}
}

func TestClassifyKeepsDirty(t *testing.T) {
	repo := newRepo(t)
	wt := addWorktree(t, repo, "dirty")
	mustWrite(t, filepath.Join(wt.Path, "notes.txt"), "wip\n")
	if v := classify(testConfig(repo), wt, idleNow()); v.Reason != reasonDirty {
		t.Fatalf("want %q, got %q", reasonDirty, v.Reason)
	}
}

func TestClassifyKeepsUnpushed(t *testing.T) {
	repo := newRepo(t)
	wt := addWorktree(t, repo, "local")
	mustWrite(t, filepath.Join(wt.Path, "feature.txt"), "work\n")
	mustGit(t, wt.Path, "add", "feature.txt")
	mustGit(t, wt.Path, "commit", "--quiet", "-m", "local work")
	if v := classify(testConfig(repo), wt, idleNow()); v.Reason != reasonUnpushed {
		t.Fatalf("want %q, got %q", reasonUnpushed, v.Reason)
	}
}

func TestClassifyKeepsLocked(t *testing.T) {
	repo := newRepo(t)
	wt := addWorktree(t, repo, "held")
	wt.Locked = true
	if v := classify(testConfig(repo), wt, idleNow()); v.Reason != reasonLocked {
		t.Fatalf("want %q, got %q", reasonLocked, v.Reason)
	}
}

func TestStatusDoesNotResetIdleClock(t *testing.T) {
	repo := newRepo(t)
	wt := addWorktree(t, repo, "quiet")
	before := lastActivity(wt.Path, gitDirFiles(wt.Path)...)
	classify(testConfig(repo), wt, idleNow())
	if after := lastActivity(wt.Path, gitDirFiles(wt.Path)...); !after.Equal(before) {
		t.Fatalf("classify changed last activity from %v to %v", before, after)
	}
}

func TestRunDoesNothingAboveSoftThreshold(t *testing.T) {
	repo := newRepo(t)
	wt := addWorktree(t, repo, "done")
	var out bytes.Buffer
	r := &Runner{cfg: testConfig(repo), now: idleNow(), out: &out,
		free: func() (float64, error) { return 50, nil }}
	if err := r.Run(); err != nil {
		t.Fatal(err)
	}
	if !exists(wt.Path) {
		t.Fatal("worktree removed although disk had enough free space")
	}
}

func TestRunStopsOnceSoftThresholdIsReached(t *testing.T) {
	repo := newRepo(t)
	a := addWorktree(t, repo, "a")
	b := addWorktree(t, repo, "b")
	cache := filepath.Join(filepath.Dir(repo), "app", "target")
	mustWrite(t, filepath.Join(cache, "obj"), "x")
	cfg := testConfig(repo)
	cfg.CacheDirs = []string{cache}
	var out bytes.Buffer
	r := &Runner{cfg: cfg, now: idleNow(), out: &out, free: func() (float64, error) {
		if exists(a.Path) && exists(b.Path) {
			return 15, nil
		}
		return 30, nil
	}}
	if err := r.Run(); err != nil {
		t.Fatal(err)
	}
	if exists(a.Path) == exists(b.Path) {
		t.Fatalf("want exactly one worktree removed\n%s", out.String())
	}
	if !exists(cache) {
		t.Fatal("cache cleared although disk never dropped below the hard threshold")
	}
}

func TestRunClearsCachesBelowHardThreshold(t *testing.T) {
	repo := newRepo(t)
	safe := addWorktree(t, repo, "done")
	dirty := addWorktree(t, repo, "dirty")
	mustWrite(t, filepath.Join(dirty.Path, "notes.txt"), "wip\n")
	orphan := filepath.Join(repo, ".claude", "worktrees", "leftover")
	mustWrite(t, filepath.Join(orphan, "react", "dist", "app.js"), "")
	cache := filepath.Join(repo, "target")
	mustWrite(t, filepath.Join(cache, "obj"), "x")
	cfg := testConfig(repo)
	cfg.CacheDirs = []string{cache}
	var out bytes.Buffer
	r := &Runner{cfg: cfg, now: idleNow().Add(cfg.orphanIdle()), out: &out,
		free: func() (float64, error) { return 5, nil }}
	if err := r.Run(); err != nil {
		t.Fatal(err)
	}
	if exists(safe.Path) || exists(orphan) || exists(cache) {
		t.Fatalf("safe worktree, orphan and cache should all be gone\n%s", out.String())
	}
	if !exists(filepath.Join(dirty.Path, "notes.txt")) {
		t.Fatal("dirty worktree was removed")
	}
}

func TestDryRunDeletesNothing(t *testing.T) {
	repo := newRepo(t)
	wt := addWorktree(t, repo, "done")
	cache := filepath.Join(repo, "target")
	mustWrite(t, filepath.Join(cache, "obj"), "x")
	cfg := testConfig(repo)
	cfg.CacheDirs = []string{cache}
	var out bytes.Buffer
	r := &Runner{cfg: cfg, now: idleNow(), dryRun: true, out: &out,
		free: func() (float64, error) { return 5, nil }}
	if err := r.Run(); err != nil {
		t.Fatal(err)
	}
	if !exists(wt.Path) || !exists(cache) {
		t.Fatal("dry run deleted something")
	}
	if !strings.Contains(out.String(), "would remove worktree "+wt.Path) {
		t.Fatalf("dry run did not report the worktree\n%s", out.String())
	}
}

func TestDeletableRefusesShallowPaths(t *testing.T) {
	for path, want := range map[string]bool{
		"/":                        false,
		"/Users/me":                false,
		"/Users/me/repos":          false,
		"relative/a/b/c/d":         false,
		"/Users/me/repos/x/target": true,
	} {
		if got := deletable(path); got != want {
			t.Errorf("deletable(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestLoadConfigRejectsUnknownFieldsAndBadThresholds(t *testing.T) {
	dir := t.TempDir()
	unknown := filepath.Join(dir, "unknown.json")
	mustWrite(t, unknown, `{"soft_free_percnt": 30}`)
	if _, err := loadConfig(unknown); err == nil {
		t.Error("unknown field accepted")
	}
	inverted := filepath.Join(dir, "inverted.json")
	mustWrite(t, inverted, `{"soft_free_percent": 5, "hard_free_percent": 10}`)
	if _, err := loadConfig(inverted); err == nil {
		t.Error("hard above soft accepted")
	}
	if cfg, err := loadConfig(filepath.Join(dir, "missing.json")); err != nil || cfg.SoftFreePercent != 20 {
		t.Errorf("missing file should yield defaults, got %+v, %v", cfg, err)
	}
}

func TestRenderPlistEscapesAndSetsInterval(t *testing.T) {
	plist := renderPlist("/bin/a&b", "/cfg.json", "/log", "/usr/bin", 2*time.Hour)
	if !strings.Contains(plist, "/bin/a&amp;b") || !strings.Contains(plist, "<integer>7200</integer>") {
		t.Fatalf("unexpected plist:\n%s", plist)
	}
}

func TestPaletteOffLeavesTextAndPathsPlain(t *testing.T) {
	p := palette{}
	if got := p.green("SAFE"); got != "SAFE" {
		t.Fatalf("green = %q", got)
	}
	if got := p.path("/a/b/c/worktree"); got != "/a/b/c/worktree" {
		t.Fatalf("path = %q", got)
	}
	var buf bytes.Buffer
	if colorEnabled(&buf) {
		t.Fatal("a buffer is not a terminal")
	}
}

func TestPaletteOnShortensHomeAndHighlightsName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := palette{on: true}.path(filepath.Join(home, "repos", "shop", "wt"))
	want := "\x1b[2m~/repos/shop/\x1b[0m\x1b[1mwt\x1b[0m"
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

func TestScanSummary(t *testing.T) {
	cases := map[string]string{
		scanSummary(0, 0, 0): "no managed worktrees found",
		scanSummary(3, 1, 0): "3 worktrees: 1 safe, 2 kept",
		scanSummary(3, 1, 2): "3 worktrees: 1 safe, 2 kept; 2 orphan leftovers",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("summary = %q, want %q", got, want)
		}
	}
}

func TestLoopRepeatsUntilCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	passes := 0
	err := loop(ctx, time.Millisecond, &out, func() error {
		passes++
		if passes == 3 {
			cancel()
		}
		if passes == 2 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil || passes != 3 {
		t.Fatalf("err = %v, passes = %d", err, passes)
	}
	if !strings.Contains(out.String(), "failed: transient") || !strings.HasSuffix(out.String(), "stopped\n") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestLoopFailsFastOnFirstPass(t *testing.T) {
	passes := 0
	err := loop(context.Background(), time.Hour, io.Discard, func() error {
		passes++
		return errors.New("bad config")
	})
	if err == nil || passes != 1 {
		t.Fatalf("err = %v, passes = %d", err, passes)
	}
}

func TestLoopRejectsShortInterval(t *testing.T) {
	if err := runLoop("", time.Second, false, io.Discard); err == nil {
		t.Fatal("expected an error for -every below 1m")
	}
	if err := startBackgroundLoop("", time.Second, false, io.Discard); err == nil {
		t.Fatal("expected an error for -every below 1m")
	}
}

// A stale pid file must never make "stop" signal a process that is not garbaj.
func TestStopIgnoresPidFileOfAnotherProgram(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writePid := func(pid int) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(pidFilePath()), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pidFilePath(), []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := runningLoopPid(); ok {
		t.Fatal("no pid file should mean no running loop")
	}
	writePid(os.Getppid())
	if pid, ok := runningLoopPid(); ok {
		t.Fatalf("pid %d belongs to the test runner, not garbaj", pid)
	}
	if err := stopBackgroundLoop(io.Discard); err == nil {
		t.Fatal("stop should refuse when no garbaj loop is running")
	}
	if _, err := os.Stat(pidFilePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale pid file should be cleaned up")
	}
	writePid(os.Getpid())
	if pid, ok := runningLoopPid(); !ok || pid != os.Getpid() {
		t.Fatal("this test binary should be recognised as the running program")
	}
}
