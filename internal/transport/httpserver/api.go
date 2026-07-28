package httpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"magnet-to-strm/internal/ingest"
)

type TaskStore interface {
	ListJobs(context.Context, ...string) ([]ingest.Job, error)
	Job(context.Context, string) (ingest.Job, error)
	ResultByInfoHash(context.Context, string) (ingest.Result, error)
}

type TaskController interface {
	Cancel(context.Context, string) error
	Delete(context.Context, string) error
	RebuildSTRMs(context.Context, string) (int, error)
}

type jobResponse struct {
	GID        string     `json:"gid"`
	InfoHash   string     `json:"info_hash"`
	Name       string     `json:"name"`
	Progress   float64    `json:"progress"`
	MagnetURI  string     `json:"magnet_uri"`
	State      string     `json:"state"`
	Error      string     `json:"error"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

func registerAPI(
	mux *http.ServeMux,
	store TaskStore,
	controller TaskController,
	p115Enabled bool,
) {
	mux.HandleFunc("/api/v1/status", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, http.MethodGet)
			return
		}
		message := "115 TOKEN 和工作目录已配置，远端操作可用。"
		mode := "full"
		if !p115Enabled {
			mode = "local"
			message = "未配置 115 TOKEN 或工作目录；仍可查看本地数据库记录，115 相关操作已停用。"
		}
		writeJSON(writer, http.StatusOK, map[string]any{
			"p115_enabled": p115Enabled,
			"mode":         mode,
			"message":      message,
		})
	})
	mux.HandleFunc("/api/v1/jobs", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, http.MethodGet)
			return
		}
		jobs, err := store.ListJobs(request.Context())
		if err != nil {
			writeAPIError(writer, http.StatusInternalServerError, "读取任务记录失败")
			return
		}
		response := make([]jobResponse, len(jobs))
		for index, job := range jobs {
			response[len(jobs)-1-index] = presentJob(job)
		}
		writeJSON(writer, http.StatusOK, map[string]any{"jobs": response})
	})
	mux.HandleFunc("/api/v1/jobs/", func(writer http.ResponseWriter, request *http.Request) {
		tail := strings.Trim(strings.TrimPrefix(request.URL.Path, "/api/v1/jobs/"), "/")
		parts := strings.Split(tail, "/")
		if tail == "" || len(parts) > 2 {
			writeAPIError(writer, http.StatusNotFound, "任务不存在")
			return
		}
		gid := parts[0]
		action := ""
		if len(parts) == 2 {
			action = parts[1]
		}
		if request.Method == http.MethodPost && action == "cancel" {
			if controller == nil {
				writeAPIError(writer, http.StatusServiceUnavailable, "任务操作不可用")
				return
			}
			if err := controller.Cancel(request.Context(), gid); err != nil {
				if errors.Is(err, ingest.ErrJobNotFound) {
					writeAPIError(writer, http.StatusNotFound, "任务不存在")
					return
				}
				writeAPIError(writer, http.StatusConflict, err.Error())
				return
			}
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		if request.Method == http.MethodPost && action == "rebuild-strm" {
			if controller == nil {
				writeAPIError(writer, http.StatusServiceUnavailable, "任务操作不可用")
				return
			}
			count, err := controller.RebuildSTRMs(request.Context(), gid)
			if err != nil {
				if errors.Is(err, ingest.ErrJobNotFound) {
					writeAPIError(writer, http.StatusNotFound, "任务不存在")
					return
				}
				writeAPIError(writer, http.StatusConflict, err.Error())
				return
			}
			writeJSON(writer, http.StatusOK, map[string]int{"rebuilt": count})
			return
		}
		if request.Method == http.MethodDelete && action == "" {
			if controller == nil {
				writeAPIError(writer, http.StatusServiceUnavailable, "任务操作不可用")
				return
			}
			if err := controller.Delete(request.Context(), gid); err != nil {
				if errors.Is(err, ingest.ErrJobNotFound) {
					writeAPIError(writer, http.StatusNotFound, "任务不存在")
					return
				}
				writeAPIError(writer, http.StatusConflict, err.Error())
				return
			}
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		if request.Method != http.MethodGet || action != "" {
			methodNotAllowed(writer, "GET, DELETE, POST")
			return
		}
		job, err := store.Job(request.Context(), gid)
		if errors.Is(err, ingest.ErrJobNotFound) {
			writeAPIError(writer, http.StatusNotFound, "任务不存在")
			return
		}
		if err != nil {
			writeAPIError(writer, http.StatusInternalServerError, "读取任务详情失败")
			return
		}
		response := map[string]any{"job": presentJob(job)}
		result, err := store.ResultByInfoHash(request.Context(), job.InfoHash)
		if err == nil {
			response["result"] = result
		} else if !errors.Is(err, sql.ErrNoRows) {
			writeAPIError(writer, http.StatusInternalServerError, "读取任务解析结果失败")
			return
		}
		writeJSON(writer, http.StatusOK, response)
	})
}

func presentJob(job ingest.Job) jobResponse {
	name := job.Name
	if name == "" {
		if magnet, err := url.Parse(job.MagnetURI); err == nil {
			name = magnet.Query().Get("dn")
		}
	}
	return jobResponse{
		GID: job.GID, InfoHash: job.InfoHash, Name: name, Progress: job.Progress,
		MagnetURI: job.MagnetURI,
		State:     job.State, Error: job.Error, CreatedAt: job.CreatedAt,
		StartedAt: job.StartedAt, FinishedAt: job.FinishedAt,
	}
}

func methodNotAllowed(writer http.ResponseWriter, allow string) {
	writer.Header().Set("Allow", allow)
	writeAPIError(writer, http.StatusMethodNotAllowed, "请求方法不受支持")
}

func writeAPIError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]string{"error": message})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
