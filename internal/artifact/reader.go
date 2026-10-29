package artifact

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
)

// Read streams logical bytes; callers must consume the stream or return an error.
// Native zstd files remain untouched. No decompressed temporary file is created.
func Read(ctx context.Context, name, encoding string, consume func(io.Reader) error) error {
	fd, err := syscall.Open(name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open artifact: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("artifact is not regular")
	}
	if encoding == Plain {
		return consume(file)
	}
	if encoding != Zstd {
		return fmt.Errorf("unsupported artifact encoding %q", encoding)
	}
	cmd := exec.CommandContext(ctx, "zstd", "-q", "-dc")
	cmd.Stdin = file
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("start zstd decoder: %w", err)
	}
	readErr := consume(stdout)
	if readErr != nil {
		_ = stdout.Close()
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if waitErr != nil {
		waitErr = fmt.Errorf("decode zstd: %w", waitErr)
	}
	return errors.Join(readErr, waitErr)
}
