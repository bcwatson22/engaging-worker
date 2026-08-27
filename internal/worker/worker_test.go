package worker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bcwatson22/engaging-worker/internal/queue"
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
}

func (f *fakeUploader) Upload(_ context.Context, key string, body []byte, _, _ string) (string, error) {
	f.key, f.body = key, body

	return "https://pub.r2.dev/" + key, f.err
}

type fakeRenderer struct {
	pdf    []byte
	err    error
	closes int
}

func (f *fakeRenderer) PDF(string) ([]byte, error) { return f.pdf, f.err }
func (f *fakeRenderer) Close()                     { f.closes++ }

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
	if store.set["billy-watson-cv.pdf"] == "" {
		t.Error("should record the hash it rendered")
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

	return stub.assertChanged(context.Background(), w.SiteURL+"/cv", "unused", true)
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
			job:  queue.Job{V: queue.Version, Job: "startup-images"},
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
