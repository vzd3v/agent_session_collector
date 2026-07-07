package checksum

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

func ReaderSHA256(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", fmt.Errorf("hash reader: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func FileSHA256(path string) (sum string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open file for sha256: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close file after sha256: %w", closeErr)
		}
	}()
	return ReaderSHA256(f)
}
