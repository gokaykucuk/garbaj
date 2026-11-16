package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

const usage = `usage: garbaj <command> [flags]

commands:
  scan       list every managed worktree and whether it is safe to remove
  run        clean up if free space is below the thresholds (-dry-run to preview)
  init       write the default config file
  install    schedule "garbaj run" with launchd (-every 6h)
  uninstall  remove the launchd schedule

flags:
  -config path   config file (default ~/.config/garbaj/config.json)`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "garbaj:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "config file")
	dryRun := fs.Bool("dry-run", false, "print what run would delete without deleting")
	every := fs.Duration("every", 6*time.Hour, "interval between scheduled runs")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	switch args[0] {
	case "init":
		return writeDefaultConfig(*configPath, out)
	case "install":
		return installAgent(*configPath, *every, out)
	case "uninstall":
		return uninstallAgent(out)
	case "scan", "run":
		return runCleanup(args[0], *configPath, *dryRun, out)
	}
	return errors.New(usage)
}

func runCleanup(command, configPath string, dryRun bool, out io.Writer) error {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	runner := newRunner(cfg, dryRun, out)
	if command == "scan" {
		return runner.Scan()
	}
	return runner.Run()
}
