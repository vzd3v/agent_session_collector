package catalog

import "time"

type Session struct {
	SourceID             string
	ArtifactID           string
	RelativePath         string
	ArtifactRole         string
	SourceEncoding       string
	LatestPhysicalSHA256 string
	LatestRawSizeBytes   int64
	DataClass            string
	MetadataJSON         string
	NativeSessionID      string
	NativeAgentID        string
	NativeRootID         string
	NativeParentID       string
	NativeForkID         string
	SessionKey           string
	Agent                string
	Sender               string
	User                 string
	Env                  string
	HostName             string
	SessionID            string
	ConversationID       string
	MirrorPath           string
	LatestSizeBytes      int64
	LatestMtimeUnixNS    int64
	LatestRawSHA256      string
	ArchiveStatus        string
	LatestArchivePath    string
	LatestArchiveSHA256  string
	FirstSeenAt          time.Time
	LastIndexedAt        time.Time
	LastChangedAt        time.Time
	LastIndexEventAt     time.Time
}

type Archive struct {
	SessionKey     string
	Version        int
	Path           string
	RawSizeBytes   int64
	RawMtimeUnixNS int64
	RawSHA256      string
	ArchiveSHA256  string
	CreatedAt      time.Time
	SupersededAt   time.Time
}
