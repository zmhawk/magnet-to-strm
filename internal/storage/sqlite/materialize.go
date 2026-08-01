package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"magnet-to-strm/internal/materialize"
)

func (d *DB) AssetBySHA1(ctx context.Context, sha1Value string) (materialize.Asset, error) {
	var asset materialize.Asset
	var lastAccessed sql.NullString
	var createdAt, updatedAt string
	err := d.sql.QueryRowContext(ctx, `
SELECT c.id, c.sha1, c.size_bytes, c.preferred_name, c.last_accessed_at,
       c.created_at, c.updated_at
FROM content_objects c WHERE c.sha1 = ?
`, strings.ToLower(sha1Value)).Scan(
		&asset.ID, &asset.SHA1, &asset.SizeBytes,
		&asset.PreferredName, &lastAccessed, &createdAt, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return materialize.Asset{}, materialize.ErrAssetNotFound
	}
	if err != nil {
		return materialize.Asset{}, err
	}
	if lastAccessed.Valid {
		value, _ := parseTime(lastAccessed.String)
		asset.LastAccessedAt = &value
	}
	asset.CreatedAt, _ = parseTime(createdAt)
	asset.UpdatedAt, _ = parseTime(updatedAt)
	rows, err := d.sql.QueryContext(ctx, `
SELECT id, remote_file_id, remote_parent_id, pick_code, remote_path,
       ownership, materialized_at
FROM remote_locations
WHERE content_id = ? AND provider = 'p115' AND deleted_at IS NULL
  AND (
      ownership = 'external' OR EXISTS (
          SELECT 1 FROM managed_artifacts artifact
          WHERE artifact.id = remote_locations.artifact_id
            AND artifact.state = 'active'
      )
  )
ORDER BY CASE ownership WHEN 'external' THEN 0 ELSE 1 END,
         verified_at DESC, id DESC
`, asset.ID)
	if err != nil {
		return materialize.Asset{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var location materialize.Location
		var materialized sql.NullString
		if err := rows.Scan(
			&location.ID, &location.RemoteFileID, &location.RemoteParentID,
			&location.PickCode, &location.RemotePath, &location.Ownership, &materialized,
		); err != nil {
			return materialize.Asset{}, err
		}
		if materialized.Valid {
			value, _ := parseTime(materialized.String)
			location.MaterializedAt = &value
		}
		asset.Locations = append(asset.Locations, location)
	}
	if err := rows.Err(); err != nil {
		return materialize.Asset{}, err
	}
	sourceRows, err := d.sql.QueryContext(ctx, `
SELECT DISTINCT t.info_hash, t.magnet_uri, t.total_bytes
FROM torrent_files tf
JOIN torrents t ON t.id = tf.torrent_id
WHERE tf.content_id = ?
  AND tf.removed_at IS NULL
ORDER BY COALESCE(t.scanned_at, t.updated_at) DESC, t.total_bytes ASC,
t.info_hash ASC
`, asset.ID)
	if err != nil {
		return materialize.Asset{}, err
	}
	defer sourceRows.Close()
	for sourceRows.Next() {
		var source materialize.Source
		if err := sourceRows.Scan(
			&source.InfoHash, &source.MagnetURI, &source.TotalBytes,
		); err != nil {
			return materialize.Asset{}, err
		}
		asset.Sources = append(asset.Sources, source)
	}
	return asset, sourceRows.Err()
}

func (d *DB) AssetsBySHA1Prefix(
	ctx context.Context,
	prefix string,
) ([]materialize.Asset, error) {
	rows, err := d.sql.QueryContext(ctx, `
SELECT id, sha1, size_bytes, preferred_name, last_accessed_at,
       created_at, updated_at
FROM content_objects
WHERE sha1 LIKE ? || '%'
ORDER BY sha1
`, strings.ToLower(prefix))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var assets []materialize.Asset
	for rows.Next() {
		var asset materialize.Asset
		var lastAccessed sql.NullString
		var createdAt, updatedAt string
		if err := rows.Scan(
			&asset.ID, &asset.SHA1, &asset.SizeBytes,
			&asset.PreferredName, &lastAccessed, &createdAt, &updatedAt,
		); err != nil {
			return nil, err
		}
		if lastAccessed.Valid {
			value, _ := parseTime(lastAccessed.String)
			asset.LastAccessedAt = &value
		}
		asset.CreatedAt, _ = parseTime(createdAt)
		asset.UpdatedAt, _ = parseTime(updatedAt)
		assets = append(assets, asset)
	}
	return assets, rows.Err()
}

func (d *DB) SHA1Prefixes(
	ctx context.Context,
	prefix string,
	length int,
) ([]string, error) {
	rows, err := d.sql.QueryContext(ctx, `
SELECT DISTINCT substr(sha1, 1, ?)
FROM content_objects
WHERE sha1 LIKE ? || '%'
ORDER BY 1
`, length, strings.ToLower(prefix))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var prefixes []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		prefixes = append(prefixes, value)
	}
	return prefixes, rows.Err()
}

func (d *DB) SaveLocation(
	ctx context.Context,
	contentID int64,
	location materialize.Location,
) error {
	now := formatTime(time.Now())
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var torrentID sql.NullInt64
	var artifactID sql.NullInt64
	if location.SourceInfoHash != "" {
		if err := tx.QueryRowContext(ctx,
			"SELECT id FROM torrents WHERE info_hash = ?",
			strings.ToLower(location.SourceInfoHash),
		).Scan(&torrentID); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
SELECT id FROM managed_artifacts
WHERE torrent_id = ? AND provider = 'p115' AND state = 'active'
ORDER BY id DESC LIMIT 1
`, torrentID.Int64).Scan(&artifactID); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE remote_locations
SET deleted_at = ?, updated_at = ?
WHERE content_id = ?
  AND provider = 'p115'
  AND ownership = 'managed_cache'
  AND deleted_at IS NULL
  AND (torrent_id = ? OR torrent_id IS NULL)
  AND remote_file_id <> ?
`, now, now, contentID, torrentID.Int64, location.RemoteFileID); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO remote_locations (
    content_id, torrent_id, artifact_id, provider, remote_file_id, remote_parent_id, pick_code, remote_path,
    ownership, verified_at, materialized_at, created_at, updated_at
) VALUES (?, ?, ?, 'p115', ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(provider, remote_file_id) DO UPDATE SET
    content_id = excluded.content_id,
    torrent_id = COALESCE(excluded.torrent_id, remote_locations.torrent_id),
    artifact_id = COALESCE(excluded.artifact_id, remote_locations.artifact_id),
    remote_parent_id = excluded.remote_parent_id,
    pick_code = excluded.pick_code,
    remote_path = excluded.remote_path,
    ownership = CASE
        WHEN remote_locations.ownership = 'managed_cache' THEN 'managed_cache'
        ELSE excluded.ownership
    END,
    verified_at = excluded.verified_at,
    materialized_at = excluded.materialized_at,
    deleted_at = NULL,
    updated_at = excluded.updated_at
`, contentID, torrentID, artifactID, location.RemoteFileID, location.RemoteParentID, location.PickCode,
		location.RemotePath, location.Ownership, now, now, now, now)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) MarkLocationDeleted(ctx context.Context, locationID int64) error {
	now := formatTime(time.Now())
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
UPDATE remote_locations SET deleted_at = ?, updated_at = ? WHERE id = ?
`, now, now, locationID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM managed_artifacts
WHERE state IN ('active', 'orphaned')
  AND id = (SELECT artifact_id FROM remote_locations WHERE id = ?)
  AND NOT EXISTS (
      SELECT 1 FROM remote_locations location
      WHERE location.artifact_id = managed_artifacts.id
        AND location.deleted_at IS NULL
  )
`, locationID); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) MarkArtifactDeleted(ctx context.Context, artifactID int64) error {
	if artifactID == 0 {
		return nil
	}
	_, err := d.sql.ExecContext(ctx, "DELETE FROM managed_artifacts WHERE id = ?", artifactID)
	return err
}

func (d *DB) MarkSourceLocationsDeleted(ctx context.Context, infoHash string) error {
	now := formatTime(time.Now())
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
UPDATE remote_locations
SET deleted_at = ?, updated_at = ?
WHERE provider = 'p115'
  AND ownership = 'managed_cache'
  AND deleted_at IS NULL
  AND artifact_id IN (
      SELECT artifact.id FROM managed_artifacts artifact
      JOIN torrents torrent ON torrent.id = artifact.torrent_id
      WHERE torrent.info_hash = ? AND artifact.state IN ('active', 'orphaned')
  )
`, now, now, strings.ToLower(infoHash)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM managed_artifacts
WHERE state IN ('active', 'orphaned') AND torrent_id = (
    SELECT id FROM torrents WHERE info_hash = ?
)
`, strings.ToLower(infoHash)); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) TouchAsset(ctx context.Context, contentID int64) error {
	now := formatTime(time.Now())
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
UPDATE content_objects SET last_accessed_at = ?, updated_at = ? WHERE id = ?
`, now, now, contentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE managed_artifacts
SET last_accessed_at = ?, updated_at = ?
WHERE state = 'active' AND id IN (
    SELECT artifact_id FROM remote_locations
    WHERE content_id = ? AND deleted_at IS NULL AND artifact_id IS NOT NULL
)
`, now, now, contentID); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) ManagedCacheLocations(ctx context.Context) ([]materialize.Asset, error) {
	rows, err := d.sql.QueryContext(ctx, `
SELECT c.id, c.sha1, c.size_bytes, c.preferred_name, c.last_accessed_at,
       c.created_at, l.id, l.remote_file_id, l.remote_parent_id, l.pick_code,
       l.remote_path, l.ownership, l.materialized_at
FROM remote_locations l
JOIN content_objects c ON c.id = l.content_id
JOIN managed_artifacts artifact ON artifact.id = l.artifact_id
WHERE l.provider = 'p115' AND l.ownership = 'managed_cache' AND l.deleted_at IS NULL
  AND artifact.state = 'active'
ORDER BY COALESCE(artifact.last_accessed_at, artifact.created_at), c.id, l.id
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []materialize.Asset
	for rows.Next() {
		var asset materialize.Asset
		var lastAccessed, createdAt, materialized sql.NullString
		var location materialize.Location
		if err := rows.Scan(&asset.ID, &asset.SHA1, &asset.SizeBytes, &asset.PreferredName,
			&lastAccessed, &createdAt, &location.ID, &location.RemoteFileID,
			&location.RemoteParentID, &location.PickCode, &location.RemotePath,
			&location.Ownership, &materialized); err != nil {
			return nil, err
		}
		if lastAccessed.Valid {
			value, _ := parseTime(lastAccessed.String)
			asset.LastAccessedAt = &value
		}
		asset.CreatedAt, _ = parseTime(createdAt.String)
		if materialized.Valid {
			value, _ := parseTime(materialized.String)
			location.MaterializedAt = &value
		}
		asset.Locations = []materialize.Location{location}
		result = append(result, asset)
	}
	return result, rows.Err()
}

func (d *DB) ManagedCacheArtifacts(ctx context.Context) ([]materialize.CacheArtifact, error) {
	rows, err := d.sql.QueryContext(ctx, `
SELECT artifact.id, artifact.result_remote_id, artifact.last_accessed_at,
       artifact.created_at
FROM managed_artifacts artifact
WHERE artifact.provider = 'p115'
  AND artifact.state = 'active'
  AND EXISTS (
      SELECT 1 FROM remote_locations location
      WHERE location.artifact_id = artifact.id
        AND location.ownership = 'managed_cache'
        AND location.deleted_at IS NULL
  )
ORDER BY COALESCE(artifact.last_accessed_at, artifact.created_at), artifact.id
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []materialize.CacheArtifact
	for rows.Next() {
		var artifact materialize.CacheArtifact
		var lastAccessed sql.NullString
		var createdAt string
		if err := rows.Scan(&artifact.ID, &artifact.ResultRemoteID, &lastAccessed, &createdAt); err != nil {
			return nil, err
		}
		if lastAccessed.Valid {
			value, _ := parseTime(lastAccessed.String)
			artifact.LastAccessedAt = &value
		}
		artifact.CreatedAt, _ = parseTime(createdAt)
		result = append(result, artifact)
	}
	return result, rows.Err()
}

func (d *DB) ExpiredManagedLocations(
	ctx context.Context,
	cutoff time.Time,
) ([]materialize.Asset, error) {
	rows, err := d.sql.QueryContext(ctx, `
SELECT c.id, c.sha1, c.size_bytes, c.preferred_name,
       l.id, l.remote_file_id, l.remote_parent_id, l.pick_code, l.remote_path,
       l.ownership, l.materialized_at,
       artifact.id, artifact.state, torrent.info_hash, torrent.magnet_uri, torrent.total_bytes,
       artifact.delete_file_id, artifact.work_dir_remote_id
FROM managed_artifacts artifact
JOIN torrents torrent ON torrent.id = artifact.torrent_id
JOIN remote_locations l ON l.artifact_id = artifact.id
JOIN content_objects c ON c.id = l.content_id
WHERE artifact.provider = 'p115'
  AND artifact.state IN ('active', 'orphaned')
  AND l.ownership = 'managed_cache'
  AND l.deleted_at IS NULL
  AND COALESCE(artifact.last_accessed_at, artifact.created_at) < ?
ORDER BY artifact.id, c.id, l.id
`, formatTime(cutoff))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var assets []materialize.Asset
	for rows.Next() {
		var asset materialize.Asset
		var location materialize.Location
		var source materialize.Source
		var materialized sql.NullString
		if err := rows.Scan(
			&asset.ID, &asset.SHA1, &asset.SizeBytes, &asset.PreferredName,
			&location.ID, &location.RemoteFileID, &location.RemoteParentID,
			&location.PickCode, &location.RemotePath, &location.Ownership,
			&materialized,
			&source.ArtifactID, &source.ArtifactState, &source.InfoHash, &source.MagnetURI,
			&source.TotalBytes, &source.DeleteFileID, &source.WPPathID,
		); err != nil {
			return nil, err
		}
		if materialized.Valid {
			value, _ := parseTime(materialized.String)
			location.MaterializedAt = &value
		}
		asset.Locations = []materialize.Location{location}
		asset.Sources = []materialize.Source{source}
		assets = append(assets, asset)
	}
	return assets, rows.Err()
}
