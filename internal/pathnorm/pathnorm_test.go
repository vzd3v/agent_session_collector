package pathnorm

import (
	"errors"
	"testing"
)

func TestRelativeCleansSafePath(t *testing.T) {
	got, err := Relative("./.codex/sessions/2026/07/07")
	if err != nil {
		t.Fatalf("Relative returned error: %v", err)
	}
	if got != ".codex/sessions/2026/07/07" {
		t.Fatalf("Relative = %q", got)
	}
}

func TestRelativeRejectsUnsafePaths(t *testing.T) {
	tests := []struct {
		name string
		path string
		want error
	}{
		{name: "absolute", path: "/home/operator/.codex", want: ErrAbsolutePath},
		{name: "traversal", path: "../.codex", want: ErrTraversalPath},
		{name: "mid traversal", path: "a/../b", want: ErrTraversalPath},
		{name: "end traversal", path: "a/b/..", want: ErrTraversalPath},
		{name: "empty before traversal", path: "a//../b", want: ErrTraversalPath},
		{name: "backslash", path: `..\sessions`, want: ErrBackslashPath},
		{name: "empty", path: " ", want: ErrEmptyPath},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Relative(tc.path)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Relative(%q) error = %v, want %v", tc.path, err, tc.want)
			}
		})
	}
}
