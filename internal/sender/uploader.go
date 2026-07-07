package sender

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/vzd3v/agent_session_collector/internal/config"
	"github.com/vzd3v/agent_session_collector/internal/rsyncprofile"
)

type RsyncUploader struct {
	Config config.SenderConfig
	Stderr io.Writer
}

func (u RsyncUploader) Upload(ctx context.Context, files []File) error {
	if len(files) == 0 {
		return nil
	}
	stage, err := os.MkdirTemp("", "asc-sender-stage-*")
	if err != nil {
		return fmt.Errorf("create rsync stage: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(stage)
	}()

	filesFrom := make([]string, 0, len(files))
	for _, file := range files {
		if err := stageFile(stage, file); err != nil {
			return err
		}
		filesFrom = append(filesFrom, file.RemotePath)
	}
	listPath := filepath.Join(stage, ".asc-files-from")
	if err := os.WriteFile(listPath, []byte(strings.Join(filesFrom, "\x00")+"\x00"), 0o600); err != nil {
		return fmt.Errorf("write rsync files-from: %w", err)
	}

	profile := rsyncprofile.Profile{
		RsyncPath:      u.Config.Sync.RsyncPath,
		SSHTarget:      u.Config.Hub.SSHTarget,
		RemoteCommand:  u.Config.Hub.RemoteCommand,
		BandwidthLimit: u.Config.Sync.BandwidthLimit,
	}
	args := rsyncprofile.SenderArgs(profile, stage)
	args = append([]string{"--from0", "--files-from=" + listPath}, args...)
	cmd := exec.CommandContext(ctx, rsyncprofile.SenderCommand(profile), args...)
	cmd.Stderr = u.Stderr
	if output, err := cmd.Output(); err != nil {
		if len(output) > 0 && u.Stderr != nil {
			_, _ = u.Stderr.Write(output)
		}
		return fmt.Errorf("run rsync: %w", err)
	}
	return nil
}

func stageFile(stage string, file File) error {
	target := filepath.Join(stage, filepath.FromSlash(file.RemotePath))
	rel, err := filepath.Rel(stage, target)
	if err != nil {
		return fmt.Errorf("validate stage path for %s: %w", file.DisplayPath, err)
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." {
		return fmt.Errorf("stage path escapes staging root for %s", file.DisplayPath)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("create stage dir for %s: %w", file.DisplayPath, err)
	}
	if err := copyRegularFile(file, target); err != nil {
		return fmt.Errorf("stage %s: %w", file.DisplayPath, err)
	}
	mtime := time.Unix(0, file.MtimeUnixNS)
	if err := os.Chtimes(target, mtime, mtime); err != nil {
		return fmt.Errorf("preserve stage mtime for %s: %w", file.DisplayPath, err)
	}
	return nil
}

func copyRegularFile(file File, target string) error {
	fd, err := syscall.Open(file.LocalPath, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	in := os.NewFile(uintptr(fd), file.LocalPath)
	defer func() {
		_ = in.Close()
	}()
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return fmt.Errorf("source is not a regular file")
	}
	if file.Device != 0 || file.Inode != 0 {
		if uint64(stat.Dev) != file.Device || stat.Ino != file.Inode {
			return fmt.Errorf("source changed since scan")
		}
	}
	if file.SizeBytes != stat.Size || file.MtimeUnixNS != statMtimeUnixNS(stat) {
		return fmt.Errorf("source changed since scan")
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create target: %w", err)
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	return errors.Join(copyErr, closeErr)
}

func statMtimeUnixNS(stat syscall.Stat_t) int64 {
	return stat.Mtim.Sec*int64(time.Second) + stat.Mtim.Nsec
}
