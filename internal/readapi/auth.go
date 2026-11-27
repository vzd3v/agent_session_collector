package readapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/vzd3v/agent_session_collector/sessionapi"
)

type token struct {
	SHA256        string    `json:"sha256"`
	ExpiresAt     time.Time `json:"expires_at"`
	ReadSources   []string  `json:"read_sources"`
	ExportSources []string  `json:"export_sources"`
}

func readTokens(name string) (map[[sha256.Size]byte]token, error) {
	const maxSize = 4 << 20
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxSize {
		return nil, errors.New("token configuration must be a bounded regular file")
	}
	var entries []token
	dec := json.NewDecoder(io.LimitReader(f, maxSize+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&entries); err != nil {
		return nil, fmt.Errorf("invalid token configuration: %w", err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid token configuration trailing data")
	}
	out := make(map[[sha256.Size]byte]token, len(entries))
	for _, entry := range entries {
		if !sessionapi.ValidSHA256(entry.SHA256) || entry.ExpiresAt.IsZero() || len(entry.ReadSources) > sessionapi.MaxSources || len(entry.ExportSources) > sessionapi.MaxSources {
			return nil, errors.New("invalid token configuration entry")
		}
		for _, ids := range [][]string{entry.ReadSources, entry.ExportSources} {
			for _, id := range ids {
				if !sessionapi.ValidSourceID(id) {
					return nil, errors.New("invalid token source ID")
				}
			}
		}
		var hash [sha256.Size]byte
		_, _ = hex.Decode(hash[:], []byte(entry.SHA256))
		if _, exists := out[hash]; exists {
			return nil, errors.New("duplicate configured token digest")
		}
		out[hash] = entry
	}
	return out, nil
}

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (token, bool) {
	header := r.Header.Values("Authorization")
	if len(header) != 1 {
		unauthorized(w)
		return token{}, false
	}
	scheme, value, ok := strings.Cut(header[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || len(value) < 32 || len(value) > 1024 || strings.ContainsAny(value, " \t\r\n,") {
		unauthorized(w)
		return token{}, false
	}
	tokens, err := readTokens(s.tokensFile)
	if err != nil {
		s.log.Error("load read API token configuration", "error", err)
		http.Error(w, "authorization unavailable", http.StatusServiceUnavailable)
		return token{}, false
	}
	entry, ok := tokens[sha256.Sum256([]byte(value))]
	if !ok || !entry.ExpiresAt.After(time.Now()) {
		unauthorized(w)
		return token{}, false
	}
	return entry, true
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(w, "authentication required", http.StatusUnauthorized)
}

func intersect(allowed, requested []string) []string {
	if requested == nil {
		out := slices.Clone(allowed)
		slices.Sort(out)
		return slices.Compact(out)
	}
	out := make([]string, 0, len(requested))
	for _, id := range requested {
		if slices.Contains(allowed, id) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
