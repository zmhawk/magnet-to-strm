package qbittorrent

import (
	"context"
	"strings"

	"magnet-to-strm/internal/ingest"
	"magnet-to-strm/internal/task"
)

type CategoryRepository interface {
	CreateCategory(context.Context, string, string) error
	Categories(context.Context) (map[string]string, error)
}

// Controller owns qBittorrent-specific task semantics and metadata.
type Controller struct {
	runner     *task.Manager
	categories CategoryRepository
}

func NewController(runner *task.Manager, categories CategoryRepository) *Controller {
	return &Controller{runner: runner, categories: categories}
}

func (c *Controller) AddURIWithCategory(
	ctx context.Context, magnetURI, category string,
) (string, error) {
	job, err := ingest.NewJobForSource(magnetURI, ingest.TaskSourceQBittorrent)
	if err != nil {
		return "", err
	}
	job.Category = strings.TrimSpace(category)
	return c.runner.Submit(ctx, job)
}

func (c *Controller) Jobs(ctx context.Context, states ...string) ([]ingest.Job, error) {
	return c.runner.JobsBySource(ctx, ingest.TaskSourceQBittorrent, states...)
}

func (c *Controller) Job(ctx context.Context, gid string) (ingest.Job, error) {
	return c.runner.Job(ctx, gid)
}

func (c *Controller) Result(ctx context.Context, infoHash string) (ingest.Result, error) {
	return c.runner.Result(ctx, infoHash)
}

func (c *Controller) SetCategory(ctx context.Context, gid, category string) error {
	job, err := c.runner.Job(ctx, gid)
	if err != nil {
		return err
	}
	job.Category = strings.TrimSpace(category)
	return c.runner.Update(ctx, job)
}

func (c *Controller) CreateCategory(ctx context.Context, name, savePath string) error {
	return c.categories.CreateCategory(ctx, name, savePath)
}

func (c *Controller) Categories(ctx context.Context) (map[string]string, error) {
	return c.categories.Categories(ctx)
}

func (c *Controller) DeleteWithFiles(ctx context.Context, gid string, deleteFiles bool) error {
	return c.runner.DeleteWithFiles(ctx, gid, deleteFiles)
}
