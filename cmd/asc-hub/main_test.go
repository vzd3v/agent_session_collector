package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunHelpListsHubCommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"asc-hub", "help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(stdout.String(), "index") || !strings.Contains(stdout.String(), "compact") || !strings.Contains(stdout.String(), "doctor") {
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

func TestRunCompactHelpDoesNotContinueExecution(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"asc-hub", "compact", "-h"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if strings.Contains(stdout.String(), "compact scanned=") {
		t.Fatalf("stdout shows compact execution after help: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Usage of compact") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestMigrateIsQuietIdempotentAndPreviewNeedsNoTools(t *testing.T) {
	root := t.TempDir()
	body, err := os.ReadFile("../../examples/hub.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "hub.toml")
	body = []byte(strings.ReplaceAll(string(body), "/var/lib/agent-session-collector", filepath.ToSlash(root)))
	if err := os.WriteFile(configPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()) // Neither command invokes rsync, zstd or HTTP.
	for _, want := range []string{"migrate applied=5\n", "migrate applied=0\n"} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"asc-hub", "migrate", "-config", configPath}, &stdout, &stderr); code != 0 || stdout.String() != want || stderr.Len() != 0 {
			t.Fatalf("migrate code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"asc-hub", "compact", "-config", configPath, "-dry-run"}, &stdout, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("preview code=%d stderr=%q", code, stderr.String())
	}
	var plan map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan["archive_bytes"] != float64(0) || plan["pressure_unsatisfied"] != false {
		t.Fatalf("preview=%v", plan)
	}
	for _, dir := range []string{"mirror", "archive", "extracted"} {
		if _, err := os.Stat(filepath.Join(root, dir)); !os.IsNotExist(err) {
			t.Fatalf("command created %s: %v", dir, err)
		}
	}
}
