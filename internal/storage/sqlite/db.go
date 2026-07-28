package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const schemaVersion = 8

type DB struct {
	sql *sql.DB
}

func Open(databasePath string) (*DB, error) {
	if databasePath == "" {
		return nil, fmt.Errorf("数据库路径不能为空")
	}
	dsn := databasePath
	if databasePath == ":memory:" {
		dsn = "file:magnet-to-strm?mode=memory&cache=shared"
	} else {
		absolutePath, err := filepath.Abs(databasePath)
		if err != nil {
			return nil, fmt.Errorf("解析数据库路径: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(absolutePath), 0o755); err != nil {
			return nil, fmt.Errorf("创建数据库目录: %w", err)
		}
		file, err := os.OpenFile(absolutePath, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return nil, fmt.Errorf("创建 SQLite 文件: %w", err)
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
		if err := os.Chmod(absolutePath, 0o600); err != nil {
			return nil, fmt.Errorf("保护 SQLite 文件权限: %w", err)
		}
		dsn = (&url.URL{Scheme: "file", Path: absolutePath}).String()
	}
	separator := "?"
	if len(dsn) > 0 {
		for _, character := range dsn {
			if character == '?' {
				separator = "&"
				break
			}
		}
	}
	dsn += separator +
		"_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"

	handle, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开 SQLite: %w", err)
	}
	handle.SetMaxOpenConns(4)
	handle.SetMaxIdleConns(4)
	db := &DB{sql: handle}
	if err := db.initialize(context.Background()); err != nil {
		handle.Close()
		return nil, err
	}
	return db, nil
}

func (d *DB) Close() error {
	return d.sql.Close()
}

func (d *DB) Ping() error {
	return d.sql.Ping()
}

func (d *DB) initialize(ctx context.Context) error {
	connection, err := d.sql.Conn(ctx)
	if err != nil {
		return fmt.Errorf("获取数据库连接: %w", err)
	}
	defer connection.Close()

	var currentVersion, tableCount int
	if err := connection.QueryRowContext(ctx, "PRAGMA user_version").Scan(&currentVersion); err != nil {
		return fmt.Errorf("读取数据库版本: %w", err)
	}
	if err := connection.QueryRowContext(ctx, `
SELECT count(*) FROM sqlite_master
WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
`).Scan(&tableCount); err != nil {
		return fmt.Errorf("检查数据库结构: %w", err)
	}
	if currentVersion > schemaVersion {
		return fmt.Errorf(
			"数据库结构版本 %d 高于当前程序支持的版本 %d，请升级程序",
			currentVersion, schemaVersion,
		)
	}
	if tableCount == 0 {
		return initializeSchema(ctx, connection)
	}
	if currentVersion == 0 {
		return fmt.Errorf("数据库已有表但未记录结构版本，无法安全打开")
	}
	if currentVersion < schemaVersion {
		return fmt.Errorf(
			"数据库结构版本 %d 低于当前程序的基线版本 %d，不支持升级",
			currentVersion, schemaVersion,
		)
	}
	return nil
}

const schema = `
CREATE TABLE IF NOT EXISTS torrents (
    id                      INTEGER PRIMARY KEY,
    info_hash               TEXT NOT NULL UNIQUE
                                CHECK(length(info_hash) = 40 AND info_hash = lower(info_hash)),
    magnet_uri              TEXT NOT NULL,
    display_name            TEXT NOT NULL DEFAULT '',
    provider_task_status    INTEGER NOT NULL DEFAULT 0,
    provider_task_update    INTEGER NOT NULL DEFAULT 0,
    provider_task_progress  REAL NOT NULL DEFAULT 0
                                CHECK(provider_task_progress >= 0 AND provider_task_progress <= 100),
    result_remote_id        TEXT NOT NULL DEFAULT '',
    task_delete_file_id     TEXT NOT NULL DEFAULT '',
    task_wp_path_id         TEXT NOT NULL DEFAULT '',
    total_bytes             INTEGER NOT NULL DEFAULT 0 CHECK(total_bytes >= 0),
    file_count              INTEGER NOT NULL DEFAULT 0 CHECK(file_count >= 0),
    strm_root               TEXT NOT NULL DEFAULT '',
    created_at              TEXT NOT NULL,
    updated_at              TEXT NOT NULL,
    scanned_at              TEXT
);

CREATE TABLE IF NOT EXISTS ingest_jobs (
    gid                     TEXT PRIMARY KEY,
    info_hash               TEXT NOT NULL,
    magnet_uri              TEXT NOT NULL,
    state                   TEXT NOT NULL
                                CHECK(state IN ('queued', 'running', 'succeeded', 'failed', 'canceled')),
    error_message           TEXT NOT NULL DEFAULT '',
    created_at              TEXT NOT NULL,
    started_at              TEXT,
    finished_at             TEXT
);

CREATE TABLE IF NOT EXISTS content_objects (
    id                      INTEGER PRIMARY KEY,
    sha1                    TEXT NOT NULL UNIQUE
                                CHECK(length(sha1) = 40 AND sha1 = lower(sha1)),
    size_bytes              INTEGER NOT NULL CHECK(size_bytes >= 0),
    preferred_name          TEXT NOT NULL,
    last_accessed_at        TEXT,
    created_at              TEXT NOT NULL,
    updated_at              TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS torrent_files (
    id                      INTEGER PRIMARY KEY,
    torrent_id              INTEGER NOT NULL REFERENCES torrents(id) ON DELETE CASCADE,
    content_id              INTEGER NOT NULL REFERENCES content_objects(id),
    relative_path           TEXT NOT NULL,
    source_created_at       INTEGER NOT NULL DEFAULT 0,
    source_updated_at       INTEGER NOT NULL DEFAULT 0,
    strm_seeded_at          TEXT,
    last_seen_at            TEXT NOT NULL,
    removed_at              TEXT,
    UNIQUE(torrent_id, relative_path)
);

CREATE TABLE IF NOT EXISTS remote_locations (
    id                      INTEGER PRIMARY KEY,
    content_id              INTEGER NOT NULL REFERENCES content_objects(id) ON DELETE CASCADE,
    torrent_id              INTEGER REFERENCES torrents(id) ON DELETE CASCADE,
    provider                TEXT NOT NULL,
    remote_file_id          TEXT NOT NULL,
    remote_parent_id        TEXT NOT NULL DEFAULT '',
    pick_code               TEXT NOT NULL DEFAULT '',
    remote_path             TEXT NOT NULL DEFAULT '',
    ownership               TEXT NOT NULL
                                CHECK(ownership IN ('managed_cache', 'external')),
    root_remote_id          TEXT NOT NULL DEFAULT '',
    verified_at             TEXT,
    materialized_at         TEXT,
    deleted_at              TEXT,
    created_at              TEXT NOT NULL,
    updated_at              TEXT NOT NULL,
    UNIQUE(provider, remote_file_id)
);

CREATE TABLE IF NOT EXISTS credentials (
    provider                TEXT PRIMARY KEY,
    access_token            TEXT NOT NULL,
    refresh_token           TEXT NOT NULL,
    expires_at              TEXT,
    updated_at              TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS leases (
    name                    TEXT PRIMARY KEY,
    owner                   TEXT NOT NULL,
    expires_at              TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_torrent_files_content ON torrent_files(content_id);
CREATE INDEX IF NOT EXISTS idx_torrent_files_active ON torrent_files(torrent_id, removed_at);
CREATE INDEX IF NOT EXISTS idx_remote_locations_content ON remote_locations(content_id, deleted_at);
CREATE INDEX IF NOT EXISTS idx_remote_locations_torrent_content
    ON remote_locations(torrent_id, content_id, deleted_at);
CREATE INDEX IF NOT EXISTS idx_ingest_jobs_state ON ingest_jobs(state, created_at);
CREATE INDEX IF NOT EXISTS idx_ingest_jobs_info_hash_state
    ON ingest_jobs(info_hash, state, finished_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_torrents_strm_root
    ON torrents(strm_root) WHERE strm_root <> '';

PRAGMA user_version = 8;
`

func initializeSchema(ctx context.Context, connection *sql.Conn) error {
	transaction, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始初始化数据库: %w", err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("初始化数据库: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("提交数据库初始化: %w", err)
	}
	return nil
}
