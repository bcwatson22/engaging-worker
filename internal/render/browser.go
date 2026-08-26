// Package render drives headless Chrome over the live site to produce the
// artifacts the site links to.
package render

import (
	"fmt"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
)

// Browser owns a Chrome process for the life of one job. It is deliberately
// not long-lived: an orphaned Chrome holds its memory for the life of the
// container, and this worker exits between jobs anyway.
type Browser struct {
	browser *rod.Browser
	cleanup func()
}

// Launch starts Chrome. chromePath is set in the image to the distribution's
// Chromium; when it is empty an already-installed browser is used instead.
//
// The fallback matters: left to itself go-rod downloads a ~150MB Chromium
// snapshot on first run, which is both a surprise locally and a different
// build from the one the comparison baseline uses.
//
// --no-sandbox because the container has no user namespaces to sandbox into;
// --disable-dev-shm-usage because most runtimes give /dev/shm 64MB, which
// Chrome exhausts part-way through a render. --export-tagged-pdf is what
// Puppeteer passes, and without it the PDF loses its accessibility tree.
func Launch(chromePath string) (*Browser, error) {
	l := launcher.New().
		Set("no-sandbox").
		Set("disable-dev-shm-usage").
		Set("export-tagged-pdf")

	if chromePath == "" {
		if found, ok := launcher.LookPath(); ok {
			chromePath = found
		}
	}

	if chromePath != "" {
		l = l.Bin(chromePath)
	}

	controlURL, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("launching chrome: %w", err)
	}

	browser := rod.New().ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		l.Cleanup()
		return nil, fmt.Errorf("connecting to chrome: %w", err)
	}

	return &Browser{browser: browser, cleanup: l.Cleanup}, nil
}

// Close always runs, even on a failed render.
func (b *Browser) Close() {
	if b.browser != nil {
		_ = b.browser.Close()
	}
	if b.cleanup != nil {
		b.cleanup()
	}
}
