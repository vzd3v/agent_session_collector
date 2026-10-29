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

func TestOpaqueArtifactIdentityDoesNotGuessConversation(t *testing.T) {
	name := "rollout-example-aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee_11111111-2222-4333-8444-555555555555.jsonl"
	left, err := ForArtifact(constants.AgentCodex, "sessions/"+name)
	if err != nil {
		t.Fatal(err)
	}
	right, err := ForArtifact(constants.AgentCodex, ".codex/sessions/"+name)
	if err != nil || left != right || len(left) != len("artifact-")+64 {
		t.Fatalf("opaque IDs differ: %q %q %v", left, right, err)
	}
	if _, err := FromPath(constants.AgentCodex, name); !errors.Is(err, ErrSessionIDMissing) {
		t.Fatalf("opaque artifact was treated as a conversation: %v", err)
	}
}
