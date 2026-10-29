package rsyncprofile

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSenderArgsUseSafeProfile(t *testing.T) {
	args := SenderArgs(Profile{
		SSHTarget:     "upload_alpha@collector.example.net",
		RemoteCommand: "asc-receive",
	}, "/tmp/source")

	required := []string{
		"-rt",
		"--ignore-times",
		"--safe-links",
		"--no-links",
		"--no-perms",
		"--no-owner",
		"--no-group",
		"--no-devices",
		"--no-specials",
		"--partial",
	}
	for _, want := range required {
		if !slices.Contains(args, want) {
			t.Fatalf("SenderArgs missing %q in %#v", want, args)
		}
	}
	forbiddenPrefixes := []string{"--delete", "--devices", "--specials", "--owner", "--group", "--perms", "--links"}
	for _, arg := range args {
		if arg == "-l" || arg == "-rlt" {
			t.Fatalf("SenderArgs contains symlink-preserving arg %q", arg)
		}
		for _, prefix := range forbiddenPrefixes {
			if strings.HasPrefix(arg, prefix) {
				t.Fatalf("SenderArgs contains unsafe arg %q", arg)
			}
		}
	}
	if got := args[len(args)-1]; got != "upload_alpha@collector.example.net:/" {
		t.Fatalf("remote destination = %q", got)
	}
}

func TestPlannedEqualSizeAndMtimeUpdateIsTransferred(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skipf("rsync not installed: %v", err)
	}
	source, target := t.TempDir(), t.TempDir()
	sourceFile, targetFile := filepath.Join(source, "descriptor.json"), filepath.Join(target, "descriptor.json")
	if err := os.WriteFile(sourceFile, []byte(`{"team":"one"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetFile, []byte(`{"team":"two"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1700000000, 0)
	for _, name := range []string{sourceFile, targetFile} {
		if err := os.Chtimes(name, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	args := SenderArgs(Profile{SSHTarget: "unused@example.invalid"}, source)
	args[len(args)-1] = target + "/"
	if output, err := exec.Command("rsync", args...).CombinedOutput(); err != nil {
		t.Fatalf("rsync: %v %s", err, output)
	}
	body, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"team":"one"}` {
		t.Fatalf("selected metadata was silently skipped: %s", body)
	}
}
