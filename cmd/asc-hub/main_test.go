package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunHelpListsHubCommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"asc-hub", "help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(stdout.String(), "index") || !strings.Contains(stdout.String(), "doctor") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunIndexRequiresConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"asc-hub", "index"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(stderr.String(), "-config is required") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
