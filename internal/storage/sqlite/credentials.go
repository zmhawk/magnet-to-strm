package sqlite

import (
	"context"
	"database/sql"
	"time"

	"magnet-to-strm/internal/provider/p115"
)

func (d *DB) LoadCredential(ctx context.Context, provider string) (p115.Credential, error) {
	var credential p115.Credential
	var expiresAt sql.NullString
	err := d.sql.QueryRowContext(ctx, `
SELECT access_token, refresh_token, expires_at
FROM credentials WHERE provider = ?
`, provider).Scan(&credential.AccessToken, &credential.RefreshToken, &expiresAt)
	if err == sql.ErrNoRows {
		return p115.Credential{}, p115.ErrCredentialNotFound
	}
	if err != nil {
		return p115.Credential{}, err
	}
	if expiresAt.Valid {
		value, _ := parseTime(expiresAt.String)
		credential.ExpiresAt = &value
	}
	return credential, nil
}

func (d *DB) SeedCredential(
	ctx context.Context,
	provider string,
	credential p115.Credential,
) error {
	_, err := d.sql.ExecContext(ctx, `
INSERT INTO credentials (
    provider, access_token, refresh_token, expires_at, updated_at
) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(provider) DO NOTHING
`, provider, credential.AccessToken, credential.RefreshToken,
		nullableTime(credential.ExpiresAt), formatTime(time.Now()))
	return err
}

func (d *DB) SaveCredential(
	ctx context.Context,
	provider string,
	credential p115.Credential,
) error {
	_, err := d.sql.ExecContext(ctx, `
INSERT INTO credentials (
    provider, access_token, refresh_token, expires_at, updated_at
) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(provider) DO UPDATE SET
    access_token = excluded.access_token,
    refresh_token = excluded.refresh_token,
    expires_at = excluded.expires_at,
    updated_at = excluded.updated_at
`, provider, credential.AccessToken, credential.RefreshToken,
		nullableTime(credential.ExpiresAt), formatTime(time.Now()))
	return err
}

func (d *DB) ReconcileRefreshToken(
	ctx context.Context,
	provider string,
	refreshToken string,
	fingerprint string,
) (bool, error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	markerName := "credential-source:" + provider
	var importedFingerprint string
	err = tx.QueryRowContext(
		ctx,
		"SELECT owner FROM leases WHERE name = ?",
		markerName,
	).Scan(&importedFingerprint)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if err == sql.ErrNoRows {
		_, err = tx.ExecContext(ctx, `
INSERT INTO leases (name, owner, expires_at)
VALUES (?, ?, ?)
ON CONFLICT(name) DO NOTHING
`, markerName, fingerprint, formatTime(time.Date(
			9999, time.December, 31, 23, 59, 59, 0, time.UTC,
		)))
		if err != nil {
			return false, err
		}
		return false, tx.Commit()
	}
	if err == nil && importedFingerprint == fingerprint {
		return false, tx.Commit()
	}

	expired := time.Unix(0, 0).UTC()
	result, err := tx.ExecContext(ctx, `
UPDATE credentials
SET access_token = '', refresh_token = ?, expires_at = ?, updated_at = ?
WHERE provider = ?
`, refreshToken, formatTime(expired), formatTime(time.Now()), provider)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if count != 1 {
		return false, p115.ErrCredentialNotFound
	}

	_, err = tx.ExecContext(ctx, `
INSERT INTO leases (name, owner, expires_at)
VALUES (?, ?, ?)
ON CONFLICT(name) DO UPDATE SET
    owner = excluded.owner,
    expires_at = excluded.expires_at
`, markerName, fingerprint, formatTime(time.Date(
		9999, time.December, 31, 23, 59, 59, 0, time.UTC,
	)))
	if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(
		ctx,
		"DELETE FROM leases WHERE name = ?",
		"credential-refresh:"+provider,
	); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (d *DB) AcquireLease(
	ctx context.Context,
	name string,
	owner string,
	until time.Time,
) (bool, error) {
	now := formatTime(time.Now())
	result, err := d.sql.ExecContext(ctx, `
INSERT INTO leases (name, owner, expires_at)
VALUES (?, ?, ?)
ON CONFLICT(name) DO UPDATE SET
    owner = excluded.owner,
    expires_at = excluded.expires_at
WHERE leases.expires_at < ? OR leases.owner = excluded.owner
`, name, owner, formatTime(until), now)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (d *DB) ReleaseLease(ctx context.Context, name, owner string) error {
	_, err := d.sql.ExecContext(ctx, "DELETE FROM leases WHERE name = ? AND owner = ?", name, owner)
	return err
}
