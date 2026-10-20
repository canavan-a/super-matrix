package main

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// RGB is a color with float channels so blends don't round early.
type RGB [3]float64

// Palette is the part of an app theme the logo uses. The math below mirrors
// app/src/logo/logoTheme.ts exactly; a test in the app compares the two
// through the generated iconPalette.json.
type Palette struct {
	Name                                      string
	Bg, Surface, Border, Muted, Text, Primary RGB
}

func hexRGB(s string) (RGB, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) != 6 {
		return RGB{}, fmt.Errorf("bad color %q", s)
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return RGB{}, err
	}
	return RGB{float64(v >> 16), float64(v >> 8 & 0xff), float64(v & 0xff)}, nil
}

func clamp(f float64) uint8 { return uint8(math.Max(0, math.Min(255, math.Round(f)))) }

func toHex(c RGB) string { return fmt.Sprintf("#%02x%02x%02x", clamp(c[0]), clamp(c[1]), clamp(c[2])) }

var (
	themeBlock = regexp.MustCompile(`(?s)const (\w+): Theme = \{(.*?)\n\};`)
	themeField = regexp.MustCompile(`(\w+):\s*'(#[0-9a-fA-F]{6})'`)
)

// parseThemes reads the palettes out of app/src/theme.tsx, so colors are
// never copied by hand.
func parseThemes(src string) (map[string]Palette, error) {
	out := map[string]Palette{}
	for _, m := range themeBlock.FindAllStringSubmatch(src, -1) {
		f := map[string]RGB{}
		for _, kv := range themeField.FindAllStringSubmatch(m[2], -1) {
			c, err := hexRGB(kv[2])
			if err != nil {
				return nil, err
			}
			f[kv[1]] = c
		}
		for _, need := range []string{"bg", "surface", "border", "textMuted", "text", "primary"} {
			if _, ok := f[need]; !ok {
				return nil, fmt.Errorf("theme %s has no %s", m[1], need)
			}
		}
		out[m[1]] = Palette{m[1], f["bg"], f["surface"], f["border"], f["textMuted"], f["text"], f["primary"]}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no themes found")
	}
	return out, nil
}

func lum(c RGB) float64 { return 0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2] }

func lerp(a, b RGB, t float64) RGB {
	return RGB{a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t, a[2] + (b[2]-a[2])*t}
}

func (p Palette) light() bool { return lum(p.Bg) > 128 }

// ramp is page, raised surface, body, mid, text: the art's dark→light order.
func (p Palette) ramp() [5]RGB {
	var body RGB
	if p.light() {
		body = lerp(p.Muted, p.Bg, 0.45)
	} else {
		body = lerp(p.Border, p.Muted, 0.35)
	}
	return [5]RGB{p.Bg, p.Surface, body, p.Muted, p.Text}
}

// accent lifts a dark theme's primary toward white so it reads on near-black.
func (p Palette) accent() RGB {
	if p.light() {
		return p.Primary
	}
	return lerp(p.Primary, RGB{255, 255, 255}, 0.35)
}

// color is a tone's color: u along the ramp (0..4), leaning w toward the accent.
func (p Palette) color(t Tone) RGB {
	r := p.ramp()
	i := min(3, int(math.Floor(t.U)))
	base := lerp(r[i], r[i+1], t.U-float64(i))
	acc, top := p.accent(), r[4]
	return RGB{base[0] + t.W*(acc[0]-top[0]), base[1] + t.W*(acc[1]-top[1]), base[2] + t.W*(acc[2]-top[2])}
}
