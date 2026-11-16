package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

const agentLabel = "io.garbaj.agent"

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array><string>%s</string><string>run</string><string>-config</string><string>%s</string></array>
	<key>StartInterval</key><integer>%d</integer>
	<key>RunAtLoad</key><true/>
	<key>ProcessType</key><string>Background</string>
	<key>StandardOutPath</key><string>%s</string>
	<key>StandardErrorPath</key><string>%s</string>
	<key>EnvironmentVariables</key>
	<dict><key>PATH</key><string>%s</string></dict>
</dict>
</plist>
`

func plistPath() string {
	return expandHome("~/Library/LaunchAgents/" + agentLabel + ".plist")
}

func agentTarget() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func xmlEscape(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

func renderPlist(exe, configPath, logPath, pathEnv string, every time.Duration) string {
	return fmt.Sprintf(plistTemplate, agentLabel, xmlEscape(exe), xmlEscape(configPath),
		int(every.Seconds()), xmlEscape(logPath), xmlEscape(logPath), xmlEscape(pathEnv))
}

func installAgent(configPath string, every time.Duration, out io.Writer) error {
	if runtime.GOOS != "darwin" {
		return errors.New("install supports macOS launchd only; schedule `garbaj run` with cron or systemd elsewhere")
	}
	if every < time.Minute {
		return errors.New("-every must be at least 1m")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	logPath := logFilePath()
	plist := renderPlist(exe, configPath, logPath, os.Getenv("PATH"), every)
	if err := os.WriteFile(plistPath(), []byte(plist), 0o644); err != nil {
		return err
	}
	_ = exec.Command("launchctl", "bootout", agentTarget(), plistPath()).Run()
	if err := exec.Command("launchctl", "bootstrap", agentTarget(), plistPath()).Run(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w", err)
	}
	fmt.Fprintf(out, "installed %s: runs %s every %s, logs to %s\n", agentLabel, exe, every, logPath)
	return nil
}

func uninstallAgent(out io.Writer) error {
	_ = exec.Command("launchctl", "bootout", agentTarget(), plistPath()).Run()
	if err := os.Remove(plistPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Fprintln(out, "uninstalled", agentLabel)
	return nil
}
