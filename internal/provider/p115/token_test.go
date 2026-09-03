package p115

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/OpenListTeam/115-sdk-go"

	"magnet-to-strm/internal/config"
)

func TestSanitizedRequestURL(t *testing.T) {
	requestURL, err := url.Parse(
		"https://example.test/files?access_token=secret&cid=123&password=hidden",
	)
	if err != nil {
		t.Fatal(err)
	}
	got := sanitizedRequestURL(requestURL)
	if strings.Contains(got, "secret") || strings.Contains(got, "hidden") {
		t.Fatalf("sensitive query value was logged: %s", got)
	}
	if !strings.Contains(got, "cid=123") ||
		!strings.Contains(got, "access_token=%5BREDACTED%5D") {
		t.Fatalf("sanitized URL = %s", got)
	}
}

func TestRestyClientLogsEveryRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	var log strings.Builder
	client := newRestyClient(&log)
	for range 2 {
		response, err := client.R().Get(server.URL + "/files?cid=123")
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
	}

	if got := strings.Count(log.String(), "115 API: GET "); got != 2 {
		t.Fatalf("request log count = %d, log = %q", got, log.String())
	}
	if !strings.Contains(log.String(), "/files?cid=123") {
		t.Fatalf("request URL missing from log: %q", log.String())
	}
}

type tokenRepositoryStub struct {
	acquiredUntil []time.Time
	releases      int
	saved         []Credential
}

type busyTokenRepository struct {
	tokenRepositoryStub
	credential Credential
}

func (r *busyTokenRepository) LoadCredential(context.Context, string) (Credential, error) {
	return r.credential, nil
}

func (r *busyTokenRepository) AcquireLease(
	context.Context,
	string,
	string,
	time.Time,
) (bool, error) {
	return false, nil
}

func (r *tokenRepositoryStub) LoadCredential(context.Context, string) (Credential, error) {
	return Credential{}, ErrCredentialNotFound
}

func (r *tokenRepositoryStub) SeedCredential(context.Context, string, Credential) error {
	return nil
}

func (r *tokenRepositoryStub) SaveCredential(
	_ context.Context, _ string, credential Credential,
) error {
	r.saved = append(r.saved, credential)
	return nil
}

func (r *tokenRepositoryStub) ReconcileRefreshToken(
	context.Context,
	string,
	string,
	string,
) (bool, error) {
	return false, nil
}

func (r *tokenRepositoryStub) AcquireLease(
	_ context.Context,
	_, _ string,
	until time.Time,
) (bool, error) {
	r.acquiredUntil = append(r.acquiredUntil, until)
	return true, nil
}

func (r *tokenRepositoryStub) ReleaseLease(context.Context, string, string) error {
	r.releases++
	return nil
}

func TestEnsureFreshKeepsCooldownLeaseAfterRefreshFailure(t *testing.T) {
	repository := &tokenRepositoryStub{}
	expired := time.Now().Add(-time.Minute)
	client := &Client{
		credentials:  repository,
		refreshAhead: 10 * time.Minute,
		stderr:       io.Discard,
		owner:        "test-owner",
		credential: Credential{
			AccessToken: "access", RefreshToken: "refresh", ExpiresAt: &expired,
		},
		refresh: func(context.Context) (*sdk.RefreshTokenResp, error) {
			return nil, errors.New("refresh token error")
		},
	}

	startedAt := time.Now()
	err := client.ensureFresh(context.Background())
	if err == nil {
		t.Fatal("ensureFresh() error = nil, want refresh failure")
	}
	if len(repository.acquiredUntil) != 2 {
		t.Fatalf("AcquireLease() calls = %d, want 2", len(repository.acquiredUntil))
	}
	if repository.acquiredUntil[1].Before(startedAt.Add(refreshFailureBackoff - time.Second)) {
		t.Fatalf(
			"cooldown lease expires at %v, want approximately %v",
			repository.acquiredUntil[1],
			startedAt.Add(refreshFailureBackoff),
		)
	}
	if repository.releases != 0 {
		t.Fatalf("ReleaseLease() calls = %d, want 0", repository.releases)
	}
}

func TestNewWithoutCredentialStartsUnavailable(t *testing.T) {
	client, err := New(
		context.Background(),
		&tokenRepositoryStub{},
		config.P115{},
		"",
		io.Discard,
	)
	if err != nil {
		t.Fatal(err)
	}
	if client.Available() {
		t.Fatal("client.Available() = true, want false")
	}
	if err := client.before(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("client.before() error = %v, want ErrUnavailable", err)
	}
}

func TestNewOptionalDegradesWhenRefreshLeaseIsBusy(t *testing.T) {
	expired := time.Now().Add(-time.Minute)
	repository := &busyTokenRepository{credential: Credential{
		AccessToken: "expired-access", RefreshToken: "refresh", ExpiresAt: &expired,
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	client, err := NewOptional(
		ctx,
		repository,
		config.P115{TokenRefreshAhead: time.Minute},
		"",
		io.Discard,
	)
	if err != nil {
		t.Fatal(err)
	}
	if client.Available() {
		t.Fatal("client.Available() = true, want local-only fallback")
	}
	if err := client.before(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("client.before() error = %v, want ErrUnavailable", err)
	}
}

func TestForceRefreshUsesNewRefreshTokenAndPersistsCredential(t *testing.T) {
	repository := &tokenRepositoryStub{}
	oldExpiry := time.Now().Add(time.Hour)
	client := &Client{
		credentials: repository,
		owner:       "test-owner",
		credential: Credential{
			AccessToken: "old-access", RefreshToken: "old-refresh",
			ExpiresAt: &oldExpiry,
		},
		available: true,
		refresh: func(context.Context) (*sdk.RefreshTokenResp, error) {
			return &sdk.RefreshTokenResp{
				AccessToken: "new-access", RefreshToken: "rotated-refresh",
				ExpiresIn: 3600,
			}, nil
		},
	}

	if err := client.ForceRefresh(context.Background(), "  new-refresh  "); err != nil {
		t.Fatal(err)
	}
	if len(repository.saved) != 1 {
		t.Fatalf("SaveCredential() calls = %d, want 1", len(repository.saved))
	}
	saved := repository.saved[0]
	if saved.AccessToken != "new-access" || saved.RefreshToken != "rotated-refresh" {
		t.Fatalf("saved credential = %+v", saved)
	}
	current := client.getCredential()
	if current.AccessToken != saved.AccessToken || current.RefreshToken != saved.RefreshToken {
		t.Fatalf("in-memory credential = %+v, want %+v", current, saved)
	}
	if current.ExpiresAt == nil || !current.ExpiresAt.After(time.Now()) {
		t.Fatalf("in-memory expiry = %v, want future time", current.ExpiresAt)
	}
}

func TestForceRefreshKeepsOldCredentialWhenRefreshFails(t *testing.T) {
	repository := &tokenRepositoryStub{}
	oldExpiry := time.Now().Add(time.Hour)
	old := Credential{
		AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: &oldExpiry,
	}
	client := &Client{
		credentials: repository, owner: "test-owner", credential: old, available: true,
		refresh: func(context.Context) (*sdk.RefreshTokenResp, error) {
			return nil, errors.New("invalid refresh token")
		},
	}

	if err := client.ForceRefresh(context.Background(), "new-refresh"); err == nil {
		t.Fatal("ForceRefresh() error = nil, want refresh failure")
	}
	if len(repository.saved) != 0 {
		t.Fatalf("SaveCredential() calls = %d, want 0", len(repository.saved))
	}
	current := client.getCredential()
	if current.AccessToken != old.AccessToken || current.RefreshToken != old.RefreshToken {
		t.Fatalf("credential after failure = %+v, want %+v", current, old)
	}
}

func TestAPILimiterAllowsBurstThenAppliesAverageRate(t *testing.T) {
	const interval = 50 * time.Millisecond
	limiter := newAPILimiter(20, 2, 2)
	start := make(chan struct{})
	var wait sync.WaitGroup
	var mu sync.Mutex
	var completed []time.Time
	wait.Add(3)
	for range 3 {
		go func() {
			defer wait.Done()
			<-start
			if err := limiter.wait(context.Background()); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			completed = append(completed, time.Now())
			mu.Unlock()
		}()
	}
	close(start)
	wait.Wait()
	sort.Slice(completed, func(i, j int) bool {
		return completed[i].Before(completed[j])
	})
	if len(completed) != 3 {
		t.Fatalf("completed requests = %d, want 3", len(completed))
	}
	if gap := completed[1].Sub(completed[0]); gap > interval/2 {
		t.Fatalf("initial burst gap = %s, want two immediate requests", gap)
	}
	if wait := completed[2].Sub(completed[0]); wait < interval-10*time.Millisecond {
		t.Fatalf("third request waited %s, want approximately %s", wait, interval)
	}
}

func TestAPILimiterCapsConcurrentRequests(t *testing.T) {
	limiter := newAPILimiter(1000, 2, 2)
	if err := limiter.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := limiter.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	blockedCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := limiter.acquire(blockedCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third acquire error = %v, want deadline exceeded", err)
	}
	limiter.release()
	if err := limiter.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	limiter.release()
	limiter.release()
}
