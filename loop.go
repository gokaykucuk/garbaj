package main

import (
	"bytes"
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

const (
	minInterval = time.Minute
	stopWait    = 5 * time.Second
)

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
	pid, running, err := runningLoopPid()
	if err != nil {
		return err
	}
	if running {
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
	pid = cmd.Process.Pid
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		_ = cmd.Process.Kill()
		return err
	}
	_ = cmd.Process.Release()
	fmt.Fprintf(out, "started background loop (pid %d): runs every %s, logs to %s\nstop it with `garbaj stop`\n",
		pid, every, logPath)
	return nil
}

// stopBackgroundLoop leaves the pid file to the loop itself, which removes it
// on exit. Until then the loop stays the recorded owner, so a new "loop -bg"
// cannot start while the old one is still finishing a pass.
func stopBackgroundLoop(out io.Writer) error {
	pid, running, err := runningLoopPid()
	if err != nil {
		return err
	}
	if !running {
		_ = os.Remove(pidFilePath())
		return errors.New("no background loop is running")
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("stop pid %d: %w", pid, err)
	}
	if waitForExit(pid, stopWait) {
		removePidFileOf(pid)
		fmt.Fprintf(out, "stopped background loop (pid %d)\n", pid)
		return nil
	}
	fmt.Fprintf(out, "stopping background loop (pid %d): it exits when its current pass finishes\n", pid)
	return nil
}

func waitForExit(pid int, limit time.Duration) bool {
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return true
		}
	}
	return false
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
// "stop" signal an unrelated program that reused the pid. An error means the
// process could not be inspected: callers must then neither start a second
// loop nor discard the pid file.
func runningLoopPid() (pid int, running bool, err error) {
	pid, ok := readPidFile()
	if !ok || errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return 0, false, nil
	}
	name, found, err := processName(pid)
	if err != nil {
		return pid, false, fmt.Errorf("cannot tell whether pid %d from %s is a running garbaj loop: %w",
			pid, pidFilePath(), err)
	}
	return pid, found && isGarbaj(name), nil
}

// processName reads /proc on Linux, which needs no external tool, and asks ps
// elsewhere. Only a positive "no such process" counts as absence: a missing
// /proc entry, or ps exiting 1 with nothing printed. Every other failure is an
// error, not an answer.
func processName(pid int) (name string, found bool, err error) {
	if runtime.GOOS == "linux" {
		if _, procErr := os.Stat("/proc/self"); procErr == nil {
			data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
				return "", false, nil
			}
			if err != nil {
				return "", false, err
			}
			return strings.TrimSpace(string(data)), true, nil
		}
	}
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	name = strings.TrimSpace(string(out))
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 && name == "" && len(bytes.TrimSpace(exit.Stderr)) == 0 {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("ps: %w", err)
	}
	if name == "" {
		return "", false, errors.New("ps printed no process name")
	}
	return name, true, nil
}

// Linux truncates process names to 15 bytes.
func isGarbaj(processName string) bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	name, want := filepath.Base(processName), filepath.Base(exe)
	return name == want || (len(name) == 15 && strings.HasPrefix(want, name))
}

func removeOwnPidFile() { removePidFileOf(os.Getpid()) }

func removePidFileOf(pid int) {
	if recorded, ok := readPidFile(); ok && recorded == pid {
		_ = os.Remove(pidFilePath())
	}
}
