package task

import (
	"context"
	"errors"
	"fmt"
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
	quotaMu     sync.Mutex
	quota       ingest.OfflineQuotaProvider
	quotaMin    int
}

type scopedJobRecoverer interface {
	RecoverRunningJobsBySource(context.Context, ...string) error
}

func NewManager(
	ctx context.Context,
	service *ingest.Service,
	repository ingest.JobRepository,
	timeout time.Duration,
	logf func(string, ...any),
	offlineQuotaMinRemaining ...int,
) (*Manager, error) {
	return newManager(
		ctx, service, repository, timeout, logf, nil,
		offlineQuotaMinRemaining...,
	)
}

func NewManagerForSources(
	ctx context.Context,
	service *ingest.Service,
	repository ingest.JobRepository,
	timeout time.Duration,
	logf func(string, ...any),
	resumeSources []string,
	offlineQuotaMinRemaining ...int,
) (*Manager, error) {
	return newManager(
		ctx, service, repository, timeout, logf, resumeSources,
		offlineQuotaMinRemaining...,
	)
}

func newManager(
	ctx context.Context,
	service *ingest.Service,
	repository ingest.JobRepository,
	timeout time.Duration,
	logf func(string, ...any),
	resumeSources []string,
	offlineQuotaMinRemaining ...int,
) (*Manager, error) {
	var recoverErr error
	if len(resumeSources) > 0 {
		if recoverer, ok := repository.(scopedJobRecoverer); ok {
			recoverErr = recoverer.RecoverRunningJobsBySource(ctx, resumeSources...)
		} else {
			recoverErr = repository.RecoverRunningJobs(ctx)
		}
	} else {
		recoverErr = repository.RecoverRunningJobs(ctx)
	}
	if recoverErr != nil {
		return nil, recoverErr
	}
	manager := &Manager{
		ctx: ctx, service: service, repository: repository, timeout: timeout,
		logf: logf, queue: make(chan []string, maxConcurrentDownloads),
		queued: make(map[string]bool), enabled: true,
		running: make(map[string]context.CancelFunc),
	}
	if len(offlineQuotaMinRemaining) > 0 {
		manager.quotaMin = offlineQuotaMinRemaining[0]
	}
	if manager.quotaMin > 0 {
		quota, ok := service.Provider.(ingest.OfflineQuotaProvider)
		if !ok {
			return nil, errors.New("115 提供方不支持查询离线下载剩余额度")
		}
		manager.quota = quota
	}
	manager.wg.Add(1)
	go func() {
		defer manager.wg.Done()
		manager.worker()
	}()
	var jobs []ingest.Job
	if resumeSources == nil {
		var err error
		jobs, err = repository.ListJobs(ctx, ingest.JobQueued)
		if err != nil {
			return nil, err
		}
	} else {
		for _, source := range resumeSources {
			sourceJobs, err := repository.ListJobsBySource(
				ctx, source, ingest.JobQueued,
			)
			if err != nil {
				return nil, err
			}
			jobs = append(jobs, sourceJobs...)
		}
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

func (m *Manager) Submit(ctx context.Context, job ingest.Job) (string, error) {
	if !m.enabled {
		return "", m.disabledErr
	}
	existing, err := m.repository.LatestJob(ctx, job.Source, job.InfoHash)
	if errors.Is(err, ingest.ErrJobNotFound) {
		if occupied, lookupErr := m.repository.Job(ctx, job.GID); lookupErr == nil &&
			occupied.Source != job.Source {
			if err := ingest.AssignNewGID(&job); err != nil {
				return "", err
			}
		} else if lookupErr != nil && !errors.Is(lookupErr, ingest.ErrJobNotFound) {
			return "", lookupErr
		}
		if err := m.checkOfflineQuota(ctx); err != nil {
			return "", err
		}
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
	if job.Category != "" && existing.Category != job.Category {
		existing.Category = job.Category
		if err := m.repository.UpdateJob(ctx, existing); err != nil {
			return "", err
		}
	}
	return existing.GID, nil
}

func (m *Manager) SubmitMany(
	ctx context.Context, jobs []ingest.Job,
) ([]string, error) {
	if !m.enabled {
		if m.disabledErr != nil {
			return nil, m.disabledErr
		}
		return nil, errors.New("115 未配置，无法添加磁链任务")
	}
	if len(jobs) == 0 {
		return nil, errors.New("磁力链接列表不能为空")
	}
	gids := make([]string, len(jobs))
	var queued []string
	var newJobs []ingest.Job
	var retryJobs []ingest.Job
	for index, job := range jobs {
		if occupied, lookupErr := m.repository.Job(ctx, job.GID); lookupErr == nil &&
			occupied.Source != job.Source {
			if err := ingest.AssignNewGID(&job); err != nil {
				return nil, err
			}
			jobs[index] = job
		} else if lookupErr != nil && !errors.Is(lookupErr, ingest.ErrJobNotFound) {
			return nil, lookupErr
		}
		gids[index] = job.GID
		existing, err := m.repository.LatestJob(ctx, job.Source, job.InfoHash)
		switch {
		case errors.Is(err, ingest.ErrJobNotFound):
			newJobs = append(newJobs, job)
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
			retryJobs = append(retryJobs, existing)
		}
	}
	if len(newJobs)+len(retryJobs) > 0 {
		if err := m.checkOfflineQuota(ctx); err != nil {
			return nil, err
		}
	}
	for _, job := range newJobs {
		if err := m.repository.CreateJob(ctx, job); err != nil {
			return nil, err
		}
		queued = append(queued, job.GID)
	}
	for _, job := range retryJobs {
		if err := m.repository.UpdateJob(ctx, job); err != nil {
			return nil, err
		}
		queued = append(queued, job.GID)
	}
	if err := m.enqueueMany(queued); err != nil {
		return nil, err
	}
	return gids, nil
}

func (m *Manager) checkOfflineQuota(ctx context.Context) error {
	if m.quotaMin <= 0 {
		return nil
	}
	m.quotaMu.Lock()
	defer m.quotaMu.Unlock()
	return m.checkOfflineQuotaLocked(ctx)
}

func (m *Manager) checkOfflineQuotaLocked(ctx context.Context) error {
	remaining, err := m.quota.OfflineQuotaRemaining(ctx)
	if err != nil {
		return fmt.Errorf("查询 115 离线下载剩余额度: %w", err)
	}
	if remaining < m.quotaMin {
		return fmt.Errorf(
			"115 离线下载额度仅剩 %d 次，低于配置的保护值 %d 次，已禁止创建任务",
			remaining, m.quotaMin,
		)
	}
	return nil
}

func (m *Manager) Job(ctx context.Context, gid string) (ingest.Job, error) {
	return m.repository.Job(ctx, gid)
}

func (m *Manager) Jobs(ctx context.Context, states ...string) ([]ingest.Job, error) {
	return m.repository.ListJobs(ctx, states...)
}

func (m *Manager) JobsBySource(
	ctx context.Context, source string, states ...string,
) ([]ingest.Job, error) {
	return m.repository.ListJobsBySource(ctx, source, states...)
}

func (m *Manager) Update(ctx context.Context, job ingest.Job) error {
	return m.repository.UpdateJob(ctx, job)
}

func (m *Manager) Result(ctx context.Context, infoHash string) (ingest.Result, error) {
	return m.service.Result(ctx, infoHash)
}

func (m *Manager) Cancel(ctx context.Context, gid string) error {
	return m.cancel(ctx, gid, false)
}

func (m *Manager) cancel(ctx context.Context, gid string, deleteFiles bool) error {
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
		if err := m.service.Provider.DeleteOfflineTask(ctx, job.InfoHash, deleteFiles); err != nil {
			m.log("取消任务 %s 后删除 115 离线任务失败: %v", gid, err)
		}
	}
	return nil
}

func (m *Manager) Delete(ctx context.Context, gid string) error {
	return m.DeleteWithFiles(ctx, gid, false)
}

func (m *Manager) DeleteWithFiles(ctx context.Context, gid string, deleteFiles bool) error {
	job, err := m.repository.Job(ctx, gid)
	if err != nil {
		return err
	}
	if job.State == ingest.JobQueued || job.State == ingest.JobRunning {
		if err := m.cancel(ctx, gid, deleteFiles); err != nil {
			return err
		}
		return m.repository.DeleteJob(ctx, gid)
	}
	if job.State == ingest.JobSucceeded {
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
		return errors.New("任务队列已满")
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
					if m.quotaMin > 0 {
						m.quotaMu.Lock()
						defer m.quotaMu.Unlock()
						if prepareErr = m.checkOfflineQuotaLocked(ctx); prepareErr != nil {
							return
						}
					}
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
			m.log("%s 任务 %s 已取消", job.Source, job.GID)
			return
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			if cleanupErr := m.service.CleanupTimedOutTask(job.InfoHash); cleanupErr != nil {
				m.log("%s 任务 %s 超时，清理 115 离线任务及源文件失败: %v",
					job.Source, job.GID, cleanupErr)
			} else {
				m.log("%s 任务 %s 超时，已删除 115 离线任务及源文件",
					job.Source, job.GID)
			}
		}
		m.log("%s 任务 %s 失败: %v", job.Source, job.GID, err)
		return
	}
	m.log("%s 任务 %s 已完成，共 %d 个文件", job.Source, job.GID, len(result.Files))
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
