package main

import (
	"os"

	"github.com/vzd3v/agent_session_collector/internal/cli"
)

var version = "dev"

func main() {
	os.Exit(cli.RunSkeleton(os.Stdout, os.Stderr, os.Args, cli.Skeleton{
		Name:    "asc-receive",
		Version: version,
		Summary: "forced-command receive behavior is implemented in a later phase",
	}))
}
