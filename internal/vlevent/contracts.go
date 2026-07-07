package vlevent

import "time"

type AgentSessionIndex struct {
	EventName      string    `json:"event.name"`
	ServiceName    string    `json:"service.name"`
	Agent          string    `json:"agent"`
	HostName       string    `json:"host.name,omitempty"`
	OSUser         string    `json:"os.user,omitempty"`
	Env            string    `json:"env,omitempty"`
	SessionID      string    `json:"session.id"`
	ConversationID string    `json:"conversation.id,omitempty"`
	FileSizeBytes  int64     `json:"file.size_bytes"`
	FileMtime      time.Time `json:"file.mtime"`
	ArchiveStatus  string    `json:"archive.status"`
	SyncStatus     string    `json:"sync.status"`
	CollectorRunID string    `json:"collector.run_id"`
}
