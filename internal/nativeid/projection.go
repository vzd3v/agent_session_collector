// Package nativeid extracts rebuildable identity evidence, never interpreting prompts as authority.
package nativeid

import (
	"bufio"
	"encoding/json"
	"io"
	"path"
	"regexp"
	"strings"

	"github.com/vzd3v/agent_session_collector/internal/artifact"
	"github.com/vzd3v/agent_session_collector/internal/sessionid"
)

const Version = 1
const MaxRecordBytes = 16 * 1024 * 1024
const MaxHeaderIDs = 256

var uuidRE = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var agentRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

type Projection struct {
	SessionID string `json:"session_id,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
	RootID    string `json:"root_id,omitempty"`
	ParentID  string `json:"parent_id,omitempty"`
	ForkID    string `json:"fork_id,omitempty"`
	Status    string `json:"status"`
	Evidence  string `json:"evidence"`
}

type codexMeta struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	Parent    string          `json:"parent_thread_id"`
	Fork      string          `json:"forked_from_id"`
	Source    json.RawMessage `json:"source"`
}

func Parse(r io.Reader, agent, rel, role string) Projection {
	p := Projection{Status: "unresolved", Evidence: "no unambiguous native identity"}
	if role != artifact.Transcript && role != artifact.SubagentMetadata {
		p.Status = "not_applicable"
		return p
	}
	headers := map[string]codexMeta{}
	rootIDs := map[string]bool{}
	parentIDs := map[string]bool{}
	malformed, oversized := false, false
	reader := bufio.NewReaderSize(r, 64*1024)
	for {
		var record []byte
		tooLarge := false
		for {
			part, more, err := reader.ReadLine()
			if len(record)+len(part) > MaxRecordBytes {
				tooLarge = true
			}
			if !tooLarge {
				record = append(record, part...)
			}
			if err != nil {
				if err != io.EOF {
					malformed = true
				}
				if len(record) == 0 {
					goto done
				}
			}
			if !more {
				break
			}
		}
		if tooLarge {
			oversized = true
			continue
		}
		var row struct {
			Type          string          `json:"type"`
			Payload       json.RawMessage `json:"payload"`
			SessionID     string          `json:"sessionId"`
			ParentAgentID string          `json:"parentAgentId"`
		}
		if json.Unmarshal(record, &row) != nil {
			malformed = true
			continue
		}
		if agent == "codex" && row.Type == "session_meta" {
			var m codexMeta
			if json.Unmarshal(row.Payload, &m) == nil && uuidRE.MatchString(m.ID) {
				m.ID = strings.ToLower(m.ID)
				if !uuidRE.MatchString(m.SessionID) {
					m.SessionID = ""
				} else {
					m.SessionID = strings.ToLower(m.SessionID)
				}
				if !uuidRE.MatchString(m.Parent) {
					m.Parent = ""
				} else {
					m.Parent = strings.ToLower(m.Parent)
				}
				if !uuidRE.MatchString(m.Fork) {
					m.Fork = ""
				} else {
					m.Fork = strings.ToLower(m.Fork)
				}
				if m.Parent == "" {
					var s struct {
						Subagent struct {
							Spawn struct {
								Parent string `json:"parent_thread_id"`
							} `json:"thread_spawn"`
						} `json:"subagent"`
					}
					if json.Unmarshal(m.Source, &s) == nil && uuidRE.MatchString(s.Subagent.Spawn.Parent) {
						m.Parent = strings.ToLower(s.Subagent.Spawn.Parent)
					}
				}
				// Retain only bounded identity fields, never arbitrary multi-megabyte source payloads.
				m.Source = nil
				if old, ok := headers[m.ID]; ok && (old.SessionID != m.SessionID || old.Parent != m.Parent || old.Fork != m.Fork) {
					p.Status = "conflict"
					p.Evidence = "conflicting native headers"
					return p
				}
				if _, exists := headers[m.ID]; exists || len(headers) < MaxHeaderIDs {
					headers[m.ID] = m
				}
			}
		}
		if agent == "claude" {
			if uuidRE.MatchString(row.SessionID) && len(rootIDs) < MaxHeaderIDs {
				rootIDs[row.SessionID] = true
			}
			if agentRE.MatchString(row.ParentAgentID) && len(parentIDs) < MaxHeaderIDs {
				parentIDs[row.ParentAgentID] = true
			}
		}
	}
done:
	if agent == "codex" {
		candidate, _ := sessionid.FromPath(agent, strings.TrimSuffix(rel, ".zst"))
		m, found := headers[candidate]
		if !found && len(headers) == 1 {
			for _, only := range headers {
				m = only
				found = true
			}
		}
		if found {
			p.SessionID = strings.ToLower(m.ID)
			p.Status = "resolved"
			p.Evidence = "session_meta.id selected by filename or sole distinct native header"
			if uuidRE.MatchString(m.SessionID) {
				p.RootID = m.SessionID
			}
			if uuidRE.MatchString(m.Parent) {
				p.ParentID = m.Parent
			}
			if uuidRE.MatchString(m.Fork) {
				p.ForkID = m.Fork
			}
		} else if len(headers) > 1 {
			p.Status = "conflict"
			p.Evidence = "multiple native headers without independent owner match"
		}
	} else if agent == "claude" {
		stem := strings.TrimSuffix(strings.TrimSuffix(path.Base(rel), ".zst"), ".jsonl")
		if role == artifact.SubagentMetadata {
			stem = strings.TrimSuffix(path.Base(rel), ".meta.json")
		}
		parts := strings.Split(rel, "/")
		for i, part := range parts {
			if part == "subagents" && i > 0 && uuidRE.MatchString(parts[i-1]) {
				p.RootID = parts[i-1]
				break
			}
		}
		if strings.HasPrefix(stem, "agent-") && p.RootID != "" && agentRE.MatchString(strings.TrimPrefix(stem, "agent-")) {
			p.AgentID = strings.TrimPrefix(stem, "agent-")
			p.SessionID = p.RootID
			p.Status = "resolved"
			p.Evidence = "subagent filename and containing root directory; no direct-parent inference"
		} else if uuidRE.MatchString(stem) && (len(rootIDs) == 0 || rootIDs[stem]) {
			p.SessionID = stem
			p.RootID = stem
			p.Status = "resolved"
			p.Evidence = "native UUID transcript filename corroborated where available"
		} else if len(rootIDs) == 1 && role == artifact.Transcript {
			for id := range rootIDs {
				p.SessionID = id
				p.RootID = id
			}
			p.Status = "resolved"
			p.Evidence = "sole transcript sessionId"
		}
		if len(parentIDs) == 1 {
			for id := range parentIDs {
				p.ParentID = id
			}
			p.Evidence += "; explicit parentAgentId"
		}
	}
	if (malformed || oversized) && p.Status == "unresolved" {
		p.Evidence = "unknown, incomplete or oversized records preserved without identity claims"
	}
	return p
}
