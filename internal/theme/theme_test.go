package theme

import (
	"math"
	"testing"
)

func TestRatio(t *testing.T) {
	for _, tc := range []struct {
		a, b RGB
		want float64
	}{
		{Black, White, 21},
		{White, White, 1},
		{RGB{0x76, 0x76, 0x76}, White, 4.54}, // the classic AA grey
		{RGB{0x59, 0x59, 0x59}, White, 7.0},
	} {
		if got := Ratio(tc.a, tc.b); math.Abs(got-tc.want) > 0.01 {
			t.Errorf("Ratio(%s, %s) = %.3f, want %.2f", tc.a.Hex(), tc.b.Hex(), got, tc.want)
		}
		if Ratio(tc.a, tc.b) != Ratio(tc.b, tc.a) {
			t.Error("Ratio is not symmetric")
		}
	}
}

// TC-001: the default theme is a white background with black text.
func TestDefault(t *testing.T) {
	p := NewPalette(Default)
	if p.Background != White || p.Text != Black || Default.Preset != PresetAA || Default.Min != 4.5 {
		t.Fatalf("default: %+v %+v", Default, p)
	}
}

// TC-003, TC-004: presets and the custom range.
func TestParse(t *testing.T) {
	for _, tc := range []struct {
		preset string
		ratio  float64
		want   Contrast
		bad    bool
	}{
		{"AA", 0, Contrast{PresetAA, 4.5}, false},
		{"aaa", 99, Contrast{PresetAAA, 7}, false},
		{"MAX", 0, Contrast{PresetMax, 21}, false},
		{"custom", 10, Contrast{PresetCustom, 10}, false},
		{"custom", 4.5, Contrast{PresetCustom, 4.5}, false},
		{"custom", 21, Contrast{PresetCustom, 21}, false},
		{"custom", 4.4, Default, true},
		{"custom", 21.5, Default, true},
		{"custom", math.NaN(), Default, true},
		{"high", 0, Default, true},
		{"", 0, Default, true},
	} {
		got, err := Parse(tc.preset, tc.ratio)
		if (err != nil) != tc.bad || got != tc.want {
			t.Errorf("Parse(%q, %g) = %+v, %v", tc.preset, tc.ratio, got, err)
		}
	}
	if s := (Contrast{PresetCustom, 5.25}).String(); s != "custom (5.25:1)" {
		t.Errorf("String: %q", s)
	}
	if s := (Contrast{PresetAAA, 7}).String(); s != "AAA (7:1)" {
		t.Errorf("String: %q", s)
	}
}

// TC-002, TC-003: every pair of every palette meets its preset's minimum,
// and never less than 4.5.
func TestPalettesMeetMinimum(t *testing.T) {
	cs := []Contrast{{PresetAA, 4.5}, {PresetAAA, 7}, {PresetMax, 21}}
	for r := 4.5; r <= 21; r += 0.5 {
		cs = append(cs, Contrast{PresetCustom, r})
	}
	for _, c := range cs {
		p := NewPalette(c)
		for name, pair := range p.Pairs() {
			if r := Ratio(pair[0], pair[1]); r < c.Min-1e-9 || r < 4.5 {
				t.Errorf("%s: %s %s on %s = %.2f:1", c, name, pair[0].Hex(), pair[1].Hex(), r)
			}
		}
	}
}

// TC-008: Max is black and white only; status colours elsewhere differ.
func TestMaxIsBlackAndWhite(t *testing.T) {
	p := NewPalette(Contrast{PresetMax, 21})
	for name, pair := range p.Pairs() {
		for _, c := range pair {
			if c != Black && c != White {
				t.Errorf("Max %s uses %s", name, c.Hex())
			}
		}
	}
	aa := NewPalette(Default)
	if aa.Up == aa.Down || aa.Up == aa.Unknown || aa.Down == aa.Unknown {
		t.Errorf("AA status colours not distinct: %s %s %s", aa.Up.Hex(), aa.Down.Hex(), aa.Unknown.Hex())
	}
	if aa.Status("up") != aa.Up || aa.Status("down") != aa.Down || aa.Status("unknown") != aa.Unknown || aa.Status("") != aa.Unknown {
		t.Error("Status mapping")
	}
}
