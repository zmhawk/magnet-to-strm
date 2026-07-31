package p115

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/OpenListTeam/115-sdk-go"

	"magnet-to-strm/internal/ingest"
	"magnet-to-strm/internal/materialize"
)

const maxNFOSize = 16 << 20

func (c *Client) OfflineQuotaRemaining(ctx context.Context) (int, error) {
	if err := c.before(ctx); err != nil {
		return 0, err
	}
	defer c.after()
	info, err := c.sdk.OfflineQuotaInfo(ctx)
	if err != nil {
		return 0, err
	}
	return info.Surplus, nil
}

func (c *Client) ListOfflineTasks(
	ctx context.Context,
	page int64,
) ([]ingest.Task, int, error) {
	if err := c.before(ctx); err != nil {
		return nil, 0, err
	}
	defer c.after()
	var response offlineTaskListResponse
	_, err := c.sdk.AuthRequest(
		ctx,
		sdk.ApiOfflineList,
		http.MethodGet,
		&response,
		sdk.ReqWithForm(sdk.Form{"page": strconv.FormatInt(page, 10)}),
	)
	if err != nil {
		return nil, 0, err
	}
	tasks := make([]ingest.Task, 0, len(response.Tasks))
	for _, item := range response.Tasks {
		tasks = append(tasks, taskFromOfflineResponse(item))
	}
	return tasks, response.PageCount, nil
}

func taskFromOfflineResponse(item offlineTaskResponse) ingest.Task {
	return ingest.Task{
		InfoHash: item.InfoHash, Name: item.Name, ResultID: item.FileID,
		DeleteFileID: item.DeleteFileID, WPPathID: item.WPPathID,
		Status: item.Status, LastUpdate: item.LastUpdate, SizeBytes: item.Size,
		Progress: float64(item.PercentDone), Done: item.Status == 2,
		Failed: item.Status == -1,
	}
}

type offlineTaskListResponse struct {
	PageCount int                   `json:"page_count"`
	Tasks     []offlineTaskResponse `json:"tasks"`
}

type offlineTaskResponse struct {
	InfoHash     string          `json:"info_hash"`
	PercentDone  flexibleFloat64 `json:"percentDone"`
	Size         int64           `json:"size"`
	Name         string          `json:"name"`
	LastUpdate   int64           `json:"last_update"`
	FileID       string          `json:"file_id"`
	DeleteFileID string          `json:"delete_file_id"`
	WPPathID     string          `json:"wp_path_id"`
	Status       int             `json:"status"`
}

// flexibleFloat64 keeps the API value as-is while accepting both the documented
// integer representation and fractional values observed in actual responses.
type flexibleFloat64 float64

func (v *flexibleFloat64) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		*v = 0
		return nil
	}
	if strings.HasPrefix(raw, `"`) {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		raw = strings.TrimSpace(value)
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fmt.Errorf("解析 percentDone %q: %w", raw, err)
	}
	*v = flexibleFloat64(value)
	return nil
}

func (c *Client) AddOfflineTasks(
	ctx context.Context,
	magnetURIs []string,
	workDirID string,
) ([]ingest.OfflineTaskCreateResult, error) {
	if err := c.before(ctx); err != nil {
		return nil, err
	}
	defer c.after()
	var items []offlineTaskAddResponse
	response, err := c.sdk.AuthRequest(
		ctx,
		sdk.ApiAddOffline,
		http.MethodPost,
		&items,
		sdk.ReqWithForm(sdk.Form{
			"urls":       strings.Join(magnetURIs, "\n"),
			"wp_path_id": workDirID,
		}),
	)
	if err != nil {
		var responseBody []byte
		if response != nil {
			responseBody = response.Bytes()
		}
		if duplicateErr := offlineTaskDuplicateError(err, responseBody); duplicateErr != nil {
			var duplicate struct {
				InfoHash     string `json:"info_hash"`
				ErrorCode    int64  `json:"errcode"`
				ErrorMessage string `json:"error_msg"`
			}
			if json.Unmarshal(responseBody, &duplicate) == nil &&
				duplicate.InfoHash != "" {
				return []ingest.OfflineTaskCreateResult{{
					InfoHash: duplicate.InfoHash, ErrCode: duplicate.ErrorCode,
					Error: duplicate.ErrorMessage,
				}}, nil
			}
			if len(magnetURIs) == 1 {
				if infoHash, parseErr := ingest.ParseInfoHash(magnetURIs[0]); parseErr == nil {
					return []ingest.OfflineTaskCreateResult{{
						InfoHash: infoHash, ErrCode: 10008,
						Error: duplicateErr.Error(),
					}}, nil
				}
			}
			return nil, duplicateErr
		}
		return nil, err
	}
	results := make([]ingest.OfflineTaskCreateResult, 0, len(items))
	for _, item := range items {
		if item.InfoHash != "" {
			results = append(results, item.result())
		}
	}
	if len(results) == 0 {
		return nil, errors.New("115 未返回新任务的 info hash")
	}
	return results, nil
}

type offlineTaskAddResponse struct {
	State        bool   `json:"state"`
	InfoHash     string `json:"info_hash"`
	ErrorCode    int64  `json:"errcode"`
	LegacyCode   int64  `json:"errno"`
	ErrorMessage string `json:"error_msg"`
	Message      string `json:"message"`
}

func (r offlineTaskAddResponse) result() ingest.OfflineTaskCreateResult {
	code := r.ErrorCode
	if code == 0 {
		code = r.LegacyCode
	}
	message := r.ErrorMessage
	if message == "" {
		message = r.Message
	}
	return ingest.OfflineTaskCreateResult{
		InfoHash: r.InfoHash, Created: r.State,
		ErrCode: code, Error: message,
	}
}

func offlineTaskDuplicateError(requestErr error, data []byte) error {
	var sdkErr *sdk.Error
	if errors.As(requestErr, &sdkErr) && sdkErr.Code == 10008 {
		return fmt.Errorf("%w：%s", ingest.ErrOfflineTaskExists, sdkErr.Message)
	}
	var duplicate struct {
		ErrorMessage string `json:"error_msg"`
		ErrorCode    int64  `json:"errcode"`
		InfoHash     string `json:"info_hash"`
	}
	if json.Unmarshal(data, &duplicate) != nil || duplicate.ErrorCode != 10008 {
		return nil
	}
	return fmt.Errorf(
		"%w：%s (%s)",
		ingest.ErrOfflineTaskExists,
		duplicate.ErrorMessage,
		duplicate.InfoHash,
	)
}

func (c *Client) DeleteOfflineTask(
	ctx context.Context,
	infoHash string,
	deleteSourceFile bool,
) error {
	if err := c.before(ctx); err != nil {
		return err
	}
	defer c.after()
	return c.sdk.DeleteOfflineTask(ctx, infoHash, deleteSourceFile)
}

// DeleteRecycleBin permanently removes all files currently in the 115
// recycle bin. An empty tid means "clear the recycle bin" for this API.
func (c *Client) DeleteRecycleBin(ctx context.Context) error {
	if err := c.before(ctx); err != nil {
		return err
	}
	defer c.after()
	_, err := c.sdk.RbDelete(ctx, "")
	return err
}

func (c *Client) DeleteOfflineTaskIfExists(
	ctx context.Context,
	infoHash string,
	deleteSourceFile bool,
) (bool, error) {
	for page := int64(1); ; page++ {
		tasks, pageCount, err := c.ListOfflineTasks(ctx, page)
		if err != nil {
			return false, err
		}
		for _, task := range tasks {
			if !strings.EqualFold(task.InfoHash, infoHash) {
				continue
			}
			if err := c.DeleteOfflineTask(ctx, infoHash, deleteSourceFile); err != nil {
				return true, err
			}
			return true, nil
		}
		if page >= int64(pageCount) {
			return false, nil
		}
	}
}

func (c *Client) OfflineFolderInfo(
	ctx context.Context,
	fileID string,
) (ingest.RemoteNode, error) {
	if err := c.before(ctx); err != nil {
		return ingest.RemoteNode{}, err
	}
	defer c.after()
	info, err := c.sdk.GetFolderInfo(ctx, fileID)
	if errors.Is(err, sdk.ErrObjectNotFound) {
		return ingest.RemoteNode{}, ingest.ErrOfflineResultNotFound
	}
	if err != nil {
		return ingest.RemoteNode{}, err
	}
	size := info.SizeByte
	if size == 0 {
		size, err = parseSize(info.Size)
		if err != nil {
			return ingest.RemoteNode{}, err
		}
	}
	node := ingest.RemoteNode{
		ID: info.FileID, Name: info.FileName, SHA1: strings.ToLower(info.Sha1),
		SizeBytes: size, PickCode: info.PickCode,
		IsDir: info.FileCategory != "1" && info.Sha1 == "",
	}
	if len(info.Paths) > 0 {
		node.ParentID = info.Paths[len(info.Paths)-1].FileID
	}
	for _, parent := range info.Paths {
		node.Parents = append(node.Parents, ingest.RemoteParent{
			ID: parent.FileID, Name: parent.FileName,
		})
	}
	return node, nil
}

func (c *Client) OfflineListFolder(
	ctx context.Context,
	folderID string,
	offset int64,
	limit int64,
) ([]ingest.RemoteNode, int64, error) {
	if err := c.before(ctx); err != nil {
		return nil, 0, err
	}
	defer c.after()
	response, err := c.sdk.GetFiles(ctx, &sdk.GetFilesReq{
		CID: folderID, Limit: limit, Offset: offset, ASC: true,
		O: "file_name", Cur: 1, ShowDir: true,
	})
	if err != nil {
		return nil, 0, err
	}
	nodes := make([]ingest.RemoteNode, 0, len(response.Data))
	for _, item := range response.Data {
		nodes = append(nodes, ingest.RemoteNode{
			ID: item.Fid, ParentID: item.Pid, Name: item.Fn,
			SHA1: strings.ToLower(item.Sha1), SizeBytes: item.FS,
			PickCode: item.Pc, IsDir: item.Fc == "0",
			CreatedAt: item.UpPt, UpdatedAt: item.Uet,
		})
	}
	return nodes, response.Count, nil
}

func (c *Client) FileInfo(
	ctx context.Context,
	fileID string,
) (materialize.RemoteFile, error) {
	if err := c.before(ctx); err != nil {
		return materialize.RemoteFile{}, err
	}
	defer c.after()
	info, err := c.sdk.GetFolderInfo(ctx, fileID)
	if errors.Is(err, sdk.ErrObjectNotFound) {
		return materialize.RemoteFile{}, materialize.ErrRemoteNotFound
	}
	if err != nil {
		return materialize.RemoteFile{}, err
	}
	size := info.SizeByte
	if size == 0 {
		size, err = parseSize(info.Size)
		if err != nil {
			return materialize.RemoteFile{}, err
		}
	}
	file := materialize.RemoteFile{
		ID: info.FileID, Name: info.FileName, SHA1: strings.ToLower(info.Sha1),
		SizeBytes: size, PickCode: info.PickCode,
	}
	if len(info.Paths) > 0 {
		file.ParentID = info.Paths[len(info.Paths)-1].FileID
	}
	for _, parent := range info.Paths {
		file.Parents = append(file.Parents, materialize.RemoteParent{
			ID: parent.FileID, Name: parent.FileName,
		})
	}
	return file, nil
}

func (c *Client) SearchFiles(
	ctx context.Context,
	name string,
	offset int64,
	limit int64,
) ([]materialize.RemoteFile, int64, error) {
	if err := c.before(ctx); err != nil {
		return nil, 0, err
	}
	defer c.after()
	response, err := c.sdk.SearchFiles(ctx, &sdk.SearchFilesReq{
		SearchValue: name, Limit: limit, Offset: offset, FC: "2",
	})
	if err != nil {
		return nil, 0, err
	}
	files := make([]materialize.RemoteFile, 0, len(response.Data))
	for _, item := range response.Data {
		size, err := parseSize(item.FileSize)
		if err != nil {
			return nil, 0, err
		}
		files = append(files, materialize.RemoteFile{
			ID: item.FileID, Name: item.FileName, SHA1: strings.ToLower(item.Sha1),
			ParentID: item.ParentID, SizeBytes: size, PickCode: item.PickCode,
		})
	}
	return files, response.Count, nil
}

func (c *Client) ListFiles(
	ctx context.Context,
	folderID string,
	offset int64,
	limit int64,
) ([]materialize.RemoteFile, int64, error) {
	if err := c.before(ctx); err != nil {
		return nil, 0, err
	}
	defer c.after()
	response, err := c.sdk.GetFiles(ctx, &sdk.GetFilesReq{
		CID: folderID, Limit: limit, Offset: offset, ASC: false,
		O: "user_utime", Cur: 1, ShowDir: false,
	})
	if err != nil {
		return nil, 0, err
	}
	files := make([]materialize.RemoteFile, 0, len(response.Data))
	for _, item := range response.Data {
		files = append(files, materialize.RemoteFile{
			ID: item.Fid, ParentID: item.Pid, Name: item.Fn,
			SHA1: strings.ToLower(item.Sha1), SizeBytes: item.FS, PickCode: item.Pc,
		})
	}
	return files, response.Count, nil
}

func (c *Client) DownloadURL(
	ctx context.Context,
	pickCode string,
	userAgent string,
) (string, error) {
	if err := c.before(ctx); err != nil {
		return "", err
	}
	defer c.after()
	urls, err := c.sdk.DownURL(ctx, pickCode, userAgent)
	if err != nil {
		return "", err
	}
	for _, item := range urls {
		if item.URL.URL != "" {
			return item.URL.URL, nil
		}
	}
	return "", errors.New("115 未返回下载地址")
}

func (c *Client) ReadNFO(ctx context.Context, pickCode string) ([]byte, error) {
	downloadURL, err := c.DownloadURL(ctx, pickCode, "magnet-to-strm")
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "magnet-to-strm")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("下载 NFO 返回 HTTP %d", response.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, maxNFOSize+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxNFOSize {
		return nil, fmt.Errorf("NFO 文件超过 %d 字节限制", maxNFOSize)
	}
	return content, nil
}

func (c *Client) Delete(ctx context.Context, fileID, parentID string) error {
	if err := c.before(ctx); err != nil {
		return err
	}
	defer c.after()
	_, err := c.sdk.DelFile(ctx, &sdk.DelFileReq{FileIDs: fileID, ParentID: parentID})
	return err
}

func parseSize(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	size, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("解析文件大小 %q: %w", value, err)
	}
	return size, nil
}
