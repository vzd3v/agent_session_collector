package fixtures_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestTranscriptFixturesAreSynthetic(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "fixtures")
	forbidden := []string{
		"/home/",
		"/srv/repos/",
		"/srv/sandbox/",
		"BEGIN PRIVATE KEY",
		"access_token",
		"customer_",
	}
	privateHostPattern := regexp.MustCompile(`\b[0-9]{2}[a-z][a-z0-9_]*\b`)

	seenFixture := false
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(body)
		if strings.HasSuffix(entry.Name(), ".jsonl") {
			if !strings.Contains(text, "ASC_SYNTHETIC_FIXTURE") {
				t.Fatalf("fixture %s is missing ASC_SYNTHETIC_FIXTURE marker", path)
			}
			seenFixture = true
		}
		for _, marker := range forbidden {
			if strings.Contains(path, marker) || strings.Contains(text, marker) {
				t.Fatalf("fixture %s contains forbidden marker %q", path, marker)
			}
		}
		if privateHostPattern.MatchString(path) || privateHostPattern.MatchString(text) {
			t.Fatalf("fixture %s contains private-host-shaped marker", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan fixtures: %v", err)
	}
	if !seenFixture {
		t.Fatalf("fixtures must contain ASC_SYNTHETIC_FIXTURE marker")
	}
}
