package materialize

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

const (
	RedirectTypeDirect    = "direct"
	RedirectTypeStableDAV = "stable_dav"
)

type Service struct {
	Provider          Provider
	OfflineTasks      OfflineTaskCleaner
	Repository        Repository
	Restorer          Restorer
	WorkDirID         string
	RedirectBaseURL   *url.URL
	RedirectType      string
	CacheTTL          time.Duration
	CacheMaxSizeBytes int64
	OperationContext  context.Context
	OperationTimeout  time.Duration
	ResolutionCache   *ResolutionCache
	Now               func() time.Time
	Logf              func(string, ...any)
	cacheOnce         sync.Once
	localCache        *ResolutionCache
	recycleBinMu      sync.Mutex
	recycleBinRunning bool
	recycleBinPending bool
}

func (s *Service) Redirect(ctx context.Context, sha1Value string) (string, error) {
	return s.RedirectFrom(ctx, sha1Value, "")
}

func (s *Service) RedirectFrom(
	ctx context.Context,
	sha1Value string,
	preferredInfoHash string,
) (string, error) {
	resolution, err := s.ResolveFrom(ctx, sha1Value, preferredInfoHash)
	if err != nil {
		return "", err
	}
	return s.redirectURL(resolution.Asset, resolution.Location.RemotePath), nil
}

func (s *Service) Resolve(
	ctx context.Context,
	sha1Value string,
) (Resolution, error) {
	return s.ResolveFrom(ctx, sha1Value, "")
}

func (s *Service) ResolveFrom(
	ctx context.Context,
	sha1Value string,
	preferredInfoHash string,
) (Resolution, error) {
	cacheKey := strings.ToLower(sha1Value)
	if resolution, ok := s.cachedResolution(cacheKey); ok {
		return resolution, nil
	}
	if err := ctx.Err(); err != nil {
		return Resolution{}, err
	}
	flight := s.startResolution(cacheKey, sha1Value, preferredInfoHash)
	select {
	case <-ctx.Done():
		return Resolution{}, ctx.Err()
	case <-flight.done:
		if flight.err != nil {
			return Resolution{}, flight.err
		}
		return flight.resolution, nil
	}
}

func (s *Service) resolve(
	ctx context.Context,
	cacheKey string,
	sha1Value string,
	preferredInfoHash string,
) (Resolution, error) {
	asset, err := s.Repository.AssetBySHA1(ctx, sha1Value)
	if err != nil {
		return Resolution{}, err
	}
	info, ownership, err := s.findExisting(ctx, &asset)
	if err != nil {
		return Resolution{}, err
	}
	if info == nil {
		if s.Restorer == nil {
			return Resolution{}, fmt.Errorf(
				"115 中已找不到文件 %s，且未配置离线任务恢复器", asset.SHA1,
			)
		}
		sources, sourceErr := preferredSources(asset.Sources, preferredInfoHash)
		if sourceErr != nil {
			return Resolution{}, sourceErr
		}
		if len(sources) == 0 {
			return Resolution{}, fmt.Errorf(
				"115 中已找不到文件 %s，且没有关联的成功历史磁链", asset.SHA1,
			)
		}
		var restoreErrors []error
		restoredSourceInfoHash := ""
		for _, source := range sources {
			s.logf("正在通过历史磁链恢复文件 %s：info_hash=%s",
				asset.SHA1, source.InfoHash)
			restored, restoreErr := s.Restorer.RestoreContent(
				ctx, source.MagnetURI, asset.SHA1,
			)
			if restoreErr == nil {
				info = &restored
				ownership = "managed_cache"
				restoredSourceInfoHash = source.InfoHash
				break
			}
			if ctx.Err() != nil {
				return Resolution{}, ctx.Err()
			}
			restoreErrors = append(restoreErrors, fmt.Errorf(
				"info_hash=%s: %w", source.InfoHash, restoreErr,
			))
			s.logf("通过历史磁链恢复文件 %s 失败：info_hash=%s，错误=%v",
				asset.SHA1, source.InfoHash, restoreErr)
		}
		if info == nil {
			return Resolution{}, fmt.Errorf(
				"通过所有历史磁链恢复文件 %s 均失败: %w",
				asset.SHA1, errors.Join(restoreErrors...),
			)
		}
		if info != nil {
			info.SourceInfoHash = restoredSourceInfoHash
		}
		err = nil
	}
	rootRemoteID := ""
	if ownership == "managed_cache" {
		rootRemoteID = s.WorkDirID
	}
	location := Location{
		RemoteFileID:   info.ID,
		RemoteParentID: info.ParentID,
		PickCode:       info.PickCode,
		RemotePath:     remotePath(*info),
		Ownership:      ownership,
		RootRemoteID:   rootRemoteID,
		SourceInfoHash: info.SourceInfoHash,
	}
	if err := s.Repository.SaveLocation(ctx, asset.ID, location); err != nil {
		return Resolution{}, err
	}
	if err := s.Repository.TouchAsset(ctx, asset.ID); err != nil {
		return Resolution{}, err
	}
	resolution := Resolution{Asset: asset, Location: location}
	s.cacheResolution(cacheKey, resolution)
	return resolution, nil
}

func preferredSources(sources []Source, preferredInfoHash string) ([]Source, error) {
	preferredInfoHash = strings.ToLower(strings.TrimSpace(preferredInfoHash))
	if preferredInfoHash == "" {
		return sources, nil
	}
	result := make([]Source, 0, len(sources))
	found := false
	for _, source := range sources {
		if strings.EqualFold(source.InfoHash, preferredInfoHash) {
			result = append(result, source)
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf(
			"指定的 info hash %s 未关联内容或没有成功历史任务",
			preferredInfoHash,
		)
	}
	for _, source := range sources {
		if !strings.EqualFold(source.InfoHash, preferredInfoHash) {
			result = append(result, source)
		}
	}
	return result, nil
}

func (s *Service) findExisting(
	ctx context.Context,
	asset *Asset,
) (*RemoteFile, string, error) {
	for _, location := range asset.Locations {
		info, err := s.Provider.FileInfo(ctx, location.RemoteFileID)
		if errors.Is(err, ErrRemoteNotFound) {
			if markErr := s.Repository.MarkLocationDeleted(ctx, location.ID); markErr != nil {
				return nil, "", markErr
			}
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("检查 115 文件 %s: %w", location.RemoteFileID, err)
		}
		if strings.EqualFold(info.SHA1, asset.SHA1) {
			return &info, location.Ownership, nil
		}
	}
	return nil, "", nil
}

func (s *Service) Cleanup(ctx context.Context, retention time.Duration) error {
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	assets, err := s.Repository.ExpiredManagedLocations(ctx, now.Add(-retention))
	if err != nil {
		return err
	}
	cleanedSources := make(map[string]sourceCleanupResult)
	for _, asset := range assets {
		allowFileFallback, ok := s.cleanupTaskSources(
			ctx, asset.Sources, cleanedSources,
		)
		if !ok {
			continue
		}
		if !allowFileFallback {
			continue
		}
		for _, location := range asset.Locations {
			info, err := s.Provider.FileInfo(ctx, location.RemoteFileID)
			if errors.Is(err, ErrRemoteNotFound) {
				if err := s.Repository.MarkLocationDeleted(ctx, location.ID); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				s.logf("跳过清理 %s：无法读取文件信息: %v", location.RemoteFileID, err)
				continue
			}
			if !under(info, s.WorkDirID) {
				s.logf("跳过清理 %s：文件不在配置的工作目录下", location.RemoteFileID)
				continue
			}
			if !allowFileFallback {
				s.logf(
					"任务源清理后文件 %s 仍存在，留待下一轮复核",
					location.RemoteFileID,
				)
				continue
			}
			if err := s.Provider.Delete(ctx, info.ID, info.ParentID); err != nil {
				s.logf("清理 115 临时文件 %s 失败: %v", info.ID, err)
				continue
			}
			s.deleteRecycleBinAsync()
			if err := s.Repository.MarkLocationDeleted(ctx, location.ID); err != nil {
				return err
			}
			s.logf("已清理过期的 115 临时文件 %s", info.ID)
		}
	}
	return s.cleanupOverCapacity(ctx)
}

func (s *Service) cleanupOverCapacity(ctx context.Context) error {
	if s.CacheMaxSizeBytes <= 0 || strings.TrimSpace(s.WorkDirID) == "" {
		return nil
	}
	lister, ok := s.Repository.(ManagedCacheLister)
	if !ok {
		s.logf("跳过临时目录空间清理：存储未提供缓存候选列表")
		return nil
	}
	info, err := s.Provider.FileInfo(ctx, s.WorkDirID)
	if errors.Is(err, ErrRemoteNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取 115 临时目录大小失败: %w", err)
	}
	if info.SizeBytes <= s.CacheMaxSizeBytes {
		return nil
	}
	candidates, err := lister.ManagedCacheLocations(ctx)
	if err != nil {
		return fmt.Errorf("读取临时缓存清理候选失败: %w", err)
	}
	for _, asset := range candidates {
		for _, location := range asset.Locations {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			remote, err := s.Provider.FileInfo(ctx, location.RemoteFileID)
			if errors.Is(err, ErrRemoteNotFound) {
				if err := s.Repository.MarkLocationDeleted(ctx, location.ID); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				s.logf("跳过空间清理 %s：无法读取文件信息: %v", location.RemoteFileID, err)
				continue
			}
			if !under(remote, s.WorkDirID) {
				continue
			}
			if err := s.Provider.Delete(ctx, remote.ID, remote.ParentID); err != nil {
				s.logf("空间不足时清理 115 临时文件 %s 失败: %v", remote.ID, err)
				continue
			}
			s.deleteRecycleBinAsync()
			if err := s.Repository.MarkLocationDeleted(ctx, location.ID); err != nil {
				return err
			}
			s.logf("临时目录超过空间上限，已清理最久未访问缓存 %s", remote.ID)
			info, err = s.Provider.FileInfo(ctx, s.WorkDirID)
			if errors.Is(err, ErrRemoteNotFound) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("刷新 115 临时目录大小失败: %w", err)
			}
			if info.SizeBytes <= s.CacheMaxSizeBytes {
				return nil
			}
		}
	}
	if info.SizeBytes > s.CacheMaxSizeBytes {
		s.logf("临时目录仍超过空间上限：当前 %d 字节，上限 %d 字节",
			info.SizeBytes, s.CacheMaxSizeBytes)
	}
	return nil
}

type sourceCleanupResult struct {
	allowFileFallback bool
}

func (s *Service) cleanupTaskSources(
	ctx context.Context,
	sources []Source,
	cleaned map[string]sourceCleanupResult,
) (bool, bool) {
	if len(sources) == 0 {
		return true, true
	}
	if s.OfflineTasks == nil {
		s.logf("跳过完成过期缓存清理：未配置 115 离线任务清理器")
		return false, false
	}
	allowFileFallback := false
	for _, source := range sources {
		infoHash := strings.ToLower(strings.TrimSpace(source.InfoHash))
		if infoHash == "" {
			continue
		}
		if result, ok := cleaned[infoHash]; ok {
			allowFileFallback = allowFileFallback || result.allowFileFallback
			continue
		}
		deleteFileID := strings.TrimSpace(source.DeleteFileID)
		if deleteFileID != "" {
			if !s.deleteTaskSource(ctx, infoHash, deleteFileID) {
				return false, false
			}
			if err := s.Repository.MarkSourceLocationsDeleted(ctx, infoHash); err != nil {
				s.logf("标记 115 磁链位置已删除失败：info_hash=%s，错误=%v",
					infoHash, err)
				return false, false
			}
			s.deleteRecycleBinAsync()
			cleaned[infoHash] = sourceCleanupResult{}
			allowFileFallback = false
			continue
		}
		found, err := s.OfflineTasks.DeleteOfflineTaskIfExists(ctx, infoHash, true)
		if err != nil {
			s.logf("清理 115 历史离线任务 %s 失败: %v", infoHash, err)
			return false, false
		}
		if found {
			if err := s.Repository.MarkSourceLocationsDeleted(ctx, infoHash); err != nil {
				s.logf("标记 115 磁链位置已删除失败：info_hash=%s，错误=%v",
					infoHash, err)
				return false, false
			}
			s.deleteRecycleBinAsync()
			cleaned[infoHash] = sourceCleanupResult{}
			allowFileFallback = false
			s.logf("已通过 115 离线任务清理任务源：%s", infoHash)
			continue
		}
		cleaned[infoHash] = sourceCleanupResult{allowFileFallback: true}
		allowFileFallback = true
		s.logf("115 历史离线任务 %s 已不存在且未记录 delete_file_id，将逐文件清理",
			infoHash)
	}
	return allowFileFallback, true
}

func (s *Service) deleteTaskSource(
	ctx context.Context,
	infoHash string,
	deleteFileID string,
) bool {
	info, err := s.Provider.FileInfo(ctx, deleteFileID)
	if errors.Is(err, ErrRemoteNotFound) {
		s.logf("115 任务源 %s 已不存在：info_hash=%s", deleteFileID, infoHash)
		return true
	}
	if err != nil {
		s.logf("读取 115 任务源 %s 失败：info_hash=%s，错误=%v",
			deleteFileID, infoHash, err)
		return false
	}
	if !under(info, s.WorkDirID) {
		s.logf("拒绝清理 115 任务源 %s：不在配置的工作目录下", deleteFileID)
		return false
	}
	if err := s.Provider.Delete(ctx, info.ID, info.ParentID); err != nil {
		s.logf("按 delete_file_id 清理 115 任务源 %s 失败：info_hash=%s，错误=%v",
			deleteFileID, infoHash, err)
		return false
	}
	s.deleteRecycleBinAsync()
	s.logf("已按 delete_file_id 清理 115 任务源 %s：info_hash=%s",
		deleteFileID, infoHash)
	return true
}

// deleteRecycleBinAsync coalesces concurrent cleanup requests. The operation
// uses the service lifetime context because the cleanup caller may already be
// returning or have a short-lived deadline.
func (s *Service) deleteRecycleBinAsync() {
	cleaner, ok := s.Provider.(RecycleBinCleaner)
	if !ok {
		return
	}
	s.recycleBinMu.Lock()
	s.recycleBinPending = true
	if s.recycleBinRunning {
		s.recycleBinMu.Unlock()
		return
	}
	s.recycleBinRunning = true
	s.recycleBinMu.Unlock()

	go func() {
		for {
			s.recycleBinMu.Lock()
			s.recycleBinPending = false
			s.recycleBinMu.Unlock()
			ctx := s.OperationContext
			if ctx == nil {
				ctx = context.Background()
			}
			cancel := func() {}
			if s.OperationTimeout > 0 {
				ctx, cancel = context.WithTimeout(ctx, s.OperationTimeout)
			}
			err := cleaner.DeleteRecycleBin(ctx)
			cancel()
			if err != nil {
				s.logf("异步清理 115 回收站失败: %v", err)
			}
			s.recycleBinMu.Lock()
			if !s.recycleBinPending {
				s.recycleBinRunning = false
				s.recycleBinMu.Unlock()
				return
			}
			s.recycleBinMu.Unlock()
		}
	}()
}

func under(info RemoteFile, folderID string) bool {
	for _, parent := range info.Parents {
		if parent.ID == folderID {
			return true
		}
	}
	return false
}

func remotePath(info RemoteFile) string {
	if value := strings.TrimSpace(info.RemotePath); value != "" {
		return "/" + strings.TrimLeft(value, "/")
	}
	segments := make([]string, 0, len(info.Parents)+1)
	for _, parent := range info.Parents {
		if parent.ID != "0" && parent.Name != "" {
			segments = append(segments, parent.Name)
		}
	}
	segments = append(segments, info.Name)
	return "/" + strings.TrimLeft(path.Join(segments...), "/")
}

func appendURLPath(base *url.URL, remotePathValue string) string {
	copyURL := *base
	copyURL.Path = strings.TrimRight(copyURL.Path, "/") + "/" + strings.TrimLeft(remotePathValue, "/")
	return copyURL.String()
}

func stableObjectPath(asset Asset) string {
	extension := strings.ToLower(path.Ext(asset.PreferredName))
	if extension == "" {
		extension = ".bin"
	}
	return path.Join(
		"objects",
		asset.SHA1[:2],
		asset.SHA1[2:4],
		asset.SHA1+extension,
	)
}

func (s *Service) logf(format string, values ...any) {
	if s.Logf != nil {
		s.Logf(format, values...)
	}
}

type ResolutionCache struct {
	mu      sync.Mutex
	entries map[string]resolutionCacheEntry
	flights map[string]*resolutionFlight
}

type resolutionCacheEntry struct {
	resolution Resolution
	lastAccess time.Time
}

type resolutionFlight struct {
	done       chan struct{}
	resolution Resolution
	err        error
}

func (s *Service) cachedResolution(key string) (Resolution, bool) {
	if s.CacheTTL <= 0 {
		return Resolution{}, false
	}
	now := s.now()
	cache := s.resolutionCache()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, ok := cache.entries[key]
	if !ok {
		return Resolution{}, false
	}
	if now.Sub(entry.lastAccess) >= s.CacheTTL {
		delete(cache.entries, key)
		return Resolution{}, false
	}
	entry.lastAccess = now
	cache.entries[key] = entry
	return entry.resolution, true
}

func (s *Service) cacheResolution(key string, resolution Resolution) {
	if s.CacheTTL <= 0 {
		return
	}
	cache := s.resolutionCache()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.entries == nil {
		cache.entries = make(map[string]resolutionCacheEntry)
	}
	now := s.now()
	for cachedKey, entry := range cache.entries {
		if now.Sub(entry.lastAccess) >= s.CacheTTL {
			delete(cache.entries, cachedKey)
		}
	}
	cache.entries[key] = resolutionCacheEntry{
		resolution: resolution, lastAccess: now,
	}
}

func (s *Service) resolutionCache() *ResolutionCache {
	if s.ResolutionCache != nil {
		return s.ResolutionCache
	}
	s.cacheOnce.Do(func() {
		s.localCache = &ResolutionCache{}
	})
	return s.localCache
}

func (s *Service) startResolution(
	key string,
	sha1Value string,
	preferredInfoHash string,
) *resolutionFlight {
	cache := s.resolutionCache()
	cache.mu.Lock()
	if s.CacheTTL > 0 {
		now := s.now()
		if entry, ok := cache.entries[key]; ok {
			if now.Sub(entry.lastAccess) < s.CacheTTL {
				entry.lastAccess = now
				cache.entries[key] = entry
				flight := &resolutionFlight{
					done:       make(chan struct{}),
					resolution: entry.resolution,
				}
				close(flight.done)
				cache.mu.Unlock()
				return flight
			}
			delete(cache.entries, key)
		}
	}
	if cache.flights == nil {
		cache.flights = make(map[string]*resolutionFlight)
	}
	if flight := cache.flights[key]; flight != nil {
		cache.mu.Unlock()
		return flight
	}
	flight := &resolutionFlight{done: make(chan struct{})}
	cache.flights[key] = flight
	cache.mu.Unlock()
	s.logf("已启动文件 %s 的后台物化任务", sha1Value)

	go func() {
		ctx := s.OperationContext
		if ctx == nil {
			ctx = context.Background()
		}
		cancel := func() {}
		if s.OperationTimeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, s.OperationTimeout)
		}
		defer cancel()

		resolution, err := s.resolve(
			ctx, key, sha1Value, preferredInfoHash,
		)
		cache.mu.Lock()
		flight.resolution = resolution
		flight.err = err
		close(flight.done)
		delete(cache.flights, key)
		cache.mu.Unlock()
		if err != nil {
			s.logf("文件 %s 的后台物化任务失败：%v", sha1Value, err)
		} else {
			s.logf("文件 %s 的后台物化任务已完成", sha1Value)
		}
	}()
	return flight
}

func (s *Service) redirectURL(asset Asset, remotePath string) string {
	if s.RedirectType == RedirectTypeStableDAV {
		return appendURLPath(s.RedirectBaseURL, stableObjectPath(asset))
	}
	return appendURLPath(s.RedirectBaseURL, remotePath)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
