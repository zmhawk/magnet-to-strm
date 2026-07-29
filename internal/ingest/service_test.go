package ingest

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const testInfoHash = "0123456789abcdef0123456789abcdef01234567"

func TestParseInfoHash(t *testing.T) {
	value, err := ParseInfoHash("magnet:?xt=urn:btih:" + testInfoHash)
	if err != nil {
		t.Fatal(err)
	}
	if value != testInfoHash {
		t.Fatalf("got %q", value)
	}
	base32Value, err := ParseInfoHash("magnet:?xt=urn:btih:AERUKZ4JVPG66AJDIVTYTK6N54ASGRLH")
	if err != nil {
		t.Fatal(err)
	}
	if base32Value != testInfoHash {
		t.Fatalf("base32 got %q", base32Value)
	}
}

func TestResolveScansNestedResult(t *testing.T) {
	provider := &fakeProvider{}
	repository := &fakeRepository{}
	service := Service{
		Provider: provider, Repository: repository, WorkDirID: "work",
		STRMStore: fakeSTRMStore{}, PollInterval: time.Millisecond,
	}
	result, err := service.Resolve(
		context.Background(), "magnet:?xt=urn:btih:"+testInfoHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ReusedTask || len(result.Files) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Files[0].RelativePath != "Season 1/Episode 1.mkv" {
		t.Fatalf("unexpected relative path: %q", result.Files[0].RelativePath)
	}
	if result.Files[0].STRMPath !=
		filepath.Join("/strms", "Example", "Season 1", "Episode 1.mkv.strm") {
		t.Fatalf("unexpected STRM path: %q", result.Files[0].STRMPath)
	}
}

func TestRestoreContentDoesNotPublishSTRMs(t *testing.T) {
	store := &recordingSTRMStore{}
	service := Service{
		Provider: &fakeProvider{}, Repository: &fakeRepository{},
		WorkDirID: "work", STRMStore: store, PollInterval: time.Millisecond,
	}

	file, err := service.RestoreContent(
		context.Background(), "magnet:?xt=urn:btih:"+testInfoHash,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	)
	if err != nil {
		t.Fatal(err)
	}
	if file.RemoteID != "episode" {
		t.Fatalf("unexpected restored file: %+v", file)
	}
	if len(store.written) != 0 {
		t.Fatalf("RestoreContent wrote STRM files: %v", store.written)
	}
}

func TestRestoreContentReturnsTargetBeforeBackgroundScanFinishes(t *testing.T) {
	provider := &slowTailProvider{
		tailStarted: make(chan struct{}),
		releaseTail: make(chan struct{}),
	}
	repository := &backgroundScanRepository{
		saved: make(chan Result, 1),
	}
	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	defer stopBackground()
	service := Service{
		Provider: provider, Repository: repository, WorkDirID: "work",
		PollInterval: time.Millisecond, BackgroundContext: backgroundCtx,
		BackgroundTimeout: time.Second,
	}

	result := make(chan struct {
		file File
		err  error
	}, 1)
	go func() {
		file, err := service.RestoreContent(
			context.Background(),
			"magnet:?xt=urn:btih:"+testInfoHash,
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		)
		result <- struct {
			file File
			err  error
		}{file, err}
	}()

	<-provider.tailStarted
	select {
	case restored := <-result:
		if restored.err != nil {
			t.Fatal(restored.err)
		}
		if restored.file.RemoteID != "target" {
			t.Fatalf("unexpected restored file: %+v", restored.file)
		}
	case <-time.After(time.Second):
		t.Fatal("RestoreContent waited for the remaining result scan")
	}
	select {
	case saved := <-repository.saved:
		t.Fatalf("scan was saved before the tail completed: %+v", saved)
	default:
	}

	close(provider.releaseTail)
	select {
	case saved := <-repository.saved:
		if len(saved.Files) != 2 {
			t.Fatalf("background scan saved %d files, want 2", len(saved.Files))
		}
	case <-time.After(time.Second):
		t.Fatal("background scan was not saved")
	}
}

func TestResolveRetriesOfflineListErrorAndLogsTaskFiles(t *testing.T) {
	provider := &flakyProvider{failures: 1}
	repository := &fakeRepository{}
	var logs []string
	service := Service{
		Provider: provider, Repository: repository, WorkDirID: "work",
		STRMStore: fakeSTRMStore{}, PollInterval: time.Millisecond,
		Logf: func(format string, values ...any) {
			logs = append(logs, fmt.Sprintf(format, values...))
		},
	}
	result, err := service.Resolve(
		context.Background(), "magnet:?xt=urn:btih:"+testInfoHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls < 2 {
		t.Fatalf("ListOfflineTasks calls = %d, want at least 2", provider.calls)
	}
	joined := strings.Join(logs, "\n")
	for _, want := range []string{
		"轮询 115 离线任务失败",
		`种子="Example"`,
		`文件="Season 1/Episode 1.mkv"`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("logs do not contain %q:\n%s", want, joined)
		}
	}
	if len(result.Files) != 1 {
		t.Fatalf("unexpected files: %+v", result.Files)
	}
}

func TestWaitForTaskRetriesPollingError(t *testing.T) {
	provider := &pollingProvider{}
	service := Service{
		Provider: provider, Repository: &fakeRepository{},
		PollInterval: time.Millisecond,
	}
	task, err := service.waitForTask(
		context.Background(),
		"magnet:?xt=urn:btih:"+testInfoHash,
		testInfoHash,
		Task{InfoHash: testInfoHash, Name: "Example", Status: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !task.Done || task.ResultID != "root" {
		t.Fatalf("unexpected task: %+v", task)
	}
	if provider.calls != 2 {
		t.Fatalf("ListOfflineTasks calls = %d, want 2", provider.calls)
	}
}

func TestFindTaskSharesOfflineListSnapshotAcrossConcurrentJobs(t *testing.T) {
	const secondInfoHash = "1123456789abcdef0123456789abcdef01234567"
	provider := &snapshotProvider{tasks: []Task{
		{InfoHash: testInfoHash, Name: "First"},
		{InfoHash: secondInfoHash, Name: "Second"},
	}}
	service := Service{Provider: provider, PollInterval: time.Hour}
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(2)
	errs := make(chan error, 2)
	for _, infoHash := range []string{testInfoHash, secondInfoHash} {
		go func() {
			defer wait.Done()
			<-start
			if _, found, err := service.findTask(context.Background(), infoHash); err != nil {
				errs <- err
			} else if !found {
				errs <- errors.New("task not found")
			}
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if provider.callCount() != 1 {
		t.Fatalf("ListOfflineTasks calls = %d, want 1", provider.callCount())
	}
}

func TestPrepareOfflineTasksSubmitsAllAndReusesConflicts(t *testing.T) {
	const (
		secondInfoHash = "1123456789abcdef0123456789abcdef01234567"
		thirdInfoHash  = "2123456789abcdef0123456789abcdef01234567"
	)
	provider := &snapshotProvider{tasks: []Task{{
		InfoHash: testInfoHash, Name: "Existing", Status: 1,
	}}}
	service := Service{
		Provider: provider, WorkDirID: "work", PollInterval: time.Hour,
	}
	prepared, err := service.PrepareOfflineTasks(context.Background(), []string{
		"magnet:?xt=urn:btih:" + testInfoHash,
		"magnet:?xt=urn:btih:" + secondInfoHash,
		"magnet:?xt=urn:btih:" + thirdInfoHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !prepared[testInfoHash].Reused {
		t.Fatal("existing task was not reused")
	}
	if prepared[secondInfoHash].Reused || prepared[thirdInfoHash].Reused {
		t.Fatal("missing tasks were marked as reused")
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.submissions) != 1 || len(provider.submissions[0]) != 3 {
		t.Fatalf("batch submissions = %v, want one batch with all three magnets",
			provider.submissions)
	}
}

func TestPrepareOfflineTasksDoesNotQueryWhenAllCreatesSucceed(t *testing.T) {
	const secondInfoHash = "1123456789abcdef0123456789abcdef01234567"
	provider := &snapshotProvider{}
	service := Service{Provider: provider, WorkDirID: "work"}

	prepared, err := service.PrepareOfflineTasks(context.Background(), []string{
		"magnet:?xt=urn:btih:" + testInfoHash,
		"magnet:?xt=urn:btih:" + secondInfoHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared[testInfoHash].Reused || prepared[secondInfoHash].Reused {
		t.Fatalf("successful creates were marked reused: %+v", prepared)
	}
	if provider.callCount() != 0 {
		t.Fatalf("ListOfflineTasks calls = %d, want 0", provider.callCount())
	}
}

func TestResolveRecreatesOfflineTaskWhenCompletedResultIsMissing(t *testing.T) {
	for _, test := range []struct {
		name          string
		alwaysMissing bool
		wantError     bool
	}{
		{name: "rebuild succeeds"},
		{name: "second result is also missing", alwaysMissing: true, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &missingResultProvider{alwaysMissing: test.alwaysMissing}
			service := Service{
				Provider: provider, Repository: &fakeRepository{},
				STRMStore: fakeSTRMStore{}, WorkDirID: "work",
				PollInterval: time.Millisecond,
			}
			result, err := service.Resolve(
				context.Background(), "magnet:?xt=urn:btih:"+testInfoHash,
			)
			if test.wantError {
				if !errors.Is(err, ErrOfflineResultNotFound) {
					t.Fatalf("got error %v, want missing result error", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if result.ReusedTask {
					t.Fatal("rebuilt task was still marked as reused")
				}
				if len(result.Files) != 1 {
					t.Fatalf("unexpected files: %+v", result.Files)
				}
			}
			provider.mu.Lock()
			defer provider.mu.Unlock()
			wantDeletes := 1
			if !test.wantError {
				wantDeletes = 2
			}
			if provider.deletes != wantDeletes || provider.adds != 2 {
				t.Fatalf("delete/add calls = %d/%d, want %d/2",
					provider.deletes, provider.adds, wantDeletes)
			}
			if !provider.deleteSource {
				t.Fatal("task source with matching wp_path_id was not deleted")
			}
		})
	}
}

func TestRestoreContentRecreatesTaskWhenCompletedResultLacksTarget(t *testing.T) {
	provider := &partialResultProvider{}
	service := Service{
		Provider: provider, Repository: &fakeRepository{}, WorkDirID: "work",
		PollInterval: time.Hour, PollMinInterval: time.Millisecond,
	}
	file, err := service.RestoreContent(
		context.Background(),
		"magnet:?xt=urn:btih:"+testInfoHash,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	)
	if err != nil {
		t.Fatal(err)
	}
	if file.RemoteID != "episode" {
		t.Fatalf("unexpected restored file: %+v", file)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.deletes != 1 || provider.adds != 2 {
		t.Fatalf("delete/add calls = %d/%d, want 1/2",
			provider.deletes, provider.adds)
	}
	if !provider.deleteSource {
		t.Fatal("source folder under work_dir_id was not deleted with the task")
	}
}

func TestRebuildDoesNotDeleteTaskSourceFromAnotherWorkDir(t *testing.T) {
	provider := &deleteTrackingProvider{}
	service := Service{Provider: provider, WorkDirID: "work"}
	err := service.deleteOfflineTaskForRebuild(context.Background(), Task{
		InfoHash: testInfoHash, DeleteFileID: "delete-root", WPPathID: "archive",
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.deleteSource {
		t.Fatal("source folder outside work_dir_id was deleted")
	}
}

func TestRestoreContentReplacesDuplicateTask(t *testing.T) {
	provider := &duplicateTaskProvider{}
	service := Service{
		Provider: provider, Repository: &fakeRepository{}, WorkDirID: "work",
		PollInterval: time.Hour, PollMinInterval: time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := service.RestoreContent(
		ctx,
		"magnet:?xt=urn:btih:"+testInfoHash,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.adds != 2 || provider.deletes != 1 || provider.listCalls < 1 {
		t.Fatalf("adds/deletes/list calls = %d/%d/%d, want 2/1/at least 1",
			provider.adds, provider.deletes, provider.listCalls)
	}
	if !provider.deleteSource {
		t.Fatal("duplicate task source under work_dir_id was not deleted")
	}
}

func TestReplacingDuplicateStopsWhenTaskLookupIsCanceled(t *testing.T) {
	provider := &canceledDuplicateProvider{}
	service := Service{Provider: provider, WorkDirID: "work"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := service.replaceConflictingOfflineTask(
		ctx,
		"magnet:?xt=urn:btih:"+testInfoHash,
		testInfoHash,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if provider.deletes != 0 {
		t.Fatalf("DeleteOfflineTask calls = %d, want 0", provider.deletes)
	}
}

func TestAdaptivePollInterval(t *testing.T) {
	for _, test := range []struct {
		name    string
		elapsed time.Duration
		want    time.Duration
	}{
		{name: "minimum at start", elapsed: 0, want: 2 * time.Second},
		{name: "minimum below ten seconds", elapsed: 9 * time.Second, want: 2 * time.Second},
		{name: "one fifth of elapsed time", elapsed: 80 * time.Second, want: 16 * time.Second},
		{name: "preserves subsecond precision", elapsed: 81 * time.Second, want: 16200 * time.Millisecond},
		{name: "maximum", elapsed: 30 * time.Minute, want: 300 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := adaptivePollInterval(test.elapsed); got != test.want {
				t.Fatalf("adaptivePollInterval(%s) = %s, want %s",
					test.elapsed, got, test.want)
			}
		})
	}
}

func TestAdaptivePollIntervalUsesConfiguredBounds(t *testing.T) {
	service := Service{
		PollMinInterval: 3 * time.Second,
		PollMaxInterval: 10 * time.Second,
	}
	if got := service.adaptivePollInterval(0); got != 3*time.Second {
		t.Fatalf("configured minimum = %s", got)
	}
	if got := service.adaptivePollInterval(time.Minute); got != 10*time.Second {
		t.Fatalf("configured maximum = %s", got)
	}
}

func TestSTRMRelativePathRejectsTraversal(t *testing.T) {
	if _, err := strmRelativePath("Movie", "../escape.mkv"); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
}

func TestPublishSTRMsOnlyWritesVideoFiles(t *testing.T) {
	repository := &recordingRepository{}
	store := &recordingSTRMStore{}
	service := Service{Repository: repository, STRMStore: store}
	result := Result{
		InfoHash: testInfoHash,
		STRMRoot: "Example",
		Files: []File{
			{RelativePath: "Movie.MKV", SHA1: "video", NeedsSTRM: true},
			{RelativePath: "Movie.zh-CN.srt", SHA1: "subtitle", NeedsSTRM: true},
			{RelativePath: "poster.jpg", SHA1: "image", NeedsSTRM: true},
		},
	}

	published, err := service.publishSTRMs(context.Background(), result)
	if err != nil {
		t.Fatal(err)
	}
	wantWritten := []string{"Example/Movie.MKV.strm"}
	if !reflect.DeepEqual(store.written, wantWritten) {
		t.Fatalf("written STRMs = %v, want %v", store.written, wantWritten)
	}
	if !reflect.DeepEqual(store.sources, []string{testInfoHash}) {
		t.Fatalf("STRM sources = %v, want %s", store.sources, testInfoHash)
	}
	if !reflect.DeepEqual(repository.seeded, []string{"Movie.MKV"}) {
		t.Fatalf("seeded files = %v", repository.seeded)
	}
	if published.Files[0].STRMPath == "" {
		t.Fatal("video file has no STRM path")
	}
	for _, file := range published.Files[1:] {
		if file.STRMPath != "" {
			t.Fatalf("non-video file %q has STRM path %q", file.RelativePath, file.STRMPath)
		}
		if !file.NeedsSTRM {
			t.Fatalf("non-video file %q was marked as STRM-seeded", file.RelativePath)
		}
	}
}

func TestRebuildSTRMsWritesAllSavedVideoFiles(t *testing.T) {
	repository := &recordingRepository{result: Result{
		InfoHash: testInfoHash,
		STRMRoot: "Example",
		Files: []File{
			{RelativePath: "video.mkv", SHA1: "video"},
			{RelativePath: "subtitle.srt", SHA1: "subtitle"},
		},
	}}
	store := &recordingSTRMStore{}
	service := Service{Repository: repository, STRMStore: store}
	count, err := service.RebuildSTRMs(context.Background(), testInfoHash)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rebuilt count = %d, want 1", count)
	}
	if !reflect.DeepEqual(store.written, []string{"Example/video.mkv.strm"}) {
		t.Fatalf("written STRMs = %v", store.written)
	}
	if !reflect.DeepEqual(store.sources, []string{testInfoHash}) {
		t.Fatalf("STRM sources = %v, want %s", store.sources, testInfoHash)
	}
	if !reflect.DeepEqual(repository.seeded, []string{"video.mkv"}) {
		t.Fatalf("seeded files = %v", repository.seeded)
	}
}

func TestAttachSTRMPathsOnlyAttachesVideoFiles(t *testing.T) {
	service := Service{STRMStore: fakeSTRMStore{}}
	result, err := service.attachSTRMPaths(Result{
		STRMRoot: "Example",
		Files: []File{
			{RelativePath: "video.mp4"},
			{RelativePath: "subtitle.ass"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Files[0].STRMPath == "" {
		t.Fatal("video file has no STRM path")
	}
	if result.Files[1].STRMPath != "" {
		t.Fatalf("subtitle has STRM path %q", result.Files[1].STRMPath)
	}
}

type fakeProvider struct{}

type slowTailProvider struct {
	fakeProvider
	tailStarted chan struct{}
	releaseTail chan struct{}
}

func (p *slowTailProvider) OfflineListFolder(
	ctx context.Context,
	folderID string,
	_, _ int64,
) ([]RemoteNode, int64, error) {
	if folderID == "root" {
		return []RemoteNode{
			{
				ID: "target", ParentID: "root", Name: "Target.mkv",
				SHA1:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				SizeBytes: 1024, PickCode: "target-pick",
			},
			{ID: "tail", ParentID: "root", Name: "Extras", IsDir: true},
		}, 2, nil
	}
	close(p.tailStarted)
	select {
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	case <-p.releaseTail:
		return []RemoteNode{{
			ID: "extra", ParentID: "tail", Name: "Extra.mkv",
			SHA1:      "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			SizeBytes: 512, PickCode: "extra-pick",
		}}, 1, nil
	}
}

type deleteTrackingProvider struct {
	fakeProvider
	deleteSource bool
	deletedHash  string
	contextErr   error
	recycleBin   chan struct{}
}

func (p *deleteTrackingProvider) DeleteRecycleBin(context.Context) error {
	if p.recycleBin != nil {
		p.recycleBin <- struct{}{}
	}
	return nil
}

func (p *deleteTrackingProvider) DeleteOfflineTask(
	ctx context.Context,
	infoHash string,
	deleteSource bool,
) error {
	p.deleteSource = deleteSource
	p.deletedHash = infoHash
	p.contextErr = ctx.Err()
	return nil
}

func TestCleanupTimedOutTaskDeletesTaskAndSourceFiles(t *testing.T) {
	provider := &deleteTrackingProvider{recycleBin: make(chan struct{}, 1)}
	service := Service{Provider: provider}
	if err := service.CleanupTimedOutTask(testInfoHash); err != nil {
		t.Fatal(err)
	}
	if provider.deletedHash != testInfoHash {
		t.Fatalf("deleted info hash = %q, want %q", provider.deletedHash, testInfoHash)
	}
	if !provider.deleteSource {
		t.Fatal("timed-out task was deleted without deleting source files")
	}
	if provider.contextErr != nil {
		t.Fatalf("cleanup received an expired context: %v", provider.contextErr)
	}
	select {
	case <-provider.recycleBin:
	case <-time.After(time.Second):
		t.Fatal("recycle bin was not cleaned asynchronously")
	}
}

func (fakeProvider) ListOfflineTasks(context.Context, int64) ([]Task, int, error) {
	return []Task{{
		InfoHash: testInfoHash, Name: "Example", ResultID: "root",
		DeleteFileID: "delete-root", WPPathID: "work", Status: 2, Done: true,
	}}, 1, nil
}

func (fakeProvider) AddOfflineTasks(
	_ context.Context,
	magnetURIs []string,
	_ string,
) ([]OfflineTaskCreateResult, error) {
	results := make([]OfflineTaskCreateResult, 0, len(magnetURIs))
	for _, magnetURI := range magnetURIs {
		infoHash, err := ParseInfoHash(magnetURI)
		if err != nil {
			return nil, err
		}
		results = append(results, OfflineTaskCreateResult{
			InfoHash: infoHash, Created: true,
		})
	}
	return results, nil
}

func (fakeProvider) DeleteOfflineTask(context.Context, string, bool) error { return nil }

type flakyProvider struct {
	fakeProvider
	calls    int
	failures int
}

func (p *flakyProvider) ListOfflineTasks(
	ctx context.Context,
	page int64,
) ([]Task, int, error) {
	p.calls++
	if p.calls <= p.failures {
		return nil, 0, errors.New("temporary decode error")
	}
	return p.fakeProvider.ListOfflineTasks(ctx, page)
}

type pollingProvider struct {
	fakeProvider
	calls int
}

type snapshotProvider struct {
	fakeProvider
	mu          sync.Mutex
	calls       int
	tasks       []Task
	submissions [][]string
}

type missingResultProvider struct {
	fakeProvider
	mu            sync.Mutex
	rebuilt       bool
	alwaysMissing bool
	deletes       int
	deleteSource  bool
	adds          int
}

type partialResultProvider struct {
	fakeProvider
	mu           sync.Mutex
	rebuilt      bool
	deletes      int
	deleteSource bool
	adds         int
}

func (p *partialResultProvider) DeleteOfflineTask(
	_ context.Context,
	_ string,
	deleteSource bool,
) error {
	p.mu.Lock()
	p.deletes++
	p.deleteSource = p.deleteSource || deleteSource
	p.mu.Unlock()
	return nil
}

func (p *partialResultProvider) AddOfflineTasks(
	ctx context.Context,
	magnetURIs []string,
	workDirID string,
) ([]OfflineTaskCreateResult, error) {
	results := make([]OfflineTaskCreateResult, 0, len(magnetURIs))
	for _, magnetURI := range magnetURIs {
		infoHash, err := ParseInfoHash(magnetURI)
		if err != nil {
			return nil, err
		}
		results = append(results, OfflineTaskCreateResult{
			InfoHash: infoHash, Created: true,
		})
	}
	p.mu.Lock()
	p.adds++
	p.rebuilt = p.adds > 1
	p.mu.Unlock()
	return results, nil
}

func (p *partialResultProvider) OfflineListFolder(
	ctx context.Context,
	folderID string,
	offset int64,
	limit int64,
) ([]RemoteNode, int64, error) {
	p.mu.Lock()
	rebuilt := p.rebuilt
	p.mu.Unlock()
	if !rebuilt && folderID != "root" {
		return []RemoteNode{{
			ID: "wrong", ParentID: folderID, Name: "wrong.mkv",
			SHA1: "cccccccccccccccccccccccccccccccccccccccc", SizeBytes: 1024,
		}}, 1, nil
	}
	return p.fakeProvider.OfflineListFolder(ctx, folderID, offset, limit)
}

type duplicateTaskProvider struct {
	fakeProvider
	mu           sync.Mutex
	adds         int
	deletes      int
	deleteSource bool
	listCalls    int
}

type canceledDuplicateProvider struct {
	fakeProvider
	deletes int
}

func (*canceledDuplicateProvider) AddOfflineTasks(
	context.Context,
	[]string,
	string,
) ([]OfflineTaskCreateResult, error) {
	return []OfflineTaskCreateResult{{
		InfoHash: testInfoHash, ErrCode: 10008, Error: "duplicate",
	}}, nil
}

func (*canceledDuplicateProvider) ListOfflineTasks(
	ctx context.Context,
	_ int64,
) ([]Task, int, error) {
	return nil, 0, ctx.Err()
}

func (p *canceledDuplicateProvider) DeleteOfflineTask(
	context.Context,
	string,
	bool,
) error {
	p.deletes++
	return nil
}

func (p *duplicateTaskProvider) ListOfflineTasks(
	context.Context,
	int64,
) ([]Task, int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.listCalls++
	return []Task{{
		InfoHash: testInfoHash, Name: "Example", ResultID: "root",
		DeleteFileID: "delete-root", WPPathID: "work", Status: 2, Done: true,
	}}, 1, nil
}

func (p *duplicateTaskProvider) AddOfflineTasks(
	ctx context.Context,
	magnetURIs []string,
	workDirID string,
) ([]OfflineTaskCreateResult, error) {
	p.mu.Lock()
	p.adds++
	adds := p.adds
	p.mu.Unlock()
	if adds == 1 {
		return []OfflineTaskCreateResult{{
			InfoHash: testInfoHash, ErrCode: 10008, Error: "duplicate",
		}}, nil
	}
	return p.fakeProvider.AddOfflineTasks(ctx, magnetURIs, workDirID)
}

func (p *duplicateTaskProvider) DeleteOfflineTask(
	_ context.Context,
	_ string,
	deleteSource bool,
) error {
	p.mu.Lock()
	p.deletes++
	p.deleteSource = p.deleteSource || deleteSource
	p.mu.Unlock()
	return nil
}

func (p *missingResultProvider) ListOfflineTasks(
	context.Context,
	int64,
) ([]Task, int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	resultID := "stale-result"
	if p.rebuilt {
		resultID = "rebuilt-result"
	}
	return []Task{{
		InfoHash: testInfoHash, Name: "Example", ResultID: resultID,
		DeleteFileID: "delete-root", WPPathID: "work", Status: 2, Done: true,
	}}, 1, nil
}

func (p *missingResultProvider) DeleteOfflineTask(
	_ context.Context,
	_ string,
	deleteSource bool,
) error {
	p.mu.Lock()
	p.deletes++
	p.deleteSource = p.deleteSource || deleteSource
	p.mu.Unlock()
	return nil
}

func (p *missingResultProvider) AddOfflineTasks(
	ctx context.Context,
	magnetURIs []string,
	workDirID string,
) ([]OfflineTaskCreateResult, error) {
	p.mu.Lock()
	p.adds++
	adds := p.adds
	p.rebuilt = adds > 1
	p.mu.Unlock()
	results := make([]OfflineTaskCreateResult, 0, len(magnetURIs))
	for _, magnetURI := range magnetURIs {
		infoHash, err := ParseInfoHash(magnetURI)
		if err != nil {
			return nil, err
		}
		result := OfflineTaskCreateResult{InfoHash: infoHash, Created: adds > 1}
		if !result.Created {
			result.ErrCode = 10008
			result.Error = "duplicate"
		}
		results = append(results, result)
	}
	return results, nil
}

func (p *missingResultProvider) OfflineFolderInfo(
	ctx context.Context,
	resultID string,
) (RemoteNode, error) {
	p.mu.Lock()
	missing := resultID == "stale-result" || p.alwaysMissing
	p.mu.Unlock()
	if missing {
		return RemoteNode{}, ErrOfflineResultNotFound
	}
	return p.fakeProvider.OfflineFolderInfo(ctx, resultID)
}

func (p *snapshotProvider) ListOfflineTasks(
	context.Context,
	int64,
) ([]Task, int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return append([]Task(nil), p.tasks...), 1, nil
}

func (p *snapshotProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *snapshotProvider) AddOfflineTasks(
	_ context.Context,
	magnetURIs []string,
	_ string,
) ([]OfflineTaskCreateResult, error) {
	p.mu.Lock()
	p.submissions = append(p.submissions, append([]string(nil), magnetURIs...))
	p.mu.Unlock()
	existing := make(map[string]bool, len(p.tasks))
	for _, task := range p.tasks {
		existing[strings.ToLower(task.InfoHash)] = true
	}
	results := make([]OfflineTaskCreateResult, 0, len(magnetURIs))
	for _, magnetURI := range magnetURIs {
		infoHash, err := ParseInfoHash(magnetURI)
		if err != nil {
			return nil, err
		}
		result := OfflineTaskCreateResult{InfoHash: infoHash, Created: !existing[infoHash]}
		if !result.Created {
			result.ErrCode = 10008
			result.Error = "duplicate"
		}
		results = append(results, result)
	}
	return results, nil
}

func (p *pollingProvider) ListOfflineTasks(
	ctx context.Context,
	page int64,
) ([]Task, int, error) {
	p.calls++
	if p.calls == 1 {
		return nil, 0, errors.New("temporary decode error")
	}
	return p.fakeProvider.ListOfflineTasks(ctx, page)
}

func (fakeProvider) OfflineFolderInfo(context.Context, string) (RemoteNode, error) {
	return RemoteNode{
		ID: "root", Name: "Example", IsDir: true,
		Parents: []RemoteParent{{ID: "work", Name: "work"}},
	}, nil
}

func (fakeProvider) OfflineListFolder(
	_ context.Context,
	folderID string,
	_, _ int64,
) ([]RemoteNode, int64, error) {
	if folderID == "root" {
		return []RemoteNode{{ID: "season", ParentID: "root", Name: "Season 1", IsDir: true}}, 1, nil
	}
	return []RemoteNode{{
		ID: "episode", ParentID: "season", Name: "Episode 1.mkv",
		SHA1: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SizeBytes: 1024,
		PickCode: "episode-pick-code",
	}}, 1, nil
}

type fakeRepository struct{}

func (fakeRepository) SaveTask(context.Context, string, Task) error { return nil }
func (fakeRepository) SaveScan(_ context.Context, result Result, _ Task) (Result, error) {
	result.STRMRoot = "Example"
	for index := range result.Files {
		result.Files[index].NeedsSTRM = true
	}
	return result, nil
}
func (fakeRepository) MarkSTRMSeeded(context.Context, string, []string) error { return nil }
func (fakeRepository) ResultByInfoHash(context.Context, string) (Result, error) {
	return Result{}, nil
}

type backgroundScanRepository struct {
	fakeRepository
	saved chan Result
}

func (r *backgroundScanRepository) SaveScan(
	_ context.Context,
	result Result,
	_ Task,
) (Result, error) {
	r.saved <- result
	return result, nil
}

type fakeSTRMStore struct{}

func (fakeSTRMStore) Path(value string) (string, error) {
	return filepath.Join("/strms", filepath.FromSlash(value)), nil
}

func (fakeSTRMStore) Write(
	_ context.Context,
	value string,
	_ string,
	_ string,
) (string, error) {
	return filepath.Join("/strms", filepath.FromSlash(value)), nil
}

type recordingRepository struct {
	fakeRepository
	seeded []string
	result Result
}

func (r *recordingRepository) ResultByInfoHash(context.Context, string) (Result, error) {
	return r.result, nil
}

func (r *recordingRepository) MarkSTRMSeeded(
	_ context.Context,
	_ string,
	paths []string,
) error {
	r.seeded = append(r.seeded, paths...)
	return nil
}

type recordingSTRMStore struct {
	written []string
	sources []string
}

func (s *recordingSTRMStore) Path(value string) (string, error) {
	return filepath.Join("/strms", filepath.FromSlash(value)), nil
}

func (s *recordingSTRMStore) Write(
	_ context.Context,
	value string,
	_ string,
	infoHash string,
) (string, error) {
	s.written = append(s.written, value)
	s.sources = append(s.sources, infoHash)
	return s.Path(value)
}
