// Package render drives headless Chrome over the live site to produce the
// artifacts the site links to.
package render

import (
	"fmt"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// Browser owns a Chrome process for the life of one job. It is deliberately
// not long-lived: an orphaned Chrome holds its memory for the life of the
// container, and this worker exits between jobs anyway.
type Browser struct {
	// newPage is a field rather than a method so a test can hand back a page
	// that fails, without a browser being involved at all.
	newPage func() (page, error)
	cleanup func()
}

// startChrome resolves a browser and starts it, returning the control URL and
// the cleanup that must run whether or not connecting succeeds.
func startChrome(chromePath string) (string, func(), error) {
	// Left to itself go-rod downloads a ~150MB Chromium snapshot on first run,
	// which is both a surprise locally and a different build from the one the
	// comparison baseline uses.
	if chromePath == "" {
		if found, ok := launcher.LookPath(); ok {
			chromePath = found
		}
	}

	// --no-sandbox because the container has no user namespaces to sandbox
	// into; --disable-dev-shm-usage because most runtimes give /dev/shm 64MB,
	// which Chrome exhausts part-way through a render. --export-tagged-pdf is
	// what Puppeteer passes, and without it the PDF loses its accessibility
	// tree.
	l := launcher.New().
		Set("no-sandbox").
		Set("disable-dev-shm-usage").
		Set("export-tagged-pdf")

	if chromePath != "" {
		l = l.Bin(chromePath)
	}

	controlURL, err := l.Launch()
	if err != nil {
		return "", func() {}, err
	}

	return controlURL, l.Cleanup, nil
}

func connectChrome(controlURL string) (*rod.Browser, error) {
	b := rod.New().ControlURL(controlURL)
	if err := b.Connect(); err != nil {
		return nil, err
	}

	return b, nil
}

// Launch starts Chrome. chromePath is set in the image to the distribution's
// Chromium; when it is empty an already-installed browser is used instead.
func Launch(chromePath string) (*Browser, error) {
	return launch(chromePath, startChrome, connectChrome)
}

func launch(
	chromePath string,
	start func(string) (string, func(), error),
	connect func(string) (*rod.Browser, error),
) (*Browser, error) {
	controlURL, cleanup, err := start(chromePath)
	if err != nil {
		return nil, fmt.Errorf("launching chrome: %w", err)
	}

	browser, err := connect(controlURL)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("connecting to chrome: %w", err)
	}

	return &Browser{
		newPage: pageFactory(func() (*rod.Page, error) {
			return browser.Page(proto.TargetCreateTarget{})
		}),
		cleanup: func() {
			_ = browser.Close()
			cleanup()
		},
	}, nil
}

// pageFactory wraps a page in the adapter and labels a failure to open one.
// Separate from launch so the labelling is testable: a rod.Browser that was
// never connected panics rather than returning an error, so it cannot stand in
// for one that fails.
func pageFactory(open func() (*rod.Page, error)) func() (page, error) {
	return func() (page, error) {
		p, err := open()
		if err != nil {
			return nil, fmt.Errorf("opening page: %w", err)
		}

		return rodPage{p: p}, nil
	}
}

// Close always runs, even on a failed render.
func (b *Browser) Close() {
	if b.cleanup != nil {
		b.cleanup()
	}
}

// PDF renders url to a PDF.
func (b *Browser) PDF(url string) ([]byte, error) {
	p, err := b.newPage()
	if err != nil {
		return nil, err
	}
	defer p.close()

	return renderPDF(p, url)
}
