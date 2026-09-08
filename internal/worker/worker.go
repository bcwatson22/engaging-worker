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
	StartupImages(siteURL string) ([]render.StartupImage, error)
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

// hashKey namespaces the last-rendered hash by where the artifact was written.
//
// engaging-service records its own renders under the bare artifact key, and
// while both services render the same publish they must not share that key.
// Whichever finished first would record the new hash, and the other's
// unchanged-content check would then refuse to render at all — so the parallel
// run this split depends on would quietly produce one output instead of two to
// compare, looking like a broken worker rather than a collision.
//
// Deriving it from Prefix means one switch moves both the object and its hash:
// with candidate/ the two services are independent, and at cutover the prefix
// empties and this becomes exactly the key engaging-service already uses.
func (w *Worker) hashKey(artifact render.Artifact) string {
	return w.Prefix + artifact.Key
}

// Job names, matching the payload engaging-service writes.
const (
	CVPDFJob         = "cv-pdf"
	StartupImagesJob = "startup-images"
)

// artifacts maps a job name to what it renders.
var artifacts = map[string]render.Artifact{
	CVPDFJob:         render.CVPDF,
	StartupImagesJob: render.StartupImagesArtifact,
}

// Handle renders one job.
func (w *Worker) Handle(ctx context.Context, job queue.Job) error {
	artifact, ok := artifacts[job.Job]
	if !ok {
		// Permanent: retrying will not teach this worker an artifact it does
		// not implement, and attempt five fails exactly as attempt one did.
		return fmt.Errorf("%w: unknown artifact %q", queue.ErrPermanent, job.Job)
	}

	urls := make([]string, 0, len(artifact.Paths))
	for _, path := range artifact.Paths {
		urls = append(urls, w.SiteURL+path)
	}

	live, err := w.assertChanged(ctx, urls, w.hashKey(artifact), job.Force)
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

	published, err := w.publish(ctx, browser, job.Job, artifact, urls)
	if err != nil {
		return err
	}

	// Recorded only after every upload has succeeded, so a run that failed
	// part-way retries against the same previous hash rather than being
	// treated as done.
	if err := w.Store.Set(ctx, w.hashKey(artifact), live); err != nil {
		return err
	}

	slog.Info("artifact published", "job", job.Job, "result", published,
		"ms", time.Since(start).Milliseconds())

	return nil
}

// publish renders and uploads, and reports what it produced — a public URL for
// the PDF, a count for the splash screens, which have no single URL between
// them.
func (w *Worker) publish(
	ctx context.Context,
	browser Renderer,
	job string,
	artifact render.Artifact,
	urls []string,
) (string, error) {
	if job == StartupImagesJob {
		return w.uploadStartupImages(ctx, browser, artifact)
	}

	pdf, err := browser.PDF(urls[0])
	if err != nil {
		return "", err
	}

	return w.Uploader.Upload(ctx, w.Prefix+artifact.Key, pdf,
		artifact.ContentType, artifact.CacheControl)
}

// uploadStartupImages uploads sequentially, so a failure part-way leaves the
// earlier images uploaded and the hash unrecorded — the retry simply
// overwrites them.
func (w *Worker) uploadStartupImages(
	ctx context.Context,
	browser Renderer,
	artifact render.Artifact,
) (string, error) {
	captured, err := browser.StartupImages(w.SiteURL)
	if err != nil {
		return "", err
	}

	for _, image := range captured {
		if _, err := w.Uploader.Upload(ctx, w.Prefix+image.Key, image.Image,
			artifact.ContentType, artifact.CacheControl); err != nil {
			return "", err
		}
	}

	return fmt.Sprintf("%d startup images", len(captured)), nil
}

// assertChanged returns the live hash, refusing to render while the site is
// still serving what was rendered last time.
func (w *Worker) assertChanged(ctx context.Context, urls []string, artifactKey string, force bool) (string, error) {
	live, err := hash.Combined(ctx, w.Client, urls)
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
