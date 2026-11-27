package readapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

var errCursor = errors.New("invalid cursor")

type eventPosition struct {
	SHA256 string `json:"sha256"`
	Offset int64  `json:"offset"`
}

func (s *Server) signCursor(scope any, position any) string {
	content, _ := json.Marshal(position)
	encoder := base64.RawURLEncoding
	return encoder.EncodeToString(content) + "." + encoder.EncodeToString(s.cursorMAC(scope, content))
}

func (s *Server) parseCursor(value string, scope, position any) error {
	if value == "" {
		return nil
	}
	if len(value) > 1024 {
		return errCursor
	}
	part, signed, ok := strings.Cut(value, ".")
	if !ok {
		return errCursor
	}
	content, err := base64.RawURLEncoding.DecodeString(part)
	signature, sigErr := base64.RawURLEncoding.DecodeString(signed)
	if err != nil || sigErr != nil || !hmac.Equal(signature, s.cursorMAC(scope, content)) {
		return errCursor
	}
	if err := json.Unmarshal(content, position); err != nil {
		return errCursor
	}
	return nil
}

func (s *Server) cursorMAC(scope any, content []byte) []byte {
	encodedScope, _ := json.Marshal(scope)
	mac := hmac.New(sha256.New, s.cursorKey[:])
	_, _ = mac.Write(encodedScope)
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(content)
	return mac.Sum(nil)
}
