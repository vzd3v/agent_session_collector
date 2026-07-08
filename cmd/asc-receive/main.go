package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/vzd3v/agent_session_collector/internal/config"
	"github.com/vzd3v/agent_session_collector/internal/receive"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args, os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		switch args[1] {
		case "version", "--version", "-v":
			_, _ = fmt.Fprintf(stdout, "asc-receive %s\n", version)
			return 0
		case "help", "--help", "-h":
			printUsage(stdout)
			return 0
		}
	}
	prefixArgs, directArgs := splitServerArgs(args[1:])
	fs := flag.NewFlagSet("asc-receive", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "hub TOML config path")
	senderID := fs.String("sender", "", "configured sender_id bound to this forced command")
	rsyncPath := fs.String("rsync-path", "/usr/bin/rsync", "rsync executable path")
	if err := fs.Parse(prefixArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *configPath == "" || *senderID == "" {
		_, _ = fmt.Fprintln(stderr, "asc-receive: -config and -sender are required")
		return 2
	}
	cfg, err := config.LoadHubFile(*configPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "asc-receive: load config: %v\n", err)
		return 1
	}
	err = receive.Exec(cfg, receive.Request{
		SenderID:        *senderID,
		OriginalCommand: os.Getenv("SSH_ORIGINAL_COMMAND"),
		DirectArgs:      directArgs,
		RsyncPath:       *rsyncPath,
	}, os.Environ())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "asc-receive: %v\n", err)
		return 1
	}
	return 0
}

func printUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, `Usage: asc-receive -config <hub.toml> -sender <sender_id> [-rsync-path <path>] [--server <rsync args>]

SSH forced-command gate for receive-side rsync.

Options:
  -config       hub TOML config path
  -sender       configured sender_id bound to this forced command
  -rsync-path   rsync executable path on the hub; default /usr/bin/rsync

Commands:
  version

AuthorizedKeys example:
  command="/usr/local/bin/asc-receive -config /etc/agent-session-collector/hub.toml -sender sender-alpha",restrict ssh-ed25519 AAAA... sender-alpha`)
}

func splitServerArgs(args []string) ([]string, []string) {
	for i, arg := range args {
		if arg == "--server" {
			return args[:i], args[i:]
		}
	}
	return args, nil
}
