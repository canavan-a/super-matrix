package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"path/filepath"
)

// The web app (a PWA): icons for the manifest and the iOS home screen, and
// the launch screens iOS shows while a home-screen web app starts. iOS has
// no manifest-driven splash; it wants one exact-size image per device,
// matched by media query.

// iosScreens are portrait CSS sizes and pixel ratios of current iPhones and
// iPads. A device not listed gets a plain screen instead.
var iosScreens = []struct{ w, h, dpr int }{
	{440, 956, 3}, {402, 874, 3}, {430, 932, 3}, {393, 852, 3}, {428, 926, 3},
	{390, 844, 3}, {375, 812, 3}, {414, 896, 3}, {414, 896, 2}, {414, 736, 3},
	{375, 667, 2}, {320, 568, 2},
	{1032, 1376, 2}, {1024, 1366, 2}, {834, 1210, 2}, {834, 1194, 2}, {834, 1112, 2},
	{820, 1180, 2}, {810, 1080, 2}, {768, 1024, 2}, {744, 1133, 2},
}

// SplashLink is one <link rel="apple-touch-startup-image">, injected into
// index.html at build time (see app/vite.config.ts).
type SplashLink struct {
	Href  string `json:"href"`
	Media string `json:"media"`
}

func writeWeb(public string, p Palette, mark, word, icon Grid) error {
	iw := iconWidth(icon)
	icons := []struct {
		name   string
		size   int
		width  float64
		corner float64
	}{
		// Manifest icons: Android and desktops draw these as given.
		{"icon-192.png", 192, iw, 0.2},
		{"icon-512.png", 512, iw, 0.2},
		// Maskable: full bleed, with the art inside the central safe circle.
		{"maskable-512.png", 512, iw * 0.8, 0},
		// iOS rounds the corners itself and turns transparency black, so a
		// full, opaque square.
		{"apple-touch-icon.png", 180, iw, 0},
		{"favicon-64.png", 64, iw * 1.15, 0.2},
	}
	for _, ic := range icons {
		if err := writePNG(filepath.Join(public, "icons", ic.name), renderIcon(p, icon, ic.size, false, ic.width, ic.corner)); err != nil {
			return err
		}
	}

	var links []SplashLink
	for _, s := range iosScreens {
		name := fmt.Sprintf("splash-%dx%d.png", s.w*s.dpr, s.h*s.dpr)
		if err := writePNG(filepath.Join(public, "splash", name), renderSplash(p, mark, word, s.w*s.dpr, s.h*s.dpr)); err != nil {
			return err
		}
		links = append(links, SplashLink{
			Href: "/splash/" + name,
			Media: fmt.Sprintf("(device-width: %dpx) and (device-height: %dpx) and (-webkit-device-pixel-ratio: %d) and (orientation: portrait)",
				s.w, s.h, s.dpr),
		})
	}
	b, err := json.MarshalIndent(links, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(public, "splash", "splash.json"), string(b)+"\n")
}

// renderSplash draws the logo (the broom over the stacked wordmark, as the
// in-app splash shows it) centered on the theme's background.
func renderSplash(p Palette, mark, word Grid, w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	bg := p.Bg
	draw.Draw(img, img.Bounds(), &image.Uniform{color.NRGBA{clamp(bg[0]), clamp(bg[1]), clamp(bg[2]), 255}}, image.Point{}, draw.Src)
	const gap = 4
	rows := len(mark) + gap + len(word)
	cols := max(mark.w(), word.w())
	// The logo spans 40% of the narrow side; cells are 3:4 like the art's.
	cw := 0.4 * float64(min(w, h)) / float64(cols)
	ch := cw * pixAspect
	x0 := (float64(w) - cw*float64(cols)) / 2
	y0 := (float64(h) - ch*float64(rows)) / 2
	fill := func(g Grid, rowOff int) {
		for y, row := range g {
			for x := range len(row) {
				if row[x] == '.' {
					continue
				}
				c := p.color(legend[row[x]])
				r := image.Rect(
					int(math.Round(x0+float64(x)*cw)), int(math.Round(y0+float64(y+rowOff)*ch)),
					int(math.Round(x0+float64(x+1)*cw)), int(math.Round(y0+float64(y+rowOff+1)*ch)),
				)
				draw.Draw(img, r, &image.Uniform{color.NRGBA{clamp(c[0]), clamp(c[1]), clamp(c[2]), 255}}, image.Point{}, draw.Src)
			}
		}
	}
	fill(mark, 0)
	fill(word, len(mark)+gap)
	return img
}

// writeMarketplace renders Google Play's store graphics: the 512x512 app
// icon (a full square, which Play rounds itself) and the 1024x500 feature
// graphic (the one-line wordmark alone on the dark theme's background).
func writeMarketplace(dir string, p Palette, line, icon Grid) error {
	if err := writePNG(filepath.Join(dir, "icon-512.png"), renderIcon(p, icon, 512, false, iconWidth(icon), 0)); err != nil {
		return err
	}
	return writePNG(filepath.Join(dir, "feature-graphic-1024x500.png"), renderBanner(p, line, 1024, 500))
}

// renderBanner draws a wordmark centered on the theme's background, 60% of
// the width (or less, if that would be too tall); cells are 3:4 like the art's.
func renderBanner(p Palette, g Grid, w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	bg := p.Bg
	draw.Draw(img, img.Bounds(), &image.Uniform{color.NRGBA{clamp(bg[0]), clamp(bg[1]), clamp(bg[2]), 255}}, image.Point{}, draw.Src)
	cw := math.Min(0.6*float64(w)/float64(g.w()), 0.5*float64(h)/(float64(len(g))*pixAspect))
	ch := cw * pixAspect
	x0 := (float64(w) - cw*float64(g.w())) / 2
	y0 := (float64(h) - ch*float64(len(g))) / 2
	for y, row := range g {
		for x := range len(row) {
			if row[x] == '.' {
				continue
			}
			c := p.color(legend[row[x]])
			r := image.Rect(
				int(math.Round(x0+float64(x)*cw)), int(math.Round(y0+float64(y)*ch)),
				int(math.Round(x0+float64(x+1)*cw)), int(math.Round(y0+float64(y+1)*ch)),
			)
			draw.Draw(img, r, &image.Uniform{color.NRGBA{clamp(c[0]), clamp(c[1]), clamp(c[2]), 255}}, image.Point{}, draw.Src)
		}
	}
	return img
}
