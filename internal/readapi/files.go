package readapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/vzd3v/agent_session_collector/internal/artifact"
	"github.com/vzd3v/agent_session_collector/sessionapi"
)

type capture struct {
	row
	file     *os.File
	encoding string
}

type contextReader struct {
	context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func (s *Server) openCapture(ctx context.Context, p row) (capture, error) {
	if p.SizeBytes < 0 || p.SizeBytes > s.maxBytes {
		return capture{}, errLimit
	}
	type candidate struct {
		root                 *os.File
		path, encoding, hash string
		size                 int64
	}
	candidates := []candidate{
		{s.archive, p.archivePath, artifact.Zstd, p.archiveSHA, -1},
		{s.mirror, p.mirrorPath, p.mirrorEncoding, p.physicalSHA, p.physicalSize},
	}
	for _, c := range candidates {
		if c.path == "" || !sessionapi.ValidSHA256(c.hash) || (c.encoding != artifact.Plain && c.encoding != artifact.Zstd) {
			continue
		}
		f, err := openIndexed(c.root, c.path)
		if err != nil {
			continue
		}
		valid, err := verifyFile(ctx, f, c.size, c.hash, s.maxBytes)
		if valid && err == nil {
			return capture{row: p, file: f, encoding: c.encoding}, nil
		}
		_ = f.Close()
		if ctx.Err() != nil {
			return capture{}, ctx.Err()
		}
	}
	return capture{}, errGeneration
}

// openIndexed resolves only beneath the configured directory descriptor. Each
// step rejects symlinks, including within the root, and the leaf cannot block on
// a FIFO. Linux openat is also used by the surrounding hub filesystem code.
func openIndexed(root *os.File, name string) (*os.File, error) {
	rel, err := filepath.Rel(root.Name(), name)
	if err != nil || !filepath.IsLocal(rel) || rel == "." {
		return nil, errGeneration
	}
	parts := strings.Split(rel, string(filepath.Separator))
	fd := int(root.Fd())
	for i, part := range parts {
		flags := syscall.O_NOFOLLOW | syscall.O_CLOEXEC | syscall.O_RDONLY
		if i < len(parts)-1 {
			flags |= syscall.O_DIRECTORY
		} else {
			flags |= syscall.O_NONBLOCK
		}
		next, err := syscall.Openat(fd, part, flags, 0)
		if i > 0 {
			_ = syscall.Close(fd)
		}
		if err != nil {
			return nil, err
		}
		fd = next
	}
	f := os.NewFile(uintptr(fd), name)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, errGeneration
	}
	return f, nil
}

func verifyFile(ctx context.Context, f *os.File, expectedSize int64, expectedHash string, maxBytes int64) (bool, error) {
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	if (expectedSize >= 0 && info.Size() != expectedSize) || info.Size() > maxBytes+(1<<20) {
		return false, nil
	}
	h := sha256.New()
	n, err := io.Copy(h, contextReader{ctx, io.LimitReader(f, info.Size()+1)})
	if err != nil {
		return false, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	return n == info.Size() && hex.EncodeToString(h.Sum(nil)) == expectedHash, nil
}

func (p capture) read(ctx context.Context, offset int64, consume func(io.Reader) error) error {
	if p.encoding == artifact.Plain {
		if _, err := p.file.Seek(offset, io.SeekStart); err != nil {
			return err
		}
		offset = 0
	}
	return artifact.ReadFile(ctx, p.file, p.encoding, func(src io.Reader) error {
		src = contextReader{ctx, src}
		if offset != 0 {
			if _, err := io.CopyN(io.Discard, src, offset); err != nil {
				return err
			}
		}
		return consume(src)
	})
}

func (p capture) write(ctx context.Context, dst io.Writer) error {
	return p.read(ctx, 0, func(src io.Reader) error {
		if _, err := io.CopyN(dst, src, p.SizeBytes); err != nil {
			return err
		}
		var extra [1]byte
		n, err := io.ReadFull(src, extra[:])
		if n != 0 || !errors.Is(err, io.EOF) {
			return errGeneration
		}
		return nil
	})
}
