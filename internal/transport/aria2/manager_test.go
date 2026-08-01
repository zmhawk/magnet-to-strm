package aria2

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"magnet-to-strm/internal/ingest"
	"magnet-to-strm/internal/storage/sqlite"
	"magnet-to-strm/internal/strm"
	"magnet-to-strm/internal/task"
)

func NewManager(
	ctx context.Context,
	service *ingest.Service,
	repository ingest.JobRepository,
	timeout time.Duration,
	logf func(string, ...any),
	offlineQuotaMinRemaining ...int,
) (*Controller, error) {
	runner, err := task.NewManager(
		ctx, service, repository, timeout, logf, offlineQuotaMinRemaining...,
	)
	if err != nil {
		return nil, err
	}
	return NewController(runner), nil
}

func TestJobsPersistAndJSONRPCReportsResult(t *testing.T) {
	const infoHash = "0123456789abcdef0123456789abcdef01234567"
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	strmStore, err := strm.New(filepath.Join(t.TempDir(), "strms"), "https://media.test")
	if err != nil {
		t.Fatal(err)
	}
	service := &ingest.Service{
		Provider: jobProvider{}, Repository: database, WorkDirID: "work",
		STRMStore: strmStore, PollInterval: time.Millisecond,
	}
	manager, err := NewManager(ctx, service, database, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := &Handler{Manager: manager, Secret: "secret"}
	payload := map[string]any{
		"jsonrpc": "2.0", "id": "add", "method": "aria2.addUri",
		"params": []any{
			"token:secret", []string{"magnet:?xt=urn:btih:" + infoHash},
		},
	}
	body, _ := json.Marshal(payload)
	request := httptest.NewRequest(http.MethodPost, "/jsonrpc", bytes.NewReader(body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("add returned %d: %s", response.Code, response.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		job, err := database.Job(context.Background(), infoHash[:16])
		if err != nil {
			t.Fatal(err)
		}
		if job.State == ingest.JobSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not finish: %+v", job)
		}
		time.Sleep(10 * time.Millisecond)
	}
	result, err := service.Result(context.Background(), infoHash)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || result.Files[0].STRMPath == "" {
		t.Fatalf("persisted result is incomplete: %+v", result)
	}
	content, err := os.ReadFile(result.Files[0].STRMPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(content),
		"https://media.test/redirect/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"+
			"?info_hash=0123456789abcdef0123456789abcdef01234567\n"; got != want {
		t.Fatalf("STRM content = %q, want %q", got, want)
	}

	cancel()
	manager.Wait()
	restartCtx, restartCancel := context.WithCancel(context.Background())
	defer restartCancel()
	restarted, err := NewManager(restartCtx, service, database, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := restarted.Job(context.Background(), infoHash[:16])
	if err != nil || persisted.State != ingest.JobSucceeded {
		t.Fatalf("job was not persisted: %+v, %v", persisted, err)
	}
	restartCancel()
	restarted.Wait()
}

func TestManagerStartsOfflineJobsConcurrently(t *testing.T) {
	const secondInfoHash = "1123456789abcdef0123456789abcdef01234567"
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "concurrent-jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &concurrentJobProvider{added: make(chan string, 2)}
	service := &ingest.Service{
		Provider: provider, Repository: database, WorkDirID: "work",
		PollInterval: time.Second,
	}
	manager, err := NewManager(ctx, service, database, 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, infoHash := range []string{
		"0123456789abcdef0123456789abcdef01234567",
		secondInfoHash,
	} {
		if _, err := manager.AddURI(
			context.Background(), "magnet:?xt=urn:btih:"+infoHash,
		); err != nil {
			t.Fatal(err)
		}
	}
	seen := make(map[string]bool)
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for len(seen) < 2 {
		select {
		case infoHash := <-provider.added:
			seen[infoHash] = true
		case <-timer.C:
			t.Fatalf("only %d offline jobs started; jobs are still serial", len(seen))
		}
	}
	cancel()
	manager.Wait()
}

func TestManagerRejectsNewAPITaskBelowOfflineQuotaProtection(t *testing.T) {
	const infoHash = "0123456789abcdef0123456789abcdef01234567"
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "quota-jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx, cancel := context.WithCancel(context.Background())
	provider := &quotaJobProvider{remaining: 9}
	service := &ingest.Service{
		Provider: provider, Repository: database, WorkDirID: "work",
		PollInterval: time.Second,
	}
	manager, err := NewManager(ctx, service, database, time.Minute, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.AddURI(
		context.Background(), "magnet:?xt=urn:btih:"+infoHash,
	)
	if err == nil {
		t.Fatal("new task was accepted below the configured quota protection")
	}
	if _, lookupErr := database.Job(context.Background(), infoHash[:16]); lookupErr == nil {
		t.Fatal("rejected task was persisted")
	}
	cancel()
	manager.Wait()
}

func TestManagerAllowsExistingAPITaskBelowOfflineQuotaProtection(t *testing.T) {
	const infoHash = "0123456789abcdef0123456789abcdef01234567"
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "existing-quota-job.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	magnetURI := "magnet:?xt=urn:btih:" + infoHash
	job, err := ingest.NewJob(magnetURI)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CreateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	job.State = ingest.JobSucceeded
	if err := database.UpdateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	provider := &quotaJobProvider{remaining: 0}
	service := &ingest.Service{
		Provider: provider, Repository: database, WorkDirID: "work",
		PollInterval: time.Second,
	}
	manager, err := NewManager(ctx, service, database, time.Minute, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	gid, err := manager.AddURI(context.Background(), magnetURI)
	if err != nil {
		t.Fatalf("existing task was rejected: %v", err)
	}
	if gid != job.GID {
		t.Fatalf("GID = %q, want %q", gid, job.GID)
	}
	if provider.quotaCalls != 0 {
		t.Fatalf("quota queried %d times for an existing task", provider.quotaCalls)
	}
	cancel()
	manager.Wait()
}

func TestManagerTimeoutDeletesOfflineTaskAndSourceFiles(t *testing.T) {
	const infoHash = "0123456789abcdef0123456789abcdef01234567"
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "timeout-job.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	provider := &concurrentJobProvider{
		added:   make(chan string, 1),
		deleted: make(chan deletedOfflineTask, 1),
	}
	service := &ingest.Service{
		Provider: provider, Repository: database, WorkDirID: "work",
		PollInterval: time.Second,
	}
	manager, err := NewManager(ctx, service, database, 20*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	gid, err := manager.AddURI(
		context.Background(), "magnet:?xt=urn:btih:"+infoHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case deleted := <-provider.deleted:
		if deleted.infoHash != infoHash {
			t.Fatalf("deleted info hash = %q, want %q", deleted.infoHash, infoHash)
		}
		if !deleted.deleteSource {
			t.Fatal("timed-out task was deleted without deleting source files")
		}
	case <-time.After(time.Second):
		t.Fatal("timed-out task was not deleted from 115")
	}
	job, err := database.Job(context.Background(), gid)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != ingest.JobFailed {
		t.Fatalf("job state = %q, want failed", job.State)
	}
	const wantError = "任务处理超时：已超过 ingest.job_timeout 配置的最长时限；" +
		"115 离线下载或后续扫描、STRM 生成未在时限内完成"
	if job.Error != wantError {
		t.Fatalf("job error = %q, want %q", job.Error, wantError)
	}
	stop()
	manager.Wait()
}

func TestManagerCancelsRunningJob(t *testing.T) {
	const infoHash = "0123456789abcdef0123456789abcdef01234567"
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "cancel-job.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx, stop := context.WithCancel(context.Background())
	provider := &concurrentJobProvider{added: make(chan string, 1)}
	service := &ingest.Service{
		Provider: provider, Repository: database, WorkDirID: "work",
		PollInterval: time.Second,
	}
	manager, err := NewManager(ctx, service, database, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	gid, err := manager.AddURI(
		context.Background(), "magnet:?xt=urn:btih:"+infoHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.added:
	case <-time.After(time.Second):
		t.Fatal("offline task was not started")
	}
	deadline := time.Now().Add(time.Second)
	for {
		job, err := database.Job(context.Background(), gid)
		if err != nil {
			t.Fatal(err)
		}
		if job.State == ingest.JobRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not start: %+v", job)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := manager.Cancel(context.Background(), gid); err != nil {
		t.Fatal(err)
	}
	canceled, err := database.Job(context.Background(), gid)
	if err != nil {
		t.Fatal(err)
	}
	if canceled.State != ingest.JobCanceled {
		t.Fatalf("job state = %q, want canceled", canceled.State)
	}
	stop()
	manager.Wait()
}

func TestAria2BatchFormsUseOneOfflineSubmission(t *testing.T) {
	const (
		firstInfoHash  = "0123456789abcdef0123456789abcdef01234567"
		secondInfoHash = "1123456789abcdef0123456789abcdef01234567"
	)
	tests := []struct {
		name    string
		payload func() any
	}{
		{
			name: "JSON-RPC batch",
			payload: func() any {
				return []any{
					map[string]any{
						"jsonrpc": "2.0", "id": "first", "method": "aria2.addUri",
						"params": []any{
							"token:secret",
							[]string{"magnet:?xt=urn:btih:" + firstInfoHash},
						},
					},
					map[string]any{
						"jsonrpc": "2.0", "id": "second", "method": "aria2.addUri",
						"params": []any{
							"token:secret",
							[]string{"magnet:?xt=urn:btih:" + secondInfoHash},
						},
					},
				}
			},
		},
		{
			name: "system.multicall",
			payload: func() any {
				return map[string]any{
					"jsonrpc": "2.0", "id": "multi", "method": "system.multicall",
					"params": []any{
						"token:secret",
						[]any{
							map[string]any{
								"methodName": "aria2.addUri",
								"params": []any{
									"token:secret",
									[]string{"magnet:?xt=urn:btih:" + firstInfoHash},
								},
							},
							map[string]any{
								"methodName": "aria2.addUri",
								"params": []any{
									"token:secret",
									[]string{"magnet:?xt=urn:btih:" + secondInfoHash},
								},
							},
						},
					},
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, err := sqlite.Open(filepath.Join(t.TempDir(), "batch-jobs.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			provider := &concurrentJobProvider{
				added:   make(chan string, 2),
				batches: make(chan []string, 1),
			}
			service := &ingest.Service{
				Provider: provider, Repository: database, WorkDirID: "work",
				PollInterval: time.Second,
			}
			manager, err := NewManager(ctx, service, database, 5*time.Second, nil)
			if err != nil {
				t.Fatal(err)
			}
			handler := &Handler{Manager: manager, Secret: "secret"}
			body, err := json.Marshal(test.payload())
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/jsonrpc", bytes.NewReader(body))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("batch returned %d: %s", response.Code, response.Body.String())
			}
			select {
			case batch := <-provider.batches:
				if len(batch) != 2 {
					t.Fatalf("offline submission size = %d, want 2", len(batch))
				}
			case <-time.After(time.Second):
				t.Fatal("115 batch submission was not called")
			}
			cancel()
			manager.Wait()
		})
	}
}

type jobProvider struct{}

func (jobProvider) ListOfflineTasks(context.Context, int64) ([]ingest.Task, int, error) {
	return []ingest.Task{{
		InfoHash: "0123456789abcdef0123456789abcdef01234567",
		Name:     "Movie", ResultID: "remote", Status: 2, Done: true,
	}}, 1, nil
}

func (jobProvider) AddOfflineTasks(
	_ context.Context,
	magnetURIs []string,
	_ string,
) ([]ingest.OfflineTaskCreateResult, error) {
	results := make([]ingest.OfflineTaskCreateResult, 0, len(magnetURIs))
	for _, magnetURI := range magnetURIs {
		infoHash, err := ingest.ParseInfoHash(magnetURI)
		if err != nil {
			return nil, err
		}
		results = append(results, ingest.OfflineTaskCreateResult{
			InfoHash: infoHash, ErrCode: 10008, Error: "duplicate",
		})
	}
	return results, nil
}

func (jobProvider) DeleteOfflineTask(context.Context, string, bool) error { return nil }

func (jobProvider) OfflineFolderInfo(context.Context, string) (ingest.RemoteNode, error) {
	return ingest.RemoteNode{
		ID: "remote", ParentID: "work", Name: "video.mkv",
		SHA1:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SizeBytes: 100, PickCode: "pick", IsDir: false,
		Parents: []ingest.RemoteParent{{ID: "work", Name: "work"}},
	}, nil
}

func (jobProvider) OfflineListFolder(
	context.Context,
	string,
	int64,
	int64,
) ([]ingest.RemoteNode, int64, error) {
	return nil, 0, nil
}

type quotaJobProvider struct {
	jobProvider
	remaining  int
	quotaCalls int
}

func (p *quotaJobProvider) OfflineQuotaRemaining(context.Context) (int, error) {
	p.quotaCalls++
	return p.remaining, nil
}

type concurrentJobProvider struct {
	mu      sync.Mutex
	running map[string]bool
	added   chan string
	batches chan []string
	deleted chan deletedOfflineTask
}

type deletedOfflineTask struct {
	infoHash     string
	deleteSource bool
}

func (p *concurrentJobProvider) ListOfflineTasks(
	context.Context,
	int64,
) ([]ingest.Task, int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	tasks := make([]ingest.Task, 0, len(p.running))
	for infoHash := range p.running {
		tasks = append(tasks, ingest.Task{
			InfoHash: infoHash, Name: infoHash, Status: 1,
		})
	}
	return tasks, 1, nil
}

func (p *concurrentJobProvider) AddOfflineTasks(
	_ context.Context,
	magnetURIs []string,
	_ string,
) ([]ingest.OfflineTaskCreateResult, error) {
	hashes := make([]string, 0, len(magnetURIs))
	p.mu.Lock()
	if p.running == nil {
		p.running = make(map[string]bool)
	}
	for _, magnetURI := range magnetURIs {
		infoHash, err := ingest.ParseInfoHash(magnetURI)
		if err != nil {
			p.mu.Unlock()
			return nil, err
		}
		p.running[infoHash] = true
		hashes = append(hashes, infoHash)
	}
	p.mu.Unlock()
	if p.batches != nil {
		p.batches <- append([]string(nil), magnetURIs...)
	}
	for _, infoHash := range hashes {
		if p.added != nil {
			p.added <- infoHash
		}
	}
	results := make([]ingest.OfflineTaskCreateResult, 0, len(hashes))
	for _, infoHash := range hashes {
		results = append(results, ingest.OfflineTaskCreateResult{
			InfoHash: infoHash, Created: true,
		})
	}
	return results, nil
}

func (p *concurrentJobProvider) DeleteOfflineTask(
	_ context.Context,
	infoHash string,
	deleteSource bool,
) error {
	p.mu.Lock()
	delete(p.running, infoHash)
	p.mu.Unlock()
	if p.deleted != nil {
		p.deleted <- deletedOfflineTask{infoHash: infoHash, deleteSource: deleteSource}
	}
	return nil
}

func (*concurrentJobProvider) OfflineFolderInfo(
	context.Context,
	string,
) (ingest.RemoteNode, error) {
	return ingest.RemoteNode{}, nil
}

func (*concurrentJobProvider) OfflineListFolder(
	context.Context,
	string,
	int64,
	int64,
) ([]ingest.RemoteNode, int64, error) {
	return nil, 0, nil
}
