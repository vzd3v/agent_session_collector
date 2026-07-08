package cli

import (
	"fmt"
	"io"
	"strings"
)

type Skeleton struct {
	Name     string
	Version  string
	Summary  string
	Usage    string
	Commands []string
	Examples []string
}

func RunSkeleton(stdout, stderr io.Writer, args []string, cmd Skeleton) int {
	if len(args) > 1 {
		switch args[1] {
		case "version", "--version", "-v":
			return writeExit(stdout, "%s %s\n", cmd.Name, cmd.Version)
		case "help", "--help", "-h":
			commands := cmd.Commands
			if len(commands) == 0 {
				commands = []string{"version"}
			}
			usage := cmd.Usage
			if usage == "" {
				usage = cmd.Name + " <command>"
			}
			message := fmt.Sprintf("Usage: %s\n\n%s\n\nCommands:\n  %s\n", usage, cmd.Summary, strings.Join(commands, "\n  "))
			if len(cmd.Examples) > 0 {
				message += "\nExamples:\n  " + strings.Join(cmd.Examples, "\n  ") + "\n"
			}
			return writeExit(stdout, "%s", message)
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
