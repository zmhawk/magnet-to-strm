package ingest

import (
	"errors"
	"time"
)

var (
	ErrJobNotFound           = errors.New("任务不存在")
	ErrJobCanceled           = errors.New("任务已取消")
	ErrOfflineResultNotFound = errors.New("115 离线任务结果不存在")
	ErrOfflineTaskExists     = errors.New("115 离线任务已存在")
)

type Task struct {
	InfoHash     string
	Name         string
	ResultID     string
	DeleteFileID string
	WPPathID     string
	Status       int
	LastUpdate   int64
	SizeBytes    int64
	Progress     float64
	Done         bool
	Failed       bool
}

type OfflineTaskCreateResult struct {
	InfoHash string
	Created  bool
	ErrCode  int64
	Error    string
}

func (r OfflineTaskCreateResult) Conflict() bool {
	return r.ErrCode == 10008
}

type RemoteParent struct {
	ID   string
	Name string
}

type RemoteNode struct {
	ID        string
	ParentID  string
	Name      string
	SHA1      string
	SizeBytes int64
	PickCode  string
	IsDir     bool
	CreatedAt int64
	UpdatedAt int64
	Parents   []RemoteParent
}

type File struct {
	RelativePath  string `json:"relative_path"`
	STRMPath      string `json:"strm_path,omitempty"`
	Name          string `json:"name"`
	SHA1          string `json:"sha1"`
	SizeBytes     int64  `json:"size_bytes"`
	RemoteID      string `json:"remote_id"`
	ParentID      string `json:"remote_parent_id"`
	RemotePath    string `json:"remote_path"`
	PickCode      string `json:"pick_code"`
	ManagedRootID string `json:"-"`
	CreatedAt     int64  `json:"source_created_at,omitempty"`
	UpdatedAt     int64  `json:"source_updated_at,omitempty"`
	NeedsSTRM     bool   `json:"-"`
}

type Result struct {
	Name       string    `json:"name"`
	InfoHash   string    `json:"info_hash"`
	MagnetURI  string    `json:"magnet_uri"`
	ResultID   string    `json:"result_id"`
	ReusedTask bool      `json:"reused_task"`
	TotalBytes int64     `json:"total_bytes"`
	ScannedAt  time.Time `json:"scanned_at"`
	STRMRoot   string    `json:"-"`
	Files      []File    `json:"files"`
}

type Job struct {
	GID        string
	InfoHash   string
	Name       string
	Progress   float64
	MagnetURI  string
	State      string
	Error      string
	CreatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
}

const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
	JobCanceled  = "canceled"
)
