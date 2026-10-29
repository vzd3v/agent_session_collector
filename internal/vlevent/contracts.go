package vlevent

import "time"

const IndexSchemaVersion = 2

type AgentSessionIndex struct {
	SchemaVersion   int       `json:"event.schema_version"`
	ArtifactID      string    `json:"artifact.id"`
	SourceID        string    `json:"source.id"`
	ArtifactRole    string    `json:"artifact.role"`
	AgentID         string    `json:"agent.id,omitempty"`
	RootSessionID   string    `json:"session.root_id,omitempty"`
	ParentSessionID string    `json:"session.parent_id,omitempty"`
	ParentAgentID   string    `json:"agent.parent_id,omitempty"`
	ForkedFromID    string    `json:"session.forked_from_id,omitempty"`
	EventName       string    `json:"event.name"`
	ServiceName     string    `json:"service.name"`
	Agent           string    `json:"agent"`
	SenderID        string    `json:"sender.id"`
	HostName        string    `json:"host.name,omitempty"`
	OSUser          string    `json:"os.user,omitempty"`
	Env             string    `json:"env,omitempty"`
	SessionID       string    `json:"session.id,omitempty"`
	ConversationID  string    `json:"conversation.id,omitempty"`
	FileSizeBytes   int64     `json:"file.size_bytes"`
	FileMtime       time.Time `json:"file.mtime"`
	ArchiveStatus   string    `json:"archive.status"`
	SyncStatus      string    `json:"sync.status"`
	CollectorRunID  string    `json:"collector.run_id"`
	FileChanged     bool      `json:"file.changed"`
	FileChangedAt   string    `json:"file.changed_at,omitempty"`
	IndexObservedAt time.Time `json:"index.observed_at"`
}
