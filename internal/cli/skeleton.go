package cli

import (
	"fmt"
	"io"
)

type Skeleton struct {
	Name    string
	Version string
	Summary string
}

func RunSkeleton(stdout, stderr io.Writer, args []string, cmd Skeleton) int {
	if len(args) > 1 {
		switch args[1] {
		case "version", "--version", "-v":
			return writeExit(stdout, "%s %s\n", cmd.Name, cmd.Version)
		case "help", "--help", "-h":
			return writeExit(stdout, "Usage: %s <command>\n\n%s\n\nCommands:\n  version\n", cmd.Name, cmd.Summary)
		}
	}

	if _, err := fmt.Fprintf(stderr, "%s: %s\n", cmd.Name, cmd.Summary); err != nil {
		return 1
	}
	return 2
}

func writeExit(w io.Writer, format string, args ...any) int {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		return 1
	}
	return 0
}
