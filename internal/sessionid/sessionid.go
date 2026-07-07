package sessionid

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/vzd3v/agent_session_collector/internal/constants"
)

var (
	ErrUnsupportedAgent = errors.New("unsupported agent")
	ErrSessionIDMissing = errors.New("session id missing")

	codexRolloutRE = regexp.MustCompile(`(?i)^rollout-.+-([0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12})\.jsonl$`)
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
