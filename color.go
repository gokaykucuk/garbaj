package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// palette wraps text in ANSI colors when on, and returns it unchanged
// otherwise, so logs and pipes stay plain.
type palette struct{ on bool }

// colorEnabled reports whether out is a terminal that wants color. NO_COLOR
// (https://no-color.org) and TERM=dumb turn it off.
func colorEnabled(out io.Writer) bool {
	f, ok := out.(*os.File)
	if !ok || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func (p palette) wrap(code, s string) string {
	if !p.on {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (p palette) green(s string) string  { return p.wrap("1;32", s) }
func (p palette) yellow(s string) string { return p.wrap("1;33", s) }
func (p palette) red(s string) string    { return p.wrap("1;31", s) }
func (p palette) dim(s string) string    { return p.wrap("2", s) }
func (p palette) bold(s string) string   { return p.wrap("1", s) }

// path shortens the home directory to ~ and highlights the last element,
// which is the part that tells worktrees apart. Plain output keeps the full
// path so it can be copied into scripts.
func (p palette) path(full string) string {
	if !p.on {
		return full
	}
	short := full
	if home, err := os.UserHomeDir(); err == nil {
		if rest, ok := strings.CutPrefix(full, home+string(filepath.Separator)); ok {
			short = "~" + string(filepath.Separator) + rest
		}
	}
	dir, base := filepath.Split(short)
	return p.dim(dir) + p.bold(base)
}
