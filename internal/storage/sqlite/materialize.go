package sqlite

import (
	"context"
	"database/sql"
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
       ownership, root_remote_id, materialized_at
FROM remote_locations
WHERE content_id = ? AND provider = 'p115' AND deleted_at IS NULL
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
			&location.PickCode, &location.RemotePath, &location.Ownership,
			&location.RootRemoteID, &materialized,
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
SELECT t.info_hash, t.magnet_uri, t.total_bytes,
       task.task_delete_file_id, task.task_wp_path_id
FROM torrent_files tf
JOIN torrents t ON t.id = tf.torrent_id
JOIN tasks task ON task.id = t.latest_successful_task_id
WHERE tf.content_id = ?
  AND tf.removed_at IS NULL
ORDER BY COALESCE(task.finished_at, task.created_at) DESC,
t.total_bytes ASC,
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
			&source.DeleteFileID, &source.WPPathID,
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
	if location.SourceInfoHash != "" {
		if err := tx.QueryRowContext(ctx,
			"SELECT id FROM torrents WHERE info_hash = ?",
			strings.ToLower(location.SourceInfoHash),
		).Scan(&torrentID); err != nil {
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
    content_id, torrent_id, provider, remote_file_id, remote_parent_id, pick_code, remote_path,
    ownership, root_remote_id, verified_at, materialized_at, created_at, updated_at
) VALUES (?, ?, 'p115', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(provider, remote_file_id) DO UPDATE SET
    content_id = excluded.content_id,
    torrent_id = COALESCE(excluded.torrent_id, remote_locations.torrent_id),
    remote_parent_id = excluded.remote_parent_id,
    pick_code = excluded.pick_code,
    remote_path = excluded.remote_path,
    ownership = CASE
        WHEN remote_locations.ownership = 'managed_cache' THEN 'managed_cache'
        ELSE excluded.ownership
    END,
    root_remote_id = excluded.root_remote_id,
    verified_at = excluded.verified_at,
    materialized_at = excluded.materialized_at,
    deleted_at = NULL,
    updated_at = excluded.updated_at
`, contentID, torrentID, location.RemoteFileID, location.RemoteParentID, location.PickCode,
		location.RemotePath, location.Ownership, location.RootRemoteID,
		now, now, now, now)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) MarkLocationDeleted(ctx context.Context, locationID int64) error {
	now := formatTime(time.Now())
	_, err := d.sql.ExecContext(ctx, `
UPDATE remote_locations SET deleted_at = ?, updated_at = ? WHERE id = ?
`, now, now, locationID)
	return err
}

func (d *DB) MarkSourceLocationsDeleted(ctx context.Context, infoHash string) error {
	now := formatTime(time.Now())
	_, err := d.sql.ExecContext(ctx, `
UPDATE remote_locations
SET deleted_at = ?, updated_at = ?
WHERE provider = 'p115'
  AND ownership = 'managed_cache'
  AND deleted_at IS NULL
  AND torrent_id = (SELECT id FROM torrents WHERE info_hash = ?)
`, now, now, strings.ToLower(infoHash))
	return err
}

func (d *DB) TouchAsset(ctx context.Context, contentID int64) error {
	now := formatTime(time.Now())
	_, err := d.sql.ExecContext(ctx, `
UPDATE content_objects SET last_accessed_at = ?, updated_at = ? WHERE id = ?
`, now, now, contentID)
	return err
}

func (d *DB) ManagedCacheLocations(ctx context.Context) ([]materialize.Asset, error) {
	rows, err := d.sql.QueryContext(ctx, `
SELECT c.id, c.sha1, c.size_bytes, c.preferred_name, c.last_accessed_at,
       c.created_at, l.id, l.remote_file_id, l.remote_parent_id, l.pick_code,
       l.remote_path, l.ownership, l.root_remote_id, l.materialized_at
FROM remote_locations l
JOIN content_objects c ON c.id = l.content_id
WHERE l.provider = 'p115' AND l.ownership = 'managed_cache' AND l.deleted_at IS NULL
ORDER BY COALESCE(c.last_accessed_at, c.created_at), c.id, l.id
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
			&location.Ownership, &location.RootRemoteID, &materialized); err != nil {
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

func (d *DB) ExpiredManagedLocations(
	ctx context.Context,
	cutoff time.Time,
) ([]materialize.Asset, error) {
	rows, err := d.sql.QueryContext(ctx, `
SELECT c.id, c.sha1, c.size_bytes, c.preferred_name,
       l.id, l.remote_file_id, l.remote_parent_id, l.pick_code, l.remote_path,
       l.ownership, l.root_remote_id, l.materialized_at
FROM remote_locations l
JOIN content_objects c ON c.id = l.content_id
WHERE l.provider = 'p115'
  AND l.ownership = 'managed_cache'
  AND l.deleted_at IS NULL
  AND COALESCE(c.last_accessed_at, c.created_at) < ?
  AND NOT EXISTS (
      SELECT 1
      FROM torrent_files owner
      JOIN torrent_files sibling
        ON sibling.torrent_id = owner.torrent_id
       AND sibling.removed_at IS NULL
      JOIN content_objects sibling_content
        ON sibling_content.id = sibling.content_id
      WHERE owner.content_id = c.id
        AND owner.removed_at IS NULL
        AND COALESCE(
            sibling_content.last_accessed_at,
            sibling_content.created_at
        ) >= ?
  )
ORDER BY c.id, l.id
`, formatTime(cutoff), formatTime(cutoff))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	assetsByID := make(map[int64]*materialize.Asset)
	var order []int64
	for rows.Next() {
		var assetID int64
		var sha1Value, name string
		var size int64
		var location materialize.Location
		var materialized sql.NullString
		if err := rows.Scan(
			&assetID, &sha1Value, &size, &name,
			&location.ID, &location.RemoteFileID, &location.RemoteParentID,
			&location.PickCode, &location.RemotePath, &location.Ownership,
			&location.RootRemoteID, &materialized,
		); err != nil {
			return nil, err
		}
		asset := assetsByID[assetID]
		if asset == nil {
			asset = &materialize.Asset{
				ID: assetID, SHA1: sha1Value,
				SizeBytes: size, PreferredName: name,
			}
			assetsByID[assetID] = asset
			order = append(order, assetID)
		}
		if materialized.Valid {
			value, _ := parseTime(materialized.String)
			location.MaterializedAt = &value
		}
		asset.Locations = append(asset.Locations, location)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	sourceRows, err := d.sql.QueryContext(ctx, `
SELECT DISTINCT tf.content_id, t.info_hash, t.magnet_uri, t.total_bytes,
       task.task_delete_file_id, task.task_wp_path_id
FROM torrent_files tf
JOIN torrents t ON t.id = tf.torrent_id
JOIN tasks task ON task.id = t.latest_successful_task_id
WHERE tf.removed_at IS NULL
  AND EXISTS (
      SELECT 1
      FROM remote_locations l
      JOIN content_objects c ON c.id = l.content_id
      WHERE c.id = tf.content_id
        AND l.provider = 'p115'
        AND l.ownership = 'managed_cache'
        AND l.deleted_at IS NULL
        AND COALESCE(c.last_accessed_at, c.created_at) < ?
        AND NOT EXISTS (
            SELECT 1
            FROM torrent_files owner
            JOIN torrent_files sibling
              ON sibling.torrent_id = owner.torrent_id
             AND sibling.removed_at IS NULL
            JOIN content_objects sibling_content
              ON sibling_content.id = sibling.content_id
            WHERE owner.content_id = c.id
              AND owner.removed_at IS NULL
              AND COALESCE(
                  sibling_content.last_accessed_at,
                  sibling_content.created_at
              ) >= ?
        )
  )
ORDER BY tf.content_id, t.info_hash
`, formatTime(cutoff), formatTime(cutoff))
	if err != nil {
		return nil, err
	}
	defer sourceRows.Close()
	for sourceRows.Next() {
		var assetID int64
		var source materialize.Source
		if err := sourceRows.Scan(
			&assetID, &source.InfoHash, &source.MagnetURI, &source.TotalBytes,
			&source.DeleteFileID, &source.WPPathID,
		); err != nil {
			return nil, err
		}
		if asset := assetsByID[assetID]; asset != nil {
			asset.Sources = append(asset.Sources, source)
		}
	}
	if err := sourceRows.Err(); err != nil {
		return nil, err
	}
	var assets []materialize.Asset
	for _, id := range order {
		assets = append(assets, *assetsByID[id])
	}
	return assets, rows.Err()
}
