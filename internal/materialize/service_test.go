package materialize

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConcurrentRedirectRestoresOfflineTaskOncePerSHA1(t *testing.T) {
	const sha1Value = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	repository := &fakeRepository{asset: Asset{
		ID: 1, SHA1: sha1Value, SizeBytes: 100, PreferredName: "video.mkv",
		Sources: []Source{{
			InfoHash:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			MagnetURI: "magnet:?xt=urn:btih:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		}},
	}}
	provider := &fakeProvider{}
	restorer := &fakeRestorer{}
	redirectBase, _ := url.Parse("http://files.test/content")
	service := &Service{
		Provider: provider, Repository: repository, WorkDirID: "work",
		Restorer: restorer, RedirectBaseURL: redirectBase,
	}

	var wait sync.WaitGroup
	errorsChannel := make(chan error, 2)
	results := make(chan string, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := service.Redirect(context.Background(), sha1Value)
			results <- result
			errorsChannel <- err
		}()
	}
	wait.Wait()
	close(errorsChannel)
	close(results)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	for result := range results {
		if result != "http://files.test/content/work/video.mkv" {
			t.Fatalf("unexpected redirect %q", result)
		}
	}
	if restores := restorer.restoreCount(); restores != 1 {
		t.Fatalf("offline restoration ran %d times", restores)
	}
}

func TestCanceledRedirectDoesNotCancelSharedRestoration(t *testing.T) {
	const sha1Value = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	repository := &fakeRepository{asset: Asset{
		ID: 1, SHA1: sha1Value, SizeBytes: 100, PreferredName: "video.mkv",
		Sources: []Source{{
			InfoHash:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			MagnetURI: "magnet:?xt=urn:btih:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		}},
	}}
	restorer := &controlledRestorer{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	redirectBase, _ := url.Parse("http://files.test/content")
	service := &Service{
		Provider: &fakeProvider{}, Repository: repository, WorkDirID: "work",
		Restorer: restorer, RedirectBaseURL: redirectBase,
		CacheTTL: time.Minute, OperationTimeout: time.Second,
	}

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	firstResult := make(chan error, 1)
	go func() {
		_, err := service.Redirect(requestCtx, sha1Value)
		firstResult <- err
	}()
	<-restorer.started
	cancelRequest()
	if err := <-firstResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("first redirect error = %v, want context canceled", err)
	}

	secondResult := make(chan struct {
		url string
		err error
	}, 1)
	go func() {
		redirectURL, err := service.Redirect(context.Background(), sha1Value)
		secondResult <- struct {
			url string
			err error
		}{redirectURL, err}
	}()
	close(restorer.release)
	result := <-secondResult
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.url != "http://files.test/content/work/video.mkv" {
		t.Fatalf("unexpected redirect %q", result.url)
	}
	if restores := restorer.restoreCount(); restores != 1 {
		t.Fatalf("offline restoration ran %d times", restores)
	}

	if _, err := service.Redirect(context.Background(), sha1Value); err != nil {
		t.Fatal(err)
	}
	if restores := restorer.restoreCount(); restores != 1 {
		t.Fatalf("cached redirect started restoration %d times", restores)
	}
}

func TestBackgroundRestorationHonorsOperationTimeout(t *testing.T) {
	const sha1Value = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	repository := &fakeRepository{asset: Asset{
		ID: 1, SHA1: sha1Value, PreferredName: "video.mkv",
		Sources: []Source{{
			InfoHash:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			MagnetURI: "magnet:timeout",
		}},
	}}
	restorer := &controlledRestorer{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	redirectBase, _ := url.Parse("http://files.test/content")
	service := &Service{
		Provider: &fakeProvider{}, Repository: repository,
		Restorer: restorer, RedirectBaseURL: redirectBase,
		OperationTimeout: 20 * time.Millisecond,
	}

	_, err := service.Redirect(context.Background(), sha1Value)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("redirect error = %v, want deadline exceeded", err)
	}
}

func TestRedirectPrefersRequestedSourceAndFallsBack(t *testing.T) {
	const (
		sha1Value  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		firstHash  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		secondHash = "cccccccccccccccccccccccccccccccccccccccc"
	)
	repository := &fakeRepository{asset: Asset{
		ID: 1, SHA1: sha1Value, PreferredName: "video.mkv",
		Sources: []Source{
			{InfoHash: firstHash, MagnetURI: "magnet:first"},
			{InfoHash: secondHash, MagnetURI: "magnet:second"},
		},
	}}
	restorer := &fakeRestorer{errorsByMagnet: map[string]error{
		"magnet:second": errors.New("second failed"),
	}}
	redirectBase, _ := url.Parse("http://files.test/content")
	service := &Service{
		Provider: &fakeProvider{}, Repository: repository, WorkDirID: "work",
		Restorer: restorer, RedirectBaseURL: redirectBase,
	}

	if _, err := service.RedirectFrom(
		context.Background(), sha1Value, secondHash,
	); err != nil {
		t.Fatal(err)
	}
	if got, want := restorer.restoredMagnets(), []string{"magnet:second", "magnet:first"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("restored magnets = %v, want %v", got, want)
	}
}

func TestRedirectRejectsUnassociatedPreferredSourceWhenRestoreNeeded(t *testing.T) {
	const sha1Value = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	repository := &fakeRepository{asset: Asset{
		ID: 1, SHA1: sha1Value, PreferredName: "video.mkv",
		Sources: []Source{{
			InfoHash:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			MagnetURI: "magnet:first",
		}},
	}}
	redirectBase, _ := url.Parse("http://files.test/content")
	service := &Service{
		Provider: &fakeProvider{}, Repository: repository, WorkDirID: "work",
		Restorer: &fakeRestorer{}, RedirectBaseURL: redirectBase,
	}

	_, err := service.RedirectFrom(
		context.Background(), sha1Value,
		"cccccccccccccccccccccccccccccccccccccccc",
	)
	if err == nil || !strings.Contains(err.Error(), "未关联内容") {
		t.Fatalf("error = %v, want unassociated source error", err)
	}
}

func TestRedirectRestoresImmediatelyWhenStoredFIDIsMissing(t *testing.T) {
	const sha1Value = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	repository := &fakeRepository{asset: Asset{
		ID: 1, SHA1: sha1Value, PreferredName: "video.mkv",
		Locations: []Location{{
			ID: 1, RemoteFileID: "missing-fid", Ownership: "managed_cache",
		}},
		Sources: []Source{{
			InfoHash:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			MagnetURI: "magnet:restore",
		}},
	}}
	restorer := &fakeRestorer{}
	redirectBase, _ := url.Parse("http://files.test/content")
	service := &Service{
		Provider: &missingFIDProvider{}, Repository: repository, WorkDirID: "work",
		Restorer: restorer, RedirectBaseURL: redirectBase,
	}

	if _, err := service.Redirect(context.Background(), sha1Value); err != nil {
		t.Fatal(err)
	}
	if restores := restorer.restoreCount(); restores != 1 {
		t.Fatalf("offline restoration ran %d times, want 1", restores)
	}
}

func TestStableDAVRedirectUsesContentAddressedPath(t *testing.T) {
	const sha1Value = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	repository := &fakeRepository{asset: Asset{
		ID: 1, SHA1: sha1Value, PreferredName: "Video.MKV",
		Locations: []Location{{
			ID: 1, RemoteFileID: "remote", RemotePath: "/temporary/video.mkv",
			Ownership: "external",
		}},
	}}
	redirectBase, _ := url.Parse("http://rclone.test/cache")
	service := &Service{
		Provider: &fakeProvider{}, Repository: repository,
		WorkDirID: "work", RedirectBaseURL: redirectBase,
		RedirectType: RedirectTypeStableDAV,
	}

	got, err := service.Redirect(context.Background(), sha1Value)
	if err != nil {
		t.Fatal(err)
	}
	want := "http://rclone.test/cache/objects/aa/aa/" + sha1Value + ".mkv"
	if got != want {
		t.Fatalf("redirect = %q, want %q", got, want)
	}
}

func TestRemotePathPrefersExplicitScannedPath(t *testing.T) {
	info := RemoteFile{
		Name:       "video.mkv",
		RemotePath: "/work/Season 1/video.mkv",
	}
	if got, want := remotePath(info), "/work/Season 1/video.mkv"; got != want {
		t.Fatalf("remotePath = %q, want %q", got, want)
	}
}

func TestRedirectCacheUsesSlidingExpiration(t *testing.T) {
	const sha1Value = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	repository := &fakeRepository{asset: Asset{
		ID: 1, SHA1: sha1Value, PreferredName: "video.mkv",
		Locations: []Location{{
			ID: 1, RemoteFileID: "remote", Ownership: "external",
		}},
	}}
	provider := &fakeProvider{}
	redirectBase, _ := url.Parse("http://files.test/content")
	now := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)
	service := &Service{
		Provider: provider, Repository: repository, WorkDirID: "work",
		RedirectBaseURL: redirectBase, CacheTTL: 10 * time.Minute,
		Now: func() time.Time { return now },
	}

	for _, advance := range []time.Duration{0, 9 * time.Minute, 9 * time.Minute} {
		now = now.Add(advance)
		if _, err := service.Redirect(context.Background(), sha1Value); err != nil {
			t.Fatal(err)
		}
	}
	if calls := provider.fileInfoCount(); calls != 1 {
		t.Fatalf("FileInfo called %d times within sliding TTL, want 1", calls)
	}
	now = now.Add(11 * time.Minute)
	if _, err := service.Redirect(context.Background(), sha1Value); err != nil {
		t.Fatal(err)
	}
	if calls := provider.fileInfoCount(); calls != 2 {
		t.Fatalf("FileInfo called %d times after idle expiry, want 2", calls)
	}
}

func TestSharedResolutionCacheFormatsDifferentTargetsWithoutRevalidation(t *testing.T) {
	const sha1Value = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	repository := &fakeRepository{asset: Asset{
		ID: 1, SHA1: sha1Value, PreferredName: "video.mkv",
		Locations: []Location{{
			ID: 1, RemoteFileID: "remote", Ownership: "external",
		}},
	}}
	provider := &fakeProvider{}
	cache := &ResolutionCache{}
	rcloneBase, _ := url.Parse("http://rclone.test")
	upstreamBase, _ := url.Parse("http://files.test/content")
	stable := &Service{
		Provider: provider, Repository: repository, WorkDirID: "work",
		RedirectBaseURL: rcloneBase, RedirectType: RedirectTypeStableDAV,
		CacheTTL: time.Minute, ResolutionCache: cache,
	}
	direct := &Service{
		Provider: provider, Repository: repository, WorkDirID: "work",
		RedirectBaseURL: upstreamBase, RedirectType: RedirectTypeDirect,
		CacheTTL: time.Minute, ResolutionCache: cache,
	}

	var stableTarget, directTarget string
	var stableErr, directErr error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		stableTarget, stableErr = stable.Redirect(context.Background(), sha1Value)
	}()
	go func() {
		defer wait.Done()
		directTarget, directErr = direct.Redirect(context.Background(), sha1Value)
	}()
	wait.Wait()
	if stableErr != nil || directErr != nil {
		t.Fatalf("redirect errors: stable=%v direct=%v", stableErr, directErr)
	}
	if stableTarget != "http://rclone.test/objects/aa/aa/"+sha1Value+".mkv" {
		t.Fatalf("unexpected stable target %q", stableTarget)
	}
	if directTarget != "http://files.test/content/work/video.mkv" {
		t.Fatalf("unexpected direct target %q", directTarget)
	}
	if calls := provider.fileInfoCount(); calls != 1 {
		t.Fatalf("shared cache performed %d confirmations, want 1", calls)
	}
}

func TestCleanupPrefersDeletingOfflineTaskWithItsSource(t *testing.T) {
	const infoHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	repository := &cleanupRepository{assets: []Asset{{
		ID: 1,
		Locations: []Location{{
			ID: 10, RemoteFileID: "remote", Ownership: "managed_cache",
		}},
		Sources: []Source{{InfoHash: infoHash}},
	}}}
	provider := &cleanupProvider{missing: map[string]bool{"remote": true}}
	tasks := &cleanupOfflineTasks{found: true}
	service := &Service{
		Provider: provider, OfflineTasks: tasks,
		Repository: repository, WorkDirID: "work",
	}

	if err := service.Cleanup(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if got, want := tasks.hashes, []string{infoHash}; !reflect.DeepEqual(got, want) {
		t.Fatalf("deleted task hashes = %v, want %v", got, want)
	}
	for _, deleteSource := range tasks.deleteSources {
		if !deleteSource {
			t.Fatal("historical offline task cleanup did not request source deletion")
		}
	}
	if len(provider.deleted) != 0 {
		t.Fatalf("manual remote deletes = %v, want none", provider.deleted)
	}
	if got, want := repository.marked, []int64{10}; !reflect.DeepEqual(got, want) {
		t.Fatalf("marked locations = %v, want %v", got, want)
	}
}

func TestCleanupUsesDeleteFileIDWhenOfflineTaskIsMissing(t *testing.T) {
	const infoHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	repository := &cleanupRepository{assets: []Asset{{
		ID: 1,
		Locations: []Location{{
			ID: 10, RemoteFileID: "episode", Ownership: "managed_cache",
		}},
		Sources: []Source{{
			InfoHash: infoHash, DeleteFileID: "season-root", WPPathID: "work",
		}},
	}}}
	provider := &cleanupProvider{}
	service := &Service{
		Provider: provider, OfflineTasks: &cleanupOfflineTasks{},
		Repository: repository, WorkDirID: "work",
	}

	if err := service.Cleanup(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if got, want := provider.deleted, []string{"season-root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("manual remote deletes = %v, want %v", got, want)
	}
	if tasks := service.OfflineTasks.(*cleanupOfflineTasks); len(tasks.hashes) != 0 {
		t.Fatalf("offline task list was queried despite saved delete_file_id: %v",
			tasks.hashes)
	}
	if got, want := repository.marked, []int64{10}; !reflect.DeepEqual(got, want) {
		t.Fatalf("marked locations = %v, want %v", got, want)
	}
}

func TestCleanupFallsBackToIndividualFilesForOldDatabase(t *testing.T) {
	const infoHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	repository := &cleanupRepository{assets: []Asset{{
		ID: 1,
		Locations: []Location{{
			ID: 10, RemoteFileID: "episode", Ownership: "managed_cache",
		}},
		Sources: []Source{{InfoHash: infoHash}},
	}}}
	provider := &cleanupProvider{}
	service := &Service{
		Provider: provider, OfflineTasks: &cleanupOfflineTasks{},
		Repository: repository, WorkDirID: "work",
	}

	if err := service.Cleanup(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if got, want := provider.deleted, []string{"episode"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("individual remote deletes = %v, want %v", got, want)
	}
	if got, want := repository.marked, []int64{10}; !reflect.DeepEqual(got, want) {
		t.Fatalf("marked locations = %v, want %v", got, want)
	}
}

func TestCleanupRetriesOfflineTaskWhenRemoteFileIsAlreadyMissing(t *testing.T) {
	const infoHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	repository := &cleanupRepository{assets: []Asset{{
		ID: 1,
		Locations: []Location{{
			ID: 10, RemoteFileID: "missing", Ownership: "managed_cache",
		}},
		Sources: []Source{{InfoHash: infoHash}},
	}}}
	tasks := &cleanupOfflineTasks{err: errors.New("temporary failure")}
	service := &Service{
		Provider:     &cleanupProvider{missing: map[string]bool{"missing": true}},
		OfflineTasks: tasks,
		Repository:   repository, WorkDirID: "work",
	}

	if err := service.Cleanup(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if len(repository.marked) != 0 {
		t.Fatalf("marked locations after task cleanup failure = %v", repository.marked)
	}

	tasks.err = nil
	if err := service.Cleanup(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if got, want := repository.marked, []int64{10}; !reflect.DeepEqual(got, want) {
		t.Fatalf("marked locations after retry = %v, want %v", got, want)
	}
}

func TestCleanupEvictsLeastRecentlyAccessedCacheWhenOverLimit(t *testing.T) {
	repository := &capacityRepository{
		candidates: []Asset{
			{ID: 1, Locations: []Location{{ID: 11, RemoteFileID: "old"}}},
			{ID: 2, Locations: []Location{{ID: 12, RemoteFileID: "new"}}},
		},
	}
	provider := &capacityProvider{size: 150, files: map[string]int64{"old": 60, "new": 60}}
	service := &Service{
		Provider: provider, Repository: repository, WorkDirID: "work",
		CacheMaxSizeBytes: 100,
	}

	if err := service.Cleanup(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(provider.deleted, []string{"old"}) {
		t.Fatalf("deleted files = %v, want [old]", provider.deleted)
	}
	if !reflect.DeepEqual(repository.marked, []int64{11}) {
		t.Fatalf("marked locations = %v, want [11]", repository.marked)
	}
}

type fakeRepository struct {
	mu    sync.Mutex
	asset Asset
}

type capacityRepository struct {
	cleanupRepository
	candidates []Asset
}

func (r *capacityRepository) ManagedCacheLocations(context.Context) ([]Asset, error) {
	return r.candidates, nil
}

type capacityProvider struct {
	size    int64
	files   map[string]int64
	deleted []string
}

func (p *capacityProvider) FileInfo(_ context.Context, id string) (RemoteFile, error) {
	if id == "work" {
		return RemoteFile{ID: id, SizeBytes: p.size}, nil
	}
	return RemoteFile{
		ID: id, ParentID: "work", SizeBytes: p.files[id],
		Parents: []RemoteParent{{ID: "work"}},
	}, nil
}

func (p *capacityProvider) Delete(_ context.Context, id, _ string) error {
	p.deleted = append(p.deleted, id)
	p.size -= p.files[id]
	return nil
}

func (r *fakeRepository) AssetBySHA1(context.Context, string) (Asset, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copyAsset := r.asset
	copyAsset.Locations = append([]Location(nil), r.asset.Locations...)
	return copyAsset, nil
}

func (r *fakeRepository) SaveLocation(
	_ context.Context,
	_ int64,
	location Location,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	location.ID = 1
	r.asset.Locations = []Location{location}
	return nil
}

func (*fakeRepository) MarkLocationDeleted(context.Context, int64) error { return nil }
func (*fakeRepository) MarkSourceLocationsDeleted(context.Context, string) error {
	return nil
}
func (*fakeRepository) TouchAsset(context.Context, int64) error { return nil }
func (*fakeRepository) ExpiredManagedLocations(context.Context, time.Time) ([]Asset, error) {
	return nil, nil
}

type fakeProvider struct {
	mu        sync.Mutex
	infoCalls int
}

func (p *fakeProvider) FileInfo(context.Context, string) (RemoteFile, error) {
	p.mu.Lock()
	p.infoCalls++
	p.mu.Unlock()
	return RemoteFile{
		ID: "remote", ParentID: "work", Name: "video.mkv",
		SHA1: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PickCode: "pick",
		Parents: []RemoteParent{{ID: "work", Name: "work"}},
	}, nil
}

func (p *fakeProvider) fileInfoCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.infoCalls
}

func (*fakeProvider) Delete(context.Context, string, string) error { return nil }

type missingFIDProvider struct{}

func (*missingFIDProvider) FileInfo(context.Context, string) (RemoteFile, error) {
	return RemoteFile{}, ErrRemoteNotFound
}

func (*missingFIDProvider) Delete(context.Context, string, string) error { return nil }

type cleanupRepository struct {
	assets []Asset
	marked []int64
}

func (*cleanupRepository) AssetBySHA1(context.Context, string) (Asset, error) {
	return Asset{}, ErrAssetNotFound
}

func (*cleanupRepository) SaveLocation(context.Context, int64, Location) error {
	return nil
}

func (r *cleanupRepository) MarkLocationDeleted(_ context.Context, id int64) error {
	r.marked = append(r.marked, id)
	return nil
}

func (r *cleanupRepository) MarkSourceLocationsDeleted(
	_ context.Context,
	infoHash string,
) error {
	for _, asset := range r.assets {
		for _, source := range asset.Sources {
			if !strings.EqualFold(source.InfoHash, infoHash) {
				continue
			}
			for _, location := range asset.Locations {
				r.marked = append(r.marked, location.ID)
			}
			break
		}
	}
	return nil
}

func (*cleanupRepository) TouchAsset(context.Context, int64) error { return nil }

func (r *cleanupRepository) ExpiredManagedLocations(
	context.Context,
	time.Time,
) ([]Asset, error) {
	return r.assets, nil
}

type cleanupProvider struct {
	missing map[string]bool
	deleted []string
}

func (p *cleanupProvider) FileInfo(_ context.Context, id string) (RemoteFile, error) {
	if p.missing[id] {
		return RemoteFile{}, ErrRemoteNotFound
	}
	return RemoteFile{
		ID: id, ParentID: "work",
		Parents: []RemoteParent{{ID: "work", Name: "work"}},
	}, nil
}

func (p *cleanupProvider) Delete(_ context.Context, id string, _ string) error {
	p.deleted = append(p.deleted, id)
	if p.missing == nil {
		p.missing = make(map[string]bool)
	}
	p.missing[id] = true
	if id == "season-root" {
		p.missing["episode"] = true
	}
	return nil
}

type cleanupOfflineTasks struct {
	hashes        []string
	deleteSources []bool
	found         bool
	err           error
}

func (c *cleanupOfflineTasks) DeleteOfflineTaskIfExists(
	_ context.Context,
	infoHash string,
	deleteSource bool,
) (bool, error) {
	c.hashes = append(c.hashes, infoHash)
	c.deleteSources = append(c.deleteSources, deleteSource)
	return c.found, c.err
}

type fakeRestorer struct {
	mu             sync.Mutex
	restores       int
	magnets        []string
	errorsByMagnet map[string]error
}

type controlledRestorer struct {
	mu       sync.Mutex
	restores int
	started  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (r *controlledRestorer) RestoreContent(
	ctx context.Context,
	_ string,
	_ string,
) (RemoteFile, error) {
	r.mu.Lock()
	r.restores++
	r.mu.Unlock()
	r.once.Do(func() { close(r.started) })
	select {
	case <-ctx.Done():
		return RemoteFile{}, ctx.Err()
	case <-r.release:
		return RemoteFile{
			ID: "remote", ParentID: "work", Name: "video.mkv",
			SHA1: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PickCode: "pick",
			Parents: []RemoteParent{{ID: "work", Name: "work"}},
		}, nil
	}
}

func (r *controlledRestorer) restoreCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.restores
}

func (r *fakeRestorer) RestoreContent(
	_ context.Context,
	magnetURI string,
	_ string,
) (RemoteFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.restores++
	r.magnets = append(r.magnets, magnetURI)
	if err := r.errorsByMagnet[magnetURI]; err != nil {
		return RemoteFile{}, err
	}
	return RemoteFile{
		ID: "remote", ParentID: "work", Name: "video.mkv",
		SHA1: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PickCode: "pick",
		Parents: []RemoteParent{{ID: "work", Name: "work"}},
	}, nil
}

func (r *fakeRestorer) restoredMagnets() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.magnets...)
}

func (r *fakeRestorer) restoreCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.restores
}
