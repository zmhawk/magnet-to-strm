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

type STRMStore interface {
	Path(string) (string, error)
	Write(context.Context, string, string, string) (string, error)
}
