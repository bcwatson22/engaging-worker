package render

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/launcher"
)

// pageHTML mirrors the shape the real CV has: one .wrapper carrying the border,
// with enough content to spill past a single sheet. That is all fillLastPage
// depends on, so the test needs no network and no Hygraph.
func pageHTML(paragraphs int) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><meta charset="utf-8">
	<style>
	  body { margin: 0; font-family: sans-serif; }
	  .wrapper { border: 2px solid #333; padding: 8px; }
	  p { margin: 0 0 14px; }
	</style></head><body><div class="wrapper">`)

	for i := range paragraphs {
		fmt.Fprintf(&b, "<p>Paragraph %d — content that occupies vertical space.</p>", i)
	}

	b.WriteString(`</div></body></html>`)

	return b.String()
}

// browser is shared: launching Chrome is the slow part, and every case here
// wants the same one.
func browser(t *testing.T) *Browser {
	t.Helper()

	path, found := launcher.LookPath()
	if !found {
		t.Skip("no Chrome on this machine")
	}

	b, err := Launch(path)
	if err != nil {
		t.Fatalf("launching chrome: %v", err)
	}
	t.Cleanup(b.Close)

	return b
}

func serve(t *testing.T, html string) string {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("content-type", "text/html")
			_, _ = w.Write([]byte(html))
		}))
	t.Cleanup(server.Close)

	return server.URL
}

func TestPDFRendersAMultiPageDocument(t *testing.T) {
	b := browser(t)
	url := serve(t, pageHTML(120))

	pdf, err := b.PDF(url)
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}

	if !strings.HasPrefix(string(pdf[:5]), "%PDF-") {
		t.Fatalf("not a PDF: %q", pdf[:16])
	}

	pages, err := PageCount(pdf)
	if err != nil {
		t.Fatalf("reading page count: %v", err)
	}
	if pages < 2 {
		t.Errorf("expected the document to span pages, got %d", pages)
	}

	// Puppeteer's default, and the reason for --export-tagged-pdf: without it
	// the PDF has no accessibility structure tree.
	if !strings.Contains(string(pdf), "/StructTreeRoot") {
		t.Error("expected a tagged PDF with a structure tree")
	}
}

// A page that fits on one sheet cannot be padded without adding a second, so
// the search must decline rather than pad it anyway. The render still has to
// succeed — the border is cosmetic and a short one beats no PDF.
func TestPDFSurvivesAPageThatCannotBeFilled(t *testing.T) {
	b := browser(t)
	url := serve(t, pageHTML(1))

	pdf, err := b.PDF(url)
	if err != nil {
		t.Fatalf("rendering should not fail on an unfillable page: %v", err)
	}

	pages, err := PageCount(pdf)
	if err != nil {
		t.Fatalf("reading page count: %v", err)
	}
	if pages != 1 {
		t.Errorf("expected a single page, got %d", pages)
	}
}

func TestPDFReportsNavigationFailures(t *testing.T) {
	b := browser(t)

	if _, err := b.PDF("http://127.0.0.1:1/nothing-here"); err == nil {
		t.Fatal("expected an error navigating to a dead address")
	}
}

func TestLaunchRejectsAMissingBinary(t *testing.T) {
	if _, err := Launch("/nonexistent/chrome"); err == nil {
		t.Fatal("expected an error for a missing browser binary")
	}
}

// The deployed image sets CHROME_PATH, but locally it is empty and the
// installed browser is found instead. That branch only runs when the path is
// genuinely empty, so it needs its own case.
func TestLaunchFindsAnInstalledBrowser(t *testing.T) {
	if _, found := launcher.LookPath(); !found {
		t.Skip("no Chrome on this machine")
	}

	b, err := Launch("")
	if err != nil {
		t.Fatalf("expected an installed browser to be found: %v", err)
	}
	b.Close()
}

// The adapter's own error path: a closed page cannot be printed.
func TestPrintOnAClosedPageFails(t *testing.T) {
	b := browser(t)

	p, err := b.newPage()
	if err != nil {
		t.Fatalf("opening page: %v", err)
	}
	p.close()

	if _, err := p.print(); err == nil {
		t.Fatal("expected printing a closed page to fail")
	}
}

// The adapter's screenshot path against a real browser: emulation applied by
// Chrome, and a PNG of the right pixel size out the other end.
func TestCaptureAgainstARealBrowser(t *testing.T) {
	b := browser(t)
	url := serve(t, pageHTML(4))

	device := Device{Width: 390, Height: 844, Ratio: 3}

	p, err := b.newPage()
	if err != nil {
		t.Fatalf("opening page: %v", err)
	}
	defer p.close()

	png, err := capture(p, url, device, func(time.Duration) {})
	if err != nil {
		t.Fatalf("capturing: %v", err)
	}

	if len(png) < 8 || string(png[1:4]) != "PNG" {
		t.Fatalf("not a PNG: %q", png[:min(8, len(png))])
	}

	// Bytes 16-24 of a PNG header are width and height, big-endian. The
	// manifest advertises these exact dimensions, so the device scale factor
	// has to have been applied by Chrome rather than lost.
	width := int(png[16])<<24 | int(png[17])<<16 | int(png[18])<<8 | int(png[19])
	if want := device.Width * device.Ratio; width != want {
		t.Errorf("expected %dpx wide, got %d", want, width)
	}
}

// The adapter's other error path: a closed page cannot be emulated either.
func TestSetViewportOnAClosedPageFails(t *testing.T) {
	b := browser(t)

	p, err := b.newPage()
	if err != nil {
		t.Fatalf("opening page: %v", err)
	}
	p.close()

	if err := p.setViewport(Device{Width: 390, Height: 844, Ratio: 3}); err == nil {
		t.Fatal("expected setting a viewport on a closed page to fail")
	}
}
