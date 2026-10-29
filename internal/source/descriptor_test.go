package source

import (
	"bytes"
	"strings"
	"testing"
)

func TestDescriptorBoundedVersionedRoundTrip(t *testing.T) {
	d := Descriptor{Version: 1, Sender: "sender-alpha", User: "alice", Agent: "codex", HostName: "sender.example.net", DataClass: "agent_transcript", Metadata: map[string]string{"department": "engineering", "owner": "synthetic"}}
	a, err := d.Encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(bytes.NewReader(a))
	if err != nil {
		t.Fatal(err)
	}
	b, err := decoded.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("descriptor is not deterministic")
	}
	for _, body := range []string{`{"version":999}`, string(a) + `{}`, strings.Repeat(" ", MaxDescriptorBytes+1)} {
		if _, err := Decode(strings.NewReader(body)); err == nil {
			t.Fatal("invalid descriptor accepted")
		}
	}
	d.Metadata["oversized"] = strings.Repeat("x", MaxMetadataValueBytes+1)
	if _, err := d.Encode(); err == nil {
		t.Fatal("unbounded label accepted")
	}
	if ID("a", "bc", "codex") == ID("ab", "c", "codex") {
		t.Fatal("identity tuple is ambiguous")
	}
	if ArtifactID(ID("a", "b", "codex"), "sessions/a.jsonl") == ArtifactID(ID("a", "b", "codex"), "archived_sessions/a.jsonl") {
		t.Fatal("paths collapsed")
	}
}
