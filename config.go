package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	Volume           string     `json:"volume"`
	SoftFreePercent  float64    `json:"soft_free_percent"`
	HardFreePercent  float64    `json:"hard_free_percent"`
	IdleHours        float64    `json:"idle_hours"`
	OrphanIdleHours  float64    `json:"orphan_idle_hours"`
	RepoRoots        []string   `json:"repo_roots"`
	WorktreeMarkers  []string   `json:"worktree_markers"`
	OrphanParents    []string   `json:"orphan_parents"`
	IgnoredUntracked []string   `json:"ignored_untracked"`
	CacheDirs        []string   `json:"cache_dirs"`
	CacheCommands    [][]string `json:"cache_commands"`
}

func defaultConfig() Config {
	return Config{
		Volume:           "~",
		SoftFreePercent:  20,
		HardFreePercent:  10,
		IdleHours:        12,
		OrphanIdleHours:  72,
		RepoRoots:        []string{"~/repos"},
		WorktreeMarkers:  []string{"/.claude/worktrees/", "/.codex/worktrees/"},
		OrphanParents:    []string{".claude/worktrees"},
		IgnoredUntracked: []string{"node_modules", "output", "test-results", "playwright-report"},
		CacheDirs:        []string{},
		CacheCommands:    [][]string{{"go", "clean", "-cache"}},
	}
}

func defaultConfigPath() string {
	return expandHome("~/.config/garbaj/config.json")
}

func expandHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		return filepath.Join(home, rest)
	}
	return path
}

func loadConfig(path string) (Config, error) {
	cfg := defaultConfig()
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return cfg, err
	}
	if err == nil {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			return cfg, fmt.Errorf("%s: %w", path, err)
		}
	}
	cfg.expandPaths()
	return cfg, cfg.validate()
}

func (c *Config) expandPaths() {
	c.Volume = expandHome(c.Volume)
	for i, root := range c.RepoRoots {
		c.RepoRoots[i] = expandHome(root)
	}
	for i, dir := range c.CacheDirs {
		c.CacheDirs[i] = expandHome(dir)
	}
}

func (c Config) validate() error {
	if c.HardFreePercent < 0 || c.SoftFreePercent > 100 || c.HardFreePercent > c.SoftFreePercent {
		return errors.New("need 0 <= hard_free_percent <= soft_free_percent <= 100")
	}
	if c.IdleHours <= 0 || c.OrphanIdleHours <= 0 {
		return errors.New("idle_hours and orphan_idle_hours must be positive")
	}
	return nil
}

func (c Config) idle() time.Duration {
	return time.Duration(c.IdleHours * float64(time.Hour))
}

func (c Config) orphanIdle() time.Duration {
	return time.Duration(c.OrphanIdleHours * float64(time.Hour))
}

func (c Config) managed(path string) bool {
	for _, marker := range c.WorktreeMarkers {
		if strings.Contains(path, marker) {
			return true
		}
	}
	return false
}

func writeDefaultConfig(path string, out io.Writer) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	data, err := json.MarshalIndent(defaultConfig(), "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintln(out, "wrote", path)
	return nil
}
