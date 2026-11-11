package gui

import (
	"image/color"

	"fyne.io/fyne/v2"
	fynetheme "fyne.io/fyne/v2/theme"

	"github.com/maarten-boot/go-ined/internal/theme"
)

// nrgba converts a palette colour.
func nrgba(c theme.RGB) color.NRGBA { return color.NRGBA{c.R, c.G, c.B, 0xFF} }

// fyneTheme is the Fyne theme of a palette (SPEC-001..003, 709). It is
// always the light variant: white background, black text. Fonts, icons and
// sizes come from Fyne's default theme.
type fyneTheme struct {
	p    theme.Palette
	base fyne.Theme
}

func newFyneTheme(p theme.Palette) *fyneTheme {
	return &fyneTheme{p: p, base: fynetheme.DefaultTheme()}
}

func (t *fyneTheme) Color(n fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	p := t.p
	switch n {
	case fynetheme.ColorNameBackground, fynetheme.ColorNameInputBackground, fynetheme.ColorNameMenuBackground,
		fynetheme.ColorNameOverlayBackground:
		return nrgba(p.Background)
	case fynetheme.ColorNameButton, fynetheme.ColorNameDisabledButton, fynetheme.ColorNameHeaderBackground,
		fynetheme.ColorNameScrollBarBackground:
		return nrgba(p.Surface)
	case fynetheme.ColorNameHover, fynetheme.ColorNamePressed:
		return nrgba(p.Hover)
	case fynetheme.ColorNameSelection:
		return nrgba(p.Selection)
	case fynetheme.ColorNameDisabled, fynetheme.ColorNamePlaceHolder:
		return nrgba(p.Muted)
	case fynetheme.ColorNameInputBorder, fynetheme.ColorNameSeparator, fynetheme.ColorNameScrollBar,
		fynetheme.ColorNameInnerWindowBorder, fynetheme.ColorNameInnerWindowBorderInactive:
		return nrgba(p.Border)
	case fynetheme.ColorNamePrimary, fynetheme.ColorNameFocus, fynetheme.ColorNameHyperlink:
		return nrgba(p.Primary)
	case fynetheme.ColorNameForegroundOnPrimary, fynetheme.ColorNameForegroundOnError,
		fynetheme.ColorNameForegroundOnSuccess, fynetheme.ColorNameForegroundOnWarning:
		return nrgba(p.OnPrimary)
	case fynetheme.ColorNameError:
		return nrgba(p.Error)
	case fynetheme.ColorNameSuccess:
		return nrgba(p.Up)
	case fynetheme.ColorNameWarning:
		return nrgba(p.Unknown)
	case fynetheme.ColorNameShadow:
		if p.Contrast.Preset == theme.PresetMax {
			return color.Transparent // black and white only
		}
		return color.NRGBA{0, 0, 0, 0x33}
	}
	return nrgba(p.Text) // foreground and anything new
}

func (t *fyneTheme) Font(s fyne.TextStyle) fyne.Resource { return t.base.Font(s) }

func (t *fyneTheme) Icon(n fyne.ThemeIconName) fyne.Resource { return t.base.Icon(n) }

func (t *fyneTheme) Size(n fyne.ThemeSizeName) float32 { return t.base.Size(n) }
