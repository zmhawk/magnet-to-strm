package aria2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"magnet-to-strm/internal/ingest"
)

const version = "1.37.0-magnet-to-strm"
const maxConcurrentDownloads = 128

type Handler struct {
	Manager *Controller
	Secret  string
	Dir     string
}

type rpcRequest struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      json.RawMessage   `json:"id"`
	Method  string            `json:"method"`
	Params  []json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Access-Control-Allow-Origin", "*")
	writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	if request.Method == http.MethodOptions {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", "POST, OPTIONS")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer request.Body.Close()
	var payload json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 2<<20)).
		Decode(&payload); err != nil {
		h.write(writer, rpcResponse{
			JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "Parse error"},
		})
		return
	}
	if strings.HasPrefix(strings.TrimSpace(string(payload)), "[") {
		var requests []rpcRequest
		if err := json.Unmarshal(payload, &requests); err != nil || len(requests) == 0 {
			h.write(writer, rpcResponse{
				JSONRPC: "2.0", Error: &rpcError{Code: -32600, Message: "Invalid Request"},
			})
			return
		}
		h.write(writer, h.handleBatch(request.Context(), requests))
		return
	}
	var rpcRequest rpcRequest
	if err := json.Unmarshal(payload, &rpcRequest); err != nil {
		h.write(writer, rpcResponse{
			JSONRPC: "2.0", Error: &rpcError{Code: -32600, Message: "Invalid Request"},
		})
		return
	}
	h.write(writer, h.handle(request.Context(), rpcRequest))
}

func (h *Handler) handleBatch(
	ctx context.Context,
	requests []rpcRequest,
) []rpcResponse {
	responses := make([]rpcResponse, len(requests))
	var addIndexes []int
	var magnetURIs []string
	for index, request := range requests {
		if request.Method != "aria2.addUri" {
			responses[index] = h.handle(ctx, request)
			continue
		}
		response := rpcResponse{JSONRPC: "2.0", ID: request.ID}
		params, err := h.authorize(request.Params)
		if err != nil {
			response.Error = &rpcError{Code: 1, Message: err.Error()}
			responses[index] = response
			continue
		}
		magnetURI, rpcErr := addURIParam(params)
		if rpcErr != nil {
			response.Error = rpcErr
			responses[index] = response
			continue
		}
		responses[index] = response
		addIndexes = append(addIndexes, index)
		magnetURIs = append(magnetURIs, magnetURI)
	}
	if len(magnetURIs) == 0 {
		return responses
	}
	gids, err := h.Manager.AddURIs(ctx, magnetURIs)
	for offset, index := range addIndexes {
		if err != nil {
			responses[index].Error = &rpcError{Code: 1, Message: err.Error()}
		} else {
			responses[index].Result = gids[offset]
		}
	}
	return responses
}

func (h *Handler) write(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}

func (h *Handler) handle(ctx context.Context, request rpcRequest) rpcResponse {
	response := rpcResponse{JSONRPC: "2.0", ID: request.ID}
	params, err := h.authorize(request.Params)
	if err != nil {
		response.Error = &rpcError{Code: 1, Message: err.Error()}
		return response
	}
	result, rpcErr := h.call(ctx, request.Method, params)
	if rpcErr != nil {
		response.Error = rpcErr
	} else {
		response.Result = result
	}
	return response
}

func (h *Handler) authorize(params []json.RawMessage) ([]json.RawMessage, error) {
	if h.Secret == "" {
		return params, nil
	}
	if len(params) == 0 {
		return nil, errors.New("Unauthorized")
	}
	var token string
	if err := json.Unmarshal(params[0], &token); err != nil || token != "token:"+h.Secret {
		return nil, errors.New("Unauthorized")
	}
	return params[1:], nil
}

func (h *Handler) call(
	ctx context.Context,
	method string,
	params []json.RawMessage,
) (any, *rpcError) {
	switch method {
	case "aria2.addUri":
		magnetURI, rpcErr := addURIParam(params)
		if rpcErr != nil {
			return nil, rpcErr
		}
		gid, err := h.Manager.AddURI(ctx, magnetURI)
		if err != nil {
			return nil, &rpcError{Code: 1, Message: err.Error()}
		}
		return gid, nil
	case "aria2.tellStatus":
		gid, rpcErr := stringParam(params, 0, "缺少 GID")
		if rpcErr != nil {
			return nil, rpcErr
		}
		job, err := h.Manager.Job(ctx, gid)
		if err != nil {
			return nil, &rpcError{Code: 1, Message: "GID not found"}
		}
		return h.status(ctx, job), nil
	case "aria2.remove", "aria2.forceRemove":
		gid, rpcErr := stringParam(params, 0, "缺少 GID")
		if rpcErr != nil {
			return nil, rpcErr
		}
		if err := h.Manager.Cancel(ctx, gid); err != nil {
			return nil, &rpcError{Code: 1, Message: err.Error()}
		}
		return gid, nil
	case "aria2.tellActive":
		jobs, err := h.Manager.Jobs(ctx, ingest.JobRunning)
		return h.statuses(ctx, jobs), internalError(err)
	case "aria2.tellWaiting":
		jobs, err := h.Manager.Jobs(ctx, ingest.JobQueued)
		offset, count := rangeParams(params)
		return page(h.statuses(ctx, jobs), offset, count), internalError(err)
	case "aria2.tellStopped":
		jobs, err := h.Manager.Jobs(
			ctx, ingest.JobSucceeded, ingest.JobFailed, ingest.JobCanceled,
		)
		offset, count := rangeParams(params)
		return page(h.statuses(ctx, jobs), offset, count), internalError(err)
	case "aria2.getGlobalStat":
		active, err := h.Manager.Jobs(ctx, ingest.JobRunning)
		if err != nil {
			return nil, internalError(err)
		}
		waiting, err := h.Manager.Jobs(ctx, ingest.JobQueued)
		if err != nil {
			return nil, internalError(err)
		}
		stopped, err := h.Manager.Jobs(
			ctx, ingest.JobSucceeded, ingest.JobFailed, ingest.JobCanceled,
		)
		if err != nil {
			return nil, internalError(err)
		}
		return map[string]string{
			"downloadSpeed": "0", "uploadSpeed": "0",
			"numActive": strconv.Itoa(len(active)), "numWaiting": strconv.Itoa(len(waiting)),
			"numStopped": strconv.Itoa(len(stopped)), "numStoppedTotal": strconv.Itoa(len(stopped)),
		}, nil
	case "aria2.getVersion":
		return map[string]any{
			"version": version, "enabledFeatures": []string{"BitTorrent", "RPC"},
		}, nil
	case "aria2.getSessionInfo":
		return map[string]string{"sessionId": "magnet-to-strm"}, nil
	case "aria2.getGlobalOption":
		return map[string]string{
			"dir":                      h.Dir,
			"max-concurrent-downloads": strconv.Itoa(maxConcurrentDownloads),
			"seed-time":                "0",
		}, nil
	case "aria2.changeGlobalOption":
		return "OK", nil
	case "system.listMethods":
		return []string{
			"aria2.addUri", "aria2.tellStatus", "aria2.tellActive",
			"aria2.tellWaiting", "aria2.tellStopped", "aria2.getGlobalStat",
			"aria2.remove", "aria2.forceRemove",
			"aria2.getVersion", "aria2.getSessionInfo", "aria2.getGlobalOption",
			"aria2.changeGlobalOption", "system.listMethods", "system.multicall",
		}, nil
	case "system.multicall":
		return h.multicall(ctx, params)
	default:
		return nil, &rpcError{Code: -32601, Message: "Method not found"}
	}
}

func (h *Handler) multicall(
	ctx context.Context,
	params []json.RawMessage,
) (any, *rpcError) {
	if len(params) == 0 {
		return nil, invalidParams("缺少 multicall 参数")
	}
	var calls []struct {
		MethodName string            `json:"methodName"`
		Params     []json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(params[0], &calls); err != nil {
		return nil, invalidParams("无效的 multicall 参数")
	}
	results := make([]any, len(calls))
	var addIndexes []int
	var magnetURIs []string
	flushAdds := func() {
		if len(magnetURIs) == 0 {
			return
		}
		gids, err := h.Manager.AddURIs(ctx, magnetURIs)
		for offset, index := range addIndexes {
			if err != nil {
				results[index] = map[string]any{"faultCode": 1, "faultString": err.Error()}
			} else {
				results[index] = []any{gids[offset]}
			}
		}
		addIndexes = nil
		magnetURIs = nil
	}
	for index, call := range calls {
		callParams, err := h.authorize(call.Params)
		if err != nil {
			flushAdds()
			results[index] = map[string]any{"faultCode": 1, "faultString": err.Error()}
			continue
		}
		if call.MethodName == "aria2.addUri" {
			magnetURI, rpcErr := addURIParam(callParams)
			if rpcErr != nil {
				flushAdds()
				results[index] = map[string]any{
					"faultCode": rpcErr.Code, "faultString": rpcErr.Message,
				}
				continue
			}
			addIndexes = append(addIndexes, index)
			magnetURIs = append(magnetURIs, magnetURI)
			continue
		}
		flushAdds()
		result, rpcErr := h.call(ctx, call.MethodName, callParams)
		if rpcErr != nil {
			results[index] = map[string]any{
				"faultCode": rpcErr.Code, "faultString": rpcErr.Message,
			}
		} else {
			results[index] = []any{result}
		}
	}
	flushAdds()
	return results, nil
}

func addURIParam(params []json.RawMessage) (string, *rpcError) {
	if len(params) == 0 {
		return "", invalidParams("缺少 URI 列表")
	}
	var uris []string
	if err := json.Unmarshal(params[0], &uris); err != nil || len(uris) != 1 {
		return "", invalidParams("每个任务只支持一个磁力链接")
	}
	return uris[0], nil
}

func (h *Handler) status(ctx context.Context, job ingest.Job) map[string]any {
	status := map[string]any{
		"gid": job.GID, "status": ariaState(job.State),
		"totalLength": "0", "completedLength": "0", "downloadSpeed": "0",
		"uploadSpeed": "0", "connections": "0", "dir": h.Dir,
		"files": []any{}, "bittorrent": map[string]any{
			"info": map[string]string{"name": job.InfoHash},
		},
	}
	if job.Error != "" {
		status["errorCode"] = "1"
		status["errorMessage"] = job.Error
	}
	if job.State == ingest.JobSucceeded {
		result, err := h.Manager.Result(ctx, job.InfoHash)
		if err == nil {
			status["totalLength"] = strconv.FormatInt(result.TotalBytes, 10)
			status["completedLength"] = strconv.FormatInt(result.TotalBytes, 10)
			status["bittorrent"] = map[string]any{"info": map[string]string{"name": result.Name}}
			files := make([]map[string]any, 0, len(result.Files))
			for index, file := range result.Files {
				files = append(files, map[string]any{
					"index": strconv.Itoa(index + 1), "path": file.STRMPath,
					"length":          strconv.FormatInt(file.SizeBytes, 10),
					"completedLength": strconv.FormatInt(file.SizeBytes, 10),
					"selected":        "true", "uris": []any{},
				})
			}
			status["files"] = files
		}
	}
	return status
}

func (h *Handler) statuses(ctx context.Context, jobs []ingest.Job) []map[string]any {
	statuses := make([]map[string]any, 0, len(jobs))
	for _, job := range jobs {
		statuses = append(statuses, h.status(ctx, job))
	}
	return statuses
}

func ariaState(state string) string {
	switch state {
	case ingest.JobQueued:
		return "waiting"
	case ingest.JobRunning:
		return "active"
	case ingest.JobSucceeded:
		return "complete"
	case ingest.JobCanceled:
		return "removed"
	default:
		return "error"
	}
}

func page(values []map[string]any, offset, count int) []map[string]any {
	if offset < 0 {
		offset = len(values) + offset
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(values) || count <= 0 {
		return []map[string]any{}
	}
	end := offset + count
	if end > len(values) {
		end = len(values)
	}
	return values[offset:end]
}

func stringParam(params []json.RawMessage, index int, message string) (string, *rpcError) {
	if index >= len(params) {
		return "", invalidParams(message)
	}
	var value string
	if err := json.Unmarshal(params[index], &value); err != nil || value == "" {
		return "", invalidParams(message)
	}
	return value, nil
}

func rangeParams(params []json.RawMessage) (int, int) {
	offset, count := 0, 1000
	if len(params) > 0 {
		_ = json.Unmarshal(params[0], &offset)
	}
	if len(params) > 1 {
		_ = json.Unmarshal(params[1], &count)
	}
	return offset, count
}

func invalidParams(message string) *rpcError {
	return &rpcError{Code: -32602, Message: message}
}

func internalError(err error) *rpcError {
	if err == nil {
		return nil
	}
	return &rpcError{Code: 1, Message: err.Error()}
}
