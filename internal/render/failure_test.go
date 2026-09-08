package render

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/go-rod/rod"
)

var errBrowser = errors.New("chrome went away")

// pdfWith is the smallest thing PageCount will read a page count out of.
func pdfWith(pages int) []byte {
	return fmt.Appendf(nil, "%%PDF-1.4 /Type /Pages /Count %d", pages)
}

// fakePage stands in for a browser page so every failure branch can be driven
// deliberately. The real adapter holds no logic, so nothing is lost by testing
// the logic against this instead.
type fakePage struct {
	onNavigate   func(string) error
	onEval       func(js string, args ...any) error
	onPrint      func() ([]byte, error)
	onWaitLoad   func() error
	onViewport   func(Device) error
	onScreenshot func() ([]byte, error)
	viewports    []Device
	closes       int
	waits        int
}

func (f *fakePage) navigate(url string) error {
	if f.onNavigate != nil {
		return f.onNavigate(url)
	}
	return nil
}

func (f *fakePage) waitRequestIdle() func() { return func() { f.waits++ } }

func (f *fakePage) eval(js string, args ...any) error {
	if f.onEval != nil {
		return f.onEval(js, args...)
	}
	return nil
}

func (f *fakePage) print() ([]byte, error) {
	if f.onPrint != nil {
		return f.onPrint()
	}
	return pdfWith(1), nil
}

func (f *fakePage) close() { f.closes++ }

func (f *fakePage) waitLoad() error {
	if f.onWaitLoad != nil {
		return f.onWaitLoad()
	}
	return nil
}

func (f *fakePage) setViewport(d Device) error {
	f.viewports = append(f.viewports, d)
	if f.onViewport != nil {
		return f.onViewport(d)
	}
	return nil
}

func (f *fakePage) screenshot() ([]byte, error) {
	if f.onScreenshot != nil {
		return f.onScreenshot()
	}
	return []byte("\x89PNG"), nil
}

// spillingAt models a document of base pages that gains one once the filler
// passes threshold — the only page behaviour the search depends on.
func spillingAt(base, threshold int) *fakePage {
	height := 0
	p := &fakePage{}
	p.onEval = func(js string, args ...any) error {
		if js == applyFiller && len(args) == 2 {
			height = args[1].(int)
		}
		return nil
	}
	p.onPrint = func() ([]byte, error) {
		if height > threshold {
			return pdfWith(base + 1), nil
		}
		return pdfWith(base), nil
	}
	return p
}

// failingOnEvalCall fails the nth eval and passes the rest, which is how the
// later branches are reached at all.
func failingOnEvalCall(p *fakePage, n int) {
	calls := 0
	inner := p.onEval
	p.onEval = func(js string, args ...any) error {
		calls++
		if calls == n {
			return errBrowser
		}
		if inner != nil {
			return inner(js, args...)
		}
		return nil
	}
}

func TestPrintPDFReportsAFontsFailure(t *testing.T) {
	p := &fakePage{onEval: func(string, ...any) error { return errBrowser }}

	_, err := printPDF(p)
	if !errors.Is(err, errBrowser) {
		t.Fatalf("want the underlying error, got %v", err)
	}
	// Puppeteer's waitForFonts has no CDP equivalent, so a failure here is
	// worth naming rather than reporting as a generic print error.
	if !strings.Contains(err.Error(), "waiting for fonts") {
		t.Errorf("error should say what failed: %v", err)
	}
}

func TestPrintPDFReportsAPrintFailure(t *testing.T) {
	p := &fakePage{onPrint: func() ([]byte, error) { return nil, errBrowser }}

	if _, err := printPDF(p); !errors.Is(err, errBrowser) {
		t.Fatalf("want the underlying error, got %v", err)
	}
}

func TestFillLastPageReportsFailures(t *testing.T) {
	cases := []struct {
		name string
		page func() page
	}{
		{
			"the first filler cannot be applied",
			func() page {
				p := spillingAt(3, 107)
				failingOnEvalCall(p, 1)
				return p
			},
		},
		{
			"a render mid-search fails",
			func() page {
				p := spillingAt(3, 107)
				calls := 0
				p.onPrint = func() ([]byte, error) {
					calls++
					if calls > 2 {
						return nil, errBrowser
					}
					return pdfWith(3 + calls - 1), nil
				}
				return p
			},
		},
		{
			"the output has no readable page tree",
			func() page {
				return &fakePage{onPrint: func() ([]byte, error) {
					return []byte("not a pdf"), nil
				}}
			},
		},
		{
			"the final filler cannot be applied",
			func() page {
				// Counted rather than guessed: the number of probes follows
				// from maxFill, so hard-coding it would rot the moment that
				// changes. The last eval is the write-back of the chosen
				// height, after the search has finished.
				counter := spillingAt(3, 107)
				evals := 0
				inner := counter.onEval
				counter.onEval = func(js string, args ...any) error {
					evals++
					return inner(js, args...)
				}
				if err := fillLastPage(counter); err != nil {
					panic(err)
				}

				p := spillingAt(3, 107)
				failingOnEvalCall(p, evals)
				return p
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := fillLastPage(c.page()); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestFillLastPageSucceeds(t *testing.T) {
	p := spillingAt(3, 107)

	if err := fillLastPage(p); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRenderPDFReportsNavigationFailures(t *testing.T) {
	p := &fakePage{onNavigate: func(string) error { return errBrowser }}

	_, err := renderPDF(p, "https://example.com/cv")
	if !errors.Is(err, errBrowser) {
		t.Fatalf("want the underlying error, got %v", err)
	}
	if !strings.Contains(err.Error(), "example.com/cv") {
		t.Errorf("error should name the URL it could not reach: %v", err)
	}
}

// A short border beats a failed render: the fill is cosmetic, so a document
// that cannot be padded must still produce a PDF.
func TestRenderPDFSurvivesAFailedFill(t *testing.T) {
	p := &fakePage{onPrint: func() ([]byte, error) { return pdfWith(1), nil }}

	pdf, err := renderPDF(p, "https://example.com/cv")
	if err != nil {
		t.Fatalf("a failed fill should not fail the render: %v", err)
	}
	if len(pdf) == 0 {
		t.Error("expected a PDF anyway")
	}
	if p.waits != 1 {
		t.Errorf("should wait for the network to settle once, waited %d", p.waits)
	}
}

func TestRenderPDFReportsAFinalPrintFailure(t *testing.T) {
	// A page that never spills fails the fill after two probes — the base
	// render and the maxFill one — so the third print is the render itself.
	calls := 0
	p := &fakePage{onPrint: func() ([]byte, error) {
		calls++
		if calls >= 3 {
			return nil, errBrowser
		}
		return pdfWith(1), nil
	}}

	if _, err := renderPDF(p, "https://example.com/cv"); !errors.Is(err, errBrowser) {
		t.Fatalf("want the underlying error, got %v", err)
	}
}

func TestBrowserPDFReportsAPageFailure(t *testing.T) {
	b := &Browser{newPage: func() (page, error) { return nil, errBrowser }}

	if _, err := b.PDF("https://example.com/cv"); !errors.Is(err, errBrowser) {
		t.Fatalf("want the underlying error, got %v", err)
	}
}

// An unclosed page leaks a renderer process for the life of the container.
func TestBrowserPDFAlwaysClosesThePage(t *testing.T) {
	p := spillingAt(3, 107)
	b := &Browser{newPage: func() (page, error) { return p, nil }}

	if _, err := b.PDF("https://example.com/cv"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if p.closes != 1 {
		t.Errorf("page should be closed exactly once, got %d", p.closes)
	}
}

func TestLaunchReportsAStartFailure(t *testing.T) {
	_, err := launch("",
		func(string) (string, func(), error) { return "", func() {}, errBrowser },
		func(string) (*rod.Browser, error) { return nil, nil })

	if !errors.Is(err, errBrowser) {
		t.Fatalf("want the underlying error, got %v", err)
	}
	if !strings.Contains(err.Error(), "launching chrome") {
		t.Errorf("error should say which step failed: %v", err)
	}
}

// Cleanup has to run even though connecting failed, or the Chrome that did
// start is orphaned.
func TestLaunchCleansUpWhenConnectingFails(t *testing.T) {
	cleaned := 0

	_, err := launch("",
		func(string) (string, func(), error) { return "ws://control", func() { cleaned++ }, nil },
		func(string) (*rod.Browser, error) { return nil, errBrowser })

	if !errors.Is(err, errBrowser) {
		t.Fatalf("want the underlying error, got %v", err)
	}
	if !strings.Contains(err.Error(), "connecting to chrome") {
		t.Errorf("error should say which step failed: %v", err)
	}
	if cleaned != 1 {
		t.Errorf("cleanup should run exactly once, ran %d times", cleaned)
	}
}

func TestLaunchWiresUpTheBrowser(t *testing.T) {
	b, err := launch("",
		func(string) (string, func(), error) { return "ws://control", func() {}, nil },
		func(string) (*rod.Browser, error) { return &rod.Browser{}, nil })

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.newPage == nil || b.cleanup == nil {
		t.Error("expected both the page factory and the cleanup to be wired")
	}
}

func TestPageFactoryReportsAFailureToOpen(t *testing.T) {
	_, err := pageFactory(func() (*rod.Page, error) { return nil, errBrowser })()

	if !errors.Is(err, errBrowser) {
		t.Fatalf("want the underlying error, got %v", err)
	}
	if !strings.Contains(err.Error(), "opening page") {
		t.Errorf("error should say which step failed: %v", err)
	}
}

func TestConnectChromeRejectsABadControlURL(t *testing.T) {
	if _, err := connectChrome("ws://127.0.0.1:1/nowhere"); err == nil {
		t.Fatal("expected an error connecting to a dead address")
	}
}

// Close is safe on a Browser that never got one.
func TestCloseWithoutCleanup(t *testing.T) {
	(&Browser{}).Close()
}
