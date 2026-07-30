package ingest

import "context"

type Repository interface {
	SaveTask(context.Context, string, Task) error
	SaveScan(context.Context, Result, Task) (Result, error)
	MarkSTRMSeeded(context.Context, string, []string) error
	ResultByInfoHash(context.Context, string) (Result, error)
}

type JobRepository interface {
	CreateJob(context.Context, Job) error
	UpdateJob(context.Context, Job) error
	CancelJob(context.Context, string) error
	DeleteJob(context.Context, string) error
	Job(context.Context, string) (Job, error)
	ListJobs(context.Context, ...string) ([]Job, error)
	RecoverRunningJobs(context.Context) error
	CreateCategory(context.Context, string, string) error
	Categories(context.Context) (map[string]string, error)
}

type Provider interface {
	ListOfflineTasks(context.Context, int64) ([]Task, int, error)
	AddOfflineTasks(context.Context, []string, string) ([]OfflineTaskCreateResult, error)
	DeleteOfflineTask(context.Context, string, bool) error
	OfflineFolderInfo(context.Context, string) (RemoteNode, error)
	OfflineListFolder(context.Context, string, int64, int64) ([]RemoteNode, int64, error)
}

// RecycleBinCleaner removes files that were moved to the 115 recycle bin.
// It is intentionally separate from Provider so test doubles and other
// providers do not need to implement recycle-bin maintenance.
type RecycleBinCleaner interface {
	DeleteRecycleBin(context.Context) error
}

// RemoteFileDeleter is implemented by providers that can remove a file or
// folder after its offline-task record has already been deleted.
type RemoteFileDeleter interface {
	Delete(context.Context, string, string) error
}

type TaskCleanupInfo struct {
	DeleteFileID string
	WPPathID     string
}

// TaskCleanupRepository exposes the source metadata saved with a completed
// offline task.
type TaskCleanupRepository interface {
	TaskCleanupInfo(context.Context, string) (TaskCleanupInfo, error)
}

type STRMStore interface {
	Path(string) (string, error)
	Write(context.Context, string, string, string) (string, error)
}
