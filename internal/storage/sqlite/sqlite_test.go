package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"magnet-to-strm/internal/ingest"
)

const (
	hashA = "0123456789abcdef0123456789abcdef01234567"
	hashB = "1123456789abcdef0123456789abcdef01234567"
	sha1A = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sha1B = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestContentCatalogAndSTRMSeeding(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	task := ingest.Task{
		InfoHash: hashA, Name: "Movie", ResultID: "root", Status: 2, Done: true,
		DeleteFileID: "source-folder", WPPathID: "work",
	}
	result := scan(hashA, "magnet:?xt=urn:btih:"+hashA)
	if err := db.SaveTask(ctx, result.MagnetURI, task); err != nil {
		t.Fatal(err)
	}
	saved, err := db.SaveScan(ctx, result, task)
	if err != nil {
		t.Fatal(err)
	}
	if saved.STRMRoot != "Movie" || !saved.Files[0].NeedsSTRM {
		t.Fatalf("unexpected initial STRM state: %+v", saved)
	}
	if err := db.MarkSTRMSeeded(ctx, hashA, []string{"video.mkv"}); err != nil {
		t.Fatal(err)
	}
	rescanned, err := db.SaveScan(ctx, result, task)
	if err != nil {
		t.Fatal(err)
	}
	if rescanned.Files[0].NeedsSTRM {
		t.Fatal("seeded STRM was requested again")
	}

	secondTask := task
	secondTask.InfoHash = hashB
	secondTask.Name = "Other"
	second := scan(hashB, "magnet:?xt=urn:btih:"+hashB)
	second.Files[0].RemoteID = "remote-copy"
	second.Files[0].RemotePath = "/work-copy/video.mkv"
	secondSaved, err := db.SaveScan(ctx, second, secondTask)
	if err != nil {
		t.Fatal(err)
	}
	var contentCount int
	if err := db.sql.QueryRow("SELECT count(*) FROM content_objects").Scan(&contentCount); err != nil {
		t.Fatal(err)
	}
	if contentCount != 1 {
		t.Fatalf("content was not deduplicated: %d", contentCount)
	}
	asset, err := db.AssetBySHA1(ctx, sha1A)
	if err != nil {
		t.Fatal(err)
	}
	if len(asset.Locations) != 2 {
		t.Fatalf("locations for two magnets = %+v, want 2", asset.Locations)
	}
	prefixes, err := db.SHA1Prefixes(ctx, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(prefixes) != 1 || prefixes[0] != "aa" {
		t.Fatalf("unexpected SHA1 prefixes: %v", prefixes)
	}
	assets, err := db.AssetsBySHA1Prefix(ctx, "aaaa")
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 1 || assets[0].SHA1 != sha1A || assets[0].CreatedAt.IsZero() {
		t.Fatalf("unexpected DAV assets: %+v", assets)
	}
	if secondSaved.STRMRoot == saved.STRMRoot {
		t.Fatalf("different torrents shared STRM root %q", saved.STRMRoot)
	}
}

func TestSaveScanKeepsLatestLocationPerContentAndTorrent(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	task := ingest.Task{
		InfoHash: hashA, Name: "Movie", ResultID: "root", Status: 2, Done: true,
	}
	first := scan(hashA, "magnet:?xt=urn:btih:"+hashA)
	if _, err := db.SaveScan(ctx, first, task); err != nil {
		t.Fatal(err)
	}
	second := scan(hashA, "magnet:?xt=urn:btih:"+hashA)
	second.Files[0].RemoteID = "new-remote"
	second.Files[0].RemotePath = "/work/new-video.mkv"
	if _, err := db.SaveScan(ctx, second, task); err != nil {
		t.Fatal(err)
	}

	asset, err := db.AssetBySHA1(ctx, sha1A)
	if err != nil {
		t.Fatal(err)
	}
	if len(asset.Locations) != 1 ||
		asset.Locations[0].RemoteFileID != "new-remote" {
		t.Fatalf("active locations = %+v, want only new-remote", asset.Locations)
	}
	var deleted int
	if err := db.sql.QueryRow(`
SELECT count(*) FROM remote_locations
WHERE content_id = ? AND deleted_at IS NOT NULL
`, asset.ID).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted historical locations = %d, want 1", deleted)
	}
}

func TestAssetBySHA1IncludesSuccessfulSourceMagnet(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	magnetURI := "magnet:?xt=urn:btih:" + hashA
	task := ingest.Task{
		InfoHash: hashA, Name: "Movie", ResultID: "root", Status: 2, Done: true,
		DeleteFileID: "source-folder", WPPathID: "work",
	}
	job, _ := ingest.NewJob(magnetURI)
	if err := db.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	task.JobGID = job.GID
	if _, err := db.SaveScan(ctx, scan(hashA, magnetURI), task); err != nil {
		t.Fatal(err)
	}
	job.State = ingest.JobSucceeded
	if err := db.UpdateJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	asset, err := db.AssetBySHA1(ctx, sha1A)
	if err != nil {
		t.Fatal(err)
	}
	if len(asset.Sources) != 1 || asset.Sources[0].MagnetURI != magnetURI {
		t.Fatalf("sources = %+v, want magnet %q", asset.Sources, magnetURI)
	}
	if asset.Sources[0].DeleteFileID != "source-folder" ||
		asset.Sources[0].WPPathID != "work" {
		t.Fatalf("source cleanup metadata = %+v", asset.Sources[0])
	}
}

func TestExpiredManagedLocationsIncludeSuccessfulSourceTasks(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	magnetURI := "magnet:?xt=urn:btih:" + hashA
	task := ingest.Task{
		InfoHash: hashA, Name: "Movie", ResultID: "root", Status: 2, Done: true,
	}
	job, _ := ingest.NewJob(magnetURI)
	if err := db.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	task.JobGID = job.GID
	if _, err := db.SaveScan(ctx, scan(hashA, magnetURI), task); err != nil {
		t.Fatal(err)
	}
	job.State = ingest.JobSucceeded
	if err := db.UpdateJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	assets, err := db.ExpiredManagedLocations(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 1 || len(assets[0].Sources) != 1 {
		t.Fatalf("expired assets = %+v, want one asset with one source", assets)
	}
	if assets[0].Sources[0].InfoHash != hashA ||
		assets[0].Sources[0].MagnetURI != magnetURI {
		t.Fatalf("expired asset sources = %+v", assets[0].Sources)
	}
}

func TestExpiredManagedLocationsWaitForEntireTorrentToBecomeIdle(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	magnetURI := "magnet:?xt=urn:btih:" + hashA
	task := ingest.Task{
		InfoHash: hashA, Name: "Season", ResultID: "root", Status: 2, Done: true,
	}
	result := scan(hashA, magnetURI)
	result.Files = append(result.Files, ingest.File{
		RelativePath: "episode-02.mkv", Name: "episode-02.mkv", SHA1: sha1B,
		SizeBytes: 100, RemoteID: "remote-episode-02",
		ParentID: "work", RemotePath: "/work/episode-02.mkv", PickCode: "pick-02",
	})
	job, _ := ingest.NewJob(magnetURI)
	if err := db.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	task.JobGID = job.GID
	if _, err := db.SaveScan(ctx, result, task); err != nil {
		t.Fatal(err)
	}
	job.State = ingest.JobSucceeded
	if err := db.UpdateJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	old := formatTime(now.Add(-2 * time.Hour))
	recent := formatTime(now)
	if _, err := db.sql.ExecContext(ctx, `
UPDATE content_objects
SET last_accessed_at = CASE sha1 WHEN ? THEN ? ELSE ? END
`, sha1A, recent, old); err != nil {
		t.Fatal(err)
	}
	assets, err := db.ExpiredManagedLocations(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 0 {
		t.Fatalf("expired assets while one episode is active = %+v", assets)
	}

	if _, err := db.sql.ExecContext(ctx, `
UPDATE content_objects SET last_accessed_at = ?
`, old); err != nil {
		t.Fatal(err)
	}
	assets, err = db.ExpiredManagedLocations(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 2 {
		t.Fatalf("expired assets after entire season became idle = %d, want 2", len(assets))
	}
}

func TestAssetSourcesAreUniqueAndOrderedByLatestSuccess(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for index, infoHash := range []string{hashA, hashB} {
		magnetURI := "magnet:?xt=urn:btih:" + infoHash
		task := ingest.Task{
			InfoHash: infoHash, Name: "Movie", ResultID: "root",
			Status: 2, Done: true,
		}
		job, _ := ingest.NewJob(magnetURI)
		job.CreatedAt = time.Date(
			2026, time.July, 1+index, 12, 0, 0, 0, time.UTC,
		)
		if err := db.CreateJob(ctx, job); err != nil {
			t.Fatal(err)
		}
		task.JobGID = job.GID
		if _, err := db.SaveScan(ctx, scan(infoHash, magnetURI), task); err != nil {
			t.Fatal(err)
		}
		finished := job.CreatedAt.Add(time.Hour)
		job.State = ingest.JobSucceeded
		job.FinishedAt = &finished
		if err := db.UpdateJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}

	asset, err := db.AssetBySHA1(ctx, sha1A)
	if err != nil {
		t.Fatal(err)
	}
	if len(asset.Sources) != 2 {
		t.Fatalf("sources = %+v, want 2", asset.Sources)
	}
	if asset.Sources[0].InfoHash != hashB || asset.Sources[1].InfoHash != hashA {
		t.Fatalf("sources are not ordered by latest success: %+v", asset.Sources)
	}
}

func TestNewSchemaDoesNotContainPreSHA1(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.sql.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}
	rows, err := db.sql.Query("PRAGMA table_info(content_objects)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(
			&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey,
		); err != nil {
			t.Fatal(err)
		}
		if name == "pre_sha1" {
			t.Fatal("new schema still contains pre_sha1")
		}
	}
}

func TestReopensExistingBaselineDatabase(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "baseline.db")
	db, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	job, err := ingest.NewJob("magnet:?xt=urn:btih:" + hashA)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reopened, err := db.Job(ctx, job.GID)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.InfoHash != job.InfoHash || reopened.State != job.State {
		t.Fatalf("reopened job = %+v, want %+v", reopened, job)
	}
}

func TestTasksAreSeparatedBySourceAndTorrentKeepsLatestSuccess(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	magnetURI := "magnet:?xt=urn:btih:" + hashA
	ariaJob, _ := ingest.NewJobForSource(magnetURI, ingest.TaskSourceAria2)
	if err := db.CreateJob(ctx, ariaJob); err != nil {
		t.Fatal(err)
	}
	qbitJob, _ := ingest.NewJobForSource(magnetURI, ingest.TaskSourceQBittorrent)
	if err := ingest.AssignNewGID(&qbitJob); err != nil {
		t.Fatal(err)
	}
	qbitJob.Category = "movies"
	if err := db.CreateJob(ctx, qbitJob); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveTask(ctx, magnetURI, ingest.Task{
		JobGID: ariaJob.GID, InfoHash: hashA, ResultID: "aria-result",
	}); err != nil {
		t.Fatal(err)
	}
	ariaJob.State = ingest.JobSucceeded
	if err := db.UpdateJob(ctx, ariaJob); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveTask(ctx, magnetURI, ingest.Task{
		JobGID: qbitJob.GID, InfoHash: hashA, ResultID: "qbit-result",
	}); err != nil {
		t.Fatal(err)
	}
	qbitJob.State = ingest.JobSucceeded
	if err := db.UpdateJob(ctx, qbitJob); err != nil {
		t.Fatal(err)
	}

	ariaJobs, err := db.ListJobsBySource(ctx, ingest.TaskSourceAria2)
	if err != nil || len(ariaJobs) != 1 || ariaJobs[0].GID != ariaJob.GID {
		t.Fatalf("aria2 jobs = %+v, %v", ariaJobs, err)
	}
	qbitJobs, err := db.ListJobsBySource(ctx, ingest.TaskSourceQBittorrent)
	if err != nil || len(qbitJobs) != 1 || qbitJobs[0].Category != "movies" {
		t.Fatalf("qbittorrent jobs = %+v, %v", qbitJobs, err)
	}
	var latestGID string
	if err := db.sql.QueryRow(`
SELECT task.gid FROM torrents torrent
JOIN tasks task ON task.id = torrent.latest_successful_task_id
WHERE torrent.info_hash = ?
`, hashA).Scan(&latestGID); err != nil {
		t.Fatal(err)
	}
	if latestGID != qbitJob.GID {
		t.Fatalf("latest successful gid = %q, want %q", latestGID, qbitJob.GID)
	}
}

func TestMaterializationRestoreTaskStoresTargetSHA1(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	job, err := ingest.NewJobForSource(
		"magnet:?xt=urn:btih:"+hashA,
		ingest.TaskSourceMaterializationRestore,
	)
	if err != nil {
		t.Fatal(err)
	}
	job.TargetSHA1 = strings.ToUpper(sha1A)
	if err := db.CreateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	var target string
	if err := db.sql.QueryRow(`
SELECT restore.target_sha1
FROM materialization_restore_tasks restore
JOIN tasks task ON task.id = restore.task_id
WHERE task.gid = ?
`, job.GID).Scan(&target); err != nil {
		t.Fatal(err)
	}
	if target != sha1A {
		t.Fatalf("target_sha1 = %q, want %q", target, sha1A)
	}
}

func TestMigratesV9JobsAsLegacyWithoutSourceIndexes(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "v9.db")
	handle, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = handle.Exec(`
CREATE TABLE torrents (
 id INTEGER PRIMARY KEY, info_hash TEXT NOT NULL UNIQUE, magnet_uri TEXT NOT NULL,
 display_name TEXT NOT NULL DEFAULT '', provider_task_status INTEGER NOT NULL DEFAULT 0,
 provider_task_update INTEGER NOT NULL DEFAULT 0, provider_task_progress REAL NOT NULL DEFAULT 0,
 result_remote_id TEXT NOT NULL DEFAULT '', task_delete_file_id TEXT NOT NULL DEFAULT '',
 task_wp_path_id TEXT NOT NULL DEFAULT '', total_bytes INTEGER NOT NULL DEFAULT 0,
 file_count INTEGER NOT NULL DEFAULT 0, strm_root TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL, scanned_at TEXT
);
CREATE TABLE ingest_jobs (
 gid TEXT PRIMARY KEY, info_hash TEXT NOT NULL, magnet_uri TEXT NOT NULL,
 category TEXT NOT NULL DEFAULT '', state TEXT NOT NULL, error_message TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL, started_at TEXT, finished_at TEXT
);
CREATE TABLE download_categories (name TEXT PRIMARY KEY, save_path TEXT NOT NULL DEFAULT '');
INSERT INTO torrents VALUES (
 1, '` + hashA + `', 'magnet:?xt=urn:btih:` + hashA + `', 'Movie', 2, 10, 100,
 'result', 'delete', 'work', 100, 1, 'Movie',
 '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', NULL
);
INSERT INTO ingest_jobs VALUES (
 'legacy-gid', '` + hashA + `', 'magnet:?xt=urn:btih:` + hashA + `', 'movies',
 'succeeded', '', '2026-01-01T00:00:00Z', NULL, '2026-01-01T01:00:00Z'
);
PRAGMA user_version=9;
`)
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	legacy, err := db.Job(context.Background(), "legacy-gid")
	if err != nil || legacy.Source != "legacy" || legacy.Progress != 100 {
		t.Fatalf("legacy job = %+v, %v", legacy, err)
	}
	ariaJobs, _ := db.ListJobsBySource(context.Background(), ingest.TaskSourceAria2)
	qbitJobs, _ := db.ListJobsBySource(context.Background(), ingest.TaskSourceQBittorrent)
	if len(ariaJobs) != 0 || len(qbitJobs) != 0 {
		t.Fatalf("legacy task leaked into source indexes: aria=%+v qbit=%+v", ariaJobs, qbitJobs)
	}
}

func TestRejectsDatabaseOlderThanBaseline(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "old.db")
	handle, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Exec(
		"CREATE TABLE old_files (id INTEGER PRIMARY KEY); PRAGMA user_version=7",
	); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(databasePath); err == nil {
		t.Fatal("expected pre-v8 database error")
	}
}

func TestRejectsDatabaseNewerThanProgram(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "newer.db")
	handle, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Exec(
		"CREATE TABLE future_data (id INTEGER PRIMARY KEY); PRAGMA user_version=99",
	); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(databasePath); err == nil {
		t.Fatal("expected newer database version error")
	}
}

func TestRecoverRunningJobs(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	job, _ := ingest.NewJob("magnet:?xt=urn:btih:" + hashA)
	if err := db.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	job.State = ingest.JobRunning
	job.StartedAt = &now
	if err := db.UpdateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := db.RecoverRunningJobs(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := db.Job(ctx, job.GID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != ingest.JobQueued || recovered.StartedAt != nil {
		t.Fatalf("job was not recovered: %+v", recovered)
	}
}

func TestJobProgressAndCancellation(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	job, _ := ingest.NewJob("magnet:?xt=urn:btih:" + hashA)
	if err := db.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveTask(ctx, job.MagnetURI, ingest.Task{
		InfoHash: hashA, Name: "Movie", Status: 1, Progress: 42.5,
	}); err != nil {
		t.Fatal(err)
	}
	running, err := db.Job(ctx, job.GID)
	if err != nil {
		t.Fatal(err)
	}
	if running.Progress != 42.5 {
		t.Fatalf("job progress = %v, want 42.5", running.Progress)
	}
	running.State = ingest.JobRunning
	if err := db.UpdateJob(ctx, running); err != nil {
		t.Fatal(err)
	}
	if err := db.CancelJob(ctx, job.GID); err != nil {
		t.Fatal(err)
	}
	running.State = ingest.JobFailed
	running.Error = "context canceled"
	if err := db.UpdateJob(ctx, running); !errors.Is(err, ingest.ErrJobCanceled) {
		t.Fatalf("UpdateJob() after cancellation error = %v, want ErrJobCanceled", err)
	}
	canceled, err := db.Job(ctx, job.GID)
	if err != nil {
		t.Fatal(err)
	}
	if canceled.State != ingest.JobCanceled || canceled.FinishedAt == nil {
		t.Fatalf("job was not canceled: %+v", canceled)
	}
	if err := db.DeleteJob(ctx, job.GID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Job(ctx, job.GID); !errors.Is(err, ingest.ErrJobNotFound) {
		t.Fatalf("deleted canceled job error = %v, want ErrJobNotFound", err)
	}

	failed, _ := ingest.NewJob("magnet:?xt=urn:btih:" + hashB)
	if err := db.CreateJob(ctx, failed); err != nil {
		t.Fatal(err)
	}
	failed.State = ingest.JobFailed
	failed.Error = "provider error"
	if err := db.UpdateJob(ctx, failed); err != nil {
		t.Fatal(err)
	}
	if err := db.CancelJob(ctx, failed.GID); err != nil {
		t.Fatalf("CancelJob() rejected failed task: %v", err)
	}
	failedCanceled, err := db.Job(ctx, failed.GID)
	if err != nil || failedCanceled.State != ingest.JobCanceled {
		t.Fatalf("failed task was not canceled: %+v, %v", failedCanceled, err)
	}
	if err := db.DeleteJob(ctx, failed.GID); err != nil {
		t.Fatal(err)
	}

	failed, _ = ingest.NewJob("magnet:?xt=urn:btih:" + hashB)
	if err := db.CreateJob(ctx, failed); err != nil {
		t.Fatal(err)
	}
	failed.State = ingest.JobFailed
	if err := db.UpdateJob(ctx, failed); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteJob(ctx, failed.GID); err != nil {
		t.Fatalf("DeleteJob() rejected failed task: %v", err)
	}

	completed, _ := ingest.NewJob("magnet:?xt=urn:btih:" + hashB)
	if err := db.CreateJob(ctx, completed); err != nil {
		t.Fatal(err)
	}
	completed.State = ingest.JobSucceeded
	if err := db.UpdateJob(ctx, completed); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteJob(ctx, completed.GID); err == nil {
		t.Fatal("DeleteJob() deleted a completed task")
	}
}

func scan(infoHash, magnetURI string) ingest.Result {
	return ingest.Result{
		Name: "Movie", InfoHash: infoHash, MagnetURI: magnetURI,
		ResultID: "root", TotalBytes: 100, ScannedAt: time.Now().UTC(),
		Files: []ingest.File{{
			RelativePath: "video.mkv", Name: "video.mkv", SHA1: sha1A,
			SizeBytes: 100, RemoteID: "remote-" + infoHash[:2],
			ParentID: "work", RemotePath: "/work/video.mkv", PickCode: "pick",
		}},
	}
}
