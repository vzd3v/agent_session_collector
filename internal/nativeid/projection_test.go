package nativeid

import (
	"strings"
	"testing"

	"github.com/vzd3v/agent_session_collector/internal/artifact"
)

func TestCodexHeaderOwnershipAndRelationships(t *testing.T) {
	const parent = "00000000-0000-4000-8000-000000000001"
	const child = "00000000-0000-4000-8000-000000000002"
	header := func(id, extra string) string {
		return `{"type":"session_meta","payload":{"id":"` + id + `"` + extra + `}}` + "\n"
	}
	body := header(parent, "") + header(child, `,"parent_thread_id":"`+parent+`","session_id":"`+parent+`","forked_from_id":"`+parent+`"`)
	p := Parse(strings.NewReader(body), "codex", "sessions/rollout-2026-10-29T00-00-00-"+child+".jsonl", artifact.Transcript)
	if p.SessionID != child || p.ParentID != parent || p.RootID != parent || p.ForkID != parent {
		t.Fatalf("inherited headers: %#v", p)
	}
	p = Parse(strings.NewReader(body), "codex", "sessions/unrecognized.jsonl", artifact.Transcript)
	if p.Status != "conflict" || p.SessionID != "" {
		t.Fatalf("ambiguous owner: %#v", p)
	}
	// Reverted rollouts can have a new filename but retain the sole native thread ID.
	p = Parse(strings.NewReader(header(parent, "")), "codex", "sessions/rollout-2026-10-29T00-00-00-"+child+".jsonl.zst", artifact.Transcript)
	if p.SessionID != parent {
		t.Fatalf("reverted rollout: %#v", p)
	}
	p = Parse(strings.NewReader("unknown bytes\n{partial"), "codex", "sessions/rollout-2026-10-29T00-00-00-"+child+".jsonl", artifact.Transcript)
	if p.SessionID != "" {
		t.Fatalf("filename fabricated native thread: %#v", p)
	}
	conflicting := header(child, `,"source":{"subagent":{"thread_spawn":{"parent_thread_id":"`+parent+`"}}}`) + header(child, `,"source":{"subagent":{"thread_spawn":{"parent_thread_id":"`+child+`"}}}`)
	p = Parse(strings.NewReader(conflicting), "codex", "sessions/opaque.jsonl", artifact.Transcript)
	if p.Status != "conflict" || p.SessionID != "" {
		t.Fatalf("derived source parent conflict ignored: %#v", p)
	}
}

func TestClaudeChildDirectoryProvesRootOnly(t *testing.T) {
	const root = "00000000-0000-4000-8000-000000000001"
	rel := "projects/project/" + root + "/subagents/agent-worker.jsonl"
	p := Parse(strings.NewReader(`{"sessionId":"`+root+`"}`), "claude", rel, artifact.Transcript)
	if p.RootID != root || p.AgentID != "worker" || p.ParentID != "" {
		t.Fatalf("root is not direct parent: %#v", p)
	}
	p = Parse(strings.NewReader(`{"sessionId":"`+root+`","parentAgentId":"supervisor"}`), "claude", rel, artifact.Transcript)
	if p.ParentID != "supervisor" {
		t.Fatalf("explicit parent ignored: %#v", p)
	}
	p = Parse(strings.NewReader("{}"), "codex", "session_index.jsonl", artifact.SessionNameIndex)
	if p.Status != "not_applicable" || p.SessionID != "" {
		t.Fatalf("global index is not a session: %#v", p)
	}
}
