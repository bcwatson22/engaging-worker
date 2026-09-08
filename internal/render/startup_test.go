package render

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The manifest keys its media queries on these exact pixel dimensions, and the
// site builds the same names independently — a change here silently stops the
// two matching, and the splash screens stop being found.
func TestDeviceNaming(t *testing.T) {
	d := Device{Width: 440, Height: 956, Ratio: 3}

	if got := d.Name(); got != "1320x2868" {
		t.Errorf("name: got %q", got)
	}
	if got := StartupKey("home", d); got != "startup-home-1320x2868.png" {
		t.Errorf("key: got %q", got)
	}
}

func TestStartupDevicesAreUnique(t *testing.T) {
	seen := map[string]bool{}

	for _, d := range StartupDevices {
		if seen[d.Name()] {
			t.Errorf("duplicate device dimensions: %s", d.Name())
		}
		seen[d.Name()] = true
	}

	if len(StartupDevices) != 11 || len(StartupPages) != 2 {
		t.Errorf("expected 11 devices over 2 pages, got %d over %d",
			len(StartupDevices), len(StartupPages))
	}
}

// startupBrowser wires a Browser to a fake page, with the settle delay costing
// nothing.
func startupBrowser(p page) *Browser {
	return &Browser{
		newPage: func() (page, error) { return p, nil },
		sleep:   func(time.Duration) {},
	}
}

func TestStartupImagesCapturesEveryPageAtEverySize(t *testing.T) {
	p := &fakePage{}
	b := startupBrowser(p)

	captured, err := b.StartupImages("https://example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := len(StartupPages) * len(StartupDevices)
	if len(captured) != want {
		t.Fatalf("expected %d images, got %d", want, len(captured))
	}

	// Every device emulated, not just resized: the ratio has to be applied by
	// Chrome or the images come out at the wrong pixel size.
	if len(p.viewports) != want {
		t.Errorf("expected a viewport per capture, got %d", len(p.viewports))
	}

	// A page per capture, closed straight after — a reused page would carry
	// the previous emulation.
	if p.closes != want {
		t.Errorf("expected %d pages closed, got %d", want, p.closes)
	}

	if captured[0].Key != "startup-home-1320x2868.png" {
		t.Errorf("first key: got %q", captured[0].Key)
	}
}

func TestStartupImagesWaitsForThePageToSettle(t *testing.T) {
	slept := 0
	p := &fakePage{}
	b := &Browser{
		newPage: func() (page, error) { return p, nil },
		sleep: func(d time.Duration) {
			if d == SettleDelay {
				slept++
			}
		},
	}

	if _, err := b.StartupImages("https://example.com"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A screenshot taken before the entry animation finishes captures a
	// half-faded page, which looks broken rather than absent.
	if slept != len(StartupPages)*len(StartupDevices) {
		t.Errorf("expected a settle delay per capture, got %d", slept)
	}
}

func TestStartupImagesReportsFailures(t *testing.T) {
	cases := []struct {
		name string
		page func() *fakePage
		want string
	}{
		{
			"the viewport cannot be set",
			func() *fakePage {
				return &fakePage{onViewport: func(Device) error { return errBrowser }}
			},
			"setting the viewport",
		},
		{
			"the page cannot be reached",
			func() *fakePage {
				return &fakePage{onNavigate: func(string) error { return errBrowser }}
			},
			"navigating to",
		},
		{
			"the page never loads",
			func() *fakePage {
				return &fakePage{onWaitLoad: func() error { return errBrowser }}
			},
			"loading",
		},
		{
			"the screenshot fails",
			func() *fakePage {
				return &fakePage{onScreenshot: func() ([]byte, error) { return nil, errBrowser }}
			},
			"",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := startupBrowser(c.page()).StartupImages("https://example.com")

			if !errors.Is(err, errBrowser) {
				t.Fatalf("want the underlying error, got %v", err)
			}
			if c.want != "" && !strings.Contains(err.Error(), c.want) {
				t.Errorf("error should say which step failed: %v", err)
			}
		})
	}
}

func TestStartupImagesReportsAPageThatCannotBeOpened(t *testing.T) {
	b := &Browser{
		newPage: func() (page, error) { return nil, errBrowser },
		sleep:   func(time.Duration) {},
	}

	if _, err := b.StartupImages("https://example.com"); !errors.Is(err, errBrowser) {
		t.Fatalf("want the underlying error, got %v", err)
	}
}
