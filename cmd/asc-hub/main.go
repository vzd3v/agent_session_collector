package main

import (
	"context"
	"encoding/json"
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
		case "migrate":
			return runMigrate(args[2:], stdout, stderr)
		case "doctor":
			return runDoctor(args[2:], stdout, stderr)
		}
	}
	return cli.RunSkeleton(stdout, stderr, args, cli.Skeleton{
		Name:     "asc-hub",
		Version:  version,
		Summary:  "Index mirrored session files, emit session-index metadata, compact quiet sessions, and run hub checks.",
		Usage:    "asc-hub <command>",
		Commands: []string{"index", "compact", "migrate", "doctor", "version"},
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
	fs := flag.NewFlagSet("compact", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "hub TOML config path")
	dryRun := fs.Bool("dry-run", false, "read-only retention plan; requires migrated catalog")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *configPath == "" || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "asc-hub compact: -config is required; positional arguments are not accepted")
		return 2
	}
	cfg, loadErr := config.LoadHubFile(*configPath)
	if loadErr != nil {
		_, _ = fmt.Fprintf(stderr, "asc-hub compact: load config: %v\n", loadErr)
		return 1
	}
	result, err := hub.Compact(context.Background(), cfg, hub.CompactOptions{DryRun: *dryRun})
	if result.Plan != nil {
		if *dryRun {
			if encodeErr := json.NewEncoder(stdout).Encode(result.Plan); encodeErr != nil {
				_, _ = fmt.Fprintf(stderr, "asc-hub compact: encode plan: %v\n", encodeErr)
				return 1
			}
		} else {
			_, _ = fmt.Fprintf(stdout, "retention archive_bytes=%d pressure_unsatisfied=%t\n", result.Plan.ArchiveBytes, result.Plan.PressureUnsatisfied)
		}
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "asc-hub compact: %v\n", err)
		return 1
	}
	if !*dryRun {
		_, _ = fmt.Fprintf(stdout, "compact scanned=%d archived=%d deleted_mirror=%d\n", result.Scanned, result.Archived, result.DeletedMirror)
	}
	return 0
}

func runMigrate(args []string, stdout, stderr io.Writer) int {
	cfg, code := loadHubCommandConfig("migrate", args, stderr)
	if code == helpExitCode {
		return 0
	}
	if code != 0 {
		return code
	}
	applied, err := hub.Migrate(context.Background(), cfg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "asc-hub migrate: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "migrate applied=%d\n", applied)
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
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "asc-hub %s: positional arguments are not accepted\n", command)
		return config.HubAppConfig{}, 2
	}
	cfg, err := config.LoadHubFile(*configPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "asc-hub %s: load config: %v\n", command, err)
		return config.HubAppConfig{}, 1
	}
	return cfg, 0
}
