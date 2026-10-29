// Package artifact defines raw file roles and input encodings independently of vendor IDs.
package artifact

import (
	"path"
	"strings"
)

const (
	Transcript       = "transcript"
	SubagentMetadata = "subagent_metadata"
	SessionNameIndex = "session_name_index"
	ToolResult       = "tool_result"
	SourceDescriptor = "source_descriptor"
	Plain            = "plain"
	Zstd             = "zstd"
)

func Encoding(name string) string {
	if strings.HasSuffix(name, ".jsonl.zst") {
		return Zstd
	}
	return Plain
}
func Role(agent, rel string) string {
	base := path.Base(rel)
	if strings.Contains("/"+rel, "/tool-results/") {
		return ToolResult
	}
	if agent == "codex" && strings.TrimSuffix(base, ".zst") == "session_index.jsonl" {
		return SessionNameIndex
	}
	if agent == "claude" && strings.Contains("/"+rel, "/subagents/") && strings.HasPrefix(base, "agent-") && strings.HasSuffix(base, ".meta.json") {
		return SubagentMetadata
	}
	if strings.HasSuffix(base, ".jsonl") || strings.HasSuffix(base, ".jsonl.zst") {
		return Transcript
	}
	return ""
}
