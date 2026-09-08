package render

import (
	"fmt"
	"log/slog"
	"time"
)

// SettleDelay is long enough for the particles canvas to mount and the entry
// animations to finish. A screenshot taken before that captures a half-faded
// page, which is worse than no splash screen at all — it looks broken rather
// than absent.
const SettleDelay = 2 * time.Second

// StartupImage is one captured splash screen.
type StartupImage struct {
	Key   string
	Image []byte
}

// capture takes one page at one device's dimensions. sleep is injected so the
// settle delay costs nothing under test.
func capture(p page, url string, d Device, sleep func(time.Duration)) ([]byte, error) {
	if err := p.setViewport(d); err != nil {
		return nil, fmt.Errorf("setting the viewport for %s: %w", d.Name(), err)
	}

	if err := p.navigate(url); err != nil {
		return nil, fmt.Errorf("navigating to %s: %w", url, err)
	}

	if err := p.waitLoad(); err != nil {
		return nil, fmt.Errorf("loading %s: %w", url, err)
	}

	sleep(SettleDelay)

	return p.screenshot()
}

// StartupImages captures every page at every device size.
//
// Sequential rather than parallel, and this is deliberate: each capture is a
// full page load in a 1 GB container, and twenty-two at once would swap rather
// than finish sooner.
func (b *Browser) StartupImages(siteURL string) ([]StartupImage, error) {
	captured := make([]StartupImage, 0, len(StartupPages)*len(StartupDevices))

	for _, sp := range StartupPages {
		for _, device := range StartupDevices {
			image, err := b.captureOne(siteURL+sp.Path, device)
			if err != nil {
				return nil, err
			}

			captured = append(captured, StartupImage{
				Key:   StartupKey(sp.Name, device),
				Image: image,
			})
		}
	}

	slog.Info("captured startup images", "count", len(captured))

	return captured, nil
}

// captureOne is a page per capture, closed straight after. Reusing one page
// across devices would carry the previous emulation and any state the last
// load left behind.
func (b *Browser) captureOne(url string, d Device) ([]byte, error) {
	p, err := b.newPage()
	if err != nil {
		return nil, err
	}
	defer p.close()

	return capture(p, url, d, b.sleep)
}
