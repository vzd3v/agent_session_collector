package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSenderExample(t *testing.T) {
	cfg, err := LoadSenderFile(filepath.Join("..", "..", "examples", "sender.example.toml"))
	if err != nil {
		t.Fatalf("load sender example: %v", err)
	}
	if cfg.SenderID != "sender-alpha" {
		t.Fatalf("unexpected sender_id %q", cfg.SenderID)
	}
	if got := cfg.Sync.Interval.String(); got != "10m0s" {
		t.Fatalf("unexpected interval %q", got)
	}
}

func TestLoadHubExample(t *testing.T) {
	cfg, err := LoadHubFile(filepath.Join("..", "..", "examples", "hub.example.toml"))
	if err != nil {
		t.Fatalf("load hub example: %v", err)
	}
	if cfg.HubID != "collector-hub-alpha" {
		t.Fatalf("unexpected hub_id %q", cfg.HubID)
	}
	if cfg.Receive.MaxFileSize.Bytes != 2*1024*1024*1024 {
		t.Fatalf("unexpected max file size %d", cfg.Receive.MaxFileSize.Bytes)
	}
	if cfg.Retention.ArchiveRetentionDays.Duration != 0 {
		t.Fatalf("unexpected archive retention %s", cfg.Retention.ArchiveRetentionDays.Duration)
	}
}

func TestSenderValidationRequiresDataClass(t *testing.T) {
	cfg := SenderConfig{
		SenderID: "sender-alpha",
		Hub:      HubConfig{SSHTarget: "upload_alpha@collector.example.net"},
		Sync: SyncConfig{
			Interval:    Duration{Duration: 10},
			StatePath:   "/srv/apps/agent_session_collector/state/sender.sqlite",
			RsyncPath:   "/usr/bin/rsync",
			MaxAttempts: 1,
			Backoff:     Duration{Duration: 1},
		},
		Sources: []SourceConfig{{
			Agent:    "claude",
			User:     "operator",
			Home:     "/home/operator",
			HostName: "sender-alpha.example.net",
			Paths:    []string{".claude/projects"},
		}},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "data_class") {
		t.Fatalf("expected data_class validation error, got %v", err)
	}
}

func TestSenderValidationRejectsAbsoluteSourcePath(t *testing.T) {
	cfg := SenderConfig{
		SenderID: "sender-alpha",
		Hub:      HubConfig{SSHTarget: "upload_alpha@collector.example.net"},
		Sync: SyncConfig{
			Interval:    Duration{Duration: 10},
			StatePath:   "/srv/apps/agent_session_collector/state/sender.sqlite",
			RsyncPath:   "/usr/bin/rsync",
			MaxAttempts: 1,
			Backoff:     Duration{Duration: 1},
		},
		Sources: []SourceConfig{{
			Agent:     "claude",
			User:      "operator",
			Home:      "/home/operator",
			HostName:  "sender-alpha.example.net",
			DataClass: "internal_agent_transcript",
			Paths:     []string{"/home/operator/.claude/projects"},
		}},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "path must be relative") {
		t.Fatalf("expected absolute path validation error, got %v", err)
	}
}

func TestSenderValidationRejectsUnsafeDataClass(t *testing.T) {
	cfg := validSenderConfig()
	cfg.Sources[0].DataClass = "bad/class"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "data_class must be a safe name") {
		t.Fatalf("expected unsafe data_class error, got %v", err)
	}
}

func validSenderConfig() SenderConfig {
	return SenderConfig{
		SenderID: "sender-alpha",
		Hub:      HubConfig{SSHTarget: "upload_alpha@collector.example.net"},
		Sync: SyncConfig{
			Interval:    Duration{Duration: 10},
			StatePath:   "/srv/apps/agent_session_collector/state/sender.sqlite",
			RsyncPath:   "/usr/bin/rsync",
			MaxAttempts: 1,
			Backoff:     Duration{Duration: 1},
		},
		Sources: []SourceConfig{{
			Agent:     "claude",
			User:      "operator",
			Home:      "/home/operator",
			HostName:  "sender-alpha.example.net",
			DataClass: "internal_agent_transcript",
			Paths:     []string{".claude/projects"},
		}},
	}
}

func TestHubValidationRejectsDuplicateSender(t *testing.T) {
	cfg := HubAppConfig{
		HubID: "collector-hub-alpha",
		Paths: HubPaths{
			Root:      "/srv/apps/agent_session_collector",
			Mirror:    "/srv/apps/agent_session_collector/data/mirror",
			Archive:   "/srv/apps/agent_session_collector/data/archive",
			Catalog:   "/srv/apps/agent_session_collector/data/catalog/sessions.sqlite",
			Extracted: "/srv/apps/agent_session_collector/data/extracted",
		},
		Receive: ReceiveConfig{
			UnixNameMaxLength: 32,
			MaxFileSize:       ByteSize{Bytes: 2 * 1024 * 1024 * 1024},
			Sender: []ReceiveSender{
				{SenderID: "sender-alpha", SenderSlug: "sender-alpha", ReceiveUser: "upload_alpha", MirrorGroup: "mirror_alpha", Mirror: "sender-alpha"},
				{SenderID: "sender-alpha", SenderSlug: "sender-alpha-2", ReceiveUser: "upload_beta", MirrorGroup: "mirror_beta", Mirror: "sender-alpha-2"},
			},
		},
		DataClasses: map[string]DataClass{
			"internal_agent_transcript": {RawHubAllowed: []string{"collector-hub-alpha"}},
		},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "duplicate sender_id") {
		t.Fatalf("expected duplicate sender error, got %v", err)
	}
}

func TestHubValidationRejectsDuplicateReceiveUser(t *testing.T) {
	cfg := HubAppConfig{
		HubID: "collector-hub-alpha",
		Paths: HubPaths{
			Root:      "/srv/apps/agent_session_collector",
			Mirror:    "/srv/apps/agent_session_collector/data/mirror",
			Archive:   "/srv/apps/agent_session_collector/data/archive",
			Catalog:   "/srv/apps/agent_session_collector/data/catalog/sessions.sqlite",
			Extracted: "/srv/apps/agent_session_collector/data/extracted",
		},
		Receive: ReceiveConfig{
			UnixNameMaxLength: 32,
			MaxFileSize:       ByteSize{Bytes: 2 * 1024 * 1024 * 1024},
			Sender: []ReceiveSender{
				{SenderID: "sender-alpha", SenderSlug: "sender-alpha", ReceiveUser: "upload_alpha", MirrorGroup: "mirror_alpha", Mirror: "sender-alpha"},
				{SenderID: "sender-beta", SenderSlug: "sender-beta", ReceiveUser: "upload_alpha", MirrorGroup: "mirror_beta", Mirror: "sender-beta"},
			},
		},
		DataClasses: map[string]DataClass{
			"internal_agent_transcript": {RawHubAllowed: []string{"collector-hub-alpha"}},
		},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "duplicate receive_user") {
		t.Fatalf("expected duplicate receive_user error, got %v", err)
	}
}

func TestHubValidationRejectsUnsafeReceiveIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*HubAppConfig)
		want   string
	}{
		{
			name: "receive user",
			mutate: func(cfg *HubAppConfig) {
				cfg.Receive.Sender[0].ReceiveUser = "bad/user"
			},
			want: "receive_user must be a safe unix name",
		},
		{
			name: "mirror group",
			mutate: func(cfg *HubAppConfig) {
				cfg.Receive.Sender[0].MirrorGroup = "bad group"
			},
			want: "mirror_group must be a safe unix name",
		},
		{
			name: "sender slug",
			mutate: func(cfg *HubAppConfig) {
				cfg.Receive.Sender[0].SenderSlug = "bad/slug"
			},
			want: "sender_slug must be a safe slug",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validHubConfig()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestHubValidationRejectsMissingMaxFileSize(t *testing.T) {
	cfg := validHubConfig()
	cfg.Receive.MaxFileSize = ByteSize{}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "receive.max_file_size must be positive") {
		t.Fatalf("expected max_file_size error, got %v", err)
	}
}

func TestByteSizeRejectsOverflow(t *testing.T) {
	var size ByteSize
	err := size.UnmarshalText([]byte("0"))
	if err != nil {
		t.Fatalf("zero byte size should parse: %v", err)
	}
	err = size.UnmarshalText([]byte("9223372036854775807GiB"))
	if err == nil || !strings.Contains(err.Error(), "overflows") {
		t.Fatalf("expected overflow error, got %v", err)
	}
	err = size.UnmarshalText([]byte("9223372036854775808"))
	if err == nil {
		t.Fatalf("expected parse overflow error")
	}
}

func TestDurationRejectsNegative(t *testing.T) {
	var duration Duration
	err := duration.UnmarshalText([]byte("-1s"))
	if err == nil || !strings.Contains(err.Error(), "non-negative") {
		t.Fatalf("expected non-negative duration error, got %v", err)
	}
}

func TestHubValidationRejectsDisallowedDataClass(t *testing.T) {
	cfg := validHubConfig()
	cfg.DataClasses["internal_agent_transcript"] = DataClass{RawHubAllowed: []string{"other-hub"}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "must explicitly allow") {
		t.Fatalf("expected hub allowlist error, got %v", err)
	}
}

func TestHubValidationRejectsRedactionRequiredDataClass(t *testing.T) {
	cfg := validHubConfig()
	cfg.DataClasses["internal_agent_transcript"] = DataClass{
		RawHubAllowed:     []string{"collector-hub-alpha"},
		RedactionRequired: true,
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "redaction is not implemented") {
		t.Fatalf("expected redaction unsupported error, got %v", err)
	}
}

func TestHubValidationRejectsInvalidArchiveRanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*HubAppConfig)
		want   string
	}{
		{
			name: "zstd low",
			mutate: func(cfg *HubAppConfig) {
				cfg.Archive.ZstdLevel = 0
			},
			want: "zstd_level",
		},
		{
			name: "zstd high",
			mutate: func(cfg *HubAppConfig) {
				cfg.Archive.ZstdLevel = 20
			},
			want: "zstd_level",
		},
		{
			name: "negative versions",
			mutate: func(cfg *HubAppConfig) {
				cfg.Archive.VersionRetention = -1
			},
			want: "non-negative",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validHubConfig()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestHubValidationRejectsRelativeRoot(t *testing.T) {
	cfg := validHubConfig()
	cfg.Paths.Root = "relative/root"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "paths.root must be absolute") {
		t.Fatalf("expected absolute root error, got %v", err)
	}
}

func TestHubValidationRejectsPathOutsideRoot(t *testing.T) {
	cfg := validHubConfig()
	cfg.Paths.Archive = "/tmp/archive"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "paths.archive must be under paths.root") {
		t.Fatalf("expected under-root error, got %v", err)
	}
}

func TestHubValidationRequiresSenderSlug(t *testing.T) {
	cfg := validHubConfig()
	cfg.Receive.Sender[0].SenderSlug = ""
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "empty identity") {
		t.Fatalf("expected missing sender_slug error, got %v", err)
	}
}

func validHubConfig() HubAppConfig {
	return HubAppConfig{
		HubID: "collector-hub-alpha",
		Paths: HubPaths{
			Root:      "/srv/apps/agent_session_collector",
			Mirror:    "/srv/apps/agent_session_collector/data/mirror",
			Archive:   "/srv/apps/agent_session_collector/data/archive",
			Catalog:   "/srv/apps/agent_session_collector/data/catalog/sessions.sqlite",
			Extracted: "/srv/apps/agent_session_collector/data/extracted",
		},
		Receive: ReceiveConfig{
			UnixNameMaxLength: 32,
			MaxFileSize:       ByteSize{Bytes: 2 * 1024 * 1024 * 1024},
			Sender: []ReceiveSender{
				{SenderID: "sender-alpha", SenderSlug: "sender-alpha", ReceiveUser: "upload_alpha", MirrorGroup: "mirror_alpha", Mirror: "sender-alpha"},
			},
		},
		DataClasses: map[string]DataClass{
			"internal_agent_transcript": {RawHubAllowed: []string{"collector-hub-alpha"}},
		},
		Index:   IndexConfig{Interval: Duration{Duration: 10}},
		Archive: ArchiveConfig{Enabled: true, Compression: "zstd", Policy: "replace", ZstdLevel: 6},
	}
}
