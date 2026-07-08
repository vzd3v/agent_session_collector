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
	"github.com/vzd3v/agent_session_collector/internal/sender"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args, os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 && args[1] == "sync" {
		return runSync(args[2:], stdout, stderr)
	}
	return cli.RunSkeleton(stdout, stderr, args, cli.Skeleton{
		Name:     "asc-sender",
		Version:  version,
		Summary:  "Scan configured transcript paths and upload changed JSONL files to a hub over rsync/SSH.",
		Usage:    "asc-sender <command>",
		Commands: []string{"sync", "version"},
		Examples: []string{
			"asc-sender sync -config /etc/agent-session-collector/sender.toml -dry-run",
			"asc-sender sync -config /etc/agent-session-collector/sender.toml",
		},
	})
}

func runSync(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "sender TOML config path")
	dryRun := fs.Bool("dry-run", false, "scan and print upload plan without rsync or state updates")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *configPath == "" {
		_, _ = fmt.Fprintln(stderr, "asc-sender sync: -config is required")
		return 2
	}
	cfg, err := config.LoadSenderFile(*configPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "asc-sender sync: load config: %v\n", err)
		return 1
	}
	result, err := sender.Sync(context.Background(), cfg, sender.Options{
		DryRun: *dryRun,
		Stdout: stdout,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "asc-sender sync: %v\n", err)
		return 1
	}
	if !*dryRun {
		_, _ = fmt.Fprintf(stdout, "sync scanned=%d planned=%d skipped=%d uploaded=%d\n", result.Scanned, result.Planned, result.Skipped, result.Uploaded)
	}
	return 0
}
