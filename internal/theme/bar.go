package theme

import "strconv"

// A group's top bar can have a colour (SPEC-727). The label on it is
// black or white, whichever contrasts more; that is at least 4.58:1 for
// any colour, so AA always holds. A stricter minimum lightens or darkens
// the bar just enough; under Max the bar has no colour.

// BarColor is one colour of the fixed palette.
type BarColor struct {
	Name string
	RGB  RGB
}

// BarPalette is the fixed palette of the "Bar colour" menu, in menu order:
// 16 colours that are easy to tell apart.
var BarPalette = []BarColor{
	{"Red", RGB{0xE6, 0x19, 0x4B}}, {"Orange", RGB{0xF5, 0x82, 0x31}},
	{"Yellow", RGB{0xFF, 0xE1, 0x19}}, {"Lime", RGB{0xBF, 0xEF, 0x45}},
	{"Green", RGB{0x3C, 0xB4, 0x4B}}, {"Teal", RGB{0x46, 0x99, 0x90}},
	{"Cyan", RGB{0x42, 0xD4, 0xF4}}, {"Blue", RGB{0x43, 0x63, 0xD8}},
	{"Navy", RGB{0x00, 0x00, 0x75}}, {"Purple", RGB{0x91, 0x1E, 0xB4}},
	{"Lavender", RGB{0xDC, 0xBE, 0xFF}}, {"Magenta", RGB{0xF0, 0x32, 0xE6}},
	{"Pink", RGB{0xFA, 0xBE, 0xD4}}, {"Maroon", RGB{0x80, 0x00, 0x00}},
	{"Brown", RGB{0x9A, 0x63, 0x24}}, {"Beige", RGB{0xFF, 0xFA, 0xC8}},
}

// ParseHex reads "#RRGGBB" (either case).
func ParseHex(s string) (RGB, bool) {
	if len(s) != 7 || s[0] != '#' {
		return RGB{}, false
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return RGB{}, false
	}
	return RGB{uint8(v >> 16), uint8(v >> 8), uint8(v)}, true
}

// BarName names a stored bar colour as the menus show it: the palette's
// name, the hex value of a custom colour, or "None".
func BarName(hex string) string {
	if hex == "" {
		return "None"
	}
	for _, b := range BarPalette {
		if b.RGB.Hex() == hex {
			return b.Name
		}
	}
	return hex
}

// Bar returns how a bar colour is drawn under a contrast setting: the
// fill and the label colour, so that the label meets c.Min. ok is false
// when the bar is drawn without colour: no colour set, an invalid one, or
// the Max preset.
func Bar(hex string, c Contrast) (fill, text RGB, ok bool) {
	rgb, valid := ParseHex(hex)
	if !valid || c.Preset == PresetMax {
		return RGB{}, RGB{}, false
	}
	text, toward := Black, White // black text: lighten the bar if needed
	if Ratio(White, rgb) > Ratio(Black, rgb) {
		text, toward = White, Black
	}
	fill = rgb
	for i := 1; i <= 100 && !meets(text, fill, c.Min); i++ {
		fill = mix(rgb, toward, float64(i)/100)
	}
	return fill, text, true
}

// mix blends a toward b by t (0..1).
func mix(a, b RGB, t float64) RGB {
	m := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t + 0.5) }
	return RGB{m(a.R, b.R), m(a.G, b.G), m(a.B, b.B)}
}
