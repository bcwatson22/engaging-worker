package hash

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// These are produced by engaging-service's own implementation, not by a
// reimplementation of it:
//
//	node -e 'const {hashContent}=require("…/engaging-service/dist/render/content-hash.js");
//	         console.log(hashContent(require("fs").readFileSync(f,"utf8")))'
//
// If either side changes, this fails — which is the point. The hash is a
// contract between two languages, and nothing else in either repo would notice
// it drifting.
const (
	cvHash      = "af8324129016abf12940b14ef92c21bdf759e15795ed668ea883ba1749297b81"
	unicodeHash = "eb8f906c068d408bd27a6327567d09008aad10b64fa0490a1bdc169e8a1ad8e7"
)

func fixture(t *testing.T, name string) string {
	t.Helper()

	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}

	return string(b)
}

func TestContentMatchesTheServiceImplementation(t *testing.T) {
	cases := []struct {
		name, file, want string
	}{
		{"the live CV page", "cv.html", cvHash},
		// Go's \s omits the Unicode spaces JavaScript's includes. Without the
		// explicit class this case alone diverges, and only on pages that
		// happen to contain one.
		{"real non-breaking and thin spaces", "unicode-space.html", unicodeHash},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Content(fixture(t, c.file))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("hash disagrees with engaging-service\n  want %s\n  got  %s", c.want, got)
			}
		})
	}
}

func TestExtractStripsWhatChangesEveryDeploy(t *testing.T) {
	got, err := Extract(fixture(t, "unicode-space.html"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The RSC payload and chunk filenames change on every deploy, so a hash
	// that included them would report a change on any unrelated release.
	if strings.Contains(got, "__RSC") || strings.Contains(got, "ignore()") {
		t.Errorf("scripts should be stripped: %q", got)
	}
	if strings.Contains(got, "  ") {
		t.Errorf("runs of whitespace should collapse: %q", got)
	}
	if strings.HasPrefix(got, " ") || strings.HasSuffix(got, " ") {
		t.Errorf("should be trimmed: %q", got)
	}
}

func TestExtractRequiresABody(t *testing.T) {
	if _, err := Extract("<html><head></head></html>"); !errors.Is(err, ErrNoBody) {
		t.Fatalf("want ErrNoBody, got %v", err)
	}
	if _, err := Content("no body here"); !errors.Is(err, ErrNoBody) {
		t.Fatalf("want ErrNoBody, got %v", err)
	}
}

func serve(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The edge could otherwise answer with exactly the version this is
		// trying to detect a change from.
		if r.Header.Get("cache-control") != "no-cache" {
			t.Errorf("expected a no-cache request, got %q", r.Header.Get("cache-control"))
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	return server
}

func TestFetch(t *testing.T) {
	server := serve(t, http.StatusOK, fixture(t, "unicode-space.html"))

	got, err := Fetch(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != unicodeHash {
		t.Errorf("want %s, got %s", unicodeHash, got)
	}
}

func TestFetchReportsFailures(t *testing.T) {
	t.Run("a bad status", func(t *testing.T) {
		server := serve(t, http.StatusBadGateway, "")

		_, err := Fetch(context.Background(), server.Client(), server.URL)
		if err == nil || !strings.Contains(err.Error(), "502") {
			t.Fatalf("error should name the status, got %v", err)
		}
	})

	t.Run("an unreachable host", func(t *testing.T) {
		if _, err := Fetch(context.Background(), http.DefaultClient,
			"http://127.0.0.1:1/nothing"); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("an unbuildable request", func(t *testing.T) {
		if _, err := Fetch(context.Background(), http.DefaultClient,
			"://not a url"); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("a body that cannot be read", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("content-length", "100")
			_, _ = w.Write([]byte("short"))
		}))
		t.Cleanup(server.Close)

		if _, err := Fetch(context.Background(), server.Client(), server.URL); err == nil {
			t.Fatal("expected an error reading a truncated body")
		}
	})
}

// An artifact derived from more than one page changes when any of them does,
// so the pages are folded into a single value to compare against.
func TestCombined(t *testing.T) {
	server := serve(t, http.StatusOK, fixture(t, "unicode-space.html"))

	one, err := Combined(context.Background(), server.Client(), []string{server.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	two, err := Combined(context.Background(), server.Client(), []string{server.URL, server.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if one == two {
		t.Error("a second page should change the combined hash")
	}
	// Not the same as hashing the page directly: the combination is itself
	// hashed, which is what engaging-service does.
	if one == unicodeHash {
		t.Error("the combined hash should not equal the single page hash")
	}
}

func TestCombinedReportsFailures(t *testing.T) {
	if _, err := Combined(context.Background(), http.DefaultClient,
		[]string{"http://127.0.0.1:1/nothing"}); err == nil {
		t.Fatal("expected an error")
	}
}

// Guards the reason whitespacePattern is spelled out rather than written \s+.
// If someone simplifies it, this fails with an explanation instead of the port
// quietly disagreeing with the service on any page containing an &nbsp;.
func TestGoDefaultWhitespaceWouldDiverge(t *testing.T) {
	naive := regexp.MustCompile(`\s+`)

	m := bodyPattern.FindStringSubmatch(fixture(t, "unicode-space.html"))
	if m == nil {
		t.Fatal("fixture should have a body")
	}

	stripped := scriptPattern.ReplaceAllString(m[1], "")
	got := sum(strings.TrimSpace(naive.ReplaceAllString(stripped, " ")))

	if got == unicodeHash {
		t.Error("Go's \\s now matches JavaScript's; the explicit class may be unnecessary")
	}
}
