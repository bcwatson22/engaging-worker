package render

import "fmt"

// Device is one portrait width/height/pixel-ratio combination the PWA
// manifest advertises a splash screen for.
type Device struct {
	Width  int
	Height int
	Ratio  int
}

// StartupDevices are the unique combinations. Several device families share
// dimensions, so one entry covers them all.
//
// Duplicated from the site's src/constants/startupImages.ts, which needs it to
// build the manifest's media queries, and previously from engaging-service.
// A shared package for eleven lines of data would cost more than it saves —
// but they must stay in step, or the site advertises images nothing produces.
var StartupDevices = []Device{
	{Width: 440, Height: 956, Ratio: 3},  // 16 Pro Max
	{Width: 430, Height: 932, Ratio: 3},  // 16 Plus, 15 Pro Max, 14 Pro Max
	{Width: 428, Height: 926, Ratio: 3},  // 14 Plus, 13 Pro Max, 12 Pro Max
	{Width: 414, Height: 896, Ratio: 3},  // 11 Pro Max, XS Max
	{Width: 414, Height: 896, Ratio: 2},  // 11, XR
	{Width: 402, Height: 874, Ratio: 3},  // 16 Pro
	{Width: 393, Height: 852, Ratio: 3},  // 16, 15 Pro, 15, 14 Pro
	{Width: 390, Height: 844, Ratio: 3},  // 14, 13 Pro, 13, 12 Pro, 12
	{Width: 375, Height: 812, Ratio: 3},  // 13 mini, 12 mini, 11 Pro, XS, X
	{Width: 375, Height: 667, Ratio: 2},  // SE 3rd, SE 2nd, 8, 7, 6s
	{Width: 768, Height: 1024, Ratio: 2}, // iPad 9.7", iPad mini
}

// StartupPages are the two pages captured as splash screens. A change to
// either re-captures the whole set, which is why the content hash combines
// both rather than tracking them apart.
var StartupPages = []struct {
	Name string
	Path string
}{
	{Name: "home", Path: "/"},
	{Name: "cv", Path: "/cv"},
}

// Name is the pixel dimensions the manifest keys its media queries on.
func (d Device) Name() string {
	return fmt.Sprintf("%dx%d", d.Width*d.Ratio, d.Height*d.Ratio)
}

// StartupKey is the object key for one page on one device.
func StartupKey(page string, d Device) string {
	return fmt.Sprintf("startup-%s-%s.png", page, d.Name())
}
