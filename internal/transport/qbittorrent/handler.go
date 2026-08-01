package qbittorrent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"magnet-to-strm/internal/ingest"
)

const (
	clientVersion = "v4.6.7"
	apiVersion    = "2.8.3"
	maxAddSize    = 16 << 20
)

type Handler struct {
	Manager  *Controller
	SavePath string
	Username string
	Password string
	session  string
}

func NewHandler(
	manager *Controller,
	savePath string,
	username string,
	password string,
) *Handler {
	sum := sha256.Sum256([]byte(username + "\x00" + password))
	return &Handler{
		Manager: manager, SavePath: savePath, Username: username, Password: password,
		session: hex.EncodeToString(sum[:]),
	}
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	h.serveHTTP(writer, request)
}

func (h *Handler) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/api/v2/auth/login" {
		h.login(writer, request)
		return
	}
	if h.Username != "" && !h.authorized(request) {
		http.Error(writer, "Forbidden", http.StatusForbidden)
		return
	}
	switch request.URL.Path {
	case "/api/v2/auth/logout":
		h.logout(writer, request)
	case "/api/v2/app/version":
		h.text(writer, request, http.MethodGet, clientVersion)
	case "/api/v2/app/webapiVersion":
		h.text(writer, request, http.MethodGet, apiVersion)
	case "/api/v2/app/preferences":
		h.preferences(writer, request)
	case "/api/v2/transfer/info":
		h.transferInfo(writer, request)
	case "/api/v2/torrents/categories":
		h.categories(writer, request)
	case "/api/v2/torrents/createCategory":
		h.createCategory(writer, request)
	case "/api/v2/torrents/info":
		h.torrents(writer, request)
	case "/api/v2/torrents/add":
		h.add(writer, request)
	case "/api/v2/torrents/delete":
		h.delete(writer, request)
	case "/api/v2/torrents/setCategory":
		h.setCategory(writer, request)
	case "/api/v2/torrents/properties":
		h.properties(writer, request)
	case "/api/v2/torrents/files":
		h.files(writer, request)
	case "/api/v2/torrents/topPrio", "/api/v2/torrents/setForceStart",
		"/api/v2/torrents/setShareLimits":
		h.noop(writer, request)
	default:
		http.NotFound(writer, request)
	}
}

func (h *Handler) login(writer http.ResponseWriter, request *http.Request) {
	if !method(writer, request, http.MethodPost) {
		return
	}
	if err := request.ParseForm(); err != nil {
		http.Error(writer, "Fails.", http.StatusBadRequest)
		return
	}
	if h.Username != "" &&
		(request.Form.Get("username") != h.Username || request.Form.Get("password") != h.Password) {
		h.plain(writer, "Fails.")
		return
	}
	http.SetCookie(writer, &http.Cookie{
		Name: "SID", Value: h.session, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	h.plain(writer, "Ok.")
}

func (h *Handler) logout(writer http.ResponseWriter, request *http.Request) {
	if !method(writer, request, http.MethodPost) {
		return
	}
	http.SetCookie(writer, &http.Cookie{
		Name: "SID", Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
	})
}

func (h *Handler) authorized(request *http.Request) bool {
	if username, password, ok := request.BasicAuth(); ok {
		return username == h.Username && password == h.Password
	}
	cookie, err := request.Cookie("SID")
	return err == nil && cookie.Value == h.session
}

func (h *Handler) preferences(writer http.ResponseWriter, request *http.Request) {
	if !method(writer, request, http.MethodGet) {
		return
	}
	h.json(writer, map[string]any{
		"save_path": h.SavePath, "dht": true, "queueing_enabled": true,
		"max_ratio_enabled": false, "max_ratio": -1,
		"max_seeding_time_enabled": false, "max_seeding_time": -1,
		"max_inactive_seeding_time_enabled": false, "max_inactive_seeding_time": -1,
		"max_ratio_act": 0,
	})
}

func (h *Handler) transferInfo(writer http.ResponseWriter, request *http.Request) {
	if !method(writer, request, http.MethodGet) {
		return
	}
	h.json(writer, map[string]any{
		"dl_info_speed": 0, "dl_info_data": 0, "up_info_speed": 0, "up_info_data": 0,
		"dl_rate_limit": 0, "up_rate_limit": 0, "dht_nodes": 0,
		"connection_status": "connected",
	})
}

func (h *Handler) categories(writer http.ResponseWriter, request *http.Request) {
	if !method(writer, request, http.MethodGet) {
		return
	}
	values, err := h.Manager.Categories(request.Context())
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	response := make(map[string]map[string]string, len(values))
	for name, savePath := range values {
		response[name] = map[string]string{"name": name, "savePath": savePath}
	}
	h.json(writer, response)
}

func (h *Handler) createCategory(writer http.ResponseWriter, request *http.Request) {
	if !method(writer, request, http.MethodPost) {
		return
	}
	if err := request.ParseForm(); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(request.Form.Get("category"))
	if name == "" {
		http.Error(writer, "Category name cannot be empty", http.StatusBadRequest)
		return
	}
	if err := h.Manager.CreateCategory(
		request.Context(), name, strings.TrimSpace(request.Form.Get("savePath")),
	); err != nil {
		http.Error(writer, err.Error(), http.StatusConflict)
	}
}

func (h *Handler) torrents(writer http.ResponseWriter, request *http.Request) {
	if !method(writer, request, http.MethodGet) {
		return
	}
	jobs, err := h.Manager.Jobs(request.Context())
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	hashFilter := splitHashes(request.URL.Query().Get("hashes"))
	category, categorySet := request.URL.Query()["category"]
	filter := request.URL.Query().Get("filter")
	var response []map[string]any
	for _, job := range jobs {
		if len(hashFilter) > 0 && !hashFilter[strings.ToLower(job.InfoHash)] {
			continue
		}
		if categorySet && job.Category != category[0] {
			continue
		}
		item := h.torrent(request, job)
		if !matchesFilter(filter, item["state"].(string)) {
			continue
		}
		response = append(response, item)
	}
	if response == nil {
		response = []map[string]any{}
	}
	h.json(writer, response)
}

func (h *Handler) torrent(request *http.Request, job ingest.Job) map[string]any {
	name := magnetName(job.MagnetURI)
	size := int64(0)
	progress := 0.0
	completed := int64(0)
	contentPath := h.SavePath
	if job.State == ingest.JobSucceeded {
		if result, err := h.Manager.Result(request.Context(), job.InfoHash); err == nil {
			name, size, progress, completed = result.Name, result.TotalBytes, 1, result.TotalBytes
			contentPath = filepath.Join(h.SavePath, result.STRMRoot)
		}
	}
	if name == "" {
		name = job.InfoHash
	}
	added := job.CreatedAt.Unix()
	lastActivity := added
	if job.FinishedAt != nil {
		lastActivity = job.FinishedAt.Unix()
	}
	return map[string]any{
		"hash": job.InfoHash, "name": name, "size": size, "total_size": size,
		"progress": progress, "completed": completed, "amount_left": size - completed,
		"state": qbitState(job.State), "category": job.Category, "tags": "",
		"save_path": h.SavePath, "content_path": contentPath,
		"added_on": added, "completion_on": unixTime(job.FinishedAt),
		"last_activity": lastActivity, "eta": 8640000, "dlspeed": 0, "upspeed": 0,
		"downloaded": completed, "uploaded": 0, "ratio": 0, "ratio_limit": -2,
		"seeding_time": 0, "seeding_time_limit": -2,
		"inactive_seeding_time_limit": -2,
	}
}

func (h *Handler) add(writer http.ResponseWriter, request *http.Request) {
	if !method(writer, request, http.MethodPost) {
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxAddSize)
	if err := request.ParseMultipartForm(maxAddSize); err != nil {
		if err := request.ParseForm(); err != nil {
			h.plain(writer, "Fails.")
			return
		}
	}
	category := strings.TrimSpace(request.FormValue("category"))
	var magnets []string
	for _, value := range strings.Split(request.FormValue("urls"), "\n") {
		if value = strings.TrimSpace(value); value != "" {
			magnets = append(magnets, value)
		}
	}
	if request.MultipartForm != nil {
		for _, headers := range request.MultipartForm.File["torrents"] {
			magnet, err := magnetFromUpload(headers)
			if err != nil {
				h.plain(writer, "Fails.")
				return
			}
			magnets = append(magnets, magnet)
		}
	}
	if len(magnets) == 0 {
		h.plain(writer, "Fails.")
		return
	}
	for _, magnet := range magnets {
		if _, err := h.Manager.AddURIWithCategory(request.Context(), magnet, category); err != nil {
			h.plain(writer, "Fails.")
			return
		}
	}
	h.plain(writer, "Ok.")
}

func magnetFromUpload(header *multipart.FileHeader) (string, error) {
	file, err := header.Open()
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxAddSize+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxAddSize {
		return "", errors.New("torrent 文件过大")
	}
	magnet, err := magnetFromTorrent(data)
	if err != nil {
		return "", unsupportedTorrentMessage(err)
	}
	return magnet, nil
}

func (h *Handler) delete(writer http.ResponseWriter, request *http.Request) {
	if !method(writer, request, http.MethodPost) {
		return
	}
	if err := request.ParseForm(); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	jobs, err := h.selectedJobs(request.Context(), request.Form.Get("hashes"))
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	deleteFiles := request.Form.Get("deleteFiles") == "true"
	for _, job := range jobs {
		if err := h.Manager.DeleteWithFiles(
			request.Context(), job.GID, deleteFiles,
		); err != nil {
			http.Error(writer, err.Error(), http.StatusInternalServerError)
			return
		}
	}
}

func (h *Handler) setCategory(writer http.ResponseWriter, request *http.Request) {
	if !method(writer, request, http.MethodPost) {
		return
	}
	if err := request.ParseForm(); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	category := strings.TrimSpace(request.Form.Get("category"))
	jobs, err := h.selectedJobs(request.Context(), request.Form.Get("hashes"))
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, job := range jobs {
		if err := h.Manager.SetCategory(request.Context(), job.GID, category); err != nil &&
			!errors.Is(err, ingest.ErrJobNotFound) {
			http.Error(writer, err.Error(), http.StatusInternalServerError)
			return
		}
	}
}

func (h *Handler) selectedJobs(
	ctx context.Context,
	hashesValue string,
) ([]ingest.Job, error) {
	if strings.EqualFold(strings.TrimSpace(hashesValue), "all") {
		return h.Manager.Jobs(ctx)
	}
	var jobs []ingest.Job
	for hash := range splitHashes(hashesValue) {
		if len(hash) < 16 {
			continue
		}
		job, err := h.Manager.Job(ctx, hash[:16])
		if errors.Is(err, ingest.ErrJobNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (h *Handler) properties(writer http.ResponseWriter, request *http.Request) {
	if !method(writer, request, http.MethodGet) {
		return
	}
	job, ok := h.jobByHash(writer, request)
	if !ok {
		return
	}
	savePath := h.SavePath
	if job.State == ingest.JobSucceeded {
		if result, err := h.Manager.Result(request.Context(), job.InfoHash); err == nil {
			savePath = filepath.Join(h.SavePath, result.STRMRoot)
		}
	}
	h.json(writer, map[string]any{
		"hash": job.InfoHash, "save_path": savePath, "seeding_time": 0,
	})
}

func (h *Handler) files(writer http.ResponseWriter, request *http.Request) {
	if !method(writer, request, http.MethodGet) {
		return
	}
	job, ok := h.jobByHash(writer, request)
	if !ok {
		return
	}
	var response []map[string]any
	if result, err := h.Manager.Result(request.Context(), job.InfoHash); err == nil {
		for _, file := range result.Files {
			if file.STRMPath == "" {
				continue
			}
			response = append(response, map[string]any{
				"index": len(response), "name": file.RelativePath + ".strm",
				"size": file.SizeBytes, "progress": 1, "priority": 1,
				"is_seed": false, "piece_range": []int{0, 0},
				"availability": 1,
			})
		}
	}
	if response == nil {
		response = []map[string]any{}
	}
	h.json(writer, response)
}

func (h *Handler) jobByHash(
	writer http.ResponseWriter,
	request *http.Request,
) (ingest.Job, bool) {
	hash := strings.ToLower(strings.TrimSpace(request.URL.Query().Get("hash")))
	if len(hash) < 16 {
		http.Error(writer, "Not Found", http.StatusNotFound)
		return ingest.Job{}, false
	}
	job, err := h.Manager.Job(request.Context(), hash[:16])
	if err != nil {
		http.Error(writer, "Not Found", http.StatusNotFound)
		return ingest.Job{}, false
	}
	return job, true
}

func (h *Handler) noop(writer http.ResponseWriter, request *http.Request) {
	if !method(writer, request, http.MethodPost) {
		return
	}
}

func (h *Handler) text(
	writer http.ResponseWriter,
	request *http.Request,
	wantMethod string,
	value string,
) {
	if !method(writer, request, wantMethod) {
		return
	}
	h.plain(writer, value)
}

func (h *Handler) plain(writer http.ResponseWriter, value string) {
	writer.Header().Set("Content-Type", "text/plain; charset=UTF-8")
	_, _ = io.WriteString(writer, value)
}

func (h *Handler) json(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}

func method(writer http.ResponseWriter, request *http.Request, want string) bool {
	if request.Method == want {
		return true
	}
	writer.Header().Set("Allow", want)
	http.Error(writer, "Method Not Allowed", http.StatusMethodNotAllowed)
	return false
}

func splitHashes(value string) map[string]bool {
	hashes := make(map[string]bool)
	if strings.EqualFold(strings.TrimSpace(value), "all") || strings.TrimSpace(value) == "" {
		return hashes
	}
	for _, hash := range strings.Split(value, "|") {
		if hash = strings.ToLower(strings.TrimSpace(hash)); hash != "" {
			hashes[hash] = true
		}
	}
	return hashes
}

func magnetName(magnet string) string {
	parsed, err := url.Parse(magnet)
	if err != nil {
		return ""
	}
	return parsed.Query().Get("dn")
}

func qbitState(state string) string {
	switch state {
	case ingest.JobQueued:
		return "queuedDL"
	case ingest.JobRunning:
		return "downloading"
	case ingest.JobSucceeded:
		return "pausedUP"
	case ingest.JobFailed:
		// qBittorrent exposes task failures through the standard TorrentState
		// value "error"; TorrentInfo has no separate error-message field.
		return "error"
	default:
		return "error"
	}
}

func matchesFilter(filter, state string) bool {
	switch filter {
	case "", "all":
		return true
	case "downloading":
		return state == "queuedDL" || state == "downloading"
	case "completed", "seeding":
		return state == "pausedUP"
	case "paused":
		return state == "pausedUP"
	case "errored":
		return state == "error"
	case "active":
		return state == "downloading"
	case "inactive":
		return state != "downloading"
	default:
		return true
	}
}

func unixTime(value *time.Time) int64 {
	if value == nil {
		return -1
	}
	return value.Unix()
}
