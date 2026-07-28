package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"magnet-to-strm/internal/ingest"
	"magnet-to-strm/internal/storage/sqlite"
)

type databaseController struct {
	database *sqlite.DB
}

func (c databaseController) Cancel(ctx context.Context, gid string) error {
	return c.database.CancelJob(ctx, gid)
}

func (c databaseController) Delete(ctx context.Context, gid string) error {
	return c.database.DeleteJob(ctx, gid)
}

func (databaseController) RebuildSTRMs(context.Context, string) (int, error) {
	return 0, errors.New("not implemented")
}

func TestJobsAPIReadsLocalDatabase(t *testing.T) {
	database, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	job, err := ingest.NewJob(
		"magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CreateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveTask(context.Background(), job.MagnetURI, ingest.Task{
		InfoHash: job.InfoHash, Name: "Partial task", Status: -1,
	}); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(
		nil, database, database, databaseController{database}, http.NotFoundHandler(),
		http.NotFoundHandler(), false, nil,
	)

	response := httptest.NewRecorder()
	handler.ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("jobs API returned %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Jobs []jobResponse `json:"jobs"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Jobs) != 1 || payload.Jobs[0].GID != job.GID {
		t.Fatalf("unexpected jobs response: %+v", payload.Jobs)
	}

	detailResponse := httptest.NewRecorder()
	handler.ServeHTTP(
		detailResponse,
		httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+job.GID, nil),
	)
	var detail struct {
		Result struct {
			Files []ingest.File `json:"files"`
		} `json:"result"`
	}
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Result.Files == nil {
		t.Fatal("detail result files is null, want empty array")
	}

	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(
		statusResponse,
		httptest.NewRequest(http.MethodGet, "/api/v1/status", nil),
	)
	var status struct {
		Mode        string `json:"mode"`
		P115Enabled bool   `json:"p115_enabled"`
	}
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Mode != "local" || status.P115Enabled {
		t.Fatalf("unexpected local status: %+v", status)
	}

	cancelResponse := httptest.NewRecorder()
	handler.ServeHTTP(
		cancelResponse,
		httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+job.GID+"/cancel", nil),
	)
	if cancelResponse.Code != http.StatusNoContent {
		t.Fatalf("cancel API returned %d: %s",
			cancelResponse.Code, cancelResponse.Body.String())
	}
	canceled, err := database.Job(context.Background(), job.GID)
	if err != nil {
		t.Fatal(err)
	}
	if canceled.State != ingest.JobCanceled {
		t.Fatalf("job state after cancellation = %q", canceled.State)
	}

	deleteResponse := httptest.NewRecorder()
	handler.ServeHTTP(
		deleteResponse,
		httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+job.GID, nil),
	)
	if deleteResponse.Code != http.StatusNoContent {
		t.Fatalf("delete API returned %d: %s",
			deleteResponse.Code, deleteResponse.Body.String())
	}
}
