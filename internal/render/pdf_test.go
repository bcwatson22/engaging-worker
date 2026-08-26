package render

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestPageCount(t *testing.T) {
	cases := []struct {
		name string
		pdf  string
		want int
		err  bool
	}{
		{"reads the page tree", "/Type /Pages /Kids [1 0 R] /Count 3", 3, false},
		{"tolerates no space", "/Type/Pages/Count 12", 12, false},
		{"spans intervening keys", "/Type /Pages " + strings.Repeat("x", 100) + " /Count 2", 2, false},
		{"gives up beyond the window", "/Type /Pages " + strings.Repeat("x", 300) + " /Count 2", 0, true},
		{"no page tree at all", "not a pdf", 0, true},
		{"a count too large to be real", "/Type /Pages /Count 99999999999999999999", 0, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := PageCount([]byte(c.pdf))

			if c.err {
				if !errors.Is(err, ErrNoPageCount) {
					t.Fatalf("want ErrNoPageCount, got %v", err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("want %d pages, got %d", c.want, got)
			}
		})
	}
}

// pageAt models a page that spills onto a new sheet once the filler passes
// threshold — which is the only behaviour the search depends on.
func pageAt(base, threshold int) func(int) (int, error) {
	return func(height int) (int, error) {
		if height > threshold {
			return base + 1, nil
		}
		return base, nil
	}
}

func TestSearchFillFindsTheBoundary(t *testing.T) {
	for _, threshold := range []int{0, 1, 107, 500, maxFill - 1} {
		t.Run(fmt.Sprintf("threshold %d", threshold), func(t *testing.T) {
			got, err := searchFill(3, pageAt(3, threshold))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != threshold {
				t.Errorf("want the largest fill that fits (%d), got %d", threshold, got)
			}
		})
	}
}

// If the biggest filler still fits, the page is not behaving as assumed and
// padding it further would silently corrupt the layout.
func TestSearchFillRequiresASpill(t *testing.T) {
	_, err := searchFill(3, func(int) (int, error) { return 3, nil })
	if err == nil {
		t.Fatal("expected an error when a max-size filler does not spill")
	}
	if !strings.Contains(err.Error(), "did not spill") {
		t.Errorf("error should say why: %v", err)
	}
}

func TestSearchFillPropagatesRenderErrors(t *testing.T) {
	boom := errors.New("chrome went away")

	t.Run("on the probing render", func(t *testing.T) {
		_, err := searchFill(3, func(int) (int, error) { return 0, boom })
		if !errors.Is(err, boom) {
			t.Fatalf("want the underlying error, got %v", err)
		}
	})

	t.Run("mid-search", func(t *testing.T) {
		calls := 0
		_, err := searchFill(3, func(height int) (int, error) {
			calls++
			if calls == 1 {
				return 4, nil // the initial spill probe
			}
			return 0, boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("want the underlying error, got %v", err)
		}
	})
}

// Thirteen renders per job is most of a render's wall time, so a change that
// multiplies them should fail here rather than in production.
func TestSearchFillStaysLogarithmic(t *testing.T) {
	calls := 0
	_, err := searchFill(3, func(height int) (int, error) {
		calls++
		return pageAt(3, 107)(height)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if calls > 12 {
		t.Errorf("binary search should take ~11 renders over 0..%d, took %d", maxFill, calls)
	}
}

// The geometry is the part that fails silently: a page-count comparison still
// passes while the output subtly differs from every existing render.
func TestPuppeteerGeometryIsPreserved(t *testing.T) {
	if paperWidthIn != 8.2677 || paperHeightIn != 11.6929 {
		t.Errorf("A4 must match Puppeteer's, got %v x %v", paperWidthIn, paperHeightIn)
	}
	if !generateTagged {
		t.Error("tagged PDFs are Puppeteer's default; without it the accessibility tree is lost")
	}
	if fmt.Sprintf("%.6f", marginIn) != "0.196850" {
		t.Errorf("5mm margin in inches: got %f", marginIn)
	}
}
