package aria2

import (
	"context"

	"magnet-to-strm/internal/ingest"
	"magnet-to-strm/internal/task"
)

// Controller translates aria2 operations into source-aware task operations.
// Protocol-neutral queueing and execution live in task.Manager.
type Controller struct {
	runner *task.Manager
}

func NewController(runner *task.Manager) *Controller {
	return &Controller{runner: runner}
}

func (c *Controller) AddURI(ctx context.Context, magnetURI string) (string, error) {
	job, err := ingest.NewJobForSource(magnetURI, ingest.TaskSourceAria2)
	if err != nil {
		return "", err
	}
	return c.runner.Submit(ctx, job)
}

func (c *Controller) AddURIs(ctx context.Context, magnetURIs []string) ([]string, error) {
	jobs := make([]ingest.Job, len(magnetURIs))
	for index, magnetURI := range magnetURIs {
		job, err := ingest.NewJobForSource(magnetURI, ingest.TaskSourceAria2)
		if err != nil {
			return nil, err
		}
		jobs[index] = job
	}
	return c.runner.SubmitMany(ctx, jobs)
}

func (c *Controller) Job(ctx context.Context, gid string) (ingest.Job, error) {
	return c.runner.Job(ctx, gid)
}

func (c *Controller) Jobs(ctx context.Context, states ...string) ([]ingest.Job, error) {
	return c.runner.JobsBySource(ctx, ingest.TaskSourceAria2, states...)
}

func (c *Controller) Result(ctx context.Context, infoHash string) (ingest.Result, error) {
	return c.runner.Result(ctx, infoHash)
}

func (c *Controller) Cancel(ctx context.Context, gid string) error {
	return c.runner.Cancel(ctx, gid)
}

func (c *Controller) Delete(ctx context.Context, gid string) error {
	return c.runner.Delete(ctx, gid)
}

func (c *Controller) RebuildSTRMs(ctx context.Context, gid string) (int, error) {
	return c.runner.RebuildSTRMs(ctx, gid)
}

func (c *Controller) Wait() {
	c.runner.Wait()
}
