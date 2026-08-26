package render

import (
	"fmt"
	"io"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// page is the slice of a browser page this package actually needs.
//
// The fidelity-critical logic — the filler search, the fonts wait, the print
// options — sits above this rather than against rod directly, so every failure
// branch can be exercised without a browser that has agreed to fail on cue. It
// also keeps the CDP library at arm's length, which is what makes "go-rod was
// chosen on ergonomics" a structural claim rather than a preference.
type page interface {
	navigate(url string) error
	waitRequestIdle() func()
	eval(js string, args ...any) error
	print() ([]byte, error)
	close()
}

// rodPage adapts go-rod to the interface above. It holds no logic on purpose:
// everything here is a one-line translation, so there is nothing to test that
// the integration tests do not already cover by driving a real browser.
type rodPage struct{ p *rod.Page }

func (r rodPage) navigate(url string) error { return r.p.Navigate(url) }

func (r rodPage) waitRequestIdle() func() { return r.p.MustWaitRequestIdle() }

func (r rodPage) eval(js string, args ...any) error {
	_, err := r.p.Eval(js, args...)
	return err
}

func (r rodPage) print() ([]byte, error) {
	stream, err := r.p.PDF(&proto.PagePrintToPDF{
		PaperWidth:        ptr(paperWidthIn),
		PaperHeight:       ptr(paperHeightIn),
		MarginTop:         ptr(marginIn),
		MarginBottom:      ptr(marginIn),
		MarginLeft:        ptr(marginIn),
		MarginRight:       ptr(marginIn),
		PrintBackground:   false,
		GenerateTaggedPDF: generateTagged,
	})
	if err != nil {
		return nil, fmt.Errorf("printing pdf: %w", err)
	}

	return io.ReadAll(stream)
}

func (r rodPage) close() { _ = r.p.Close() }

func ptr[T any](v T) *T { return &v }
