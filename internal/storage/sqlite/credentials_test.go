package sqlite

import (
	"context"
	"testing"
	"time"

	"magnet-to-strm/internal/provider/p115"
)

func TestReconcileRefreshTokenReplacesCredentialWhenEnvironmentChanges(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	expiry := time.Now().Add(time.Hour)
	if err := db.SeedCredential(ctx, "p115", p115.Credential{
		AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: &expiry,
	}); err != nil {
		t.Fatal(err)
	}
	replaced, err := db.ReconcileRefreshToken(ctx, "p115", "initial", "hash-1")
	if err != nil || replaced {
		t.Fatalf("initial baseline: replaced=%v err=%v", replaced, err)
	}
	credential, err := db.LoadCredential(ctx, "p115")
	if err != nil {
		t.Fatal(err)
	}
	if credential.AccessToken != "old-access" ||
		credential.RefreshToken != "old-refresh" {
		t.Fatalf("initial baseline changed credential: %+v", credential)
	}

	rotatedExpiry := time.Now().Add(2 * time.Hour)
	if err := db.SaveCredential(ctx, "p115", p115.Credential{
		AccessToken:  "rotated-access",
		RefreshToken: "rotated-refresh",
		ExpiresAt:    &rotatedExpiry,
	}); err != nil {
		t.Fatal(err)
	}
	replaced, err = db.ReconcileRefreshToken(ctx, "p115", "initial", "hash-1")
	if err != nil || replaced {
		t.Fatalf("unchanged environment: replaced=%v err=%v", replaced, err)
	}
	credential, err = db.LoadCredential(ctx, "p115")
	if err != nil {
		t.Fatal(err)
	}
	if credential.RefreshToken != "rotated-refresh" {
		t.Fatalf("rotated refresh token was overwritten: %+v", credential)
	}

	acquired, err := db.AcquireLease(
		ctx,
		"credential-refresh:p115",
		"old-owner",
		time.Now().Add(time.Hour),
	)
	if err != nil || !acquired {
		t.Fatalf("seed refresh lease: acquired=%v err=%v", acquired, err)
	}
	replaced, err = db.ReconcileRefreshToken(ctx, "p115", "replacement", "hash-2")
	if err != nil || !replaced {
		t.Fatalf("changed environment: replaced=%v err=%v", replaced, err)
	}
	credential, err = db.LoadCredential(ctx, "p115")
	if err != nil {
		t.Fatal(err)
	}
	if credential.AccessToken != "" ||
		credential.RefreshToken != "replacement" ||
		credential.ExpiresAt == nil ||
		!credential.ExpiresAt.Equal(time.Unix(0, 0).UTC()) {
		t.Fatalf("changed refresh token was not imported: %+v", credential)
	}
	var refreshLeaseCount int
	if err := db.sql.QueryRow(
		"SELECT count(*) FROM leases WHERE name = 'credential-refresh:p115'",
	).Scan(&refreshLeaseCount); err != nil {
		t.Fatal(err)
	}
	if refreshLeaseCount != 0 {
		t.Fatal("refresh cooldown lease was not cleared")
	}
}
