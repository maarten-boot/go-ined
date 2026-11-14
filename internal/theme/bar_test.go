package theme

import "testing"

// TC-735 (contrast part, SPEC-727, 001..003): the label on a coloured bar
// meets the preset's minimum for every palette colour and some hard custom
// ones; AA keeps the palette colour as it is; Max draws no colour.
func TestBar(t *testing.T) {
	if len(BarPalette) != 16 {
		t.Fatalf("%d palette colours", len(BarPalette))
	}
	seen := map[RGB]bool{}
	hard := []RGB{{0x77, 0x77, 0x77}, {0x76, 0x76, 0x76}, {0, 0, 0}, {0xFF, 0xFF, 0xFF}, {0x80, 0x80, 0x00}}
	all := hard
	for _, b := range BarPalette {
		if seen[b.RGB] || b.Name == "" {
			t.Errorf("palette entry %+v", b)
		}
		seen[b.RGB] = true
		all = append(all, b.RGB)
	}
	for _, preset := range []string{PresetAA, PresetAAA, PresetCustom} {
		c, err := Parse(preset, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, rgb := range all {
			fill, text, ok := Bar(rgb.Hex(), c)
			if !ok || (text != Black && text != White) || Ratio(fill, text) < c.Min-1e-9 {
				t.Errorf("%s %s: fill %s text %s ratio %.2f", preset, rgb.Hex(), fill.Hex(), text.Hex(), Ratio(fill, text))
			}
			if preset == PresetAA && fill != rgb {
				t.Errorf("AA changed %s to %s", rgb.Hex(), fill.Hex())
			}
		}
	}
	max, _ := Parse(PresetMax, 0)
	if _, _, ok := Bar("#E6194B", max); ok {
		t.Error("Max draws a colour")
	}
	aa, _ := Parse(PresetAA, 0)
	for _, bad := range []string{"", "red", "#12345", "#GGGGGG"} {
		if _, _, ok := Bar(bad, aa); ok {
			t.Errorf("%q drawn", bad)
		}
	}
	if BarName("") != "None" || BarName("#4363D8") != "Blue" || BarName("#123456") != "#123456" {
		t.Error("BarName")
	}
}
