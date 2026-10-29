package sessionid

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/vzd3v/agent_session_collector/internal/constants"
)

// ForArtifact keeps raw collection independent of vendor filename changes.
// An opaque ID identifies an artifact, never a vendor conversation.
func ForArtifact(agent, path string) (string, error) {
	id, err := FromPath(agent, path)
	if err == nil || !errors.Is(err, ErrSessionIDMissing) {
		return id, err
	}
	base := filepath.Base(path)
	if !strings.HasSuffix(base, ".jsonl") || base == ".jsonl" {
		return "", ErrSessionIDMissing
	}
	sum := sha256.Sum256([]byte(base))
	return "artifact-" + hex.EncodeToString(sum[:]), nil
}

var (
	ErrUnsupportedAgent = errors.New("unsupported agent")
	ErrSessionIDMissing = errors.New("session id missing")

	codexRolloutRE = regexp.MustCompile(`(?i)^rollout-.+-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$`)
)

func FromPath(agent, path string) (string, error) {
	base := filepath.Base(path)
	switch agent {
	case constants.AgentClaude:
		if !strings.HasSuffix(base, ".jsonl") {
			return "", ErrSessionIDMissing
		}
		id := strings.TrimSuffix(base, ".jsonl")
		if id == "" {
			return "", ErrSessionIDMissing
		}
		return id, nil
	case constants.AgentCodex:
		match := codexRolloutRE.FindStringSubmatch(base)
		if len(match) != 2 {
			return "", ErrSessionIDMissing
		}
		return strings.ToLower(match[1]), nil
	default:
		return "", ErrUnsupportedAgent
	}
}
