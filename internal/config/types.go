package config

import (
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/vzd3v/agent_session_collector/internal/constants"
	"github.com/vzd3v/agent_session_collector/internal/pathnorm"
	sourcemetadata "github.com/vzd3v/agent_session_collector/internal/source"
)

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalText(text []byte) error {
	value := strings.TrimSpace(string(text))
	if value == "" {
		return fmt.Errorf("duration is empty")
	}
	if value == "0" {
		d.Duration = 0
		return nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", value, err)
	}
	if parsed < 0 {
		return fmt.Errorf("duration must be non-negative")
	}
	d.Duration = parsed
	return nil
}

type ByteSize struct {
	Bytes int64
}

var unixNameRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

func (s *ByteSize) UnmarshalText(text []byte) error {
	value := strings.TrimSpace(string(text))
	if value == "" {
		return fmt.Errorf("byte size is empty")
	}
	multiplier := int64(1)
	for _, unit := range []struct {
		suffix     string
		multiplier int64
	}{
		{suffix: "GiB", multiplier: 1024 * 1024 * 1024},
		{suffix: "MiB", multiplier: 1024 * 1024},
		{suffix: "KiB", multiplier: 1024},
		{suffix: "B", multiplier: 1},
	} {
		suffix, m := unit.suffix, unit.multiplier
		if strings.HasSuffix(value, suffix) {
			multiplier = m
			value = strings.TrimSpace(strings.TrimSuffix(value, suffix))
			break
		}
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fmt.Errorf("parse byte size: %w", err)
	}
	if n < 0 {
		return fmt.Errorf("byte size must be non-negative")
	}
	if n > math.MaxInt64/multiplier {
		return fmt.Errorf("byte size overflows int64")
	}
	s.Bytes = n * multiplier
	return nil
}

type SenderConfig struct {
	SenderID string         `toml:"sender_id"`
	Hub      HubConfig      `toml:"hub"`
	Sync     SyncConfig     `toml:"sync"`
	Sources  []SourceConfig `toml:"sources"`
	Exclude  ExcludeConfig  `toml:"exclude"`
	Logging  LoggingConfig  `toml:"logging"`
}

type HubConfig struct {
	SSHTarget     string `toml:"ssh_target"`
	RemoteCommand string `toml:"remote_command"`
}

type SyncConfig struct {
	MaxStagingBytes ByteSize `toml:"max_staging_bytes"`
	Interval        Duration `toml:"interval"`
	StatePath       string   `toml:"state_path"`
	RsyncPath       string   `toml:"rsync_path"`
	BandwidthLimit  string   `toml:"bandwidth_limit"`
	MaxAttempts     int      `toml:"max_attempts"`
	Backoff         Duration `toml:"backoff"`
}

type SourceConfig struct {
	Agent     string            `toml:"agent"`
	User      string            `toml:"user"`
	Home      string            `toml:"home"`
	HostName  string            `toml:"host_name"`
	Env       string            `toml:"env"`
	DataClass string            `toml:"data_class"`
	Paths     []string          `toml:"paths"`
	Metadata  map[string]string `toml:"metadata"`
	Capture   CaptureConfig     `toml:"capture"`
}

type CaptureConfig struct {
	SubagentMetadata *bool  `toml:"subagent_metadata"`
	SessionIndex     *bool  `toml:"session_index"`
	ToolResults      bool   `toml:"tool_results"`
	SessionIndexPath string `toml:"session_index_path"`
}

func EnabledByDefault(value *bool) bool { return value == nil || *value }

type ExcludeConfig struct {
	Patterns []string `toml:"patterns"`
}

type LoggingConfig struct {
	Format string `toml:"format"`
	Level  string `toml:"level"`
}

type HubAppConfig struct {
	HubID       string               `toml:"hub_id"`
	Paths       HubPaths             `toml:"paths"`
	Receive     ReceiveConfig        `toml:"receive"`
	DataClasses map[string]DataClass `toml:"data_classes"`
	Index       IndexConfig          `toml:"index"`
	Archive     ArchiveConfig        `toml:"archive"`
	Retention   RetentionConfig      `toml:"retention"`
	Logging     LoggingConfig        `toml:"logging"`
}

type HubPaths struct {
	Root      string `toml:"root"`
	Mirror    string `toml:"mirror"`
	Archive   string `toml:"archive"`
	Catalog   string `toml:"catalog"`
	Extracted string `toml:"extracted"`
}

type ReceiveConfig struct {
	AllowDelete       bool            `toml:"allow_delete"`
	MaxFileSize       ByteSize        `toml:"max_file_size"`
	UnixNameMaxLength int             `toml:"unix_name_max_length"`
	Sender            []ReceiveSender `toml:"sender"`
}

type ReceiveSender struct {
	SenderID    string `toml:"sender_id"`
	SenderSlug  string `toml:"sender_slug"`
	ReceiveUser string `toml:"receive_user"`
	MirrorGroup string `toml:"mirror_group"`
	Mirror      string `toml:"mirror"`
}

type DataClass struct {
	RawHubAllowed     []string `toml:"raw_hub_allowed"`
	RedactionRequired bool     `toml:"redaction_required"`
}

type IndexConfig struct {
	MaxDecodedFileSize   ByteSize `toml:"max_decoded_file_size"`
	Interval             Duration `toml:"interval"`
	EmitVLEvents         bool     `toml:"emit_vl_events"`
	VLEndpoint           string   `toml:"vl_endpoint"`
	EmitOnUnchangedEvery Duration `toml:"emit_on_unchanged_every"`
}

type ArchiveConfig struct {
	Enabled                 bool     `toml:"enabled"`
	QuietAfter              Duration `toml:"quiet_after"`
	Compression             string   `toml:"compression"`
	ZstdLevel               int      `toml:"zstd_level"`
	Policy                  string   `toml:"policy"`
	RetainPlainAfterArchive Duration `toml:"retain_plain_after_archive"`
	VersionRetention        int      `toml:"version_retention"`
	VersionRetentionDays    int      `toml:"version_retention_days"`
}

type RetentionConfig struct {
	ArchiveMaxAge            Duration `toml:"archive_max_age"`
	ArchiveMaxBytes          ByteSize `toml:"archive_max_bytes"`
	MinFreeBytes             ByteSize `toml:"min_free_bytes"`
	OrphanGrace              Duration `toml:"orphan_grace"`
	DeleteMirrorAfterArchive bool     `toml:"delete_mirror_after_archive"`
	DeleteUnknownAfter       Duration `toml:"delete_unknown_after"`
	ArchiveRetentionDays     Duration `toml:"archive_retention_days"`
}

// StagingLimit is a batching target; an individual larger artifact travels alone.
func (s SyncConfig) StagingLimit() int64 {
	if s.MaxStagingBytes.Bytes > 0 {
		return s.MaxStagingBytes.Bytes
	}
	return 512 * 1024 * 1024
}

func LoadSenderFile(path string) (SenderConfig, error) {
	var cfg SenderConfig
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return cfg, fmt.Errorf("decode sender config: %w", err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return cfg, fmt.Errorf("sender config has unknown keys: %v", undecoded)
	}
	return cfg, cfg.Validate()
}

func LoadHubFile(path string) (HubAppConfig, error) {
	var cfg HubAppConfig
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return cfg, fmt.Errorf("decode hub config: %w", err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return cfg, fmt.Errorf("hub config has unknown keys: %v", undecoded)
	}
	return cfg, cfg.Validate()
}

func (cfg SenderConfig) Validate() error {
	if cfg.Sync.MaxStagingBytes.Bytes < 0 {
		return fmt.Errorf("sync.max_staging_bytes must be non-negative")
	}
	if cfg.SenderID == "" {
		return fmt.Errorf("sender_id is required")
	}
	if cfg.Hub.SSHTarget == "" {
		return fmt.Errorf("hub.ssh_target is required")
	}
	if cfg.Sync.StatePath == "" {
		return fmt.Errorf("sync.state_path is required")
	}
	if cfg.Sync.RsyncPath == "" {
		return fmt.Errorf("sync.rsync_path is required")
	}
	if cfg.Sync.Interval.Duration <= 0 {
		return fmt.Errorf("sync.interval must be positive")
	}
	if cfg.Sync.MaxAttempts < 1 {
		return fmt.Errorf("sync.max_attempts must be at least 1")
	}
	if cfg.Sync.Backoff.Duration <= 0 {
		return fmt.Errorf("sync.backoff must be positive")
	}
	if len(cfg.Sources) == 0 {
		return fmt.Errorf("at least one source is required")
	}
	seenSources := map[string]bool{}
	for i, source := range cfg.Sources {
		key := source.User + "\x00" + source.Agent
		if seenSources[key] {
			return fmt.Errorf("ambiguous duplicate source for user %q agent %q", source.User, source.Agent)
		}
		seenSources[key] = true
		if err := source.Validate(); err != nil {
			return fmt.Errorf("sources[%d]: %w", i, err)
		}
	}
	return nil
}

func (source SourceConfig) Validate() error {
	if err := sourcemetadata.ValidateSegment(source.User); err != nil {
		return err
	}
	if err := sourcemetadata.ValidateMetadata(source.Metadata); err != nil {
		return err
	}
	switch source.Agent {
	case constants.AgentClaude, constants.AgentCodex:
	default:
		return fmt.Errorf("agent must be claude or codex")
	}
	for name, value := range map[string]string{
		"user":       source.User,
		"home":       source.Home,
		"host_name":  source.HostName,
		"data_class": source.DataClass,
	} {
		if value == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if !strings.HasPrefix(source.Home, string(os.PathSeparator)) {
		return fmt.Errorf("home must be absolute")
	}
	if !isSlug(source.DataClass) {
		return fmt.Errorf("data_class must be a safe name")
	}
	if len(source.Paths) == 0 {
		return fmt.Errorf("paths is required")
	}
	for i, sourcePath := range source.Paths {
		if _, err := pathnorm.Relative(sourcePath); err != nil {
			return fmt.Errorf("paths[%d]: %w", i, err)
		}
	}
	if _, err := source.IndexPath(); err != nil {
		return err
	}
	return nil
}

// IndexPath resolves one Codex-home global index, without guessing from unrelated paths.
func (s SourceConfig) IndexPath() (string, error) {
	if s.Capture.SessionIndexPath != "" {
		if s.Agent != constants.AgentCodex || filepath.Base(strings.TrimSuffix(s.Capture.SessionIndexPath, ".zst")) != "session_index.jsonl" {
			return "", fmt.Errorf("session_index_path must name a Codex session_index.jsonl artifact")
		}
		return pathnorm.Relative(s.Capture.SessionIndexPath)
	}
	if s.Agent != constants.AgentCodex || !EnabledByDefault(s.Capture.SessionIndex) {
		return "", nil
	}
	roots := map[string]bool{}
	for _, p := range s.Paths {
		base := filepath.Base(p)
		if base == "sessions" || base == "archived_sessions" {
			roots[filepath.Dir(p)] = true
		}
	}
	if len(roots) > 1 {
		return "", fmt.Errorf("codex source has multiple homes; configure one home per account/agent")
	}
	for root := range roots {
		return filepath.ToSlash(filepath.Join(root, "session_index.jsonl")), nil
	}
	return "", nil
}

func (cfg HubAppConfig) Validate() error {
	if cfg.Retention.ArchiveMaxAge.Duration < 0 || cfg.Retention.OrphanGrace.Duration < 0 || cfg.Retention.ArchiveMaxBytes.Bytes < 0 || cfg.Retention.MinFreeBytes.Bytes < 0 {
		return fmt.Errorf("retention limits must be non-negative")
	}
	if cfg.HubID == "" {
		return fmt.Errorf("hub_id is required")
	}
	if cfg.Paths.Root == "" || cfg.Paths.Mirror == "" || cfg.Paths.Archive == "" || cfg.Paths.Catalog == "" || cfg.Paths.Extracted == "" {
		return fmt.Errorf("paths.root, paths.mirror, paths.archive, paths.catalog, and paths.extracted are required")
	}
	if err := cfg.Paths.Validate(); err != nil {
		return err
	}
	if cfg.Receive.UnixNameMaxLength < 1 {
		return fmt.Errorf("receive.unix_name_max_length must be positive")
	}
	if cfg.Receive.MaxFileSize.Bytes <= 0 {
		return fmt.Errorf("receive.max_file_size must be positive")
	}
	if len(cfg.Receive.Sender) == 0 {
		return fmt.Errorf("at least one receive.sender is required")
	}
	seenSenderID := map[string]struct{}{}
	seenReceiveUser := map[string]struct{}{}
	seenMirrorGroup := map[string]struct{}{}
	seenMirror := map[string]struct{}{}
	for i, sender := range cfg.Receive.Sender {
		if sender.SenderID == "" || sender.SenderSlug == "" || sender.ReceiveUser == "" || sender.MirrorGroup == "" || sender.Mirror == "" {
			return fmt.Errorf("receive.sender[%d] has empty identity fields", i)
		}
		if !isUnixName(sender.ReceiveUser) {
			return fmt.Errorf("receive.sender[%d].receive_user must be a safe unix name", i)
		}
		if !isUnixName(sender.MirrorGroup) {
			return fmt.Errorf("receive.sender[%d].mirror_group must be a safe unix name", i)
		}
		if !isSlug(sender.SenderSlug) {
			return fmt.Errorf("receive.sender[%d].sender_slug must be a safe slug", i)
		}
		if len(sender.ReceiveUser) > cfg.Receive.UnixNameMaxLength || len(sender.MirrorGroup) > cfg.Receive.UnixNameMaxLength {
			return fmt.Errorf("receive.sender[%d] user/group exceeds unix_name_max_length", i)
		}
		if _, ok := seenSenderID[sender.SenderID]; ok {
			return fmt.Errorf("duplicate sender_id %q", sender.SenderID)
		}
		if _, ok := seenReceiveUser[sender.ReceiveUser]; ok {
			return fmt.Errorf("duplicate receive_user %q", sender.ReceiveUser)
		}
		if _, ok := seenMirrorGroup[sender.MirrorGroup]; ok {
			return fmt.Errorf("duplicate mirror_group %q", sender.MirrorGroup)
		}
		if _, ok := seenMirror[sender.Mirror]; ok {
			return fmt.Errorf("duplicate mirror %q", sender.Mirror)
		}
		if _, err := pathnorm.Relative(sender.Mirror); err != nil {
			return fmt.Errorf("receive.sender[%d].mirror: %w", i, err)
		}
		seenSenderID[sender.SenderID] = struct{}{}
		seenReceiveUser[sender.ReceiveUser] = struct{}{}
		seenMirrorGroup[sender.MirrorGroup] = struct{}{}
		seenMirror[sender.Mirror] = struct{}{}
	}
	if len(cfg.DataClasses) == 0 {
		return fmt.Errorf("data_classes is required")
	}
	for name, dataClass := range cfg.DataClasses {
		if !isSlug(name) {
			return fmt.Errorf("data_classes.%s must use a safe name", name)
		}
		if dataClass.RedactionRequired {
			return fmt.Errorf("data_classes.%s requires redaction, but redaction is not implemented", name)
		}
		if !containsString(dataClass.RawHubAllowed, cfg.HubID) {
			return fmt.Errorf("data_classes.%s must explicitly allow hub_id %q", name, cfg.HubID)
		}
		for i, hubID := range dataClass.RawHubAllowed {
			if !isSlug(hubID) {
				return fmt.Errorf("data_classes.%s.raw_hub_allowed[%d] must use a safe name", name, i)
			}
		}
	}
	if cfg.Archive.Policy != "" && cfg.Archive.Policy != "replace" && cfg.Archive.Policy != "versioned" {
		return fmt.Errorf("archive.policy must be replace or versioned")
	}
	if cfg.Archive.Enabled && cfg.Archive.Compression != "zstd" {
		return fmt.Errorf("archive.compression must be zstd when archive is enabled")
	}
	if cfg.Index.Interval.Duration <= 0 {
		return fmt.Errorf("index.interval must be positive")
	}
	if cfg.Index.EmitVLEvents && strings.TrimSpace(cfg.Index.VLEndpoint) == "" {
		return fmt.Errorf("index.vl_endpoint is required when index.emit_vl_events is true")
	}
	if cfg.Index.EmitVLEvents {
		parsed, err := url.Parse(cfg.Index.VLEndpoint)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("index.vl_endpoint must be an http or https URL")
		}
	}
	if cfg.Index.EmitOnUnchangedEvery.Duration < 0 {
		return fmt.Errorf("index.emit_on_unchanged_every must be non-negative")
	}
	if cfg.Archive.QuietAfter.Duration < 0 || cfg.Archive.RetainPlainAfterArchive.Duration < 0 {
		return fmt.Errorf("archive durations must be non-negative")
	}
	if cfg.Archive.ZstdLevel < 1 || cfg.Archive.ZstdLevel > 19 {
		return fmt.Errorf("archive.zstd_level must be between 1 and 19")
	}
	if cfg.Archive.VersionRetention < 0 || cfg.Archive.VersionRetentionDays < 0 {
		return fmt.Errorf("archive version retention values must be non-negative")
	}
	if cfg.Retention.DeleteUnknownAfter.Duration != 0 || cfg.Retention.ArchiveRetentionDays.Duration != 0 {
		return fmt.Errorf("retention.delete_unknown_after and archive_retention_days are reserved; use archive_max_age and orphan_grace")
	}
	return nil
}

func (paths HubPaths) Validate() error {
	root, err := cleanAbsolute("paths.root", paths.Root)
	if err != nil {
		return err
	}
	for name, value := range map[string]string{
		"paths.mirror":    paths.Mirror,
		"paths.archive":   paths.Archive,
		"paths.catalog":   paths.Catalog,
		"paths.extracted": paths.Extracted,
	} {
		clean, err := cleanAbsolute(name, value)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, clean)
		if err != nil {
			return fmt.Errorf("%s must be under paths.root: %w", name, err)
		}
		if rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." {
			return fmt.Errorf("%s must be under paths.root", name)
		}
	}
	return nil
}

func cleanAbsolute(name, value string) (string, error) {
	clean := filepath.Clean(value)
	if !filepath.IsAbs(clean) {
		return "", fmt.Errorf("%s must be absolute", name)
	}
	return clean, nil
}

func isUnixName(value string) bool {
	return unixNameRE.MatchString(value)
}

func isSlug(value string) bool {
	for _, part := range strings.Split(value, "_") {
		if part == "" {
			return false
		}
	}
	return pathSegmentSafe(value)
}

func pathSegmentSafe(value string) bool {
	if value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return value != ""
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
