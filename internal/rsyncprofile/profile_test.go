package rsyncprofile

import (
	"slices"
	"strings"
	"testing"
)

func TestSenderArgsUseSafeProfile(t *testing.T) {
	args := SenderArgs(Profile{
		SSHTarget:     "upload_alpha@collector.example.net",
		RemoteCommand: "asc-receive",
	}, "/tmp/source")

	required := []string{
		"-rt",
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
