package ingest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// offlineTaskManager owns the in-memory view of the 115 tasks that callers are
// currently waiting for. A single scheduler polls all tasks whose individual
// deadlines have elapsed.
type offlineTaskManager struct {
	provider          Provider
	logf              func(string, ...any)
	pollWait          func(time.Duration) time.Duration
	recycleBinCleanup func()

	mu         sync.Mutex
	fetchMu    sync.Mutex
	tasks      map[string]*managedOfflineTask
	snapshot   map[string]Task
	snapshotAt map[string]time.Time
	wake       chan struct{}
}

type managedOfflineTask struct {
	task       Task
	startedAt  time.Time
	lastPollAt time.Time
	nextPollAt time.Time
	waiters    map[chan taskPollResult]struct{}
}

type taskPollResult struct {
	task  Task
	found bool
	err   error
}

func newOfflineTaskManager(
	provider Provider,
	logf func(string, ...any),
	pollWait ...func(time.Duration) time.Duration,
) *offlineTaskManager {
	wait := (&Service{}).adaptivePollInterval
	if len(pollWait) > 0 && pollWait[0] != nil {
		wait = pollWait[0]
	}
	manager := &offlineTaskManager{
		provider:   provider,
		logf:       logf,
		pollWait:   wait,
		tasks:      make(map[string]*managedOfflineTask),
		snapshot:   make(map[string]Task),
		snapshotAt: make(map[string]time.Time),
		wake:       make(chan struct{}, 1),
	}
	go manager.run()
	return manager
}

func (m *offlineTaskManager) query(
	ctx context.Context,
	infoHashes []string,
	maxAge time.Duration,
) (map[string]Task, error) {
	targets := make(map[string]struct{}, len(infoHashes))
	for _, infoHash := range infoHashes {
		targets[strings.ToLower(infoHash)] = struct{}{}
	}
	return m.fetch(ctx, targets, maxAge)
}

func (m *offlineTaskManager) create(
	ctx context.Context,
	magnetURIs []string,
	workDirID string,
) ([]OfflineTaskCreateResult, error) {
	results, err := m.provider.AddOfflineTasks(ctx, magnetURIs, workDirID)
	if err != nil {
		return nil, err
	}
	infoHashes := make([]string, 0, len(results))
	for _, result := range results {
		infoHashes = append(infoHashes, result.InfoHash)
	}
	m.invalidate(infoHashes...)
	return results, nil
}

func (m *offlineTaskManager) cancel(
	ctx context.Context,
	infoHash string,
	deleteSourceFile bool,
) error {
	if err := m.provider.DeleteOfflineTask(ctx, infoHash, deleteSourceFile); err != nil {
		return err
	}
	if deleteSourceFile {
		// Deleting the task's source files moves them to the recycle bin. The
		// cleanup is deliberately detached from the task operation.
		m.onSourceDeleted()
	}
	m.invalidate(infoHash)
	return nil
}

func (m *offlineTaskManager) onSourceDeleted() {
	if m.recycleBinCleanup != nil {
		m.recycleBinCleanup()
	}
}

func (m *offlineTaskManager) wait(
	ctx context.Context,
	infoHash string,
	current Task,
) (Task, error) {
	key := strings.ToLower(infoHash)
	waiter := make(chan taskPollResult, 1)
	now := time.Now()

	m.mu.Lock()
	record := m.tasks[key]
	if record == nil {
		record = &managedOfflineTask{
			task:       current,
			startedAt:  now,
			nextPollAt: now.Add(m.pollWait(0)),
			waiters:    make(map[chan taskPollResult]struct{}),
		}
		m.tasks[key] = record
	} else if newerOfflineTask(current, record.task) {
		record.task = current
	}
	record.waiters[waiter] = struct{}{}
	m.mu.Unlock()
	m.signal()

	defer m.removeWaiter(key, waiter)
	for {
		select {
		case <-ctx.Done():
			return Task{}, ctx.Err()
		case result := <-waiter:
			if result.err != nil {
				m.log("警告：轮询 115 离线任务失败，保留当前任务并稍后重试：%v",
					result.err)
				continue
			}
			if result.found {
				return result.task, nil
			}
		}
	}
}

func (m *offlineTaskManager) invalidate(infoHashes ...string) {
	m.mu.Lock()
	if len(infoHashes) == 0 {
		clear(m.snapshot)
		clear(m.snapshotAt)
	} else {
		for _, infoHash := range infoHashes {
			key := strings.ToLower(infoHash)
			delete(m.snapshot, key)
			delete(m.snapshotAt, key)
		}
	}
	m.mu.Unlock()
	m.signal()
}

func (m *offlineTaskManager) run() {
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	for {
		next, targets := m.nextTargets(time.Now())
		if len(targets) > 0 {
			m.poll(targets)
			continue
		}
		if next.IsZero() {
			<-m.wake
			continue
		}
		resetTimer(timer, time.Until(next))
		select {
		case <-timer.C:
		case <-m.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
	}
}

func (m *offlineTaskManager) nextTargets(now time.Time) (time.Time, map[string]struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	targets := make(map[string]struct{})
	var next time.Time
	for infoHash, record := range m.tasks {
		if len(record.waiters) == 0 {
			continue
		}
		if !record.nextPollAt.After(now) {
			targets[infoHash] = struct{}{}
			continue
		}
		if next.IsZero() || record.nextPollAt.Before(next) {
			next = record.nextPollAt
		}
	}
	return next, targets
}

func (m *offlineTaskManager) poll(targets map[string]struct{}) {
	found, err := m.fetch(context.Background(), targets, 0)
	now := time.Now()

	m.mu.Lock()
	for infoHash := range targets {
		record := m.tasks[infoHash]
		if record == nil {
			continue
		}
		record.lastPollAt = now
		record.nextPollAt = now.Add(m.pollWait(now.Sub(record.startedAt)))
		result := taskPollResult{err: err}
		if task, ok := found[infoHash]; ok {
			record.task = task
			result.task = task
			result.found = true
		}
		for waiter := range record.waiters {
			select {
			case waiter <- result:
			default:
			}
		}
	}
	m.mu.Unlock()
}

// fetch walks pages until every target has been seen. Tasks encountered on the
// way are cached opportunistically, but do not keep the request paging.
func (m *offlineTaskManager) fetch(
	ctx context.Context,
	targets map[string]struct{},
	maxAge time.Duration,
) (map[string]Task, error) {
	m.fetchMu.Lock()
	defer m.fetchMu.Unlock()
	if maxAge > 0 {
		now := time.Now()
		m.mu.Lock()
		cached := make(map[string]Task, len(targets))
		for infoHash := range targets {
			task, ok := m.snapshot[infoHash]
			if !ok || now.Sub(m.snapshotAt[infoHash]) >= maxAge {
				cached = nil
				break
			}
			cached[infoHash] = task
		}
		m.mu.Unlock()
		if cached != nil {
			return cached, nil
		}
	}
	pending := make(map[string]struct{}, len(targets))
	for infoHash := range targets {
		pending[infoHash] = struct{}{}
	}
	found := make(map[string]Task, len(targets))
	for page := int64(1); len(pending) > 0; page++ {
		tasks, pageCount, err := m.provider.ListOfflineTasks(ctx, page)
		if err != nil {
			return found, fmt.Errorf("查询 115 离线任务第 %d 页: %w", page, err)
		}
		m.mu.Lock()
		fetchedAt := time.Now()
		for _, task := range tasks {
			key := strings.ToLower(task.InfoHash)
			if previous, ok := m.snapshot[key]; !ok || newerOfflineTask(task, previous) {
				m.snapshot[key] = task
			}
			m.snapshotAt[key] = fetchedAt
			if record := m.tasks[key]; record != nil &&
				newerOfflineTask(task, record.task) {
				record.task = task
				for waiter := range record.waiters {
					select {
					case waiter <- taskPollResult{task: task, found: true}:
					default:
					}
				}
			}
			if _, wanted := pending[key]; wanted {
				if previous, ok := found[key]; !ok || newerOfflineTask(task, previous) {
					found[key] = task
				}
				delete(pending, key)
			}
		}
		m.mu.Unlock()
		if pageCount <= 0 || page >= int64(pageCount) {
			break
		}
	}
	return found, nil
}

func (m *offlineTaskManager) removeWaiter(
	infoHash string,
	waiter chan taskPollResult,
) {
	m.mu.Lock()
	if record := m.tasks[infoHash]; record != nil {
		delete(record.waiters, waiter)
	}
	m.mu.Unlock()
	m.signal()
}

func (m *offlineTaskManager) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *offlineTaskManager) log(format string, values ...any) {
	if m.logf != nil {
		m.logf(format, values...)
	}
}

func newerOfflineTask(candidate Task, current Task) bool {
	return current.InfoHash == "" ||
		candidate.LastUpdate > current.LastUpdate ||
		(candidate.LastUpdate == current.LastUpdate &&
			(candidate.Status != current.Status ||
				candidate.Progress != current.Progress ||
				candidate.ResultID != current.ResultID))
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if duration < 0 {
		duration = 0
	}
	timer.Reset(duration)
}
