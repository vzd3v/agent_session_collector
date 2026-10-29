package sender

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/vzd3v/agent_session_collector/internal/config"
	"github.com/vzd3v/agent_session_collector/internal/pathnorm"
	"github.com/vzd3v/agent_session_collector/internal/sessionid"
)

func Scan(cfg config.SenderConfig) ([]File, error) {
	matcher, err := newExcludeMatcher(cfg.Exclude.Patterns)
	if err != nil {
		return nil, err
	}
	var files []File
	for _, source := range cfg.Sources {
		home := filepath.Clean(source.Home)
		for _, configuredPath := range source.Paths {
			sourceRel, err := pathnorm.Relative(configuredPath)
			if err != nil {
				return nil, fmt.Errorf("normalize source path %q: %w", configuredPath, err)
			}
			root := filepath.Join(home, filepath.FromSlash(sourceRel))
			err = filepath.WalkDir(root, func(localPath string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					if os.IsNotExist(walkErr) && localPath == root {
						return nil
					}
					return fmt.Errorf("walk %s: %w", localPath, walkErr)
				}

				relHome, err := filepath.Rel(home, localPath)
				if err != nil {
					return fmt.Errorf("relativize %s: %w", localPath, err)
				}
				relHome = filepath.ToSlash(relHome)
				if relHome == "." {
					return nil
				}
				if matcher.matches(relHome) {
					if entry.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				if entry.IsDir() {
					return nil
				}
				info, err := entry.Info()
				if err != nil {
					return fmt.Errorf("stat %s: %w", localPath, err)
				}
				if !info.Mode().IsRegular() || path.Ext(relHome) != ".jsonl" {
					return nil
				}
				stat, ok := info.Sys().(*syscall.Stat_t)
				if !ok {
					return fmt.Errorf("stat metadata unavailable for %s", localPath)
				}
				id, err := sessionid.ForArtifact(source.Agent, relHome)
				if err != nil {
					return fmt.Errorf("extract session id from %s: %w", relHome, err)
				}
				abs, err := filepath.Abs(localPath)
				if err != nil {
					return fmt.Errorf("absolute path for %s: %w", localPath, err)
				}
				remote, err := remotePath(source.User, source.Agent, relHome)
				if err != nil {
					return fmt.Errorf("build remote path for %s: %w", relHome, err)
				}
				files = append(files, File{
					Agent:       source.Agent,
					User:        source.User,
					HostName:    source.HostName,
					Env:         source.Env,
					SessionID:   id,
					SourceKey:   sourceKey(source.User, source.Agent, abs),
					SourcePath:  abs,
					DisplayPath: relHome,
					RemotePath:  remote,
					LocalPath:   abs,
					SizeBytes:   info.Size(),
					MtimeUnixNS: info.ModTime().UnixNano(),
					Device:      uint64(stat.Dev),
					Inode:       stat.Ino,
				})
				return nil
			})
			if err != nil {
				return nil, err
			}
		}
	}
	return files, nil
}

type excludeMatcher struct {
	patterns []string
}

func newExcludeMatcher(patterns []string) (excludeMatcher, error) {
	normalized := make([]string, 0, len(patterns))
	for i, rawPattern := range patterns {
		pattern, err := pathnorm.Relative(rawPattern)
		if err != nil {
			return excludeMatcher{}, fmt.Errorf("exclude pattern %d: %w", i, err)
		}
		normalized = append(normalized, pattern)
	}
	return excludeMatcher{patterns: normalized}, nil
}

func (m excludeMatcher) matches(rel string) bool {
	rel = strings.TrimPrefix(path.Clean(rel), "./")
	for _, pattern := range m.patterns {
		if strings.HasSuffix(pattern, "/**") {
			prefix := strings.TrimSuffix(pattern, "/**")
			if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
				return true
			}
			continue
		}
		ok, err := path.Match(pattern, rel)
		if err == nil && ok {
			return true
		}
	}
	return false
}

func sourceKey(user, agent, absPath string) string {
	return fmt.Sprintf("%s:%s:%s", user, agent, filepath.ToSlash(absPath))
}

func remotePath(user, agent, relHome string) (string, error) {
	if err := safePathSegment("user", user); err != nil {
		return "", err
	}
	if err := safePathSegment("agent", agent); err != nil {
		return "", err
	}
	cleanRel, err := pathnorm.Relative(relHome)
	if err != nil {
		return "", fmt.Errorf("source relative path: %w", err)
	}
	rel := cleanRel
	for _, prefix := range []string{"." + agent + "/"} {
		if strings.HasPrefix(rel, prefix) {
			rel = strings.TrimPrefix(rel, prefix)
			break
		}
	}
	return path.Join(user, agent, rel), nil
}

func safePathSegment(name, value string) error {
	clean, err := pathnorm.Relative(value)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if clean != value || strings.Contains(clean, "/") {
		return fmt.Errorf("%s must be one safe path segment", name)
	}
	return nil
}
