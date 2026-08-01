package ingest

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	adaptivePollDivisor        = 5
	defaultAdaptivePollMin     = 2 * time.Second
	defaultAdaptivePollMax     = 300 * time.Second
	timedOutTaskCleanupTimeout = 30 * time.Second
)

type Service struct {
	Provider          Provider
	Repository        Repository
	STRMStore         STRMStore
	WorkDirID         string
	PollInterval      time.Duration
	PollMinInterval   time.Duration
	PollMaxInterval   time.Duration
	ScanInterval      time.Duration
	BackgroundContext context.Context
	BackgroundTimeout time.Duration
	Logf              func(string, ...any)

	offlineMu      sync.Mutex
	offlineManager *offlineTaskManager
}

type PreparedTask struct {
	Task   Task
	Reused bool
}

func (s *Service) Resolve(ctx context.Context, magnetURI string) (Result, error) {
	return s.resolve(ctx, magnetURI, nil, true, "", false)
}

func (s *Service) ResolvePrepared(
	ctx context.Context,
	magnetURI string,
	prepared PreparedTask,
) (Result, error) {
	return s.resolve(ctx, magnetURI, &prepared, true, "", false)
}

// DeleteCompletedTaskFiles removes the source created by a completed offline
// task. The saved work-directory ID and the current parent chain are both
// checked before deletion so an old task cannot delete an unrelated file.
func (s *Service) DeleteCompletedTaskFiles(ctx context.Context, infoHash string) error {
	repository, ok := s.Repository.(TaskCleanupRepository)
	if !ok {
		return errors.New("任务存储不支持清理已完成文件")
	}
	deleter, ok := s.Provider.(RemoteFileDeleter)
	if !ok {
		return errors.New("115 提供方不支持删除已完成文件")
	}
	info, err := repository.TaskCleanupInfo(ctx, infoHash)
	if err != nil {
		return err
	}
	deleteFileID := strings.TrimSpace(info.DeleteFileID)
	if deleteFileID == "" {
		return errors.New("已完成任务缺少 delete_file_id，无法安全删除网盘文件")
	}
	if strings.TrimSpace(info.WPPathID) != strings.TrimSpace(s.WorkDirID) {
		return errors.New("任务来源不属于当前 115 工作目录，拒绝删除网盘文件")
	}
	node, err := s.Provider.OfflineFolderInfo(ctx, deleteFileID)
	if errors.Is(err, ErrOfflineResultNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取 115 任务源: %w", err)
	}
	if node.ID != s.WorkDirID {
		underWorkDir := false
		for _, parent := range node.Parents {
			if parent.ID == s.WorkDirID {
				underWorkDir = true
				break
			}
		}
		if !underWorkDir {
			return errors.New("任务源不在当前 115 工作目录下，拒绝删除网盘文件")
		}
	}
	if err := deleter.Delete(ctx, node.ID, node.ParentID); err != nil {
		return fmt.Errorf("删除 115 任务源: %w", err)
	}
	if cleaner, ok := s.Provider.(RecycleBinCleaner); ok {
		if err := cleaner.DeleteRecycleBin(ctx); err != nil {
			s.logf("清空 115 回收站失败：%v", err)
		}
	}
	return nil
}

// RestoreContent recreates a previously successful offline task when necessary,
// persists its refreshed remote locations, and returns the requested file
// without publishing or modifying STRM files.
func (s *Service) RestoreContent(
	ctx context.Context,
	magnetURI string,
	sha1Value string,
) (File, error) {
	result, err := s.resolve(
		ctx, magnetURI, nil, false, strings.ToLower(sha1Value), true,
	)
	if err != nil {
		return File{}, err
	}
	for _, file := range result.Files {
		if strings.EqualFold(file.SHA1, sha1Value) {
			return file, nil
		}
	}
	return File{}, fmt.Errorf("重建离线任务后仍未找到文件 %s", sha1Value)
}

func (s *Service) resolve(
	ctx context.Context,
	magnetURI string,
	prepared *PreparedTask,
	publishSTRMs bool,
	targetSHA1 string,
	forceCreate bool,
) (Result, error) {
	infoHash, err := ParseInfoHash(magnetURI)
	if err != nil {
		return Result{}, err
	}
	logf := s.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	var task Task
	var found bool
	if prepared == nil {
		if forceCreate {
			logf("115 文件不存在，正在通过历史磁链创建离线任务…")
		} else {
			logf("正在创建 115 离线下载任务…")
		}
		results, createErr := s.createOfflineTasks(ctx, []string{magnetURI})
		if createErr != nil {
			return Result{}, fmt.Errorf("创建 115 离线任务: %w", createErr)
		}
		createResult := results[infoHash]
		switch {
		case createResult.Created:
			task = Task{InfoHash: infoHash}
		case createResult.Conflict() && forceCreate:
			if err := s.replaceConflictingOfflineTask(
				ctx, magnetURI, infoHash,
			); err != nil {
				return Result{}, fmt.Errorf("替换冲突的 115 离线任务: %w", err)
			}
			task = Task{InfoHash: infoHash}
		case createResult.Conflict():
			task, found, err = s.findTaskWithRetry(ctx, infoHash, 0)
			if err == nil && !found {
				err = fmt.Errorf("115 报告任务冲突，但任务列表中未找到 %s", infoHash)
			}
		default:
			err = fmt.Errorf("创建 115 离线任务 %s 失败：%s",
				infoHash, createResult.Error)
		}
		if err != nil {
			return Result{}, err
		}
	} else {
		task = prepared.Task
		found = prepared.Reused
		if !strings.EqualFold(task.InfoHash, infoHash) {
			return Result{}, fmt.Errorf("预提交任务 info hash 不匹配：%s", infoHash)
		}
	}
	task.JobGID = jobGID(ctx)
	reused := found
	if !found {
		category := task.Category
		if prepared != nil {
			logf("已批量提交 115 离线任务：info_hash=%s", infoHash)
		}
		task = Task{InfoHash: infoHash, Category: category}
		task.JobGID = jobGID(ctx)
	} else {
		logf("已找到 115 离线任务：%s", task.Name)
	}
	if err := s.Repository.SaveTask(ctx, magnetURI, task); err != nil {
		return Result{}, err
	}
	var files []File
	var rootName string
	for rebuilds := 0; ; {
		task, err = s.waitForTask(ctx, magnetURI, infoHash, task)
		if err != nil {
			return Result{}, err
		}

		logf("任务已完成，正在扫描结果 %s…", task.ResultID)
		if targetSHA1 == "" {
			files, rootName, err = s.scanResult(ctx, task, nil)
		} else {
			scanCtx, scanCancel := s.backgroundContext(ctx)
			targetFound := make(chan File, 1)
			scanDone := make(chan scanOutcome, 1)
			go func() {
				matched := false
				scannedFiles, scannedRoot, scanErr := s.scanResult(
					scanCtx,
					task,
					func(file File) {
						if !matched && strings.EqualFold(file.SHA1, targetSHA1) {
							matched = true
							targetFound <- file
						}
					},
				)
				scanDone <- scanOutcome{
					files: scannedFiles, rootName: scannedRoot, err: scanErr,
				}
			}()
			select {
			case target := <-targetFound:
				target.ManagedRootID = s.WorkDirID
				logf("已找到播放目标文件 %q，剩余结果将在后台继续扫描",
					target.RelativePath)
				go s.finishBackgroundScan(
					scanCtx, scanCancel, scanDone,
					infoHash, magnetURI, task, reused,
				)
				return Result{Files: []File{target}}, nil
			case outcome := <-scanDone:
				scanCancel()
				files, rootName, err = outcome.files, outcome.rootName, outcome.err
			case <-ctx.Done():
				scanCancel()
				return Result{}, ctx.Err()
			}
		}
		if err == nil && targetSHA1 != "" && !containsSHA1(files, targetSHA1) {
			err = fmt.Errorf(
				"%w：结果中缺少目标文件 %s",
				ErrOfflineResultNotFound, targetSHA1,
			)
		}
		if err == nil {
			break
		}
		if !errors.Is(err, ErrOfflineResultNotFound) || rebuilds >= 1 {
			return Result{}, err
		}
		rebuilds++
		logf("警告：115 离线任务结果不存在，正在删除并重新创建：种子=%q，info_hash=%s，旧结果ID=%s",
			task.Name, infoHash, task.ResultID)
		if err := s.deleteOfflineTaskForRebuild(ctx, task); err != nil {
			return Result{}, fmt.Errorf("删除结果缺失的 115 离线任务 %s: %w",
				infoHash, err)
		}
		results, createErr := s.createOfflineTasks(ctx, []string{magnetURI})
		if createErr != nil || !results[infoHash].Created {
			if createErr == nil {
				createErr = fmt.Errorf("%s", results[infoHash].Error)
			}
			return Result{}, fmt.Errorf("重新创建 115 离线任务 %s: %w",
				infoHash, createErr)
		}
		reused = false
		task = Task{InfoHash: infoHash, Category: task.Category}
		task.JobGID = jobGID(ctx)
		if err := s.Repository.SaveTask(ctx, magnetURI, task); err != nil {
			return Result{}, err
		}
		logf("已重新创建 115 离线任务，继续轮询：info_hash=%s", infoHash)
	}
	return s.finishScan(
		ctx, infoHash, magnetURI, task, reused, files, rootName, publishSTRMs,
	)
}

type scanOutcome struct {
	files    []File
	rootName string
	err      error
}

func (s *Service) finishBackgroundScan(
	ctx context.Context,
	cancel context.CancelFunc,
	done <-chan scanOutcome,
	infoHash string,
	magnetURI string,
	task Task,
	reused bool,
) {
	defer cancel()
	outcome := <-done
	if outcome.err != nil {
		s.logf("后台扫描 115 任务结果失败：info_hash=%s，错误=%v",
			infoHash, outcome.err)
		return
	}
	if _, err := s.finishScan(
		ctx, infoHash, magnetURI, task, reused,
		outcome.files, outcome.rootName, false,
	); err != nil {
		s.logf("后台保存 115 任务扫描结果失败：info_hash=%s，错误=%v",
			infoHash, err)
		return
	}
	s.logf("后台扫描和保存 115 任务结果已完成：info_hash=%s", infoHash)
}

func (s *Service) finishScan(
	ctx context.Context,
	infoHash string,
	magnetURI string,
	task Task,
	reused bool,
	files []File,
	rootName string,
	publishSTRMs bool,
) (Result, error) {
	logf := s.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	displayName := rootName
	if displayName == "" {
		displayName = task.Name
	}
	logf("已解析 115 任务结果：种子=%q，文件数=%d", displayName, len(files))
	for index := range files {
		file := &files[index]
		logf("已解析文件：种子=%q，文件=%q，大小=%d 字节",
			displayName, file.RelativePath, file.SizeBytes)
	}
	var totalBytes int64
	for index := range files {
		totalBytes += files[index].SizeBytes
		files[index].ManagedRootID = s.WorkDirID
	}
	if rootName == "" {
		rootName = task.Name
	}
	result := Result{
		Name:       rootName,
		InfoHash:   infoHash,
		MagnetURI:  magnetURI,
		ResultID:   task.ResultID,
		ReusedTask: reused,
		TotalBytes: totalBytes,
		ScannedAt:  time.Now().UTC(),
		Files:      files,
	}
	result.STRMRoot = strings.TrimSpace(task.Category)
	result, err := s.Repository.SaveScan(ctx, result, task)
	if err != nil {
		return Result{}, err
	}
	logf("已将 %d 个文件保存到 SQLite", len(result.Files))
	if err := s.offlineTasks().cancel(ctx, infoHash, false); err != nil {
		logf("警告：扫描结果已保存，但删除已完成的 115 离线任务失败：info_hash=%s，错误=%v",
			infoHash, err)
	} else {
		logf("已删除完成的 115 离线任务并保留结果文件：info_hash=%s", infoHash)
	}
	if !publishSTRMs {
		return result, nil
	}
	result, err = s.publishSTRMs(ctx, result)
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func (s *Service) backgroundContext(parent context.Context) (context.Context, context.CancelFunc) {
	base := s.BackgroundContext
	if base == nil {
		base = context.WithoutCancel(parent)
	}
	if gid := jobGID(parent); gid != "" {
		base = withJobGID(base, gid)
	}
	if s.BackgroundTimeout > 0 {
		return context.WithTimeout(base, s.BackgroundTimeout)
	}
	return context.WithCancel(base)
}

func (s *Service) PrepareOfflineTasks(
	ctx context.Context,
	magnetURIs []string,
) (map[string]PreparedTask, error) {
	prepared := make(map[string]PreparedTask, len(magnetURIs))
	infoHashes := make([]string, 0, len(magnetURIs))
	uniqueMagnets := make([]string, 0, len(magnetURIs))
	parsed := make(map[string]string, len(magnetURIs))
	for _, magnetURI := range magnetURIs {
		infoHash, err := ParseInfoHash(magnetURI)
		if err != nil {
			return nil, err
		}
		if _, duplicate := parsed[infoHash]; !duplicate {
			infoHashes = append(infoHashes, infoHash)
			uniqueMagnets = append(uniqueMagnets, magnetURI)
			parsed[infoHash] = magnetURI
		}
	}
	createResults, err := s.createOfflineTasks(ctx, uniqueMagnets)
	if err != nil {
		return nil, err
	}
	var conflicts []string
	for _, infoHash := range infoHashes {
		result := createResults[infoHash]
		switch {
		case result.Created:
			prepared[infoHash] = PreparedTask{Task: Task{InfoHash: infoHash}}
		case result.Conflict():
			conflicts = append(conflicts, infoHash)
		default:
			return nil, fmt.Errorf("创建 115 离线任务 %s 失败：%s",
				infoHash, result.Error)
		}
	}
	if len(conflicts) > 0 {
		existing, err := s.offlineTasks().query(ctx, conflicts, 0)
		if err != nil {
			return nil, err
		}
		for _, infoHash := range conflicts {
			task, found := existing[infoHash]
			if !found {
				return nil, fmt.Errorf(
					"115 报告任务冲突，但任务列表中未找到 %s", infoHash,
				)
			}
			prepared[infoHash] = PreparedTask{Task: task, Reused: true}
		}
	}
	for infoHash := range parsed {
		if _, ok := prepared[infoHash]; !ok {
			return nil, fmt.Errorf("115 未返回任务 %s 的创建结果", infoHash)
		}
	}
	return prepared, nil
}

func (s *Service) createOfflineTasks(
	ctx context.Context,
	magnetURIs []string,
) (map[string]OfflineTaskCreateResult, error) {
	results, err := s.offlineTasks().create(ctx, magnetURIs, s.WorkDirID)
	if err != nil {
		return nil, err
	}
	byHash := make(map[string]OfflineTaskCreateResult, len(results))
	for _, result := range results {
		key := strings.ToLower(result.InfoHash)
		if key != "" {
			byHash[key] = result
		}
	}
	for _, magnetURI := range magnetURIs {
		infoHash, err := ParseInfoHash(magnetURI)
		if err != nil {
			return nil, err
		}
		if _, confirmed := byHash[infoHash]; !confirmed {
			return nil, fmt.Errorf("115 未返回任务 %s 的创建结果", infoHash)
		}
	}
	return byHash, nil
}

func (s *Service) replaceConflictingOfflineTask(
	ctx context.Context,
	magnetURI string,
	infoHash string,
) error {
	s.logf("115 离线任务已存在，正在删除旧任务后重新创建：info_hash=%s", infoHash)
	s.invalidateOfflineTasks(infoHash)
	task, found, lookupErr := s.findTaskWithMaxAge(ctx, infoHash, 0)
	if lookupErr != nil {
		if errors.Is(lookupErr, context.Canceled) ||
			errors.Is(lookupErr, context.DeadlineExceeded) {
			return lookupErr
		}
		s.logf("无法读取重复的 115 离线任务详情，将仅删除任务记录：info_hash=%s，错误=%v",
			infoHash, lookupErr)
	}
	if !found {
		task = Task{InfoHash: infoHash}
	}
	if err := s.deleteOfflineTaskForRebuild(ctx, task); err != nil {
		return fmt.Errorf("删除重复的 115 离线任务 %s: %w", infoHash, err)
	}
	results, err := s.createOfflineTasks(ctx, []string{magnetURI})
	if err != nil {
		return fmt.Errorf("删除旧任务后重新创建 115 离线任务 %s: %w", infoHash, err)
	}
	result := results[infoHash]
	if !result.Created {
		return fmt.Errorf("删除旧任务后重新创建 115 离线任务 %s 失败：%s",
			infoHash, result.Error)
	}
	return nil
}

func (s *Service) deleteOfflineTaskForRebuild(
	ctx context.Context,
	task Task,
) error {
	deleteSourceFile := task.WPPathID != "" && task.WPPathID == s.WorkDirID
	if deleteSourceFile {
		s.logf("115 任务源文件位于工作目录下，将随任务一并删除：info_hash=%s，文件ID=%s",
			task.InfoHash, task.DeleteFileID)
	} else {
		s.logf("115 任务的 wp_path_id 与工作目录不一致，仅删除任务记录：info_hash=%s，wp_path_id=%s",
			task.InfoHash, task.WPPathID)
	}
	return s.offlineTasks().cancel(ctx, task.InfoHash, deleteSourceFile)
}

func (s *Service) Result(ctx context.Context, infoHash string) (Result, error) {
	result, err := s.Repository.ResultByInfoHash(ctx, infoHash)
	if err != nil {
		return Result{}, err
	}
	return s.attachSTRMPaths(result)
}

// CleanupTimedOutTask removes a timed-out offline task from 115 together with
// the source files created by that task. It deliberately uses a fresh context
// because the job context has already expired when this method is called.
func (s *Service) CleanupTimedOutTask(infoHash string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timedOutTaskCleanupTimeout)
	defer cancel()
	if err := s.offlineTasks().cancel(ctx, infoHash, true); err != nil {
		return fmt.Errorf("删除超时的 115 离线任务及源文件: %w", err)
	}
	return nil
}

func (s *Service) deleteRecycleBinAsync() {
	cleaner, ok := s.Provider.(RecycleBinCleaner)
	if !ok {
		return
	}
	ctx := s.BackgroundContext
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := s.BackgroundTimeout
	if timeout <= 0 {
		timeout = timedOutTaskCleanupTimeout
	}
	go func() {
		cleanupCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := cleaner.DeleteRecycleBin(cleanupCtx); err != nil {
			s.logf("异步清理 115 回收站失败: %v", err)
		}
	}()
}

func (s *Service) RebuildSTRMs(ctx context.Context, infoHash string) (int, error) {
	result, err := s.Repository.ResultByInfoHash(ctx, infoHash)
	if err != nil {
		return 0, err
	}
	var rebuilt []string
	for _, file := range result.Files {
		if !isVideoFile(file.RelativePath) {
			continue
		}
		relativePath, err := strmRelativePath(result.STRMRoot, file.RelativePath)
		if err != nil {
			return len(rebuilt), err
		}
		if _, err := s.STRMStore.Write(
			ctx, relativePath, file.SHA1, result.InfoHash,
		); err != nil {
			return len(rebuilt), fmt.Errorf("重建 STRM %q: %w", relativePath, err)
		}
		rebuilt = append(rebuilt, file.RelativePath)
	}
	if err := s.Repository.MarkSTRMSeeded(ctx, result.InfoHash, rebuilt); err != nil {
		return len(rebuilt), fmt.Errorf("记录 STRM 重建状态: %w", err)
	}
	return len(rebuilt), nil
}

func (s *Service) publishSTRMs(ctx context.Context, result Result) (Result, error) {
	if s.STRMStore == nil {
		return Result{}, fmt.Errorf("STRM 存储未配置")
	}
	var seeded []string
	for index := range result.Files {
		file := &result.Files[index]
		if isNFOFile(file.RelativePath) {
			if err := s.publishNFO(ctx, result.STRMRoot, *file); err != nil {
				return Result{}, err
			}
			file.STRMPath = ""
			continue
		}
		if !isVideoFile(file.RelativePath) {
			file.STRMPath = ""
			continue
		}
		relativePath, err := strmRelativePath(result.STRMRoot, file.RelativePath)
		if err != nil {
			return Result{}, err
		}
		target, err := s.STRMStore.Path(relativePath)
		if err != nil {
			return Result{}, fmt.Errorf("解析 STRM 路径 %q: %w", relativePath, err)
		}
		file.STRMPath = target
		if !file.NeedsSTRM {
			continue
		}
		if _, err := s.STRMStore.Write(
			ctx, relativePath, file.SHA1, result.InfoHash,
		); err != nil {
			return Result{}, fmt.Errorf("写入 STRM %q: %w", relativePath, err)
		}
		seeded = append(seeded, file.RelativePath)
	}
	if len(seeded) > 0 {
		if err := s.Repository.MarkSTRMSeeded(ctx, result.InfoHash, seeded); err != nil {
			return Result{}, fmt.Errorf("记录 STRM 生成状态: %w", err)
		}
		for index := range result.Files {
			if isVideoFile(result.Files[index].RelativePath) {
				result.Files[index].NeedsSTRM = false
			}
		}
	}
	return result, nil
}

func (s *Service) publishNFO(ctx context.Context, root string, file File) error {
	provider, ok := s.Provider.(NFOProvider)
	if !ok {
		return errors.New("115 提供方不支持读取 NFO 文件")
	}
	store, ok := s.STRMStore.(NFOStore)
	if !ok {
		return errors.New("STRM 存储不支持写入 NFO 文件")
	}
	relativePath, err := libraryRelativePath(root, file.RelativePath)
	if err != nil {
		return err
	}
	content, err := provider.ReadNFO(ctx, file.PickCode)
	if err != nil {
		return fmt.Errorf("读取 NFO %q: %w", file.RelativePath, err)
	}
	if _, err := store.WriteNFO(ctx, relativePath, content); err != nil {
		return fmt.Errorf("写入 NFO %q: %w", relativePath, err)
	}
	return nil
}

func isNFOFile(filePath string) bool {
	return strings.EqualFold(path.Ext(strings.TrimSpace(filePath)), ".nfo")
}

func libraryRelativePath(root, filePath string) (string, error) {
	cleanRoot := path.Clean(strings.TrimSpace(root))
	cleanFile := path.Clean(strings.TrimSpace(filePath))
	if cleanRoot == "." || cleanRoot == ".." || strings.HasPrefix(cleanRoot, "../") ||
		path.IsAbs(cleanRoot) || cleanFile == "." || cleanFile == ".." ||
		strings.HasPrefix(cleanFile, "../") || path.IsAbs(cleanFile) {
		return "", fmt.Errorf("无效媒体库输出路径 %q/%q", root, filePath)
	}
	return path.Join(cleanRoot, cleanFile), nil
}

func (s *Service) attachSTRMPaths(result Result) (Result, error) {
	if s.STRMStore == nil {
		return Result{}, fmt.Errorf("STRM 存储未配置")
	}
	for index := range result.Files {
		if !isVideoFile(result.Files[index].RelativePath) {
			result.Files[index].STRMPath = ""
			continue
		}
		relativePath, err := strmRelativePath(
			result.STRMRoot, result.Files[index].RelativePath,
		)
		if err != nil {
			return Result{}, err
		}
		target, err := s.STRMStore.Path(relativePath)
		if err != nil {
			return Result{}, err
		}
		result.Files[index].STRMPath = target
	}
	return result, nil
}

func isVideoFile(filePath string) bool {
	switch strings.ToLower(path.Ext(strings.TrimSpace(filePath))) {
	case ".3g2", ".3gp", ".asf", ".avi", ".divx", ".f4v", ".flv",
		".m2t", ".m2ts", ".m4v", ".mkv", ".mov", ".mp4", ".mpe",
		".mpeg", ".mpg", ".mts", ".ogm", ".ogv", ".rm", ".rmvb",
		".ts", ".vob", ".webm", ".wmv":
		return true
	default:
		return false
	}
}

func strmRelativePath(root, filePath string) (string, error) {
	cleanRoot := path.Clean(strings.TrimSpace(root))
	cleanFile := path.Clean(strings.TrimSpace(filePath))
	if cleanRoot == "." || cleanRoot == ".." || strings.HasPrefix(cleanRoot, "../") ||
		path.IsAbs(cleanRoot) || cleanFile == "." || cleanFile == ".." ||
		strings.HasPrefix(cleanFile, "../") || path.IsAbs(cleanFile) {
		return "", fmt.Errorf("无效 STRM 输出路径 %q/%q", root, filePath)
	}
	return path.Join(cleanRoot, cleanFile+".strm"), nil
}

func NewJob(magnetURI string) (Job, error) {
	return NewJobForSource(magnetURI, TaskSourceAria2)
}

func NewJobForSource(magnetURI, source string) (Job, error) {
	infoHash, err := ParseInfoHash(magnetURI)
	if err != nil {
		return Job{}, err
	}
	return Job{
		GID:       infoHash[:16],
		Source:    strings.TrimSpace(source),
		InfoHash:  infoHash,
		MagnetURI: strings.TrimSpace(magnetURI),
		State:     JobQueued,
		CreatedAt: time.Now().UTC(),
	}, nil
}

func RunJob(
	ctx context.Context,
	repository JobRepository,
	service *Service,
	job Job,
) (Result, error) {
	return RunJobWithResolver(
		ctx,
		repository,
		job,
		func(ctx context.Context) (Result, error) {
			return service.Resolve(ctx, job.MagnetURI)
		},
	)
}

func RunJobWithResolver(
	ctx context.Context,
	repository JobRepository,
	job Job,
	resolve func(context.Context) (Result, error),
) (Result, error) {
	ctx = withJobGID(ctx, job.GID)
	started := time.Now().UTC()
	job.State = JobRunning
	job.Error = ""
	job.StartedAt = &started
	job.FinishedAt = nil
	if err := repository.UpdateJob(ctx, job); err != nil {
		return Result{}, err
	}
	result, err := resolve(ctx)
	if err != nil && (errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		err = jobTimeoutError{cause: context.DeadlineExceeded}
	}
	finished := time.Now().UTC()
	job.FinishedAt = &finished
	if err != nil {
		job.State = JobFailed
		job.Error = err.Error()
		if saveErr := repository.UpdateJob(context.WithoutCancel(ctx), job); saveErr != nil {
			if errors.Is(saveErr, ErrJobCanceled) {
				return Result{}, ErrJobCanceled
			}
			return Result{}, fmt.Errorf("%w；保存任务失败状态: %v", err, saveErr)
		}
		return Result{}, err
	}
	job.State = JobSucceeded
	if err := repository.UpdateJob(context.WithoutCancel(ctx), job); err != nil {
		return Result{}, err
	}
	return result, nil
}

type jobGIDContextKey struct{}

func withJobGID(ctx context.Context, gid string) context.Context {
	return context.WithValue(ctx, jobGIDContextKey{}, gid)
}

func jobGID(ctx context.Context) string {
	value, _ := ctx.Value(jobGIDContextKey{}).(string)
	return value
}

type jobTimeoutError struct {
	cause error
}

func (jobTimeoutError) Error() string {
	return "任务处理超时：已超过 ingest.job_timeout 配置的最长时限；" +
		"115 离线下载或后续扫描、STRM 生成未在时限内完成"
}

func (err jobTimeoutError) Unwrap() error {
	return err.cause
}

func (s *Service) findTask(ctx context.Context, infoHash string) (Task, bool, error) {
	return s.findTaskWithMaxAge(ctx, infoHash, s.pollInterval())
}

func (s *Service) findTaskWithMaxAge(
	ctx context.Context,
	infoHash string,
	maxAge time.Duration,
) (Task, bool, error) {
	tasks, err := s.offlineTasks().query(ctx, []string{infoHash}, maxAge)
	if err != nil {
		return Task{}, false, err
	}
	task, found := tasks[strings.ToLower(infoHash)]
	return task, found, nil
}

func (s *Service) invalidateOfflineTasks(infoHashes ...string) {
	s.offlineTasks().invalidate(infoHashes...)
}

func (s *Service) findTaskWithRetry(
	ctx context.Context,
	infoHash string,
	interval time.Duration,
) (Task, bool, error) {
	for {
		task, found, err := s.findTaskWithMaxAge(ctx, infoHash, interval)
		if err == nil {
			return task, found, nil
		}
		s.logf("警告：查询 115 离线任务失败，将在 %s 后重试：%v",
			interval, err)
		if err := s.waitForNextPoll(ctx, interval); err != nil {
			return Task{}, false, err
		}
	}
}

func (s *Service) waitForTask(
	ctx context.Context,
	magnetURI string,
	infoHash string,
	current Task,
) (Task, error) {
	lastStatus := -999
	lastProgress := -1.0
	lastName := ""
	for {
		if current.Name != "" &&
			(current.Status != lastStatus ||
				current.Progress != lastProgress ||
				current.Name != lastName) {
			s.logf("115 离线任务状态：种子=%q，info_hash=%s，状态=%s，进度=%g，结果ID=%q",
				current.Name, infoHash, offlineStatus(current), current.Progress,
				current.ResultID)
		}
		if current.Status != lastStatus || current.Progress != lastProgress ||
			current.Name != lastName {
			lastStatus = current.Status
			if err := s.Repository.SaveTask(ctx, magnetURI, current); err != nil {
				return Task{}, err
			}
		}
		lastProgress = current.Progress
		lastName = current.Name
		if current.Failed {
			return Task{}, fmt.Errorf("115 离线任务失败：%s", current.Name)
		}
		if current.Done && current.ResultID != "" {
			return current, nil
		}
		next, err := s.offlineTasks().wait(ctx, infoHash, current)
		if err != nil {
			return Task{}, err
		}
		if next.InfoHash != "" {
			next.Category = current.Category
			next.JobGID = current.JobGID
			current = next
		}
	}
}

func (s *Service) offlineTasks() *offlineTaskManager {
	s.offlineMu.Lock()
	defer s.offlineMu.Unlock()
	if s.offlineManager == nil {
		s.offlineManager = newOfflineTaskManager(
			s.Provider, s.Logf, s.adaptivePollInterval,
		)
		s.offlineManager.recycleBinCleanup = s.deleteRecycleBinAsync
	}
	return s.offlineManager
}

func (s *Service) adaptivePollInterval(elapsed time.Duration) time.Duration {
	minimum := s.PollMinInterval
	if minimum <= 0 {
		minimum = defaultAdaptivePollMin
	}
	maximum := s.PollMaxInterval
	if maximum <= 0 {
		maximum = defaultAdaptivePollMax
	}
	interval := elapsed / adaptivePollDivisor
	if interval < minimum {
		return minimum
	}
	if interval > maximum {
		return maximum
	}
	return interval
}

func adaptivePollInterval(elapsed time.Duration) time.Duration {
	return (&Service{}).adaptivePollInterval(elapsed)
}

func (s *Service) pollInterval() time.Duration {
	if s.PollInterval > 0 {
		return s.PollInterval
	}
	return time.Second
}

func (s *Service) waitForNextPoll(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Service) logf(format string, values ...any) {
	if s.Logf != nil {
		s.Logf(format, values...)
	}
}

func offlineStatus(task Task) string {
	switch {
	case task.Failed:
		return fmt.Sprintf("失败(%d)", task.Status)
	case task.Done:
		return fmt.Sprintf("完成(%d)", task.Status)
	case task.Status == 0:
		return "等待(0)"
	case task.Status == 1:
		return "下载中(1)"
	default:
		return fmt.Sprintf("未知(%d)", task.Status)
	}
}

func (s *Service) scanResult(
	ctx context.Context,
	task Task,
	onFile func(File),
) ([]File, string, error) {
	pacer := &requestPacer{interval: s.ScanInterval}
	if err := pacer.wait(ctx); err != nil {
		return nil, "", err
	}
	root, err := s.Provider.OfflineFolderInfo(ctx, task.ResultID)
	if err != nil {
		return nil, "", fmt.Errorf("读取 115 任务结果 %s: %w", task.ResultID, err)
	}
	if !root.IsDir {
		file := remoteFile(root, root.Name, remotePath(root.Parents, root.Name))
		if onFile != nil {
			onFile(file)
		}
		return []File{file}, root.Name, nil
	}
	rootRemotePath := remotePath(root.Parents, root.Name)
	files, err := s.scanFolder(
		ctx, root.ID, "", rootRemotePath, map[string]bool{}, pacer, onFile,
	)
	if err != nil {
		return nil, "", err
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].RelativePath < files[j].RelativePath
	})
	return files, root.Name, nil
}

func (s *Service) scanFolder(
	ctx context.Context,
	folderID string,
	prefix string,
	remotePrefix string,
	visited map[string]bool,
	pacer *requestPacer,
	onFile func(File),
) ([]File, error) {
	if visited[folderID] {
		return nil, fmt.Errorf("115 目录结构存在循环：%s", folderID)
	}
	visited[folderID] = true
	defer delete(visited, folderID)

	const pageSize int64 = 1000
	var files []File
	for offset := int64(0); ; {
		if err := pacer.wait(ctx); err != nil {
			return nil, err
		}
		items, count, err := s.Provider.OfflineListFolder(ctx, folderID, offset, pageSize)
		if err != nil {
			return nil, fmt.Errorf("扫描 115 目录 %s: %w", folderID, err)
		}
		for _, item := range items {
			relativePath := path.Join(prefix, item.Name)
			itemRemotePath := path.Join(remotePrefix, item.Name)
			if item.IsDir {
				children, err := s.scanFolder(
					ctx, item.ID, relativePath, itemRemotePath, visited, pacer, onFile,
				)
				if err != nil {
					return nil, err
				}
				files = append(files, children...)
				continue
			}
			file := remoteFile(
				item, relativePath, "/"+strings.TrimLeft(itemRemotePath, "/"),
			)
			files = append(files, file)
			if onFile != nil {
				onFile(file)
			}
		}
		offset += int64(len(items))
		if len(items) == 0 || offset >= count {
			break
		}
	}
	return files, nil
}

func remoteFile(node RemoteNode, relativePath, remotePathValue string) File {
	return File{
		RelativePath: relativePath,
		Name:         node.Name,
		SHA1:         strings.ToLower(node.SHA1),
		SizeBytes:    node.SizeBytes,
		RemoteID:     node.ID,
		ParentID:     node.ParentID,
		RemotePath:   remotePathValue,
		PickCode:     node.PickCode,
		CreatedAt:    node.CreatedAt,
		UpdatedAt:    node.UpdatedAt,
	}
}

func containsSHA1(files []File, sha1Value string) bool {
	for _, file := range files {
		if strings.EqualFold(file.SHA1, sha1Value) {
			return true
		}
	}
	return false
}

func remotePath(parents []RemoteParent, name string) string {
	segments := make([]string, 0, len(parents)+1)
	for _, parent := range parents {
		if parent.ID != "0" && parent.Name != "" {
			segments = append(segments, parent.Name)
		}
	}
	if name != "" {
		segments = append(segments, name)
	}
	return "/" + strings.TrimLeft(path.Join(segments...), "/")
}

func SafeLibraryName(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(char rune) rune {
		switch {
		case char == '/' || char == '\\':
			return '_'
		case char < 0x20 || char == 0x7f:
			return '_'
		default:
			return char
		}
	}, value)
	value = strings.Trim(value, " .")
	if value == "." || value == ".." {
		return ""
	}
	return value
}

type requestPacer struct {
	interval time.Duration
	lastCall time.Time
}

func (p *requestPacer) wait(ctx context.Context) error {
	if p.interval <= 0 {
		p.lastCall = time.Now()
		return nil
	}
	if !p.lastCall.IsZero() {
		wait := time.Until(p.lastCall.Add(p.interval))
		if wait > 0 {
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	p.lastCall = time.Now()
	return nil
}
