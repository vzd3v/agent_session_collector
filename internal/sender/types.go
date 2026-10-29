package sender

import (
	"context"
	"io"
	"time"
)

type File struct {
	Role          string
	InlineContent []byte
	Agent         string
	User          string
	HostName      string
	Env           string
	SessionID     string
	SourceKey     string
	SourcePath    string
	DisplayPath   string
	RemotePath    string
	LocalPath     string
	SizeBytes     int64
	MtimeUnixNS   int64
	Device        uint64
	Inode         uint64
}

type Plan struct {
	Planned []File
	Skipped []File
}

type Result struct {
	MetadataUploaded int
	Scanned          int
	Planned          int
	Skipped          int
	Uploaded         int
	DryRun           bool
}

type UploadResult struct {
	Uploaded []File
	Skipped  []File
}

type Options struct {
	DryRun   bool
	Stdout   io.Writer
	Uploader Uploader
	Sleeper  Sleeper
	Now      func() time.Time
}

type Uploader interface {
	Upload(context.Context, []File) (UploadResult, error)
}

type Sleeper func(context.Context, time.Duration) error
