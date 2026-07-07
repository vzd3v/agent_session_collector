package main

import (
	"os"

	"github.com/vzd3v/agent_session_collector/internal/cli"
)

var version = "dev"

func main() {
	os.Exit(cli.RunSkeleton(os.Stdout, os.Stderr, os.Args, cli.Skeleton{
		Name:    "asc-hub",
		Version: version,
		Summary: "index and compact behavior are implemented in later phases",
	}))
}
