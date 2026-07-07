package receive

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vzd3v/agent_session_collector/internal/config"
	"github.com/vzd3v/agent_session_collector/internal/rsyncprofile"
)

func TestPlanAcceptsCommandGeneratedBySenderProfile(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skipf("rsync not installed: %v", err)
	}
	_ = planFromCapturedSenderCommand(t, "sender-alpha", "")
}

func TestPlanAcceptsBandwidthLimitGeneratedBySenderProfile(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skipf("rsync not installed: %v", err)
	}
	plan := planFromCapturedSenderCommand(t, "sender-alpha", "128")
	if !contains(plan.Args, "--bwlimit=128") {
		t.Fatalf("planned args missing bwlimit: %#v", plan.Args)
	}
}

func planFromCapturedSenderCommand(t *testing.T, senderID string, bwlimit string) Plan {
	t.Helper()
	tmp := t.TempDir()
	source := filepath.Join(tmp, "source")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "a.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	filesFrom := filepath.Join(tmp, "files-from")
	if err := os.WriteFile(filesFrom, []byte("a.jsonl\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(tmp, "ssh-capture")
	captured := filepath.Join(tmp, "captured")
	script := "#!/bin/sh\nshift\nprintf '%s\\n' \"$*\" > " + shellQuote(captured) + "\nexit 1\n"
	if err := os.WriteFile(capture, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	args := rsyncprofile.SenderArgs(rsyncprofile.Profile{
		SSHTarget:      "upload_alpha@example.invalid",
		RemoteCommand:  "asc-receive -config /etc/agent_session_collector/hub.toml -sender " + senderID,
		BandwidthLimit: bwlimit,
	}, source)
	args = append([]string{"--from0", "--files-from=" + filesFrom}, args...)
	cmd := exec.Command("rsync", args...)
	cmd.Env = append(os.Environ(), "RSYNC_RSH="+capture)
	_ = cmd.Run()
	originalCommandBytes, err := os.ReadFile(captured)
	if err != nil {
		t.Fatalf("read captured rsync command: %v", err)
	}

	plan, err := PlanRsync(testHubConfig(tmp), Request{
		SenderID:        senderID,
		OriginalCommand: strings.TrimSpace(string(originalCommandBytes)),
	})
	if err != nil {
		t.Fatalf("PlanRsync rejected sender-generated command: %v", err)
	}
	if plan.MirrorPath != filepath.Join(tmp, "mirror", "sender-alpha") {
		t.Fatalf("mirror path = %q", plan.MirrorPath)
	}
	if !contains(plan.Args, "--max-size=2048") {
		t.Fatalf("planned args missing max-size: %#v", plan.Args)
	}
	if got := plan.Args[len(plan.Args)-1]; got != filepath.Join(tmp, "mirror", "sender-alpha") {
		t.Fatalf("planned destination = %q", got)
	}
	return plan
}

func TestPlanAcceptsDirectServerArgs(t *testing.T) {
	cfg := testHubConfig(t.TempDir())
	plan, err := PlanRsync(cfg, Request{
		SenderID:   "sender-alpha",
		DirectArgs: []string{"--server", "-trRe.iLsfxCIvu", "--partial", "--safe-links", ".", "/"},
	})
	if err != nil {
		t.Fatalf("PlanRsync returned error: %v", err)
	}
	if plan.Sender.SenderID != "sender-alpha" {
		t.Fatalf("sender = %#v", plan.Sender)
	}
}

func TestPlanRejectsUnsafeServerCommands(t *testing.T) {
	cfg := testHubConfig(t.TempDir())
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "delete",
			args: []string{"--server", "-trRe.iLsfxCIvu", "--delete", "--partial", "--safe-links", ".", "/"},
			want: "not allowed",
		},
		{
			name: "bad bwlimit",
			args: []string{"--server", "-trRe.iLsfxCIvu", "--bwlimit=abc", "--partial", "--safe-links", ".", "/"},
			want: "positive integer",
		},
		{
			name: "symlink flag",
			args: []string{"--server", "-trlRe.iLsfxCIvu", "--partial", "--safe-links", ".", "/"},
			want: "not allowed",
		},
		{
			name: "destination traversal",
			args: []string{"--server", "-trRe.iLsfxCIvu", "--partial", "--safe-links", ".", "../../bad"},
			want: "destination must be /",
		},
		{
			name: "missing safe links",
			args: []string{"--server", "-trRe.iLsfxCIvu", "--partial", ".", "/"},
			want: "--safe-links is required",
		},
		{
			name: "missing required short profile",
			args: []string{"--server", "-e.iLsfxCIvu", "--partial", "--safe-links", ".", "/"},
			want: "recursive, times",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := PlanRsync(cfg, Request{SenderID: "sender-alpha", DirectArgs: tt.args})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestPlanRejectsUnknownSender(t *testing.T) {
	_, err := PlanRsync(testHubConfig(t.TempDir()), Request{
		SenderID:   "sender-beta",
		DirectArgs: []string{"--server", "-trRe.iLsfxCIvu", "--partial", "--safe-links", ".", "/"},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown sender_id") {
		t.Fatalf("error = %v", err)
	}
}

func TestPlanKeepsMirrorUnderConfiguredRoot(t *testing.T) {
	cfg := testHubConfig(t.TempDir())
	cfg.Receive.Sender[0].Mirror = "../escape"
	_, err := PlanRsync(cfg, Request{
		SenderID:   "sender-alpha",
		DirectArgs: []string{"--server", "-trRe.iLsfxCIvu", "--partial", "--safe-links", ".", "/"},
	})
	if err == nil || !strings.Contains(err.Error(), "escapes paths.mirror") {
		t.Fatalf("error = %v", err)
	}
}

func TestVerifySenderIdentity(t *testing.T) {
	sender := config.ReceiveSender{
		SenderID:    "sender-alpha",
		ReceiveUser: "upload_alpha",
		MirrorGroup: "mirror_alpha",
	}
	err := verifySenderIdentity(sender, processIdentity{
		username: "upload_alpha",
		groupIDs: map[string]struct{}{"2001": {}},
	}, "2001")
	if err != nil {
		t.Fatalf("verifySenderIdentity returned error: %v", err)
	}
}

func TestVerifySenderIdentityRejectsWrongUserOrGroup(t *testing.T) {
	sender := config.ReceiveSender{
		SenderID:    "sender-alpha",
		ReceiveUser: "upload_alpha",
		MirrorGroup: "mirror_alpha",
	}
	tests := []struct {
		name     string
		identity processIdentity
		want     string
	}{
		{
			name:     "wrong user",
			identity: processIdentity{username: "upload_beta", groupIDs: map[string]struct{}{"2001": {}}},
			want:     "does not match receive_user",
		},
		{
			name:     "missing group",
			identity: processIdentity{username: "upload_alpha", groupIDs: map[string]struct{}{"2002": {}}},
			want:     "not a member",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := verifySenderIdentity(sender, tt.identity, "2001")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func testHubConfig(root string) config.HubAppConfig {
	return config.HubAppConfig{
		HubID: "collector-hub-alpha",
		Paths: config.HubPaths{
			Root:      root,
			Mirror:    filepath.Join(root, "mirror"),
			Archive:   filepath.Join(root, "archive"),
			Catalog:   filepath.Join(root, "catalog", "sessions.sqlite"),
			Extracted: filepath.Join(root, "extracted"),
		},
		Receive: config.ReceiveConfig{
			MaxFileSize:       config.ByteSize{Bytes: 2048},
			UnixNameMaxLength: 32,
			Sender: []config.ReceiveSender{
				{SenderID: "sender-alpha", SenderSlug: "sender-alpha", ReceiveUser: "upload_alpha", MirrorGroup: "mirror_alpha", Mirror: "sender-alpha"},
			},
		},
		DataClasses: map[string]config.DataClass{
			"internal_agent_transcript": {RawHubAllowed: []string{"collector-hub-alpha"}},
		},
		Index: config.IndexConfig{
			Interval: config.Duration{Duration: 1},
		},
		Archive: config.ArchiveConfig{
			Compression: "zstd",
			Policy:      "replace",
			ZstdLevel:   6,
		},
	}
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func shellQuote(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'"
}
