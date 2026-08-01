package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"magnet-to-strm/internal/ingest"
)

func (d *DB) SaveTask(ctx context.Context, magnetURI string, task ingest.Task) error {
	now := formatTime(time.Now())
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
INSERT INTO torrents (
    info_hash, magnet_uri, display_name, total_bytes, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(info_hash) DO UPDATE SET
    magnet_uri = excluded.magnet_uri,
    display_name = CASE WHEN excluded.display_name <> '' THEN excluded.display_name ELSE torrents.display_name END,
    total_bytes = CASE WHEN excluded.total_bytes > 0 THEN excluded.total_bytes ELSE torrents.total_bytes END,
    updated_at = excluded.updated_at
`, strings.ToLower(task.InfoHash), magnetURI, task.Name, task.SizeBytes, now, now)
	if err != nil {
		return fmt.Errorf("保存离线任务: %w", err)
	}
	query := `
UPDATE tasks SET
    provider_task_status = ?, provider_task_update = ?, provider_task_progress = ?,
    result_remote_id = CASE WHEN ? <> '' THEN ? ELSE result_remote_id END,
    task_delete_file_id = CASE WHEN ? <> '' THEN ? ELSE task_delete_file_id END,
    task_wp_path_id = CASE WHEN ? <> '' THEN ? ELSE task_wp_path_id END
WHERE id = (`
	values := []any{task.Status, task.LastUpdate, task.Progress,
		task.ResultID, task.ResultID, task.DeleteFileID, task.DeleteFileID,
		task.WPPathID, task.WPPathID}
	if task.JobGID != "" {
		query += `SELECT id FROM tasks WHERE gid = ?`
		values = append(values, task.JobGID)
	} else {
		query += `SELECT task.id FROM tasks task JOIN torrents torrent ON torrent.id = task.torrent_id
WHERE torrent.info_hash = ? ORDER BY task.id DESC LIMIT 1`
		values = append(values, strings.ToLower(task.InfoHash))
	}
	query += `)`
	updateResult, err := tx.ExecContext(ctx, query, values...)
	if err != nil {
		return fmt.Errorf("保存离线任务状态: %w", err)
	}
	affected, err := updateResult.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 && task.JobGID == "" {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO tasks (
    torrent_id, gid, source, magnet_uri, state,
    provider_task_status, provider_task_update, provider_task_progress,
    result_remote_id, task_delete_file_id, task_wp_path_id, created_at
)
SELECT id, ?, 'direct', ?, 'running', ?, ?, ?, ?, ?, ?, ?
FROM torrents WHERE info_hash = ?
ON CONFLICT(gid) DO UPDATE SET
    provider_task_status = excluded.provider_task_status,
    provider_task_update = excluded.provider_task_update,
    provider_task_progress = excluded.provider_task_progress,
    result_remote_id = CASE WHEN excluded.result_remote_id <> '' THEN excluded.result_remote_id ELSE tasks.result_remote_id END,
    task_delete_file_id = CASE WHEN excluded.task_delete_file_id <> '' THEN excluded.task_delete_file_id ELSE tasks.task_delete_file_id END,
    task_wp_path_id = CASE WHEN excluded.task_wp_path_id <> '' THEN excluded.task_wp_path_id ELSE tasks.task_wp_path_id END
`, "direct-"+strings.ToLower(task.InfoHash), magnetURI, task.Status,
			task.LastUpdate, task.Progress, task.ResultID, task.DeleteFileID,
			task.WPPathID, now, strings.ToLower(task.InfoHash)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) SaveScan(
	ctx context.Context,
	result ingest.Result,
	task ingest.Task,
) (ingest.Result, error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return ingest.Result{}, err
	}
	defer tx.Rollback()
	now := formatTime(time.Now())
	scannedAt := formatTime(result.ScannedAt)
	_, err = tx.ExecContext(ctx, `
INSERT INTO torrents (
    info_hash, magnet_uri, display_name, total_bytes, file_count,
    created_at, updated_at, scanned_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(info_hash) DO UPDATE SET
    magnet_uri = excluded.magnet_uri,
    display_name = excluded.display_name,
    total_bytes = excluded.total_bytes,
    file_count = excluded.file_count,
    updated_at = excluded.updated_at,
    scanned_at = excluded.scanned_at
`, result.InfoHash, result.MagnetURI, result.Name, result.TotalBytes,
		len(result.Files), now, now, scannedAt)
	if err != nil {
		return ingest.Result{}, fmt.Errorf("保存磁链: %w", err)
	}

	var torrentID int64
	var strmRoot string
	if err := tx.QueryRowContext(ctx, `
SELECT id, strm_root FROM torrents WHERE info_hash = ?
`, result.InfoHash).Scan(&torrentID, &strmRoot); err != nil {
		return ingest.Result{}, err
	}
	if task.JobGID != "" {
		if _, err := tx.ExecContext(ctx, `
UPDATE tasks SET provider_task_status = ?, provider_task_update = ?,
    provider_task_progress = ?, result_remote_id = ?,
    task_delete_file_id = CASE WHEN ? <> '' THEN ? ELSE task_delete_file_id END,
    task_wp_path_id = CASE WHEN ? <> '' THEN ? ELSE task_wp_path_id END
WHERE gid = ?
`, task.Status, task.LastUpdate, task.Progress, result.ResultID,
			task.DeleteFileID, task.DeleteFileID, task.WPPathID, task.WPPathID,
			task.JobGID); err != nil {
			return ingest.Result{}, err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE torrents SET latest_successful_task_id = (
    SELECT id FROM tasks WHERE gid = ? AND state = 'succeeded'
)
WHERE id = ? AND EXISTS (
    SELECT 1 FROM tasks WHERE gid = ? AND state = 'succeeded'
)
`, task.JobGID, torrentID, task.JobGID); err != nil {
			return ingest.Result{}, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO tasks (
    torrent_id, gid, source, magnet_uri, state,
    provider_task_status, provider_task_update, provider_task_progress,
    result_remote_id, task_delete_file_id, task_wp_path_id,
    created_at, finished_at
) VALUES (?, ?, 'direct', ?, 'succeeded', ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(gid) DO UPDATE SET
    state = 'succeeded', provider_task_status = excluded.provider_task_status,
    provider_task_update = excluded.provider_task_update,
    provider_task_progress = excluded.provider_task_progress,
    result_remote_id = excluded.result_remote_id,
    task_delete_file_id = CASE WHEN excluded.task_delete_file_id <> '' THEN excluded.task_delete_file_id ELSE tasks.task_delete_file_id END,
    task_wp_path_id = CASE WHEN excluded.task_wp_path_id <> '' THEN excluded.task_wp_path_id ELSE tasks.task_wp_path_id END,
    finished_at = excluded.finished_at
`, torrentID, "direct-"+strings.ToLower(result.InfoHash), result.MagnetURI,
			task.Status, task.LastUpdate, task.Progress, result.ResultID,
			task.DeleteFileID, task.WPPathID, now, now); err != nil {
			return ingest.Result{}, err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE torrents SET latest_successful_task_id = (
    SELECT id FROM tasks WHERE gid = ?
) WHERE id = ?
`, "direct-"+strings.ToLower(result.InfoHash), torrentID); err != nil {
			return ingest.Result{}, err
		}
	}
	if strmRoot == "" {
		preferred := ingest.SafeLibraryName(task.Name)
		if preferred == "" {
			preferred = ingest.SafeLibraryName(result.Name)
		}
		if preferred == "" {
			preferred = result.InfoHash
		}
		if category := ingest.SafeLibraryName(task.Category); category != "" {
			preferred = path.Join(category, preferred)
		}
		strmRoot, err = availableSTRMRoot(ctx, tx, preferred, result.InfoHash)
		if err != nil {
			return ingest.Result{}, err
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE torrents SET strm_root = ? WHERE id = ?",
			strmRoot, torrentID); err != nil {
			return ingest.Result{}, err
		}
	}
	result.STRMRoot = strmRoot

	if _, err := tx.ExecContext(ctx, `
UPDATE torrent_files SET removed_at = ? WHERE torrent_id = ? AND removed_at IS NULL
`, now, torrentID); err != nil {
		return ingest.Result{}, err
	}
	for index := range result.Files {
		file := &result.Files[index]
		contentID, err := upsertContent(ctx, tx, *file, now)
		if err != nil {
			return ingest.Result{}, err
		}
		if err := upsertRemoteLocation(
			ctx, tx, torrentID, contentID, *file, now,
		); err != nil {
			return ingest.Result{}, err
		}
		var seededAt sql.NullString
		err = tx.QueryRowContext(ctx, `
INSERT INTO torrent_files (
    torrent_id, content_id, relative_path, source_created_at, source_updated_at,
    last_seen_at, removed_at
) VALUES (?, ?, ?, ?, ?, ?, NULL)
ON CONFLICT(torrent_id, relative_path) DO UPDATE SET
    strm_seeded_at = CASE
        WHEN torrent_files.content_id = excluded.content_id THEN torrent_files.strm_seeded_at
        ELSE NULL
    END,
    content_id = excluded.content_id,
    source_created_at = excluded.source_created_at,
    source_updated_at = excluded.source_updated_at,
    last_seen_at = excluded.last_seen_at,
    removed_at = NULL
RETURNING strm_seeded_at
`, torrentID, contentID, file.RelativePath, file.CreatedAt, file.UpdatedAt, now).
			Scan(&seededAt)
		if err != nil {
			return ingest.Result{}, fmt.Errorf("保存磁链文件 %q: %w", file.RelativePath, err)
		}
		file.NeedsSTRM = !seededAt.Valid
	}
	if err := tx.Commit(); err != nil {
		return ingest.Result{}, err
	}
	return result, nil
}

func upsertContent(
	ctx context.Context,
	tx *sql.Tx,
	file ingest.File,
	now string,
) (int64, error) {
	var contentID int64
	var existingSize int64
	err := tx.QueryRowContext(ctx, `
INSERT INTO content_objects (
    sha1, size_bytes, preferred_name, created_at, updated_at
) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(sha1) DO UPDATE SET
    preferred_name = CASE
        WHEN content_objects.preferred_name = '' THEN excluded.preferred_name
        ELSE content_objects.preferred_name
    END,
    updated_at = excluded.updated_at
RETURNING id, size_bytes
`, strings.ToLower(file.SHA1), file.SizeBytes,
		file.Name, now, now).Scan(&contentID, &existingSize)
	if err != nil {
		return 0, fmt.Errorf("保存内容 %s: %w", file.SHA1, err)
	}
	if existingSize != file.SizeBytes {
		return 0, fmt.Errorf(
			"SHA1 %s 对应的文件大小不一致：已有 %d，新扫描 %d",
			file.SHA1, existingSize, file.SizeBytes,
		)
	}
	return contentID, nil
}

func upsertRemoteLocation(
	ctx context.Context,
	tx *sql.Tx,
	torrentID int64,
	contentID int64,
	file ingest.File,
	now string,
) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE remote_locations
SET deleted_at = ?, updated_at = ?
WHERE content_id = ?
  AND provider = 'p115'
  AND ownership = 'managed_cache'
  AND deleted_at IS NULL
  AND (torrent_id = ? OR torrent_id IS NULL)
  AND remote_file_id <> ?
`, now, now, contentID, torrentID, file.RemoteID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO remote_locations (
    content_id, torrent_id, provider, remote_file_id, remote_parent_id, pick_code, remote_path,
    ownership, root_remote_id, verified_at, materialized_at, created_at, updated_at
) VALUES (?, ?, 'p115', ?, ?, ?, ?, 'managed_cache', ?, ?, ?, ?, ?)
ON CONFLICT(provider, remote_file_id) DO UPDATE SET
    content_id = excluded.content_id,
    torrent_id = excluded.torrent_id,
    remote_parent_id = excluded.remote_parent_id,
    pick_code = excluded.pick_code,
    remote_path = excluded.remote_path,
    root_remote_id = excluded.root_remote_id,
    verified_at = excluded.verified_at,
    deleted_at = NULL,
    updated_at = excluded.updated_at
`, contentID, torrentID, file.RemoteID, file.ParentID, file.PickCode, file.RemotePath,
		file.ManagedRootID, now, now, now, now)
	return err
}

func availableSTRMRoot(
	ctx context.Context,
	tx *sql.Tx,
	preferred string,
	infoHash string,
) (string, error) {
	shortHash := infoHash[:8]
	for sequence := 1; ; sequence++ {
		candidate := preferred
		if sequence == 2 {
			candidate += " [" + shortHash + "]"
		} else if sequence > 2 {
			candidate = fmt.Sprintf("%s [%s-%d]", preferred, shortHash, sequence)
		}
		var occupied int
		if err := tx.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM torrents WHERE strm_root = ?)
`, candidate).Scan(&occupied); err != nil {
			return "", err
		}
		if occupied == 0 {
			return candidate, nil
		}
	}
}

func (d *DB) MarkSTRMSeeded(
	ctx context.Context,
	infoHash string,
	relativePaths []string,
) error {
	if len(relativePaths) == 0 {
		return nil
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := formatTime(time.Now())
	for _, relativePath := range relativePaths {
		result, err := tx.ExecContext(ctx, `
UPDATE torrent_files
SET strm_seeded_at = ?
WHERE torrent_id = (SELECT id FROM torrents WHERE info_hash = ?)
  AND relative_path = ?
`, now, strings.ToLower(infoHash), relativePath)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("没有找到 STRM 对应的磁链文件 %q", relativePath)
		}
	}
	return tx.Commit()
}

func (d *DB) ResultByInfoHash(ctx context.Context, infoHash string) (ingest.Result, error) {
	var result ingest.Result
	result.Files = make([]ingest.File, 0)
	var scannedAt sql.NullString
	var torrentID int64
	err := d.sql.QueryRowContext(ctx, `
SELECT t.id, t.display_name, t.info_hash, t.magnet_uri,
       COALESCE(task.result_remote_id, ''), t.total_bytes, t.strm_root, t.scanned_at
FROM torrents t
LEFT JOIN tasks task ON task.id = COALESCE(t.latest_successful_task_id, (
    SELECT candidate.id FROM tasks candidate WHERE candidate.torrent_id = t.id
    ORDER BY candidate.id DESC LIMIT 1
))
WHERE t.info_hash = ?
`, strings.ToLower(infoHash)).Scan(
		&torrentID, &result.Name, &result.InfoHash, &result.MagnetURI,
		&result.ResultID, &result.TotalBytes, &result.STRMRoot, &scannedAt,
	)
	if err != nil {
		return ingest.Result{}, err
	}
	if scannedAt.Valid {
		result.ScannedAt, _ = parseTime(scannedAt.String)
	}
	rows, err := d.sql.QueryContext(ctx, `
SELECT tf.relative_path, c.preferred_name, c.sha1, c.size_bytes,
       COALESCE(rl.remote_file_id, ''), COALESCE(rl.remote_parent_id, ''),
       COALESCE(rl.remote_path, ''), COALESCE(rl.pick_code, ''),
       tf.source_created_at, tf.source_updated_at, tf.strm_seeded_at
FROM torrent_files tf
JOIN content_objects c ON c.id = tf.content_id
LEFT JOIN remote_locations rl ON rl.id = (
    SELECT location.id FROM remote_locations location
    WHERE location.content_id = c.id AND location.deleted_at IS NULL
    ORDER BY CASE location.ownership WHEN 'external' THEN 0 ELSE 1 END,
             location.verified_at DESC, location.id DESC
    LIMIT 1
)
WHERE tf.torrent_id = ? AND tf.removed_at IS NULL
ORDER BY tf.relative_path
`, torrentID)
	if err != nil {
		return ingest.Result{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var file ingest.File
		var seededAt sql.NullString
		if err := rows.Scan(
			&file.RelativePath, &file.Name, &file.SHA1, &file.SizeBytes,
			&file.RemoteID, &file.ParentID, &file.RemotePath, &file.PickCode,
			&file.CreatedAt, &file.UpdatedAt, &seededAt,
		); err != nil {
			return ingest.Result{}, err
		}
		file.NeedsSTRM = !seededAt.Valid
		result.Files = append(result.Files, file)
	}
	return result, rows.Err()
}

func (d *DB) CreateJob(ctx context.Context, job ingest.Job) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := formatTime(job.CreatedAt)
	if job.Source == "" {
		job.Source = ingest.TaskSourceAria2
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO torrents (info_hash, magnet_uri, created_at, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(info_hash) DO UPDATE SET magnet_uri = excluded.magnet_uri,
    updated_at = excluded.updated_at
`, strings.ToLower(job.InfoHash), job.MagnetURI, now, now); err != nil {
		return err
	}
	var taskID int64
	if err := tx.QueryRowContext(ctx, `
INSERT INTO tasks (torrent_id, gid, source, magnet_uri, state, error_message, created_at)
SELECT id, ?, ?, ?, ?, '', ? FROM torrents WHERE info_hash = ?
RETURNING id
`, job.GID, job.Source, job.MagnetURI, job.State, now,
		strings.ToLower(job.InfoHash)).Scan(&taskID); err != nil {
		return err
	}
	var extension string
	var values []any
	switch job.Source {
	case ingest.TaskSourceAria2:
		extension = "INSERT INTO aria2_tasks(task_id) VALUES (?)"
		values = []any{taskID}
	case ingest.TaskSourceQBittorrent:
		extension = "INSERT INTO qbittorrent_tasks(task_id, category) VALUES (?, ?)"
		values = []any{taskID, strings.TrimSpace(job.Category)}
	case ingest.TaskSourceMaterializationRestore:
		extension = "INSERT INTO materialization_restore_tasks(task_id, target_sha1) VALUES (?, ?)"
		values = []any{taskID, strings.ToLower(job.TargetSHA1)}
	case ingest.TaskSourceCLI:
		extension = "INSERT INTO cli_tasks(task_id) VALUES (?)"
		values = []any{taskID}
	}
	if extension != "" {
		if _, err := tx.ExecContext(ctx, extension, values...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) UpdateJob(ctx context.Context, job ingest.Job) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
UPDATE tasks
SET magnet_uri = ?, state = ?, error_message = ?, started_at = ?, finished_at = ?
WHERE gid = ? AND (state <> 'canceled' OR ? = 'canceled')
	`, job.MagnetURI, job.State, job.Error,
		nullableTime(job.StartedAt), nullableTime(job.FinishedAt), job.GID, job.State)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		var state string
		if err := tx.QueryRowContext(
			ctx, "SELECT state FROM tasks WHERE gid = ?", job.GID,
		).Scan(&state); errors.Is(err, sql.ErrNoRows) {
			return ingest.ErrJobNotFound
		} else if err != nil {
			return err
		}
		if state == ingest.JobCanceled {
			return ingest.ErrJobCanceled
		}
	}
	if job.Source == ingest.TaskSourceQBittorrent || job.Category != "" {
		if _, err := tx.ExecContext(ctx, `
UPDATE qbittorrent_tasks SET category = ?
WHERE task_id = (SELECT id FROM tasks WHERE gid = ?)
`, strings.TrimSpace(job.Category), job.GID); err != nil {
			return err
		}
	}
	if job.State == ingest.JobSucceeded {
		_, err = tx.ExecContext(ctx, `
UPDATE torrents SET latest_successful_task_id = (
    SELECT id FROM tasks WHERE gid = ?
) WHERE id = (SELECT torrent_id FROM tasks WHERE gid = ?)
`, job.GID, job.GID)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) CancelJob(ctx context.Context, gid string) error {
	finished := formatTime(time.Now())
	result, err := d.sql.ExecContext(ctx, `
UPDATE tasks
SET state = 'canceled', error_message = '', finished_at = ?
WHERE gid = ? AND state IN ('queued', 'running', 'failed')
`, finished, gid)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	job, err := d.Job(ctx, gid)
	if err != nil {
		return err
	}
	if job.State == ingest.JobCanceled {
		return nil
	}
	return fmt.Errorf("任务状态为 %s，不能取消", job.State)
}

func (d *DB) DeleteJob(ctx context.Context, gid string) error {
	result, err := d.sql.ExecContext(ctx, `
DELETE FROM tasks WHERE gid = ? AND state IN ('canceled', 'failed')
`, gid)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	job, err := d.Job(ctx, gid)
	if err != nil {
		return err
	}
	if job.State == ingest.JobSucceeded {
		return errors.New("已完成任务不能删除")
	}
	return errors.New("只有失败或已取消的任务可以删除")
}

func (d *DB) DeleteJobAny(ctx context.Context, gid string) error {
	_, err := d.sql.ExecContext(ctx, "DELETE FROM tasks WHERE gid = ?", gid)
	return err
}

func (d *DB) TaskCleanupInfo(
	ctx context.Context,
	infoHash string,
) (ingest.TaskCleanupInfo, error) {
	var info ingest.TaskCleanupInfo
	err := d.sql.QueryRowContext(ctx, `
SELECT task.task_delete_file_id, task.task_wp_path_id
FROM torrents torrent
JOIN tasks task ON task.id = torrent.latest_successful_task_id
WHERE torrent.info_hash = ?
`, strings.ToLower(infoHash)).Scan(&info.DeleteFileID, &info.WPPathID)
	if errors.Is(err, sql.ErrNoRows) {
		return ingest.TaskCleanupInfo{}, ingest.ErrJobNotFound
	}
	return info, err
}

func (d *DB) Job(ctx context.Context, gid string) (ingest.Job, error) {
	job, err := scanJob(d.sql.QueryRowContext(ctx, `
SELECT task.id, task.gid, task.source, torrent.info_hash,
       torrent.display_name, task.provider_task_progress,
       task.magnet_uri, COALESCE(qbit.category, ''), task.state,
       task.error_message, task.created_at, task.started_at, task.finished_at
FROM tasks task
JOIN torrents torrent ON torrent.id = task.torrent_id
LEFT JOIN qbittorrent_tasks qbit ON qbit.task_id = task.id
WHERE task.gid = ?
`, gid))
	if errors.Is(err, sql.ErrNoRows) {
		return ingest.Job{}, ingest.ErrJobNotFound
	}
	return job, err
}

func (d *DB) ListJobs(ctx context.Context, states ...string) ([]ingest.Job, error) {
	return d.listJobs(ctx, "", states...)
}

func (d *DB) ListJobsBySource(
	ctx context.Context, source string, states ...string,
) ([]ingest.Job, error) {
	return d.listJobs(ctx, source, states...)
}

func (d *DB) listJobs(ctx context.Context, source string, states ...string) ([]ingest.Job, error) {
	query := `
SELECT task.id, task.gid, task.source, torrent.info_hash,
       torrent.display_name, task.provider_task_progress,
       task.magnet_uri, COALESCE(qbit.category, ''), task.state,
       task.error_message, task.created_at, task.started_at, task.finished_at
FROM tasks task
JOIN torrents torrent ON torrent.id = task.torrent_id
LEFT JOIN qbittorrent_tasks qbit ON qbit.task_id = task.id`
	var values []any
	var clauses []string
	if source != "" {
		switch source {
		case ingest.TaskSourceAria2:
			clauses = append(clauses, "EXISTS (SELECT 1 FROM aria2_tasks source_task WHERE source_task.task_id = task.id)")
		case ingest.TaskSourceQBittorrent:
			clauses = append(clauses, "qbit.task_id IS NOT NULL")
		default:
			clauses = append(clauses, "task.source = ?")
			values = append(values, source)
		}
	}
	if len(states) > 0 {
		clauses = append(clauses, "task.state IN ("+strings.TrimRight(strings.Repeat("?,", len(states)), ",")+")")
		for _, state := range states {
			values = append(values, state)
		}
	}
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY task.created_at, task.id"
	rows, err := d.sql.QueryContext(ctx, query, values...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []ingest.Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (d *DB) LatestJob(ctx context.Context, source, infoHash string) (ingest.Job, error) {
	job, err := scanJob(d.sql.QueryRowContext(ctx, `
SELECT task.id, task.gid, task.source, torrent.info_hash,
       torrent.display_name, task.provider_task_progress,
       task.magnet_uri, COALESCE(qbit.category, ''), task.state,
       task.error_message, task.created_at, task.started_at, task.finished_at
FROM tasks task
JOIN torrents torrent ON torrent.id = task.torrent_id
LEFT JOIN qbittorrent_tasks qbit ON qbit.task_id = task.id
WHERE task.source = ? AND torrent.info_hash = ?
ORDER BY task.id DESC LIMIT 1
`, source, strings.ToLower(infoHash)))
	if errors.Is(err, sql.ErrNoRows) {
		return ingest.Job{}, ingest.ErrJobNotFound
	}
	return job, err
}

func (d *DB) RecoverRunningJobs(ctx context.Context) error {
	_, err := d.sql.ExecContext(ctx, `
UPDATE tasks
SET state = 'queued', error_message = '', started_at = NULL, finished_at = NULL
WHERE state = 'running'
`)
	return err
}

func (d *DB) RecoverRunningJobsBySource(ctx context.Context, sources ...string) error {
	if len(sources) == 0 {
		return nil
	}
	values := make([]any, len(sources))
	for index, source := range sources {
		values[index] = source
	}
	_, err := d.sql.ExecContext(ctx, `
UPDATE tasks
SET state = 'queued', error_message = '', started_at = NULL, finished_at = NULL
WHERE state = 'running' AND source IN (`+
		strings.TrimRight(strings.Repeat("?,", len(sources)), ",")+")", values...)
	return err
}

func (d *DB) CreateCategory(ctx context.Context, name, savePath string) error {
	_, err := d.sql.ExecContext(ctx, `
INSERT INTO qbittorrent_categories (name, save_path) VALUES (?, ?)
ON CONFLICT(name) DO UPDATE SET save_path = excluded.save_path
`, strings.TrimSpace(name), strings.TrimSpace(savePath))
	return err
}

func (d *DB) Categories(ctx context.Context) (map[string]string, error) {
	rows, err := d.sql.QueryContext(ctx,
		"SELECT name, save_path FROM qbittorrent_categories ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]string)
	for rows.Next() {
		var name, savePath string
		if err := rows.Scan(&name, &savePath); err != nil {
			return nil, err
		}
		result[name] = savePath
	}
	return result, rows.Err()
}

type rowScanner interface {
	Scan(...any) error
}

func scanJob(scanner rowScanner) (ingest.Job, error) {
	var job ingest.Job
	var created string
	var started, finished sql.NullString
	err := scanner.Scan(
		&job.ID, &job.GID, &job.Source, &job.InfoHash, &job.Name, &job.Progress,
		&job.MagnetURI, &job.Category, &job.State, &job.Error,
		&created, &started, &finished,
	)
	if err != nil {
		return ingest.Job{}, err
	}
	job.CreatedAt, _ = parseTime(created)
	if started.Valid {
		value, _ := parseTime(started.String)
		job.StartedAt = &value
	}
	if finished.Valid {
		value, _ := parseTime(finished.String)
		job.FinishedAt = &value
	}
	return job, nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000Z")
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse("2006-01-02T15:04:05.000000Z", value)
	if err == nil {
		return parsed, nil
	}
	return time.Parse(time.RFC3339, value)
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}
