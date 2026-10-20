// Command appart generates the app's logo data and launcher icons from the
// pixel art in art/, the way super-badger's tools/appart does from its
// terminal title art, so the boot screen, the launcher icon and the
// notification icon are all the same picture.
//
//	cd tools && go run ./appart                      # writes into ../app
//	cd tools && go run ./appart -preview /tmp/out    # also renders PNG previews
//
// It writes:
//
//	app/src/logo/logoData.ts      the logo as tone-mapped vector runs
//	app/src/logo/iconPalette.json per-theme colors (a test checks the app agrees)
//	app/android/app/src/main/res  adaptive + legacy launcher icons, one per
//	                              theme, and the notification icon
//	app/public/icons, splash      the web app's icons and iOS launch screens
//	                              (dark theme, the app's default)
//
// Theme colors are read from app/src/theme.tsx, never copied.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// The app's themes, in the order Settings lists them.
var themeOrder = []string{"light", "dark", "slate", "sepia", "ember"}

// Tone is where a pixel sits on a theme's dark→light ramp (U, 0..4) and how
// far it leans toward the theme's accent (W, 0..1).
type Tone struct{ U, W float64 }

// The art files' legend. A–I are the wordmark's tones, graded letter by
// letter left to right exactly as super-badger's are; 1–3 are fills (the
// broom's tile); o marks animated dots (unused by the broom).
var legend = map[byte]Tone{
	'A': {4, 1}, 'B': {3.8333, 0.5}, 'C': {4, 0.5}, 'D': {3.6667, 0.5}, 'E': {3.5, 0.5},
	'F': {3.3333, 0.5}, 'G': {3.3333, 0}, 'H': {3.1667, 0}, 'I': {3, 0},
	'1': {1, 0}, '2': {1.5, 0}, '3': {2, 0},
	'o': {4, 1},
}

// Grid is a picture as rows of legend bytes ('.' is empty).
type Grid []string

func (g Grid) w() int {
	n := 0
	for _, r := range g {
		n = max(n, len(r))
	}
	return n
}

func (g Grid) at(x, y int) byte {
	if y < 0 || y >= len(g) || x < 0 || x >= len(g[y]) {
		return '.'
	}
	return g[y][x]
}

type span struct {
	name   string
	x0, x1 int
	// y0, y1 bound the letter's rows once the wordmark is stacked; 0, 0
	// means every row.
	y0, y1 int
}

func readArt(path string) (Grid, []span, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	var g Grid
	var slices []span
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			if rest, ok := strings.CutPrefix(line, "# slices:"); ok {
				for _, f := range strings.Fields(rest) {
					name, rng, _ := strings.Cut(f, ":")
					a, b, _ := strings.Cut(rng, "-")
					x0, _ := strconv.Atoi(a)
					x1, _ := strconv.Atoi(b)
					slices = append(slices, span{name: name, x0: x0, x1: x1})
				}
			}
			continue
		}
		for i := 0; i < len(line); i++ {
			if line[i] != '.' {
				if _, ok := legend[line[i]]; !ok {
					return nil, nil, fmt.Errorf("%s: unknown pixel %q", path, line[i])
				}
			}
		}
		g = append(g, line)
	}
	return g, slices, sc.Err()
}

// group is a set of pixels as runs [x, y, len, …] per tone.
type group map[Tone][]int

func groupOf(g Grid, x0, x1 int, keep func(byte) bool) group {
	out := group{}
	for y, row := range g {
		for x := x0; x < min(x1, len(row)); {
			c := row[x]
			if c == '.' || !keep(c) {
				x++
				continue
			}
			t := legend[c]
			n := 1
			for x+n < min(x1, len(row)) && row[x+n] != '.' && keep(row[x+n]) && legend[row[x+n]] == t {
				n++
			}
			out[t] = append(out[t], x, y, n)
			x += n
		}
	}
	return out
}

func sortedTones(g group) []Tone {
	ts := make([]Tone, 0, len(g))
	for t := range g {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool {
		if ts[i].U != ts[j].U {
			return ts[i].U < ts[j].U
		}
		return ts[i].W < ts[j].W
	})
	return ts
}

func num(f float64) string {
	s := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", f), "0"), ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

func tonesTS(g group, indent string) string {
	var b strings.Builder
	b.WriteString("[\n")
	for _, t := range sortedTones(g) {
		b.WriteString(fmt.Sprintf("%s  {u: %s, w: %s, runs: [", indent, num(t.U), num(t.W)))
		for i, v := range g[t] {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(strconv.Itoa(v))
		}
		b.WriteString("]},\n")
	}
	b.WriteString(indent + "]")
	return b.String()
}

func main() {
	artDir := flag.String("art", "../art", "the art directory")
	app := flag.String("app", "../app", "the app directory")
	preview := flag.String("preview", "", "also render PNG previews into this directory")
	market := flag.String("marketplace", "", "also render the Google Play store icon and feature graphic into this directory")
	flag.Parse()

	mark, _, err := readArt(filepath.Join(*artDir, "mark.txt"))
	must(err)
	word, slices, err := readArt(filepath.Join(*artDir, "word.txt"))
	must(err)
	themeSrc, err := os.ReadFile(filepath.Join(*app, "src/theme.tsx"))
	must(err)
	pals, err := parseThemes(string(themeSrc))
	must(err)
	for _, n := range themeOrder {
		if _, ok := pals[n]; !ok {
			must(fmt.Errorf("theme %q not found in theme.tsx", n))
		}
	}

	// The wordmark is stacked, "Super" over "Matrix", on the splash and the
	// sign-in screen; the broom mark is the launcher icon. The mark is
	// centered over the wordmark; both share one width. The one-line wordmark
	// is kept too, for the deployment list's header.
	line := crop(word)
	word, slices = stackWord(word, slices)
	width := max(word.w(), mark.w())
	mark = center(mark, width)
	shift := (width - word.w()) / 2
	word = center(word, width)
	for i := range slices {
		slices[i].x0 += shift
		slices[i].x1 += shift
	}

	must(writeLogoData(filepath.Join(*app, "src/logo/logoData.ts"), width, mark, word, slices, line))
	// The launcher icon is the broom's squares alone: the icon's own
	// background (the theme's) stands in for the tile, as in the original
	// logo, where the tile is the whole icon.
	icon := crop(bright(mark))
	must(writePalette(filepath.Join(*app, "src/logo/iconPalette.json"), pals, icon))
	must(writeAndroid(filepath.Join(*app, "android/app/src/main/res"), pals, icon))
	must(writeWeb(filepath.Join(*app, "public"), pals["dark"], mark, word, icon))
	if *market != "" {
		must(writeMarketplace(*market, pals["dark"], line, icon))
	}
	if *preview != "" {
		must(writePreviews(*preview, pals, mark, word, icon))
	}
	fmt.Println("generated logo data, palette and icons from", *artDir)
}

// bright keeps only the bright pixels (tone u >= 3): the broom without its
// tile.
func bright(g Grid) Grid {
	out := make(Grid, len(g))
	for i, r := range g {
		b := []byte(r)
		for j, c := range b {
			if c != '.' && legend[c].U < 3 {
				b[j] = '.'
			}
		}
		out[i] = string(b)
	}
	return out
}

// center pads every row to width w, keeping the picture centered.
func center(g Grid, w int) Grid {
	gw := g.w()
	left := (w - gw) / 2
	out := make(Grid, len(g))
	for i, r := range g {
		out[i] = strings.Repeat(".", left) + r + strings.Repeat(".", w-left-len(r))
	}
	return out
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "appart:", err)
		os.Exit(1)
	}
}

func writeLogoData(path string, width int, mark, word Grid, slices []span, line Grid) error {
	body := groupOf(mark, 0, width, func(c byte) bool { return c != 'o' })
	dots := groupOf(mark, 0, width, func(c byte) bool { return c == 'o' })
	var ex, ey, en float64
	for y, row := range mark {
		for x := range len(row) {
			if row[x] == 'o' {
				ex, ey, en = ex+float64(x)+0.5, ey+float64(y)+0.5, en+1
			}
		}
	}
	var b strings.Builder
	b.WriteString(`// GENERATED by "go run ./appart" (from tools/) out of art/mark.txt and
// art/word.txt. Do not edit by hand; edit the art and regenerate.
//
// The logo is described by *tone*, not color: u is the position along the
// art's own dark→light ramp (0..4) and w how far the pixel leans toward the
// accent (0..1). logoTheme.ts turns tones into a theme's colors, so one set of
// shapes serves every theme. runs is flat [x, y, length, ...] in sub-pixels
// (one sub-pixel is 1 unit wide and a bit taller; see PIXEL_ASPECT).

export interface ToneRuns {
  u: number;
  w: number;
  runs: number[];
}

export interface LogoData {
  /** width of every part, in sub-pixels */
  width: number;
  /** the mark (the broom) */
  mark: {rows: number; tones: ToneRuns[]};
  /** animated dots in the mark, in its coordinates (empty for the broom) */
  dots: {cx: number; cy: number; tones: ToneRuns[]};
  /** blank sub-rows between the mark and the wordmark */
  gap: number;
  /** the wordmark cut into letters, for a staggered reveal */
  word: {rows: number; slices: {x0: number; x1: number; tones: ToneRuns[]}[]};
  /** the mark, cropped, used for the app icon */
  icon: {w: number; h: number; tones: ToneRuns[]};
  /** the wordmark on one line, cropped (the deployment list's header) */
  line: {w: number; h: number; tones: ToneRuns[]};
}

`)
	b.WriteString(fmt.Sprintf("export const LOGO: LogoData = {\n  width: %d,\n", width))
	b.WriteString(fmt.Sprintf("  mark: {rows: %d, tones: %s},\n", len(mark), tonesTS(body, "  ")))
	if en == 0 {
		en = 1 // no dots in this mark
	}
	b.WriteString(fmt.Sprintf("  dots: {cx: %s, cy: %s, tones: %s},\n", num(ex/en), num(ey/en), tonesTS(dots, "  ")))
	b.WriteString("  gap: 4,\n")
	b.WriteString(fmt.Sprintf("  word: {\n    rows: %d,\n    slices: [\n", len(word)))
	for _, sp := range slices {
		g := groupOf(rowsOnly(word, sp.y0, sp.y1), sp.x0, sp.x1, func(byte) bool { return true })
		b.WriteString(fmt.Sprintf("      {x0: %d, x1: %d, tones: %s},\n", sp.x0, sp.x1, tonesTS(g, "      ")))
	}
	b.WriteString("    ],\n  },\n")
	icon := crop(mark)
	b.WriteString(fmt.Sprintf("  icon: {w: %d, h: %d, tones: %s},\n", icon.w(), len(icon), tonesTS(groupOf(icon, 0, icon.w(), func(byte) bool { return true }), "  ")))
	b.WriteString(fmt.Sprintf("  line: {w: %d, h: %d, tones: %s},\n};\n", line.w(), len(line), tonesTS(groupOf(line, 0, line.w(), func(byte) bool { return true }), "  ")))
	return writeFile(path, b.String())
}

// crop trims empty rows and columns around a picture.
func crop(g Grid) Grid {
	minX, minY, maxX, maxY := math.MaxInt, math.MaxInt, -1, -1
	for y, r := range g {
		for x := range len(r) {
			if r[x] != '.' {
				minX, maxX, minY, maxY = min(minX, x), max(maxX, x), min(minY, y), max(maxY, y)
			}
		}
	}
	out := Grid{}
	for y := minY; y <= maxY; y++ {
		row := g[y] + strings.Repeat(".", max(0, maxX+1-len(g[y])))
		out = append(out, row[minX:maxX+1])
	}
	return out
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// ---- palette (checked by the app's tests) ----

type toneJSON struct {
	U     float64 `json:"u"`
	W     float64 `json:"w"`
	Color string  `json:"color"`
}

func writePalette(path string, pals map[string]Palette, icon Grid) error {
	type themeJSON struct {
		Bg        string     `json:"bg"`
		Accent    string     `json:"accent"`
		Ramp      []string   `json:"ramp"`
		IconTones []toneJSON `json:"iconTones"`
	}
	out := struct {
		Themes map[string]themeJSON `json:"themes"`
	}{Themes: map[string]themeJSON{}}
	tones := sortedTones(groupOf(icon, 0, icon.w(), func(byte) bool { return true }))
	for _, n := range themeOrder {
		p := pals[n]
		t := themeJSON{Bg: toHex(p.Bg), Accent: toHex(p.accent())}
		for _, r := range p.ramp() {
			t.Ramp = append(t.Ramp, toHex(r))
		}
		for _, tone := range tones {
			t.IconTones = append(t.IconTones, toneJSON{tone.U, tone.W, toHex(p.color(tone))})
		}
		out.Themes[n] = t
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, string(b)+"\n")
}

// ---- Android icons ----

const (
	canvas    = 108.0
	overlapU  = 0.03
	pixAspect = 4.0 / 3.0 // sub-pixels are 3 wide by 4 tall
)

func rectPath(runs []int, x0, y0, pw, ph float64) string {
	var b strings.Builder
	for i := 0; i < len(runs); i += 3 {
		x := x0 + float64(runs[i])*pw
		y := y0 + float64(runs[i+1])*ph
		w := float64(runs[i+2])*pw + overlapU
		b.WriteString(fmt.Sprintf("M%s,%sh%sv%sh-%sz", num(x), num(y), num(w), num(ph+overlapU), num(w)))
	}
	return b.String()
}

func iconGeom(icon Grid, size, width float64) (pw, ph, x0, y0 float64) {
	pw = width / float64(icon.w())
	ph = pw * pixAspect
	x0 = (size - float64(icon.w())*pw) / 2
	y0 = (size - float64(len(icon))*ph) / 2
	return
}

func vector(size int, paths string) string {
	return fmt.Sprintf("<vector xmlns:android=\"http://schemas.android.com/apk/res/android\"\n    android:width=\"%ddp\"\n    android:height=\"%ddp\"\n    android:viewportWidth=\"%d\"\n    android:viewportHeight=\"%d\">\n%s</vector>\n", size, size, size, size, paths)
}

func writeAndroid(res string, pals map[string]Palette, icon Grid) error {
	g := groupOf(icon, 0, icon.w(), func(byte) bool { return true })
	pw, ph, x0, y0 := iconGeom(icon, canvas, iconWidth(icon)*canvas)
	colors := []string{"<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<!-- GENERATED by tools/appart. One launcher-icon background per app theme. -->\n<resources>\n"}
	for _, n := range themeOrder {
		p := pals[n]
		colors = append(colors, fmt.Sprintf("    <color name=\"ic_bg_%s\">%s</color>\n", n, toHex(p.Bg)))
		var paths strings.Builder
		for _, t := range sortedTones(g) {
			paths.WriteString(fmt.Sprintf("    <path android:fillColor=\"%s\" android:pathData=\"%s\" />\n", toHex(p.color(t)), rectPath(g[t], x0, y0, pw, ph)))
		}
		fg := "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<!-- GENERATED by tools/appart: the broom, in the " + n + " theme. -->\n" + vector(108, paths.String())
		if err := writeFile(filepath.Join(res, "drawable", "ic_launcher_fg_"+n+".xml"), fg); err != nil {
			return err
		}
		adaptive := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<!-- GENERATED by tools/appart. API 26+ adaptive icon for the %s theme; below
     API 26 the matching mipmap-*dpi PNGs are used. -->
<adaptive-icon xmlns:android="http://schemas.android.com/apk/res/android">
    <background android:drawable="@color/ic_bg_%s" />
    <foreground android:drawable="@drawable/ic_launcher_fg_%s" />
    <monochrome android:drawable="@drawable/ic_launcher_mono" />
</adaptive-icon>
`, n, n, n)
		for _, name := range []string{"ic_launcher_" + n, "ic_launcher_" + n + "_round"} {
			if err := writeFile(filepath.Join(res, "mipmap-anydpi-v26", name+".xml"), adaptive); err != nil {
				return err
			}
		}
		if err := writeLegacyPNGs(res, n, p, icon); err != nil {
			return err
		}
	}
	colors = append(colors, "</resources>\n")
	if err := writeFile(filepath.Join(res, "values", "ic_theme_icons.xml"), strings.Join(colors, "")); err != nil {
		return err
	}

	// Single-color versions: only the bright pixels (the broom's squares);
	// the tile would fill the whole shape.
	var all []int
	for _, t := range sortedTones(g) {
		if t.U < 3 {
			continue
		}
		all = append(all, g[t]...)
	}
	mono := "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<!-- GENERATED by tools/appart: single-color broom for Android 13+ themed icons. -->\n" +
		vector(108, fmt.Sprintf("    <path android:fillColor=\"#FFFFFF\" android:pathData=\"%s\" />\n", rectPath(all, x0, y0, pw, ph)))
	if err := writeFile(filepath.Join(res, "drawable", "ic_launcher_mono.xml"), mono); err != nil {
		return err
	}
	// The status-bar icon is the broom's squares, in white, as large as
	// fits 22 of its 24dp.
	aspect := float64(len(icon)) * pixAspect / float64(icon.w())
	npw, nph, nx0, ny0 := iconGeom(icon, 24, 22/max(1, aspect))
	notif := "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<!-- GENERATED by tools/appart: the broom as the status-bar notification icon. -->\n" +
		vector(24, fmt.Sprintf("    <path android:fillColor=\"#FFFFFF\" android:pathData=\"%s\" />\n", rectPath(all, nx0, ny0, npw, nph)))
	return writeFile(filepath.Join(res, "drawable", "ic_notification.xml"), notif)
}

var densities = []struct {
	name string
	px   int
}{{"mdpi", 48}, {"hdpi", 72}, {"xhdpi", 96}, {"xxhdpi", 144}, {"xxxhdpi", 192}}

func writeLegacyPNGs(res, theme string, p Palette, icon Grid) error {
	for _, d := range densities {
		for _, round := range []bool{false, true} {
			name := "ic_launcher_" + theme
			if round {
				name += "_round"
			}
			if err := writePNG(filepath.Join(res, "mipmap-"+d.name, name+".png"), renderIcon(p, icon, d.px, round, iconWidth(icon), 0.2)); err != nil {
				return err
			}
		}
	}
	return nil
}

func writePNG(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// renderIcon rasterizes the mark onto the theme's background in a rounded
// square (or circle), supersampled 3x3 for clean edges.
// corner is the corner radius as a fraction of the size (0: a full square).
func renderIcon(p Palette, icon Grid, size int, round bool, widthFrac, corner float64) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	pw, ph, x0, y0 := iconGeom(icon, float64(size), widthFrac*float64(size))
	const ss = 3
	r := corner * float64(size)
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var acc RGB
			var cover float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					fx := float64(px) + (float64(sx)+0.5)/ss
					fy := float64(py) + (float64(sy)+0.5)/ss
					if !inShape(fx, fy, float64(size), r, round) {
						continue
					}
					cover++
					c := p.Bg
					if ch := icon.at(int(math.Floor((fx-x0)/pw)), int(math.Floor((fy-y0)/ph))); ch != '.' {
						c = p.color(legend[ch])
					}
					acc[0], acc[1], acc[2] = acc[0]+c[0], acc[1]+c[1], acc[2]+c[2]
				}
			}
			if cover == 0 {
				continue
			}
			img.SetNRGBA(px, py, color.NRGBA{
				R: uint8(acc[0]/cover + 0.5), G: uint8(acc[1]/cover + 0.5), B: uint8(acc[2]/cover + 0.5),
				A: uint8(255 * cover / (ss * ss)),
			})
		}
	}
	return img
}

func inShape(x, y, size, r float64, round bool) bool {
	if round {
		c := size / 2
		return (x-c)*(x-c)+(y-c)*(y-c) <= c*c
	}
	cx := math.Min(math.Max(x, r), size-r)
	cy := math.Min(math.Max(y, r), size-r)
	return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r
}

// ---- previews ----

func writePreviews(dir string, pals map[string]Palette, mark, word, icon Grid) error {
	const pw, ph, scale = 3, 4, 2
	for _, n := range themeOrder {
		p := pals[n]
		rows := len(mark) + 4 + len(word)
		w := mark.w()
		img := image.NewNRGBA(image.Rect(0, 0, (w+8)*pw*scale, (rows+6)*ph*scale))
		bg := p.Bg
		draw.Draw(img, img.Bounds(), &image.Uniform{color.NRGBA{clamp(bg[0]), clamp(bg[1]), clamp(bg[2]), 255}}, image.Point{}, draw.Src)
		fill := func(g Grid, yOff int) {
			for y, row := range g {
				for x := range len(row) {
					if row[x] == '.' {
						continue
					}
					c := p.color(legend[row[x]])
					rc := image.Rect((x+4)*pw*scale, (y+yOff+3)*ph*scale, (x+5)*pw*scale, (y+yOff+4)*ph*scale)
					draw.Draw(img, rc, &image.Uniform{color.NRGBA{clamp(c[0]), clamp(c[1]), clamp(c[2]), 255}}, image.Point{}, draw.Src)
				}
			}
		}
		fill(mark, 0)
		fill(word, len(mark)+4)
		if err := writePNG(filepath.Join(dir, "logo_"+n+".png"), img); err != nil {
			return err
		}
		if err := writePNG(filepath.Join(dir, "icon_"+n+".png"), renderIcon(p, icon, 256, false, iconWidth(icon), 0.2)); err != nil {
			return err
		}
	}
	return nil
}

// stackWord cuts the wordmark after "Super" (the first five letter slices)
// and stacks "Matrix" under it, each centered, one sub-row apart. The slices
// move with their letters and gain row bounds, since the two lines now share
// columns.
func stackWord(word Grid, slices []span) (Grid, []span) {
	cut := slices[5].x0
	top, tx, _ := cropAt(sliceGrid(word, 0, cut))
	bot, bx, _ := cropAt(sliceGrid(word, cut, word.w()))
	w := max(top.w(), bot.w())
	topOff, botOff := (w-top.w())/2, (w-bot.w())/2
	botY := len(top) + 1
	out := append(center(top, w), strings.Repeat(".", w))
	out = append(out, center(bot, w)...)
	moved := make([]span, len(slices))
	for i, s := range slices {
		if s.x0 < cut {
			moved[i] = span{s.name, s.x0 - tx + topOff, s.x1 - tx + topOff, 0, len(top)}
		} else {
			moved[i] = span{s.name, s.x0 - cut - bx + botOff, s.x1 - cut - bx + botOff, botY, len(out)}
		}
		moved[i].x0, moved[i].x1 = max(0, moved[i].x0), min(w, moved[i].x1)
	}
	return out, moved
}

// cropAt is crop, also returning where the kept area started.
func cropAt(g Grid) (Grid, int, int) {
	minX, minY := math.MaxInt, math.MaxInt
	for y, r := range g {
		for x := range len(r) {
			if r[x] != '.' {
				minX, minY = min(minX, x), min(minY, y)
			}
		}
	}
	return crop(g), minX, minY
}

// rowsOnly blanks every row outside [y0, y1); 0, 0 keeps them all.
func rowsOnly(g Grid, y0, y1 int) Grid {
	if y0 == 0 && y1 == 0 {
		return g
	}
	out := make(Grid, len(g))
	for i, r := range g {
		if i >= y0 && i < y1 {
			out[i] = r
		} else {
			out[i] = strings.Repeat(".", len(r))
		}
	}
	return out
}

func sliceGrid(g Grid, x0, x1 int) Grid {
	out := make(Grid, len(g))
	for i, r := range g {
		r += strings.Repeat(".", max(0, x1-len(r)))
		out[i] = r[x0:x1]
	}
	return out
}

// iconWidth is the icon's width as a fraction of the canvas, chosen so its
// whole bounding box, corners included, fits a 62dp circle: inside the 66dp
// safe zone that every launcher mask (Pixel's circle included) leaves intact.
func iconWidth(icon Grid) float64 {
	aspect := float64(len(icon)) * pixAspect / float64(icon.w())
	return 62 / canvas / math.Sqrt(1+aspect*aspect)
}
