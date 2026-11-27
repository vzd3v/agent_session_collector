// Package sessionapi defines the versioned Collector read API and its Go client.
package sessionapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/vzd3v/agent_session_collector/internal/source"
)

// MaxSources is the maximum number of IDs in a query or token policy.
const MaxSources = 2048

// SourceID calculates the canonical ID of an explicitly configured source.
func SourceID(sender, user, provider string) string { return source.ID(sender, user, provider) }

// ValidID reports whether id has canonical source or artifact notation.
func ValidID(id string) bool { return ValidSourceID(id) || ValidArtifactID(id) }

func ValidSourceID(id string) bool {
	return strings.HasPrefix(id, "source-") && ValidSHA256(id[len("source-"):])
}

func ValidArtifactID(id string) bool {
	return strings.HasPrefix(id, "artifact-") && ValidSHA256(id[len("artifact-"):])
}

func ValidSHA256(id string) bool {
	if len(id) != sha256.Size*2 || id != strings.ToLower(id) {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// Ref identifies an artifact within its source, optionally pinning its content.
type Ref struct {
	SourceID   string `json:"source_id"`
	ArtifactID string `json:"artifact_id"`
	SHA256     string `json:"sha256,omitempty"`
}

// Session describes one transcript artifact. A native session can have multiple artifacts.
type Session struct {
	Ref
	Provider      string `json:"provider"`
	NativeID      string `json:"native_id,omitempty"`
	RootID        string `json:"root_id,omitempty"`
	AgentID       string `json:"agent_id,omitempty"`
	FirstSeenAt   string `json:"first_seen_at"`
	LastChangedAt string `json:"last_changed_at,omitempty"`
	SizeBytes     int64  `json:"size_bytes"`
}

// Query selects indexed transcript artifacts. NativeID is an exact match.
type Query struct {
	SourceIDs []string `json:"source_ids,omitempty"`
	NativeID  string   `json:"native_id,omitempty"`
	Cursor    string   `json:"cursor,omitempty"`
	Limit     int      `json:"limit,omitempty"`
}

type Page struct {
	Sessions   []Session `json:"sessions"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

type ReadQuery struct {
	Ref
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// Events contains complete native JSON records from one immutable generation.
type Events struct {
	Ref
	Records        []json.RawMessage `json:"records"`
	NextCursor     string            `json:"next_cursor,omitempty"`
	IncompleteTail bool              `json:"incomplete_tail,omitempty"`
}
