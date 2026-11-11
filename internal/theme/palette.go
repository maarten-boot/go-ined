package theme

// Palette is the set of colours of one contrast setting (decision M5-8).
// Every foreground colour meets Contrast.Min against every background
// colour (SPEC-002), and OnPrimary meets it against Primary.
type Palette struct {
	Contrast Contrast

	// Backgrounds.
	Background RGB // window and map
	Surface    RGB // buttons, inputs, headers
	Hover      RGB // an item under the pointer
	Selection  RGB // selected text or list rows

	// Foregrounds (text, lines and glyphs).
	Text      RGB
	Muted     RGB // placeholders, disabled text
	Border    RGB // input borders, separators, scroll bars
	Primary   RGB // accents: focus, the selected map item, primary buttons
	OnPrimary RGB // text on a Primary background
	Frame     RGB // frames of ordinary groups
	NetFrame  RGB // frames of network groups
	Up        RGB
	Down      RGB
	Unknown   RGB
	Off       RGB // unmonitored hosts and stale glyphs
	Error     RGB
}

// Base colours, before they are darkened to meet a contrast setting.
var (
	baseSurface   = RGB{0xF0, 0xF0, 0xF0}
	baseHover     = RGB{0xE4, 0xE4, 0xE4}
	baseSelection = RGB{0xCC, 0xE0, 0xFF}

	baseMuted    = RGB{0x76, 0x76, 0x76}
	baseBorder   = RGB{0x8A, 0x8A, 0x8A}
	basePrimary  = RGB{0x1F, 0x6F, 0xD0}
	baseFrame    = RGB{0x60, 0x60, 0x60}
	baseNetFrame = RGB{0x2B, 0x5C, 0x9E}
	baseUp       = RGB{0x2E, 0x9E, 0x44}
	baseDown     = RGB{0xE0, 0x30, 0x2A}
	baseUnknown  = RGB{0xC0, 0x8A, 0x00}
	baseOff      = RGB{0x80, 0x80, 0x80}
	baseError    = RGB{0xD0, 0x20, 0x20}
)

// NewPalette derives the palette of a contrast setting. In Max every
// colour is black or white (SPEC-003). Otherwise a light background that
// black can't reach the ratio on becomes white, and each foreground is
// darkened step by step until it meets the ratio on every background.
func NewPalette(c Contrast) Palette {
	if c.Preset == PresetMax {
		return Palette{
			Contrast:   c,
			Background: White, Surface: White, Hover: White, Selection: White,
			Text: Black, Muted: Black, Border: Black, Primary: Black, OnPrimary: White,
			Frame: Black, NetFrame: Black, Up: Black, Down: Black, Unknown: Black, Off: Black, Error: Black,
		}
	}
	bg := func(b RGB) RGB {
		if !meets(Black, b, c.Min) {
			return White
		}
		return b
	}
	p := Palette{
		Contrast:   c,
		Background: White, Surface: bg(baseSurface), Hover: bg(baseHover), Selection: bg(baseSelection),
		Text: Black, OnPrimary: White,
	}
	bgs := p.backgrounds()
	fg := func(base RGB) RGB { return darken(base, bgs, c.Min) }
	p.Muted, p.Border, p.Primary = fg(baseMuted), fg(baseBorder), fg(basePrimary)
	p.Frame, p.NetFrame = fg(baseFrame), fg(baseNetFrame)
	p.Up, p.Down, p.Unknown, p.Off, p.Error = fg(baseUp), fg(baseDown), fg(baseUnknown), fg(baseOff), fg(baseError)
	return p
}

// Status returns the glyph colour of a status ("up", "down", anything else
// is unknown). Unmonitored hosts use Off.
func (p Palette) Status(status string) RGB {
	switch status {
	case "up":
		return p.Up
	case "down":
		return p.Down
	}
	return p.Unknown
}

// Pairs returns every foreground/background pair the palette promises, by
// name, for tests and for a contrast report.
func (p Palette) Pairs() map[string][2]RGB {
	fgs := map[string]RGB{
		"text": p.Text, "muted": p.Muted, "border": p.Border, "primary": p.Primary,
		"frame": p.Frame, "netframe": p.NetFrame, "up": p.Up, "down": p.Down,
		"unknown": p.Unknown, "off": p.Off, "error": p.Error,
	}
	out := map[string][2]RGB{"on-primary/primary": {p.OnPrimary, p.Primary}}
	for bn, b := range map[string]RGB{"background": p.Background, "surface": p.Surface, "hover": p.Hover, "selection": p.Selection} {
		for fn, f := range fgs {
			out[fn+"/"+bn] = [2]RGB{f, b}
		}
	}
	return out
}

func (p Palette) backgrounds() []RGB { return []RGB{p.Background, p.Surface, p.Hover, p.Selection} }

// meets allows for rounding: 21:1 is reached by black on white.
func meets(fg, bg RGB, min float64) bool { return Ratio(fg, bg) >= min-1e-9 }

// darken scales base towards black until it meets min on every background.
// Black meets any ratio up to 21 on white, and NewPalette keeps only
// backgrounds where black meets min, so the loop always ends.
func darken(base RGB, bgs []RGB, min float64) RGB {
	for f := 1.0; f > 0; f -= 0.01 {
		c := RGB{uint8(float64(base.R) * f), uint8(float64(base.G) * f), uint8(float64(base.B) * f)}
		ok := true
		for _, b := range bgs {
			if !meets(c, b, min) {
				ok = false
				break
			}
		}
		if ok {
			return c
		}
	}
	return Black
}
