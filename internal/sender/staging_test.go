package sender

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vzd3v/agent_session_collector/internal/config"
)

func TestRsyncUploaderBoundsBatchesAndAllowsLargeSingleArtifact(t *testing.T) {
	tmp := t.TempDir()
	log := filepath.Join(tmp, "batches")
	t.Setenv("ASC_STAGE_LOG", log)
	fake := filepath.Join(tmp, "rsync")
	script := `#!/bin/sh
for a; do case "$a" in --files-from=*) list=${a#--files-from=};; esac; done
stage=${list%/*}
find "$stage" -type f ! -name .asc-files-from -printf '%s\n' | awk '{s+=$1} END{print s}' >> "$ASC_STAGE_LOG"
`
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	var files []File
	for i, n := range []int{4, 4, 4, 20, 3} {
		files = append(files, File{RemotePath: string(rune('a' + i)), InlineContent: []byte(strings.Repeat("x", n)), SizeBytes: int64(n)})
	}
	u := RsyncUploader{Config: config.SenderConfig{Hub: config.HubConfig{SSHTarget: "synthetic"}, Sync: config.SyncConfig{RsyncPath: fake, MaxStagingBytes: config.ByteSize{Bytes: 10}}}}
	result, err := u.Upload(context.Background(), files)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Uploaded) != 5 {
		t.Fatalf("uploaded=%d", len(result.Uploaded))
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "8\n4\n20\n3\n" {
		t.Fatalf("batch sizes=%q", b)
	}
}
