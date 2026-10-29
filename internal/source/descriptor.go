// Package source defines the portable, versioned source metadata contract.
package source

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/vzd3v/agent_session_collector/internal/pathnorm"
)

const Version = 1
const MaxDescriptorBytes = 32 * 1024
const MaxMetadataFields = 32
const MaxMetadataKeyBytes = 64
const MaxMetadataValueBytes = 1024
const MaxMetadataBytes = 16 * 1024

type Descriptor struct {
	Provenance string            `json:"-"`
	Version    int               `json:"version"`
	Sender     string            `json:"sender"`
	User       string            `json:"user"`
	Agent      string            `json:"agent"`
	HostName   string            `json:"host_name"`
	Env        string            `json:"env,omitempty"`
	DataClass  string            `json:"data_class"`
	Metadata   map[string]string `json:"metadata"`
}

func ID(sender, user, agent string) string { return digest("source", sender, user, agent) }
func ArtifactID(sourceID, relativePath string) string {
	return digest("artifact", sourceID, relativePath)
}
func digest(kind string, parts ...string) string {
	b, _ := json.Marshal(parts)
	sum := sha256.Sum256(b)
	return kind + "-" + hex.EncodeToString(sum[:])
}
func Digest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func DescriptorPath(user, agent string) string {
	return path.Join(".asc", "sources", user, agent+".json")
}

func ValidateSegment(value string) error {
	clean, err := pathnorm.Relative(value)
	if err != nil || clean != value || strings.Contains(value, "/") || value == ".asc" {
		return fmt.Errorf("unsafe identity segment %q", value)
	}
	return nil
}
func ValidateMetadata(fields map[string]string) error {
	if len(fields) > MaxMetadataFields {
		return fmt.Errorf("metadata exceeds %d fields", MaxMetadataFields)
	}
	for key, value := range fields {
		if key == "" || len(key) > MaxMetadataKeyBytes || len(value) > MaxMetadataValueBytes || !utf8.ValidString(key) || !utf8.ValidString(value) {
			return fmt.Errorf("invalid metadata key/value length or UTF-8")
		}
		if strings.ContainsAny(key, "\x00\r\n") || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("metadata contains prohibited control characters")
		}
	}
	b, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	if len(b) > MaxMetadataBytes {
		return fmt.Errorf("metadata exceeds %d encoded bytes", MaxMetadataBytes)
	}
	return nil
}
func (d Descriptor) Validate() error {
	if d.Version != Version {
		return fmt.Errorf("unsupported source descriptor version %d", d.Version)
	}
	if d.Sender == "" || len(d.Sender) > 256 || d.HostName == "" || len(d.HostName) > 256 || len(d.Env) > 1024 || d.DataClass == "" || len(d.DataClass) > 128 {
		return fmt.Errorf("invalid source metadata fields")
	}
	for _, value := range []string{d.Sender, d.User, d.HostName, d.Env, d.DataClass} {
		if !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("invalid source field encoding or control characters")
		}
	}
	if err := ValidateSegment(d.User); err != nil {
		return err
	}
	if d.Agent != "claude" && d.Agent != "codex" {
		return fmt.Errorf("unsupported source agent %q", d.Agent)
	}
	return ValidateMetadata(d.Metadata)
}
func (d Descriptor) Encode() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	if d.Metadata == nil {
		d.Metadata = map[string]string{}
	}
	b, err := json.Marshal(d)
	return b, err
}
func Decode(r io.Reader) (Descriptor, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxDescriptorBytes+1))
	if err != nil {
		return Descriptor{}, err
	}
	if len(b) > MaxDescriptorBytes {
		return Descriptor{}, fmt.Errorf("source descriptor too large")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var d Descriptor
	if err := dec.Decode(&d); err != nil {
		return d, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return d, fmt.Errorf("source descriptor trailing data")
	}
	return d, d.Validate()
}
