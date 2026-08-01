package ingest

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestOfflineTaskManagerStopsPagingWhenAllTargetsAreFound(t *testing.T) {
	const target = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	provider := &pagedTaskProvider{
		pages: map[int64][]Task{
			1: {{InfoHash: target, Name: "target"}},
			2: {{InfoHash: testInfoHash, Name: "unrelated"}},
		},
		pageCount: 3,
	}
	manager := newOfflineTaskManager(provider, nil)

	found, err := manager.query(context.Background(), []string{target}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if found[target].Name != "target" {
		t.Fatalf("found = %+v", found)
	}
	if got, want := provider.calledPages(), []int64{1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("called pages = %v, want %v", got, want)
	}
}

func TestOfflineTaskManagerPagesToEndWhenTargetIsMissing(t *testing.T) {
	const target = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	provider := &pagedTaskProvider{
		pages:     map[int64][]Task{1: {{InfoHash: testInfoHash}}},
		pageCount: 3,
	}
	manager := newOfflineTaskManager(provider, nil)

	found, err := manager.query(context.Background(), []string{target}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatalf("found = %+v, want empty", found)
	}
	if got, want := provider.calledPages(), []int64{1, 2, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("called pages = %v, want %v", got, want)
	}
}

func TestOfflineTaskManagerPollsOnlyDueTasks(t *testing.T) {
	const (
		dueHash    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		futureHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	provider := &pagedTaskProvider{
		pages: map[int64][]Task{
			1: {{InfoHash: dueHash, Name: "due"}},
			2: {{InfoHash: futureHash, Name: "future"}},
		},
		pageCount: 2,
	}
	manager := newOfflineTaskManager(provider, nil)
	now := time.Now()
	manager.mu.Lock()
	manager.tasks[dueHash] = &managedOfflineTask{
		startedAt:  now.Add(-time.Minute),
		nextPollAt: now.Add(-time.Second),
		waiters:    map[chan taskPollResult]struct{}{make(chan taskPollResult, 1): {}},
	}
	futureDeadline := now.Add(time.Hour)
	manager.tasks[futureHash] = &managedOfflineTask{
		startedAt:  now,
		nextPollAt: futureDeadline,
		waiters:    map[chan taskPollResult]struct{}{make(chan taskPollResult, 1): {}},
	}
	manager.mu.Unlock()

	_, targets := manager.nextTargets(now)
	manager.poll(targets)

	if _, ok := targets[dueHash]; !ok {
		t.Fatal("due task was not selected")
	}
	if _, ok := targets[futureHash]; ok {
		t.Fatal("future task was selected")
	}
	if got, want := provider.calledPages(), []int64{1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("called pages = %v, want %v", got, want)
	}
	manager.mu.Lock()
	gotFutureDeadline := manager.tasks[futureHash].nextPollAt
	manager.mu.Unlock()
	if !gotFutureDeadline.Equal(futureDeadline) {
		t.Fatalf("future deadline changed from %s to %s",
			futureDeadline, gotFutureDeadline)
	}
}

func TestOfflineTaskManagerCreateRestartsStalePollingEpoch(t *testing.T) {
	const pollMinimum = 3 * time.Second
	manager := newOfflineTaskManager(
		fakeProvider{}, nil, func(time.Duration) time.Duration { return pollMinimum },
	)
	oldStartedAt := time.Now().Add(-time.Hour)
	oldNextPollAt := time.Now().Add(6 * time.Minute)
	manager.mu.Lock()
	manager.tasks[testInfoHash] = &managedOfflineTask{
		task: Task{
			InfoHash: testInfoHash, Name: "stale", Done: true,
			ResultID: "old-result", LastUpdate: 999,
		},
		startedAt: oldStartedAt, nextPollAt: oldNextPollAt,
		waiters: make(map[chan taskPollResult]struct{}),
	}
	manager.mu.Unlock()

	beforeCreate := time.Now()
	if _, err := manager.create(
		context.Background(),
		[]string{"magnet:?xt=urn:btih:" + testInfoHash},
		"work",
	); err != nil {
		t.Fatal(err)
	}

	manager.mu.Lock()
	record := *manager.tasks[testInfoHash]
	manager.mu.Unlock()
	if record.startedAt.Before(beforeCreate) {
		t.Fatalf("startedAt = %s, want a fresh polling epoch after %s",
			record.startedAt, beforeCreate)
	}
	if got := record.nextPollAt.Sub(record.startedAt); got != pollMinimum {
		t.Fatalf("first poll delay = %s, want %s", got, pollMinimum)
	}
	if record.task.Done || record.task.ResultID != "" || record.task.Name != "" {
		t.Fatalf("new task retained stale status: %+v", record.task)
	}
	if record.task.InfoHash != testInfoHash {
		t.Fatalf("task info hash = %q, want %q", record.task.InfoHash, testInfoHash)
	}
}

type pagedTaskProvider struct {
	fakeProvider
	mu        sync.Mutex
	pages     map[int64][]Task
	pageCount int
	calls     []int64
}

func (p *pagedTaskProvider) ListOfflineTasks(
	_ context.Context,
	page int64,
) ([]Task, int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, page)
	return append([]Task(nil), p.pages[page]...), p.pageCount, nil
}

func (p *pagedTaskProvider) calledPages() []int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int64(nil), p.calls...)
}
