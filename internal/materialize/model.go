package materialize

import (
	"context"
	"errors"
	"time"
)

var (
	ErrAssetNotFound  = errors.New("内容不存在")
	ErrRemoteNotFound = errors.New("远端文件不存在")
)

type Asset struct {
	ID             int64
	SHA1           string
	SizeBytes      int64
	PreferredName  string
	LastAccessedAt *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Locations      []Location
	Sources        []Source
}

// CacheArtifact is the task-scoped unit used by capacity cleanup. Its result
// remote is a folder, so cleanup can remove one complete task result at once.
type CacheArtifact struct {
	ID             int64
	ResultRemoteID string
	LastAccessedAt *time.Time
	CreatedAt      time.Time
}

type Source struct {
	ArtifactID    int64
	ArtifactState string
	InfoHash      string
	MagnetURI     string
	TotalBytes    int64
	DeleteFileID  string
	WPPathID      string
}

type Location struct {
	ID             int64
	RemoteFileID   string
	RemoteParentID string
	PickCode       string
	RemotePath     string
	Ownership      string
	SourceInfoHash string
	MaterializedAt *time.Time
}

type Resolution struct {
	Asset    Asset
	Location Location
}

type RemoteParent struct {
	ID   string
	Name string
}

type RemoteFile struct {
	ID             string
	ParentID       string
	Name           string
	SHA1           string
	SizeBytes      int64
	PickCode       string
	RemotePath     string
	SourceInfoHash string
	Parents        []RemoteParent
}

type Repository interface {
	AssetBySHA1(context.Context, string) (Asset, error)
	SaveLocation(context.Context, int64, Location) error
	MarkLocationDeleted(context.Context, int64) error
	MarkSourceLocationsDeleted(context.Context, string) error
	TouchAsset(context.Context, int64) error
	ExpiredManagedLocations(context.Context, time.Time) ([]Asset, error)
}

// ManagedCacheLister is optional so repositories written against older
// versions keep working when the size limit is disabled.
type ManagedCacheLister interface {
	ManagedCacheLocations(context.Context) ([]Asset, error)
}

type ManagedCacheArtifactLister interface {
	ManagedCacheArtifacts(context.Context) ([]CacheArtifact, error)
}

type ArtifactDeleter interface {
	MarkArtifactDeleted(context.Context, int64) error
}

type Provider interface {
	FileInfo(context.Context, string) (RemoteFile, error)
	Delete(context.Context, string, string) error
}

type Downloader interface {
	DownloadURL(context.Context, string, string) (string, error)
}

type OfflineTaskCleaner interface {
	DeleteOfflineTaskIfExists(context.Context, string, bool) (bool, error)
}

type RecycleBinCleaner interface {
	DeleteRecycleBin(context.Context) error
}

type Restorer interface {
	RestoreContent(context.Context, string, string) (RemoteFile, error)
}
