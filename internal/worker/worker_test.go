package worker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bcwatson22/engaging-worker/internal/queue"
	"github.com/bcwatson22/engaging-worker/internal/records"
	"github.com/bcwatson22/engaging-worker/internal/render"
)

var errFake = errors.New("something broke")

type fakeStore struct {
	previous string
	getErr   error
	setErr   error
	set      map[string]string
}

func (f *fakeStore) Get(context.Context, string) (string, error) {
	return f.previous, f.getErr
}

func (f *fakeStore) Set(_ context.Context, artifact, hash string) error {
	if f.set == nil {
		f.set = map[string]string{}
	}
	f.set[artifact] = hash

	return f.setErr
}

type fakeUploader struct {
	err  error
	key  string
	body []byte
	// count and failAfter exist for the splash screens, which upload
	// twenty-two objects rather than one.
	count     int
	failAfter int
}

func (f *fakeUploader) Upload(_ context.Context, key string, body []byte, _, _ string) (string, error) {
	f.key, f.body = key, body
	f.count++

	if f.failAfter > 0 && f.count >= f.failAfter {
		return "", errFake
	}

	return "https://pub.r2.dev/" + key, f.err
}

type fakeHistory struct {
	added    []records.Record
	artifact string
	err      error
}

func (f *fakeHistory) Add(_ context.Context, artifact string, r records.Record) error {
	f.artifact = artifact
	f.added = append(f.added, r)

	return f.err
}

type fakeRenderer struct {
	pdf        []byte
	err        error
	startupErr error
	closes     int
}

func (f *fakeRenderer) PDF(string) ([]byte, error) { return f.pdf, f.err }

func (f *fakeRenderer) StartupImages(string) ([]render.StartupImage, error) {
	if f.startupErr != nil {
		return nil, f.startupErr
	}

	captured := make([]render.StartupImage, 0,
		len(render.StartupPages)*len(render.StartupDevices))

	for _, p := range render.StartupPages {
		for _, d := range render.StartupDevices {
			captured = append(captured, render.StartupImage{
				Key: render.StartupKey(p.Name, d), Image: []byte("PNG"),
			})
		}
	}

	return captured, nil
}

func (f *fakeRenderer) Close() { f.closes++ }

// setup builds a worker over a stub site, with everything succeeding unless a
// case says otherwise.
func setup(t *testing.T, options func(*Worker, *fakeStore, *fakeUploader, *fakeRenderer)) (
	*Worker, *fakeStore, *fakeUploader, *fakeRenderer,
) {
	t.Helper()

	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><body><p>live content</p></body></html>"))
	}))
	t.Cleanup(site.Close)

	store := &fakeStore{}
	uploader := &fakeUploader{}
	renderer := &fakeRenderer{pdf: []byte("%PDF-1.4")}

	w := &Worker{
		SiteURL:  site.URL,
		Prefix:   "candidate/",
		Store:    store,
		Uploader: uploader,
		Launch:   func() (Renderer, error) { return renderer, nil },
		Client:   site.Client(),
	}

	if options != nil {
		options(w, store, uploader, renderer)
	}

	return w, store, uploader, renderer
}

func job() queue.Job {
	return queue.Job{V: queue.Version, Job: "cv-pdf"}
}

func TestHandleRendersAndRecords(t *testing.T) {
	w, store, uploader, renderer := setup(t, nil)

	if err := w.Handle(context.Background(), job()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The candidate prefix is what keeps Phase 1 and 2 output away from
	// anything the site links to.
	if uploader.key != "candidate/billy-watson-cv.pdf" {
		t.Errorf("uploaded to %q", uploader.key)
	}
	if string(uploader.body) != "%PDF-1.4" {
		t.Errorf("uploaded %q", uploader.body)
	}
	// Namespaced by the same prefix as the object, so engaging-service's own
	// record of what it last rendered is untouched while both run.
	if store.set["candidate/billy-watson-cv.pdf"] == "" {
		t.Errorf("should record the hash under the candidate key, got %+v", store.set)
	}
	if _, clashed := store.set["billy-watson-cv.pdf"]; clashed {
		t.Error("must not write the key engaging-service records its own renders under")
	}
	if renderer.closes != 1 {
		t.Errorf("browser should be closed exactly once, got %d", renderer.closes)
	}
}

// The CMS notifies the site and this worker at the same moment, so the first
// attempt usually finds the page still serving its previous render.
func TestHandleWaitsForTheSiteToCatchUp(t *testing.T) {
	w, store, _, _ := setup(t, nil)

	live, err := hashOfStubSite(t, w)
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}
	store.previous = live

	if err := w.Handle(context.Background(), job()); !errors.Is(err, ErrUnchanged) {
		t.Fatalf("want ErrUnchanged, got %v", err)
	}
	if len(store.set) != 0 {
		t.Error("nothing should be recorded when nothing was rendered")
	}
}

// A manual render exists for changes the CMS knows nothing about — a print
// stylesheet — which the unchanged check would otherwise reject.
func TestHandleForcedSkipsTheContentCheck(t *testing.T) {
	w, store, _, renderer := setup(t, nil)

	live, err := hashOfStubSite(t, w)
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}
	store.previous = live

	forced := job()
	forced.Force = true

	if err := w.Handle(context.Background(), forced); err != nil {
		t.Fatalf("forced render should proceed: %v", err)
	}
	if renderer.closes != 1 {
		t.Error("expected a render")
	}
}

func hashOfStubSite(t *testing.T, w *Worker) (string, error) {
	t.Helper()

	stub := &Worker{SiteURL: w.SiteURL, Client: w.Client, Store: &fakeStore{}}

	return stub.assertChanged(context.Background(), []string{w.SiteURL + "/cv"}, "unused", true)
}

func TestHandleReportsFailures(t *testing.T) {
	cases := []struct {
		name  string
		job   queue.Job
		setup func(*Worker, *fakeStore, *fakeUploader, *fakeRenderer)
		want  string
	}{
		{
			name: "an artifact it does not know",
			job:  queue.Job{V: queue.Version, Job: "og-images"},
			want: "unknown artifact",
		},
		{
			name: "the site cannot be reached",
			job:  job(),
			setup: func(w *Worker, _ *fakeStore, _ *fakeUploader, _ *fakeRenderer) {
				w.SiteURL = "http://127.0.0.1:1"
			},
		},
		{
			name: "the store cannot be read",
			job:  job(),
			setup: func(_ *Worker, s *fakeStore, _ *fakeUploader, _ *fakeRenderer) {
				s.getErr = errFake
			},
		},
		{
			name: "the browser will not start",
			job:  job(),
			setup: func(w *Worker, _ *fakeStore, _ *fakeUploader, _ *fakeRenderer) {
				w.Launch = func() (Renderer, error) { return nil, errFake }
			},
		},
		{
			name: "the render fails",
			job:  job(),
			setup: func(_ *Worker, _ *fakeStore, _ *fakeUploader, r *fakeRenderer) {
				r.err = errFake
			},
		},
		{
			name: "the upload fails",
			job:  job(),
			setup: func(_ *Worker, _ *fakeStore, u *fakeUploader, _ *fakeRenderer) {
				u.err = errFake
			},
		},
		{
			name: "the hash cannot be recorded",
			job:  job(),
			setup: func(_ *Worker, s *fakeStore, _ *fakeUploader, _ *fakeRenderer) {
				s.setErr = errFake
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, _, _, _ := setup(t, c.setup)

			err := w.Handle(context.Background(), c.job)
			if err == nil {
				t.Fatal("expected an error")
			}
			if c.want != "" && !strings.Contains(err.Error(), c.want) {
				t.Errorf("error should mention %q: %v", c.want, err)
			}
		})
	}
}

func TestShort(t *testing.T) {
	cases := map[string]string{
		"": "none", "abc": "abc", "0123456789abcdef": "01234567",
	}

	for in, want := range cases {
		if got := short(in); got != want {
			t.Errorf("short(%q) = %q, want %q", in, got, want)
		}
	}
}

// The cutover is meant to be a prefix change and nothing else: with the prefix
// emptied, both the object key and the hash key become exactly the ones
// engaging-service already uses.
func TestHashKeyFollowsThePrefix(t *testing.T) {
	cases := map[string]string{
		"candidate/": "candidate/billy-watson-cv.pdf",
		"":           "billy-watson-cv.pdf",
	}

	for prefix, want := range cases {
		w := &Worker{Prefix: prefix}

		if got := w.hashKey(render.CVPDF); got != want {
			t.Errorf("prefix %q: want %q, got %q", prefix, want, got)
		}
	}
}

// The splash-screen set: two pages at eleven device sizes, uploaded one at a
// time under the same prefix as everything else.
func TestHandleCapturesEveryStartupImage(t *testing.T) {
	w, store, uploader, _ := setup(t, nil)

	err := w.Handle(context.Background(), queue.Job{
		V: queue.Version, Job: StartupImagesJob, Force: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := len(render.StartupPages) * len(render.StartupDevices)
	if uploader.count != want {
		t.Errorf("expected %d uploads, got %d", want, uploader.count)
	}

	// Namespaced like the PDF, so the candidate run cannot disturb what
	// engaging-service records for its own.
	if store.set["candidate/startup-images"] == "" {
		t.Errorf("should record the hash under the candidate key, got %+v", store.set)
	}
}

func TestHandleReportsAStartupCaptureFailure(t *testing.T) {
	w, _, _, _ := setup(t, func(_ *Worker, _ *fakeStore, _ *fakeUploader, r *fakeRenderer) {
		r.startupErr = errFake
	})

	err := w.Handle(context.Background(), queue.Job{
		V: queue.Version, Job: StartupImagesJob, Force: true,
	})
	if !errors.Is(err, errFake) {
		t.Fatalf("want the underlying error, got %v", err)
	}
}

// A failure part-way leaves the earlier images uploaded and the hash
// unrecorded, so the retry overwrites them rather than skipping them.
func TestHandleDoesNotRecordAPartialStartupUpload(t *testing.T) {
	w, store, uploader, _ := setup(t, func(_ *Worker, _ *fakeStore, u *fakeUploader, _ *fakeRenderer) {
		u.failAfter = 3
	})

	err := w.Handle(context.Background(), queue.Job{
		V: queue.Version, Job: StartupImagesJob, Force: true,
	})
	if err == nil {
		t.Fatal("expected an error")
	}

	if len(store.set) != 0 {
		t.Errorf("a partial upload must not record a hash, got %+v", store.set)
	}
	if uploader.count != 3 {
		t.Errorf("expected it to stop at the failure, uploaded %d", uploader.count)
	}
}

// The status endpoint engaging-service serves reads these; nothing else writes
// them now that rendering has moved.
func TestHandleRecordsWhatTheRenderCost(t *testing.T) {
	history := &fakeHistory{}
	w, _, _, _ := setup(t, func(w *Worker, _ *fakeStore, _ *fakeUploader, _ *fakeRenderer) {
		w.History = history
	})

	job := queue.Job{
		V: queue.Version, Job: CVPDFJob, Force: true, Attempt: 2,
		RequestedAt: time.Now().Add(-30 * time.Second).UTC().Format(time.RFC3339),
	}

	if err := w.Handle(context.Background(), job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(history.added) != 1 {
		t.Fatalf("expected one record, got %d", len(history.added))
	}

	got := history.added[0]
	if history.artifact != CVPDFJob {
		t.Errorf("recorded under %q", history.artifact)
	}
	if got.Attempts != 2 {
		t.Errorf("attempts: got %d", got.Attempts)
	}
	// The gap between elapsed and duration is the publish race made visible,
	// which is the only place that number exists once the logs are gone.
	if got.ElapsedMs < 29_000 {
		t.Errorf("elapsed should span from enqueue, got %d", got.ElapsedMs)
	}
	if got.Result == "" {
		t.Error("expected the result to be recorded")
	}
}

/*
Best-effort and deliberately last: the artifact is already published, so a

	missed status entry beats a render reported as failed and retried.
*/
func TestHandleSurvivesAFailedRecord(t *testing.T) {
	w, _, _, _ := setup(t, func(w *Worker, _ *fakeStore, _ *fakeUploader, _ *fakeRenderer) {
		w.History = &fakeHistory{err: errFake}
	})

	if err := w.Handle(context.Background(), job()); err != nil {
		t.Fatalf("a failed record must not fail the render: %v", err)
	}
}

// The one-off CLI path has no history to write to.
func TestHandleWithoutAHistory(t *testing.T) {
	w, _, _, _ := setup(t, nil)

	if err := w.Handle(context.Background(), job()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
