package pathnorm

import (
	"errors"
	"path"
	"path/filepath"
	"strings"
)

var (
	ErrEmptyPath       = errors.New("path is empty")
	ErrAbsolutePath    = errors.New("path must be relative")
	ErrTraversalPath   = errors.New("path contains traversal")
	ErrBackslashPath   = errors.New("path contains backslash")
	ErrUnsafePathToken = errors.New("path contains unsafe token")
)

func Relative(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", ErrEmptyPath
	}
	if strings.Contains(p, `\`) {
		return "", ErrBackslashPath
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") {
		return "", ErrAbsolutePath
	}

	raw := strings.TrimPrefix(p, "./")
	for _, part := range strings.Split(raw, "/") {
		if part == ".." {
			return "", ErrTraversalPath
		}
	}

	clean := path.Clean(p)
	if clean == "." {
		return "", ErrEmptyPath
	}
	for _, part := range strings.Split(clean, "/") {
		switch part {
		case "", ".", "..":
			return "", ErrTraversalPath
		}
		if strings.Contains(part, "\x00") {
			return "", ErrUnsafePathToken
		}
	}
	return clean, nil
}
