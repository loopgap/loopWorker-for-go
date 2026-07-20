package ui

import (
	"image/color"
)

type Theme interface {
	Name() string
	Colors() ThemeColors
	IsDark() bool
}

type ThemeColors struct {
	// Background
	Primary   color.RGBA
	Secondary color.RGBA
	Tertiary  color.RGBA
	Elevated  color.RGBA

	// Text
	TextPrimary   color.RGBA
	TextSecondary color.RGBA
	TextTertiary  color.RGBA

	// Accent
	Blue   color.RGBA
	Green  color.RGBA
	Orange color.RGBA
	Red    color.RGBA
	Purple color.RGBA
	Pink   color.RGBA
	Teal   color.RGBA
	Indigo color.RGBA

	// Border
	Border      color.RGBA
	BorderLight color.RGBA

	// Glass
	GlassBg     color.RGBA
	GlassBorder color.RGBA
	GlassShadow color.RGBA
}

type LightTheme struct{}

func (t *LightTheme) Name() string { return "light" }
func (t *LightTheme) IsDark() bool { return false }
func (t *LightTheme) Colors() ThemeColors {
	return ThemeColors{
		Primary:   color.RGBA{R: 255, G: 255, B: 255, A: 255},
		Secondary: color.RGBA{R: 242, G: 242, B: 247, A: 255},
		Tertiary:  color.RGBA{R: 249, G: 249, B: 251, A: 255},
		Elevated:  color.RGBA{R: 255, G: 255, B: 255, A: 255},

		TextPrimary:   color.RGBA{R: 0, G: 0, B: 0, A: 232},
		TextSecondary: color.RGBA{R: 60, G: 60, B: 67, A: 153},
		TextTertiary:  color.RGBA{R: 60, G: 60, B: 67, A: 102},

		Blue:   color.RGBA{R: 0, G: 122, B: 255, A: 255},
		Green:  color.RGBA{R: 52, G: 199, B: 89, A: 255},
		Orange: color.RGBA{R: 255, G: 149, B: 0, A: 255},
		Red:    color.RGBA{R: 255, G: 59, B: 48, A: 255},
		Purple: color.RGBA{R: 175, G: 82, B: 222, A: 255},
		Pink:   color.RGBA{R: 255, G: 45, B: 85, A: 255},
		Teal:   color.RGBA{R: 90, G: 200, B: 250, A: 255},
		Indigo: color.RGBA{R: 88, G: 86, B: 214, A: 255},

		Border:      color.RGBA{R: 60, G: 60, B: 67, A: 29},
		BorderLight: color.RGBA{R: 60, G: 60, B: 67, A: 18},

		GlassBg:     color.RGBA{R: 255, G: 255, B: 255, A: 183},
		GlassBorder: color.RGBA{R: 255, G: 255, B: 255, A: 128},
		GlassShadow: color.RGBA{R: 0, G: 0, B: 0, A: 20},
	}
}

type DarkTheme struct{}

func (t *DarkTheme) Name() string { return "dark" }
func (t *DarkTheme) IsDark() bool { return true }
func (t *DarkTheme) Colors() ThemeColors {
	return ThemeColors{
		Primary:   color.RGBA{R: 28, G: 28, B: 30, A: 255},
		Secondary: color.RGBA{R: 44, G: 44, B: 46, A: 255},
		Tertiary:  color.RGBA{R: 58, G: 58, B: 60, A: 255},
		Elevated:  color.RGBA{R: 44, G: 44, B: 46, A: 255},

		TextPrimary:   color.RGBA{R: 255, G: 255, B: 255, A: 232},
		TextSecondary: color.RGBA{R: 235, G: 235, B: 245, A: 153},
		TextTertiary:  color.RGBA{R: 235, G: 235, B: 245, A: 102},

		Blue:   color.RGBA{R: 10, G: 132, B: 255, A: 255},
		Green:  color.RGBA{R: 48, G: 209, B: 88, A: 255},
		Orange: color.RGBA{R: 255, G: 159, B: 10, A: 255},
		Red:    color.RGBA{R: 255, G: 69, B: 58, A: 255},
		Purple: color.RGBA{R: 191, G: 90, B: 242, A: 255},
		Pink:   color.RGBA{R: 255, G: 55, B: 95, A: 255},
		Teal:   color.RGBA{R: 100, G: 210, B: 255, A: 255},
		Indigo: color.RGBA{R: 94, G: 92, B: 230, A: 255},

		Border:      color.RGBA{R: 84, G: 84, B: 88, A: 65},
		BorderLight: color.RGBA{R: 84, G: 84, B: 88, A: 36},

		GlassBg:     color.RGBA{R: 44, G: 44, B: 46, A: 183},
		GlassBorder: color.RGBA{R: 255, G: 255, B: 255, A: 30},
		GlassShadow: color.RGBA{R: 0, G: 0, B: 0, A: 50},
	}
}

type GlassTheme struct{}

func (t *GlassTheme) Name() string { return "glass" }
func (t *GlassTheme) IsDark() bool { return false }
func (t *GlassTheme) Colors() ThemeColors {
	return ThemeColors{
		Primary:   color.RGBA{R: 255, G: 255, B: 255, A: 183},
		Secondary: color.RGBA{R: 255, G: 255, B: 255, A: 128},
		Tertiary:  color.RGBA{R: 255, G: 255, B: 255, A: 76},
		Elevated:  color.RGBA{R: 255, G: 255, B: 255, A: 204},

		TextPrimary:   color.RGBA{R: 0, G: 0, B: 0, A: 217},
		TextSecondary: color.RGBA{R: 0, G: 0, B: 0, A: 153},
		TextTertiary:  color.RGBA{R: 0, G: 0, B: 0, A: 102},

		Blue:   color.RGBA{R: 0, G: 122, B: 255, A: 255},
		Green:  color.RGBA{R: 52, G: 199, B: 89, A: 255},
		Orange: color.RGBA{R: 255, G: 149, B: 0, A: 255},
		Red:    color.RGBA{R: 255, G: 59, B: 48, A: 255},
		Purple: color.RGBA{R: 175, G: 82, B: 222, A: 255},
		Pink:   color.RGBA{R: 255, G: 45, B: 85, A: 255},
		Teal:   color.RGBA{R: 90, G: 200, B: 250, A: 255},
		Indigo: color.RGBA{R: 88, G: 86, B: 214, A: 255},

		Border:      color.RGBA{R: 255, G: 255, B: 255, A: 128},
		BorderLight: color.RGBA{R: 255, G: 255, B: 255, A: 76},

		GlassBg:     color.RGBA{R: 255, G: 255, B: 255, A: 183},
		GlassBorder: color.RGBA{R: 255, G: 255, B: 255, A: 128},
		GlassShadow: color.RGBA{R: 0, G: 0, B: 0, A: 20},
	}
}

func GetTheme(name string) Theme {
	switch name {
	case "dark":
		return &DarkTheme{}
	case "glass":
		return &GlassTheme{}
	default:
		return &LightTheme{}
	}
}

func GetThemeColors(name string) ThemeColors {
	return GetTheme(name).Colors()
}
