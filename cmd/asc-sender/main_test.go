package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunSyncHelpExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"asc-sender", "sync", "-help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "-config") {
		t.Fatalf("help output missing -config: %q", stderr.String())
	}
}

func TestRunTopLevelHelpListsSync(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"asc-sender", "help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "sync") {
		t.Fatalf("top-level help missing sync: %q", stdout.String())
	}
}
