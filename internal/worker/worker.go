// Package worker turns a queued job into a rendered, uploaded artifact.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/bcwatson22/engaging-worker/internal/hash"
	"github.com/bcwatson22/engaging-worker/internal/queue"
	"github.com/bcwatson22/engaging-worker/internal/render"
)

// ErrUnchanged is the publish race, not a failure. The CMS notifies the site
// and the service at the same moment, so the page may still be serving its
// previous render. Returning an error hands the job back to the retry ladder,
// which waits for the content to change — self-correcting, rather than a tuned
// delay.
var ErrUnchanged = errors.New("the page has not changed yet — the site is still revalidating")

// Uploader is the slice of object storage this needs.
type Uploader interface {
	Upload(ctx context.Context, key string, body []byte, contentType, cacheControl string) (string, error)
}

// Renderer opens a browser. Injectable so a test never launches Chrome.
type Renderer interface {
	PDF(url string) ([]byte, error)
	Close()
}

// Store records what was last rendered.
type Store interface {
	Get(ctx context.Context, artifact string) (string, error)
	Set(ctx context.Context, artifact, hash string) error
}

// Worker holds everything a job needs.
type Worker struct {
	SiteURL  string
	Prefix   string
	Store    Store
	Uploader Uploader
	Launch   func() (Renderer, error)
	Client   *http.Client
}

// artifacts maps a job name to what it renders.
var artifacts = map[string]render.Artifact{
	"cv-pdf": render.CVPDF,
}

// Handle renders one job.
func (w *Worker) Handle(ctx context.Context, job queue.Job) error {
	artifact, ok := artifacts[job.Job]
	if !ok {
		return fmt.Errorf("unknown artifact %q", job.Job)
	}

	url := w.SiteURL + artifact.Path

	live, err := w.assertChanged(ctx, url, artifact.Key, job.Force)
	if err != nil {
		return err
	}

	start := time.Now()

	browser, err := w.Launch()
	if err != nil {
		return err
	}
	// Always — an orphaned Chrome holds its memory for the life of the
	// container.
	defer browser.Close()

	pdf, err := browser.PDF(url)
	if err != nil {
		return err
	}

	public, err := w.Uploader.Upload(ctx, w.Prefix+artifact.Key, pdf,
		artifact.ContentType, artifact.CacheControl)
	if err != nil {
		return err
	}

	// Recorded only after a successful upload, so a failed render retries
	// against the same previous hash rather than being treated as done.
	if err := w.Store.Set(ctx, artifact.Key, live); err != nil {
		return err
	}

	slog.Info("artifact published", "url", public, "bytes", len(pdf),
		"ms", time.Since(start).Milliseconds())

	return nil
}

// assertChanged returns the live hash, refusing to render while the site is
// still serving what was rendered last time.
func (w *Worker) assertChanged(ctx context.Context, url, artifactKey string, force bool) (string, error) {
	live, err := hash.Combined(ctx, w.Client, []string{url})
	if err != nil {
		return "", err
	}

	if force {
		slog.Info("forced, skipping the content check", "artifact", artifactKey)

		return live, nil
	}

	previous, err := w.Store.Get(ctx, artifactKey)
	if err != nil {
		return "", err
	}

	slog.Info("content check", "artifact", artifactKey,
		"live", short(live), "lastRendered", short(previous))

	if previous == live {
		return "", ErrUnchanged
	}

	return live, nil
}

func short(h string) string {
	if h == "" {
		return "none"
	}
	if len(h) > 8 {
		return h[:8]
	}

	return h
}
