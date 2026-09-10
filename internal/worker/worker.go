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
	"github.com/bcwatson22/engaging-worker/internal/records"
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

// History records what a render produced and what it cost, for the status
// endpoint engaging-service serves.
type History interface {
	Add(ctx context.Context, artifact string, r records.Record) error
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
	History  History
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
func (w *Worker) hashKey(prefix string, artifact render.Artifact) string {
	return prefix + artifact.Key
}

/*
Where this job writes. The job decides, falling back to the worker's own

	setting for the CLI, which has no payload to carry one.

	Absent means production, which is deliberate in both directions: a producer
	that predates this field sends nothing and gets the right destination, and a
	candidate render has to ask for one explicitly rather than inherit it. The
	asymmetry that matters is that a lost prefix re-renders production with
	identical bytes, while a stray one would leave production stale — so the
	failure that costs nothing is the one that happens by default.
*/
func (w *Worker) prefixFor(job queue.Job) string {
	if job.Prefix != "" {
		return job.Prefix
	}

	return w.Prefix
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

	prefix := w.prefixFor(job)

	live, err := w.assertChanged(ctx, urls, w.hashKey(prefix, artifact), job.Force)
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

	published, err := w.publish(ctx, browser, job.Job, prefix, artifact, urls)
	if err != nil {
		return err
	}

	// Recorded only after every upload has succeeded, so a run that failed
	// part-way retries against the same previous hash rather than being
	// treated as done.
	if err := w.Store.Set(ctx, w.hashKey(prefix, artifact), live); err != nil {
		return err
	}

	duration := time.Since(start)

	slog.Info("artifact published", "job", job.Job, "result", published,
		"ms", duration.Milliseconds())

	w.record(ctx, job, prefix, published, duration)

	return nil
}

// record is best-effort and deliberately last. The artifact is already
// published by the time this runs, so a status page that misses an entry is a
// far better outcome than a render reported as failed and retried — which
// would re-render and re-upload something already correct.
func (w *Worker) record(
	ctx context.Context,
	job queue.Job,
	prefix string,
	result string,
	duration time.Duration,
) {
	if w.History == nil {
		return
	}

	/* Namespaced like everything else, so a candidate render does not appear
	   in the history the status page reports as real work. */
	err := w.History.Add(ctx, prefix+job.Job, records.Record{
		Result:     result,
		DurationMs: duration.Milliseconds(),
		Attempts:   job.Attempt,
		ElapsedMs:  records.Elapsed(job.RequestedAt, time.Now()),
	})
	if err != nil {
		slog.Warn("could not record the render", "job", job.Job, "err", err)
	}
}

// publish renders and uploads, and reports what it produced — a public URL for
// the PDF, a count for the splash screens, which have no single URL between
// them.
func (w *Worker) publish(
	ctx context.Context,
	browser Renderer,
	job string,
	prefix string,
	artifact render.Artifact,
	urls []string,
) (string, error) {
	if job == StartupImagesJob {
		return w.uploadStartupImages(ctx, browser, prefix, artifact)
	}

	pdf, err := browser.PDF(urls[0])
	if err != nil {
		return "", err
	}

	return w.Uploader.Upload(ctx, prefix+artifact.Key, pdf,
		artifact.ContentType, artifact.CacheControl)
}

// uploadStartupImages uploads sequentially, so a failure part-way leaves the
// earlier images uploaded and the hash unrecorded — the retry simply
// overwrites them.
func (w *Worker) uploadStartupImages(
	ctx context.Context,
	browser Renderer,
	prefix string,
	artifact render.Artifact,
) (string, error) {
	captured, err := browser.StartupImages(w.SiteURL)
	if err != nil {
		return "", err
	}

	for _, image := range captured {
		if _, err := w.Uploader.Upload(ctx, prefix+image.Key, image.Image,
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
