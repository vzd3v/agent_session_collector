package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/vzd3v/agent_session_collector/internal/cli"
	"github.com/vzd3v/agent_session_collector/internal/config"
	"github.com/vzd3v/agent_session_collector/internal/hub"
)

var version = "dev"

const helpExitCode = -1

func main() {
	os.Exit(run(os.Args, os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		switch args[1] {
		case "index":
			return runIndex(args[2:], stdout, stderr)
		case "compact":
			return runCompact(args[2:], stdout, stderr)
		case "doctor":
			return runDoctor(args[2:], stdout, stderr)
		}
	}
	return cli.RunSkeleton(stdout, stderr, args, cli.Skeleton{
		Name:     "asc-hub",
		Version:  version,
		Summary:  "Index mirrored session files, emit session-index metadata, compact quiet sessions, and run hub checks.",
		Usage:    "asc-hub <command>",
		Commands: []string{"index", "compact", "doctor", "version"},
		Examples: []string{
			"asc-hub doctor -config /etc/agent-session-collector/hub.toml",
			"asc-hub index -config /etc/agent-session-collector/hub.toml",
			"asc-hub compact -config /etc/agent-session-collector/hub.toml",
		},
	})
}

func runIndex(args []string, stdout, stderr io.Writer) int {
	cfg, code := loadHubCommandConfig("index", args, stderr)
	if code == helpExitCode {
		return 0
	}
	if code != 0 {
		return code
	}
	result, err := hub.Index(context.Background(), cfg, hub.Options{})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "asc-hub index: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "index scanned=%d upserted=%d events_emitted=%d\n", result.Scanned, result.Upserted, result.EventsEmitted)
	return 0
}

func runCompact(args []string, stdout, stderr io.Writer) int {
	cfg, code := loadHubCommandConfig("compact", args, stderr)
	if code == helpExitCode {
		return 0
	}
	if code != 0 {
		return code
	}
	result, err := hub.Compact(context.Background(), cfg, hub.CompactOptions{})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "asc-hub compact: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "compact scanned=%d archived=%d deleted_mirror=%d\n", result.Scanned, result.Archived, result.DeletedMirror)
	return 0
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	cfg, code := loadHubCommandConfig("doctor", args, stderr)
	if code == helpExitCode {
		return 0
	}
	if code != 0 {
		return code
	}
	checks := hub.Doctor(cfg)
	exitCode := 0
	for _, check := range checks {
		status := "ok"
		if !check.OK {
			status = "fail"
			exitCode = 1
		}
		_, _ = fmt.Fprintf(stdout, "%s %s: %s\n", status, check.Name, check.Message)
	}
	return exitCode
}

func loadHubCommandConfig(command string, args []string, stderr io.Writer) (config.HubAppConfig, int) {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "hub TOML config path")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return config.HubAppConfig{}, helpExitCode
		}
		return config.HubAppConfig{}, 2
	}
	if *configPath == "" {
		_, _ = fmt.Fprintf(stderr, "asc-hub %s: -config is required\n", command)
		return config.HubAppConfig{}, 2
	}
	cfg, err := config.LoadHubFile(*configPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "asc-hub %s: load config: %v\n", command, err)
		return config.HubAppConfig{}, 1
	}
	return cfg, 0
}
