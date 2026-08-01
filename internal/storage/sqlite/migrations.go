package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

type migration func(context.Context, *sql.Tx) error

// migrations is keyed by the version being upgraded. Every migration advances
// exactly one version so a database can be upgraded through the full chain.
var migrations = map[int]migration{
	9: func(ctx context.Context, tx *sql.Tx) error {
		statements := []string{
			`CREATE TABLE tasks (
    id INTEGER PRIMARY KEY,
    torrent_id INTEGER NOT NULL REFERENCES torrents(id) ON DELETE CASCADE,
    gid TEXT NOT NULL UNIQUE,
    source TEXT NOT NULL CHECK(source <> ''),
    magnet_uri TEXT NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('queued','running','succeeded','failed','canceled')),
    error_message TEXT NOT NULL DEFAULT '',
    provider_task_status INTEGER NOT NULL DEFAULT 0,
    provider_task_update INTEGER NOT NULL DEFAULT 0,
    provider_task_progress REAL NOT NULL DEFAULT 0 CHECK(provider_task_progress >= 0 AND provider_task_progress <= 100),
    result_remote_id TEXT NOT NULL DEFAULT '',
    task_delete_file_id TEXT NOT NULL DEFAULT '',
    task_wp_path_id TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT
)`,
			`CREATE TABLE aria2_tasks (
    task_id INTEGER PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE
)`,
			`CREATE TABLE qbittorrent_tasks (
    task_id INTEGER PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
    category TEXT NOT NULL DEFAULT ''
)`,
			`CREATE TABLE materialization_restore_tasks (
    task_id INTEGER PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
    target_sha1 TEXT NOT NULL DEFAULT ''
)`,
			`CREATE TABLE cli_tasks (
    task_id INTEGER PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE
)`,
			`INSERT INTO torrents (
    info_hash, magnet_uri, created_at, updated_at
)
SELECT lower(j.info_hash), max(j.magnet_uri), min(j.created_at), max(j.created_at)
FROM ingest_jobs j
WHERE NOT EXISTS (SELECT 1 FROM torrents t WHERE t.info_hash = lower(j.info_hash))
GROUP BY lower(j.info_hash)`,
			`INSERT INTO tasks (
    torrent_id, gid, source, magnet_uri, state, error_message,
    provider_task_status, provider_task_update, provider_task_progress,
    result_remote_id, task_delete_file_id, task_wp_path_id,
    created_at, started_at, finished_at
)
SELECT t.id, j.gid, 'legacy', j.magnet_uri, j.state, j.error_message,
       t.provider_task_status, t.provider_task_update, t.provider_task_progress,
       t.result_remote_id, t.task_delete_file_id, t.task_wp_path_id,
       j.created_at, j.started_at, j.finished_at
FROM ingest_jobs j JOIN torrents t ON t.info_hash = lower(j.info_hash)`,
			`ALTER TABLE download_categories RENAME TO qbittorrent_categories`,
			`ALTER TABLE torrents ADD COLUMN latest_successful_task_id INTEGER REFERENCES tasks(id) ON DELETE SET NULL`,
			`UPDATE torrents SET latest_successful_task_id = (
    SELECT task.id FROM tasks task
    WHERE task.torrent_id = torrents.id AND task.state = 'succeeded'
    ORDER BY COALESCE(task.finished_at, task.created_at) DESC, task.id DESC LIMIT 1
)`,
			`DROP TABLE ingest_jobs`,
			`ALTER TABLE torrents DROP COLUMN provider_task_status`,
			`ALTER TABLE torrents DROP COLUMN provider_task_update`,
			`ALTER TABLE torrents DROP COLUMN provider_task_progress`,
			`ALTER TABLE torrents DROP COLUMN result_remote_id`,
			`ALTER TABLE torrents DROP COLUMN task_delete_file_id`,
			`ALTER TABLE torrents DROP COLUMN task_wp_path_id`,
			`CREATE INDEX idx_tasks_state ON tasks(state, created_at)`,
			`CREATE INDEX idx_tasks_torrent_state ON tasks(torrent_id, state, finished_at)`,
			`CREATE INDEX idx_tasks_source_state ON tasks(source, state, created_at)`,
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	},
	8: func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"ALTER TABLE ingest_jobs ADD COLUMN category TEXT NOT NULL DEFAULT ''",
		); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `CREATE TABLE download_categories (
    name TEXT PRIMARY KEY,
    save_path TEXT NOT NULL DEFAULT ''
)`)
		return err
	},
	3: func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"ALTER TABLE ingest_jobs ADD COLUMN category TEXT NOT NULL DEFAULT ''",
		); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
CREATE TABLE download_categories (
    name                    TEXT PRIMARY KEY,
    save_path               TEXT NOT NULL DEFAULT ''
)`)
		return err
	},
}

func runMigration(
	ctx context.Context,
	connection *sql.Conn,
	fromVersion int,
	migrate migration,
) (returnError error) {
	if _, err := connection.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return fmt.Errorf("迁移数据库 v%d→v%d 前关闭外键检查: %w",
			fromVersion, fromVersion+1, err)
	}
	defer func() {
		if _, err := connection.ExecContext(ctx, "PRAGMA foreign_keys = ON"); returnError == nil && err != nil {
			returnError = fmt.Errorf("迁移数据库后恢复外键检查: %w", err)
		}
	}()

	transaction, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始迁移数据库 v%d→v%d: %w", fromVersion, fromVersion+1, err)
	}
	defer transaction.Rollback()

	if err := migrate(ctx, transaction); err != nil {
		return fmt.Errorf("迁移数据库 v%d→v%d: %w", fromVersion, fromVersion+1, err)
	}
	if err := checkForeignKeys(ctx, transaction); err != nil {
		return fmt.Errorf("迁移数据库 v%d→v%d 后检查外键: %w",
			fromVersion, fromVersion+1, err)
	}
	if _, err := transaction.ExecContext(
		ctx, fmt.Sprintf("PRAGMA user_version = %d", fromVersion+1),
	); err != nil {
		return fmt.Errorf("记录数据库版本 %d: %w", fromVersion+1, err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("提交数据库迁移 v%d→v%d: %w", fromVersion, fromVersion+1, err)
	}
	return nil
}

func checkForeignKeys(ctx context.Context, transaction *sql.Tx) error {
	rows, err := transaction.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID sql.NullInt64
		var parent string
		var foreignKeyID int
		if err := rows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			return err
		}
		return fmt.Errorf("表 %s 的记录 %v 引用了不存在的 %s 记录（外键 %d）",
			table, rowID, parent, foreignKeyID)
	}
	return rows.Err()
}
