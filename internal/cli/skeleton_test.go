package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunSkeletonVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := RunSkeleton(&stdout, &stderr, []string{"asc-hub", "version"}, Skeleton{
		Name:    "asc-hub",
		Version: "test",
		Summary: "summary",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if got := strings.TrimSpace(stdout.String()); got != "asc-hub test" {
		t.Fatalf("stdout = %q", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunSkeletonUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := RunSkeleton(&stdout, &stderr, []string{"asc-sender", "sync"}, Skeleton{
		Name:    "asc-sender",
		Version: "test",
		Summary: "sync behavior is implemented later",
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "asc-sender") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
