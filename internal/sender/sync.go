package sender

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/vzd3v/agent_session_collector/internal/config"
)

func Sync(ctx context.Context, cfg config.SenderConfig, opts Options) (Result, error) {
	if err := cfg.Validate(); err != nil {
		return Result{}, err
	}
	var store *stateStore
	var err error
	if opts.DryRun {
		store, err = openStateIfExists(ctx, cfg.Sync.StatePath)
	} else {
		store, err = openState(ctx, cfg.Sync.StatePath)
	}
	if err != nil {
		return Result{}, err
	}
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()

	files, err := Scan(cfg)
	if err != nil {
		return Result{}, err
	}
	plan, err := planUploads(ctx, store, files)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		Scanned: artifactCount(files),
		Planned: artifactCount(plan.Planned),
		Skipped: artifactCount(plan.Skipped),
		DryRun:  opts.DryRun,
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.DryRun {
		writeDryRun(opts.Stdout, plan)
		return result, nil
	}
	if len(plan.Planned) == 0 {
		return result, nil
	}
	uploader := opts.Uploader
	if uploader == nil {
		uploader = RsyncUploader{Config: cfg, Stderr: os.Stderr}
	}
	if opts.Sleeper == nil {
		opts.Sleeper = sleepContext
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	uploadResult, err := uploadWithRetry(ctx, uploader, opts.Sleeper, cfg.Sync.MaxAttempts, cfg.Sync.Backoff.Duration, plan.Planned)
	if err != nil {
		return result, err
	}
	if err := store.markSynced(ctx, uploadResult.Uploaded, opts.Now()); err != nil {
		return result, err
	}
	result.Uploaded = artifactCount(uploadResult.Uploaded)
	result.MetadataUploaded = len(uploadResult.Uploaded) - result.Uploaded
	result.Skipped += artifactCount(uploadResult.Skipped)
	return result, nil
}

func artifactCount(files []File) int {
	n := 0
	for _, f := range files {
		if f.InlineContent == nil {
			n++
		}
	}
	return n
}

func writeDryRun(w io.Writer, plan Plan) {
	_, _ = fmt.Fprintf(w, "DRY-RUN planned=%d skipped=%d\n", len(plan.Planned), len(plan.Skipped))
	for _, file := range plan.Planned {
		_, _ = fmt.Fprintf(w, "PLAN agent=%s user=%s session=%s path=%s size=%d mtime_unix_ns=%d\n",
			file.Agent, file.User, file.SessionID, file.DisplayPath, file.SizeBytes, file.MtimeUnixNS)
	}
	for _, file := range plan.Skipped {
		_, _ = fmt.Fprintf(w, "SKIP agent=%s user=%s session=%s path=%s size=%d mtime_unix_ns=%d\n",
			file.Agent, file.User, file.SessionID, file.DisplayPath, file.SizeBytes, file.MtimeUnixNS)
	}
}

func uploadWithRetry(ctx context.Context, uploader Uploader, sleeper Sleeper, maxAttempts int, backoff time.Duration, files []File) (UploadResult, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, err := uploader.Upload(ctx, files)
		if err != nil {
			lastErr = err
			if attempt == maxAttempts {
				break
			}
			if err := sleeper(ctx, backoff); err != nil {
				return UploadResult{}, fmt.Errorf("wait before rsync retry: %w", err)
			}
			continue
		}
		return result, nil
	}
	return UploadResult{}, fmt.Errorf("upload failed after %d attempts: %w", maxAttempts, lastErr)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
