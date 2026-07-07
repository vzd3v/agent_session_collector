package sessionid

import (
	"errors"
	"testing"

	"github.com/vzd3v/agent_session_collector/internal/constants"
)

func TestFromPath(t *testing.T) {
	tests := []struct {
		name  string
		agent string
		path  string
		want  string
	}{
		{
			name:  "claude",
			agent: constants.AgentClaude,
			path:  "testdata/claude/projects/example/claude-session-0001.jsonl",
			want:  "claude-session-0001",
		},
		{
			name:  "codex",
			agent: constants.AgentCodex,
			path:  "sessions/2026/07/07/rollout-2026-07-07T00-00-00-aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee.jsonl",
			want:  "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FromPath(tc.agent, tc.path)
			if err != nil {
				t.Fatalf("FromPath returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("FromPath = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFromPathRejectsInvalidCodexName(t *testing.T) {
	_, err := FromPath(constants.AgentCodex, "rollout-no-session.jsonl")
	if !errors.Is(err, ErrSessionIDMissing) {
		t.Fatalf("expected ErrSessionIDMissing, got %v", err)
	}
}
