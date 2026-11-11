// Package theme holds the colour contrast rules of the go-ined user
// interfaces (SPEC-001..005): WCAG contrast math, the contrast presets and
// the palettes derived from them. It has no GUI dependency, so any UI can
// use it and every colour pair can be tested.
package theme

import (
	"fmt"
	"math"
	"strings"
)

// RGB is an opaque sRGB colour.
type RGB struct{ R, G, B uint8 }

// Hex returns the colour as "#RRGGBB".
func (c RGB) Hex() string { return fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B) }

// Standard colours.
var (
	White = RGB{0xFF, 0xFF, 0xFF}
	Black = RGB{0, 0, 0}
)

// Luminance returns the WCAG 2.x relative luminance of c (0..1).
func Luminance(c RGB) float64 {
	lin := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}

// Ratio returns the WCAG 2.x contrast ratio of two colours (1..21).
func Ratio(a, b RGB) float64 {
	la, lb := Luminance(a), Luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// Preset names (SPEC-003).
const (
	PresetAA     = "AA"
	PresetAAA    = "AAA"
	PresetMax    = "Max"
	PresetCustom = "custom"
)

// Presets lists the preset names in the order a selector shows them.
var Presets = []string{PresetAA, PresetAAA, PresetMax, PresetCustom}

// Ratio limits for a custom preset (SPEC-003).
const (
	MinCustom = 4.5
	MaxCustom = 21.0
)

// Contrast is a validated contrast setting.
type Contrast struct {
	Preset string  // one of Presets
	Min    float64 // the minimum ratio every colour pair meets
}

// Default is the default contrast setting: AA (SPEC-003).
var Default = Contrast{Preset: PresetAA, Min: 4.5}

// Parse validates a preset name (case-insensitive) and, for "custom", its
// ratio. The ratio is ignored for the other presets.
func Parse(preset string, ratio float64) (Contrast, error) {
	switch strings.ToLower(strings.TrimSpace(preset)) {
	case "aa":
		return Contrast{PresetAA, 4.5}, nil
	case "aaa":
		return Contrast{PresetAAA, 7}, nil
	case "max":
		return Contrast{PresetMax, 21}, nil
	case "custom":
		if math.IsNaN(ratio) || ratio < MinCustom || ratio > MaxCustom {
			return Default, fmt.Errorf("theme: custom contrast ratio %g is outside %g–%g", ratio, MinCustom, MaxCustom)
		}
		return Contrast{PresetCustom, ratio}, nil
	}
	return Default, fmt.Errorf("theme: unknown contrast preset %q (want AA, AAA, Max or custom)", preset)
}

// String describes the setting, for example "AAA (7:1)".
func (c Contrast) String() string {
	return fmt.Sprintf("%s (%s:1)", c.Preset, trimFloat(c.Min))
}

func trimFloat(f float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", f), "0"), ".")
}
