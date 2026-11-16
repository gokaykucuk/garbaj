package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const minInterval = time.Minute

func logFilePath() string {
	if runtime.GOOS == "darwin" {
		return expandHome("~/Library/Logs/garbaj.log")
	}
	return expandHome("~/.local/state/garbaj/garbaj.log")
}

func pidFilePath() string {
	return filepath.Join(filepath.Dir(defaultConfigPath()), "loop.pid")
}

// runLoop runs cleanup passes in the foreground until interrupted. The config
// is reloaded on every pass, so edits apply without a restart.
func runLoop(configPath string, every time.Duration, dryRun bool, out io.Writer) error {
	if every < minInterval {
		return errors.New("-every must be at least 1m")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	defer removeOwnPidFile()
	return loop(ctx, every, out, func() error {
		return runCleanup("run", configPath, dryRun, out)
	})
}

// loop fails fast when the first pass fails, which is almost always a bad
// config, and afterwards logs errors and keeps going.
func loop(ctx context.Context, every time.Duration, out io.Writer, pass func() error) error {
	pal := palette{on: colorEnabled(out)}
	for first := true; ; first = false {
		if err := pass(); err != nil {
			if first {
				return err
			}
			fmt.Fprintln(out, pal.red("  failed:"), err)
		}
		fmt.Fprintln(out, pal.dim(fmt.Sprintf("next run in %s", every)))
		select {
		case <-ctx.Done():
			fmt.Fprintln(out, "stopped")
			return nil
		case <-time.After(every):
		}
	}
}

// startBackgroundLoop re-executes garbaj as a detached "loop" that outlives
// the terminal, and records its pid for "garbaj stop".
func startBackgroundLoop(configPath string, every time.Duration, dryRun bool, out io.Writer) error {
	if every < minInterval {
		return errors.New("-every must be at least 1m")
	}
	if _, err := loadConfig(configPath); err != nil {
		return err
	}
	if pid, ok := runningLoopPid(); ok {
		return fmt.Errorf("a background loop is already running (pid %d); stop it with `garbaj stop`", pid)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logPath, pidPath := logFilePath(), pidFilePath()
	for _, dir := range []string{filepath.Dir(logPath), filepath.Dir(pidPath)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()

	args := []string{"loop", "-config", configPath, "-every", every.String()}
	if dryRun {
		args = append(args, "-dry-run")
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		_ = cmd.Process.Kill()
		return err
	}
	_ = cmd.Process.Release()
	fmt.Fprintf(out, "started background loop (pid %d): runs every %s, logs to %s\nstop it with `garbaj stop`\n",
		pid, every, logPath)
	return nil
}

func stopBackgroundLoop(out io.Writer) error {
	pid, ok := runningLoopPid()
	if !ok {
		_ = os.Remove(pidFilePath())
		return errors.New("no background loop is running")
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("stop pid %d: %w", pid, err)
	}
	_ = os.Remove(pidFilePath())
	fmt.Fprintf(out, "stopped background loop (pid %d)\n", pid)
	return nil
}

func readPidFile() (int, bool) {
	data, err := os.ReadFile(pidFilePath())
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid, err == nil && pid > 1
}

// runningLoopPid trusts the pid file only while that pid still belongs to a
// garbaj process, so a stale file left by a crash or reboot can never make
// "stop" signal an unrelated program that reused the pid.
func runningLoopPid() (int, bool) {
	pid, ok := readPidFile()
	if !ok {
		return 0, false
	}
	name, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, false
	}
	return pid, isGarbaj(string(name))
}

func isGarbaj(processName string) bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return filepath.Base(strings.TrimSpace(processName)) == filepath.Base(exe)
}

func removeOwnPidFile() {
	if pid, ok := readPidFile(); ok && pid == os.Getpid() {
		_ = os.Remove(pidFilePath())
	}
}
