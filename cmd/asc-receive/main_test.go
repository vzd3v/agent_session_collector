package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/vzd3v/agent_session_collector/internal/rsyncprofile"
)

var safeUnixNameRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"asc-receive", "version"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if strings.TrimSpace(stdout.String()) != "asc-receive dev" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunRequiresConfigAndSender(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"asc-receive"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(stderr.String(), "-config and -sender are required") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestSplitServerArgs(t *testing.T) {
	prefix, direct := splitServerArgs([]string{"-config", "hub.toml", "-sender", "sender-alpha", "--server", "-trRe.iLsfxCIvu", ".", "/"})
	if strings.Join(prefix, " ") != "-config hub.toml -sender sender-alpha" {
		t.Fatalf("prefix = %#v", prefix)
	}
	if strings.Join(direct, " ") != "--server -trRe.iLsfxCIvu . /" {
		t.Fatalf("direct = %#v", direct)
	}
}

func TestReceiveRsyncSmoke(t *testing.T) {
	if os.Getenv("ASC_RECEIVE_HELPER") == "1" {
		t.Skip("helper process only")
	}
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skipf("rsync not installed: %v", err)
	}
	currentUser, err := user.Current()
	if err != nil {
		t.Skipf("current user lookup failed: %v", err)
	}
	currentGroup, err := user.LookupGroupId(currentUser.Gid)
	if err != nil {
		t.Skipf("current group lookup failed: %v", err)
	}
	if !safeUnixNameRE.MatchString(currentUser.Username) || !safeUnixNameRE.MatchString(currentGroup.Name) {
		t.Skipf("current user/group is not compatible with hub config validation: %q/%q", currentUser.Username, currentGroup.Name)
	}

	tmp := t.TempDir()
	source := filepath.Join(tmp, "source")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "session.jsonl"), []byte(`{"ASC_SYNTHETIC_FIXTURE":true}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	filesFrom := filepath.Join(tmp, "files-from")
	if err := os.WriteFile(filesFrom, []byte("session.jsonl\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "hub.toml")
	writeSmokeHubConfig(t, configPath, tmp, currentUser.Username, currentGroup.Name)
	wrapper := filepath.Join(tmp, "fake-ssh")
	script := fmt.Sprintf("#!/bin/sh\nshift\nSSH_ORIGINAL_COMMAND=\"$*\" ASC_RECEIVE_HELPER=1 exec %s -test.run '^TestReceiveHelperProcess$' -- -config %s -sender sender-alpha\n", shellQuote(os.Args[0]), shellQuote(configPath))
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	args := rsyncprofile.SenderArgs(rsyncprofile.Profile{
		SSHTarget:     "upload_alpha@example.invalid",
		RemoteCommand: "asc-receive -config /ignored/hub.toml -sender sender-alpha",
	}, source)
	args = append([]string{"--from0", "--files-from=" + filesFrom}, args...)
	cmd := exec.Command("rsync", args...)
	cmd.Env = append(os.Environ(), "RSYNC_RSH="+wrapper)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rsync smoke failed: %v\n%s", err, output)
	}

	receivedPath := filepath.Join(tmp, "mirror", "sender-alpha", "session.jsonl")
	received, err := os.ReadFile(receivedPath)
	if err != nil {
		t.Fatalf("read received file: %v", err)
	}
	if string(received) != `{"ASC_SYNTHETIC_FIXTURE":true}`+"\n" {
		t.Fatalf("received content = %q", received)
	}
	info, err := os.Stat(receivedPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o007 != 0 {
		t.Fatalf("received file grants other permissions: %v", info.Mode().Perm())
	}
}

func TestReceiveHelperProcess(t *testing.T) {
	if os.Getenv("ASC_RECEIVE_HELPER") != "1" {
		return
	}
	args := []string{"asc-receive"}
	for i, arg := range os.Args {
		if arg == "--" {
			args = append(args, os.Args[i+1:]...)
			break
		}
	}
	os.Exit(run(args, os.Stdout, os.Stderr))
}

func writeSmokeHubConfig(t *testing.T, path string, root string, username string, groupName string) {
	t.Helper()
	config := fmt.Sprintf(`
hub_id = "collector-hub-alpha"

[paths]
root = %q
mirror = %q
archive = %q
catalog = %q
extracted = %q

[receive]
allow_delete = false
max_file_size = "2GiB"
unix_name_max_length = 64

[[receive.sender]]
sender_id = "sender-alpha"
sender_slug = "sender-alpha"
receive_user = %q
mirror_group = %q
mirror = "sender-alpha"

[data_classes.internal_agent_transcript]
raw_hub_allowed = ["collector-hub-alpha"]
redaction_required = false

[index]
interval = "10m"
emit_vl_events = false
vl_endpoint = ""
emit_on_unchanged_every = "24h"

[archive]
enabled = true
quiet_after = "24h"
compression = "zstd"
zstd_level = 6
policy = "replace"
retain_plain_after_archive = "48h"
version_retention = 3
version_retention_days = 30

[retention]
delete_mirror_after_archive = true
delete_unknown_after = "0s"
archive_retention_days = "0"

[logging]
format = "json"
level = "info"
`, root, filepath.Join(root, "mirror"), filepath.Join(root, "archive"), filepath.Join(root, "catalog", "sessions.sqlite"), filepath.Join(root, "extracted"), username, groupName)
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
