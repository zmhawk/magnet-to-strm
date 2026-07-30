package aria2

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"magnet-to-strm/internal/ingest"
)

const maxConcurrentDownloads = 128

type Manager struct {
	ctx         context.Context
	service     *ingest.Service
	repository  ingest.JobRepository
	timeout     time.Duration
	logf        func(string, ...any)
	queue       chan []string
	queuedMu    sync.Mutex
	queued      map[string]bool
	wg          sync.WaitGroup
	enabled     bool
	disabledErr error
	runningMu   sync.Mutex
	running     map[string]context.CancelFunc
}

func NewManager(
	ctx context.Context,
	service *ingest.Service,
	repository ingest.JobRepository,
	timeout time.Duration,
	logf func(string, ...any),
) (*Manager, error) {
	if err := repository.RecoverRunningJobs(ctx); err != nil {
		return nil, err
	}
	manager := &Manager{
		ctx: ctx, service: service, repository: repository, timeout: timeout,
		logf: logf, queue: make(chan []string, maxConcurrentDownloads),
		queued: make(map[string]bool), enabled: true,
		running: make(map[string]context.CancelFunc),
	}
	manager.wg.Add(1)
	go func() {
		defer manager.wg.Done()
		manager.worker()
	}()
	jobs, err := repository.ListJobs(ctx, ingest.JobQueued)
	if err != nil {
		return nil, err
	}
	gids := make([]string, 0, len(jobs))
	for _, job := range jobs {
		gids = append(gids, job.GID)
	}
	if err := manager.enqueueMany(gids); err != nil {
		return nil, err
	}
	return manager, nil
}

func NewDisabledManager(
	ctx context.Context,
	service *ingest.Service,
	repository ingest.JobRepository,
	timeout time.Duration,
	logf func(string, ...any),
	reason error,
) *Manager {
	return &Manager{
		ctx: ctx, service: service, repository: repository, timeout: timeout,
		logf: logf, queued: make(map[string]bool), disabledErr: reason,
		running: make(map[string]context.CancelFunc),
	}
}

func (m *Manager) AddURI(ctx context.Context, magnetURI string) (string, error) {
	return m.AddURIWithCategory(ctx, magnetURI, "")
}

func (m *Manager) AddURIWithCategory(ctx context.Context, magnetURI, category string) (string, error) {
	if !m.enabled {
		return "", m.disabledErr
	}
	job, err := ingest.NewJob(magnetURI)
	if err != nil {
		return "", err
	}
	job.Category = strings.TrimSpace(category)
	existing, err := m.repository.Job(ctx, job.GID)
	if errors.Is(err, ingest.ErrJobNotFound) {
		if err := m.repository.CreateJob(ctx, job); err != nil {
			return "", err
		}
		if err := m.enqueueMany([]string{job.GID}); err != nil {
			return "", err
		}
		return job.GID, nil
	}
	if err != nil {
		return "", err
	}
	if existing.InfoHash != job.InfoHash {
		return "", errors.New("GID 冲突")
	}
	if category != "" && existing.Category != job.Category {
		existing.Category = job.Category
		if err := m.repository.UpdateJob(ctx, existing); err != nil {
			return "", err
		}
	}
	return existing.GID, nil
}

func (m *Manager) AddURIs(ctx context.Context, magnetURIs []string) ([]string, error) {
	if !m.enabled {
		if m.disabledErr != nil {
			return nil, m.disabledErr
		}
		return nil, errors.New("115 未配置，无法添加磁链任务")
	}
	if len(magnetURIs) == 0 {
		return nil, errors.New("磁力链接列表不能为空")
	}
	jobs := make([]ingest.Job, len(magnetURIs))
	for index, magnetURI := range magnetURIs {
		job, err := ingest.NewJob(magnetURI)
		if err != nil {
			return nil, err
		}
		jobs[index] = job
	}
	gids := make([]string, len(jobs))
	var queued []string
	for index, job := range jobs {
		gids[index] = job.GID
		existing, err := m.repository.Job(ctx, job.GID)
		switch {
		case errors.Is(err, ingest.ErrJobNotFound):
			if err := m.repository.CreateJob(ctx, job); err != nil {
				return nil, err
			}
			queued = append(queued, job.GID)
		case err != nil:
			return nil, err
		case existing.InfoHash != job.InfoHash:
			return nil, errors.New("GID 冲突")
		case existing.State == ingest.JobFailed || existing.State == ingest.JobCanceled:
			existing.State = ingest.JobQueued
			existing.Error = ""
			existing.MagnetURI = job.MagnetURI
			existing.StartedAt = nil
			existing.FinishedAt = nil
			if err := m.repository.UpdateJob(ctx, existing); err != nil {
				return nil, err
			}
			queued = append(queued, existing.GID)
		}
	}
	if err := m.enqueueMany(queued); err != nil {
		return nil, err
	}
	return gids, nil
}

func (m *Manager) SetCategory(ctx context.Context, gid, category string) error {
	job, err := m.repository.Job(ctx, gid)
	if err != nil {
		return err
	}
	job.Category = strings.TrimSpace(category)
	return m.repository.UpdateJob(ctx, job)
}

func (m *Manager) CreateCategory(ctx context.Context, name, savePath string) error {
	return m.repository.CreateCategory(ctx, name, savePath)
}

func (m *Manager) Categories(ctx context.Context) (map[string]string, error) {
	return m.repository.Categories(ctx)
}

func (m *Manager) Job(ctx context.Context, gid string) (ingest.Job, error) {
	return m.repository.Job(ctx, gid)
}

func (m *Manager) Jobs(ctx context.Context, states ...string) ([]ingest.Job, error) {
	return m.repository.ListJobs(ctx, states...)
}

func (m *Manager) Result(ctx context.Context, infoHash string) (ingest.Result, error) {
	return m.service.Result(ctx, infoHash)
}

func (m *Manager) Cancel(ctx context.Context, gid string) error {
	job, err := m.repository.Job(ctx, gid)
	if err != nil {
		return err
	}
	if job.State != ingest.JobQueued && job.State != ingest.JobRunning &&
		job.State != ingest.JobFailed &&
		job.State != ingest.JobCanceled {
		return errors.New("只能取消未完成或失败的任务")
	}
	if err := m.repository.CancelJob(ctx, gid); err != nil {
		return err
	}
	m.runningMu.Lock()
	cancel := m.running[gid]
	m.runningMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if m.enabled {
		if err := m.service.Provider.DeleteOfflineTask(ctx, job.InfoHash, false); err != nil {
			m.log("取消任务 %s 后删除 115 离线任务失败: %v", gid, err)
		}
	}
	return nil
}

func (m *Manager) Delete(ctx context.Context, gid string) error {
	return m.DeleteWithFiles(ctx, gid, false)
}

func (m *Manager) DeleteWithFiles(ctx context.Context, gid string, deleteFiles bool) error {
	if job, err := m.repository.Job(ctx, gid); err == nil && job.State == ingest.JobSucceeded {
		if deleteFiles {
			if err := m.service.DeleteCompletedTaskFiles(ctx, job.InfoHash); err != nil {
				return err
			}
		}
		if force, ok := m.repository.(interface {
			DeleteJobAny(context.Context, string) error
		}); ok {
			return force.DeleteJobAny(ctx, gid)
		}
	}
	return m.repository.DeleteJob(ctx, gid)
}

func (m *Manager) RebuildSTRMs(ctx context.Context, gid string) (int, error) {
	job, err := m.repository.Job(ctx, gid)
	if err != nil {
		return 0, err
	}
	if job.State != ingest.JobSucceeded {
		return 0, errors.New("只有已完成的任务可以重建 STRM")
	}
	return m.service.RebuildSTRMs(ctx, job.InfoHash)
}

func (m *Manager) Wait() {
	m.wg.Wait()
}

func (m *Manager) enqueueMany(gids []string) error {
	m.queuedMu.Lock()
	pending := make([]string, 0, len(gids))
	for _, gid := range gids {
		if gid == "" || m.queued[gid] {
			continue
		}
		m.queued[gid] = true
		pending = append(pending, gid)
	}
	m.queuedMu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	select {
	case <-m.ctx.Done():
		m.unmarkMany(pending)
		return m.ctx.Err()
	case m.queue <- pending:
		return nil
	default:
		m.unmarkMany(pending)
		return errors.New("aria2 任务队列已满")
	}
}

func (m *Manager) worker() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case gids := <-m.queue:
			m.unmarkMany(gids)
			m.wg.Add(1)
			go func() {
				defer m.wg.Done()
				m.runBatch(gids)
			}()
		}
	}
}

func (m *Manager) runBatch(gids []string) {
	jobs := make([]ingest.Job, 0, len(gids))
	magnetURIs := make([]string, 0, len(gids))
	for _, gid := range gids {
		job, err := m.repository.Job(m.ctx, gid)
		if err != nil || job.State != ingest.JobQueued {
			continue
		}
		jobs = append(jobs, job)
		magnetURIs = append(magnetURIs, job.MagnetURI)
	}
	if len(jobs) == 0 {
		return
	}
	var prepareOnce sync.Once
	var prepared map[string]ingest.PreparedTask
	var prepareErr error
	for _, job := range jobs {
		job := job
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.runJob(job, func(ctx context.Context) (ingest.Result, error) {
				prepareOnce.Do(func() {
					prepared, prepareErr = m.service.PrepareOfflineTasks(ctx, magnetURIs)
				})
				if prepareErr != nil {
					return ingest.Result{}, prepareErr
				}
				task, found := prepared[job.InfoHash]
				if !found {
					return ingest.Result{}, errors.New("批量提交结果缺少当前任务")
				}
				task.Task.Category = job.Category
				return m.service.ResolvePrepared(ctx, job.MagnetURI, task)
			})
		}()
	}
}

func (m *Manager) runJob(
	job ingest.Job,
	resolve func(context.Context) (ingest.Result, error),
) {
	ctx, cancel := context.WithTimeout(m.ctx, m.timeout)
	m.runningMu.Lock()
	m.running[job.GID] = cancel
	m.runningMu.Unlock()
	defer func() {
		m.runningMu.Lock()
		delete(m.running, job.GID)
		m.runningMu.Unlock()
	}()
	defer cancel()
	result, err := ingest.RunJobWithResolver(ctx, m.repository, job, resolve)
	if err != nil {
		if errors.Is(err, ingest.ErrJobCanceled) {
			m.log("aria2 任务 %s 已取消", job.GID)
			return
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			if cleanupErr := m.service.CleanupTimedOutTask(job.InfoHash); cleanupErr != nil {
				m.log("aria2 任务 %s 超时，清理 115 离线任务及源文件失败: %v",
					job.GID, cleanupErr)
			} else {
				m.log("aria2 任务 %s 超时，已删除 115 离线任务及源文件", job.GID)
			}
		}
		m.log("aria2 任务 %s 失败: %v", job.GID, err)
		return
	}
	m.log("aria2 任务 %s 已完成，共 %d 个文件", job.GID, len(result.Files))
}

func (m *Manager) unmarkMany(gids []string) {
	m.queuedMu.Lock()
	for _, gid := range gids {
		delete(m.queued, gid)
	}
	m.queuedMu.Unlock()
}

func (m *Manager) log(format string, values ...any) {
	if m.logf != nil {
		m.logf(format, values...)
	}
}
