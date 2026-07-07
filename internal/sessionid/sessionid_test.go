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
			name:  "codex uuid v4",
			agent: constants.AgentCodex,
			path:  "sessions/2026/07/07/rollout-2026-07-07T00-00-00-aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee.jsonl",
			want:  "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		},
		{
			name:  "codex uuid v7",
			agent: constants.AgentCodex,
			path:  "sessions/2026/04/22/rollout-2026-04-22T15-49-28-019db5e1-ce78-7710-9f6f-493265341666.jsonl",
			want:  "019db5e1-ce78-7710-9f6f-493265341666",
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
