package render

import (
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
)

const (
	fillerID = "pdf-page-filler"

	// An A4 content box is ~1085px tall at 96dpi, so a filler this size always
	// spills onto a new page — it seeds the upper bound of the search.
	maxFill = 1200

	mmPerInch = 25.4
	marginMM  = 5.0
)

// Puppeteer's own A4, to four decimal places. Not 8.27 x 11.7: the 0.0071in
// difference in height moves the filler by a pixel, which is enough to make a
// byte comparison against the existing renders fail.
const (
	paperWidthIn  = 8.2677
	paperHeightIn = 11.6929
)

// Puppeteer defaults `tagged` to true, which Chrome's raw printToPDF does not.
// Losing it costs the PDF its accessibility structure tree — invisible in a
// page-count comparison, and the one regression that matters most here.
var (
	generateTagged = true
	marginIn       = marginMM / mmPerInch
)

var countPattern = regexp.MustCompile(`(?s)/Type\s*/Pages.{0,200}?/Count\s+(\d+)`)

// applyFiller is serialised into the browser, so it can only reach its own
// arguments — no package scope.
const applyFiller = `(id, height) => {
  const filler = document.getElementById(id) ?? document.createElement('div');
  filler.id = id;
  filler.setAttribute('aria-hidden', 'true');
  filler.style.height = height + 'px';
  document.querySelector('.wrapper')?.append(filler);
}`

// Puppeteer defaults waitForFonts to true, and there is no CDP parameter for
// it — it awaits document.fonts.ready itself. Without this the webfont can be
// missing from the output while every other check still passes.
const fontsReady = `() => document.fonts.ready.then(() => true)`

// ErrNoPageCount means the generated PDF had no readable page tree, which
// should be impossible and is worth failing on rather than guessing around.
var ErrNoPageCount = fmt.Errorf("could not read the page count from the generated PDF")

// PageCount reads the page tree straight out of the PDF bytes. Chrome writes
// the catalogue uncompressed, so this needs no PDF library.
func PageCount(pdf []byte) (int, error) {
	m := countPattern.FindSubmatch(pdf)
	if m == nil {
		return 0, ErrNoPageCount
	}

	n, err := strconv.Atoi(string(m[1]))
	if err != nil {
		return 0, ErrNoPageCount
	}

	return n, nil
}

// searchFill finds the largest filler height that still fits the same number
// of pages.
func searchFill(base int, count func(int) (int, error)) (int, error) {
	spilled, err := count(maxFill)
	if err != nil {
		return 0, err
	}

	if spilled == base {
		return 0, fmt.Errorf("a %dpx filler did not spill onto a new page", maxFill)
	}

	fits, spills := 0, maxFill
	for spills-fits > 1 {
		next := (fits + spills) / 2

		n, err := count(next)
		if err != nil {
			return 0, err
		}

		if n == base {
			fits = next
		} else {
			spills = next
		}
	}

	return fits, nil
}

// printPDF waits for fonts, then renders the page as it currently stands.
func printPDF(p page) ([]byte, error) {
	if err := p.eval(fontsReady); err != nil {
		return nil, fmt.Errorf("waiting for fonts: %w", err)
	}

	return p.print()
}

// fillLastPage grows the wrapper so its bottom border lands flush with the
// final page break.
//
// The CV's border lives on a single .wrapper element, so Chrome closes it
// where the content ends rather than at the bottom of the last page. The page
// height isn't knowable up front, so the fill is binary-searched against real
// renders — about thirteen of them, which is most of a render's wall time.
func fillLastPage(p page) error {
	count := func(height int) (int, error) {
		if err := p.eval(applyFiller, fillerID, height); err != nil {
			return 0, err
		}

		pdf, err := printPDF(p)
		if err != nil {
			return 0, err
		}

		return PageCount(pdf)
	}

	base, err := count(0)
	if err != nil {
		return err
	}

	fits, err := searchFill(base, count)
	if err != nil {
		return err
	}

	if err := p.eval(applyFiller, fillerID, fits); err != nil {
		return err
	}

	slog.Info("filled the last PDF page", "pages", base, "fillPx", fits)

	return nil
}

// renderPDF loads url and prints it, padding the last page on the way.
func renderPDF(p page, url string) ([]byte, error) {
	// The closest analogue to Puppeteer's waitUntil:'networkidle0' — go-rod
	// counts in-flight requests rather than relying on a lifecycle event.
	wait := p.waitRequestIdle()
	if err := p.navigate(url); err != nil {
		return nil, fmt.Errorf("navigating to %s: %w", url, err)
	}
	wait()

	// Cosmetic only — a short border beats a failed render, which is the same
	// call the Nest implementation makes.
	if err := fillLastPage(p); err != nil {
		slog.Warn("could not fill the last PDF page", "err", err)
	}

	return printPDF(p)
}
