package ui

import (
	"image/color"
	"testing"
)

func TestGetTheme(t *testing.T) {
	tests := []struct {
		name   string
		isDark bool
	}{
		{"light", false},
		{"dark", true},
		{"glass", false},
		{"unknown", false},
	}

	for _, tt := range tests {
		theme := GetTheme(tt.name)
		if theme == nil {
			t.Errorf("theme %s should not be nil", tt.name)
		}
		if theme.IsDark() != tt.isDark {
			t.Errorf("theme %s IsDark() = %v, want %v", tt.name, theme.IsDark(), tt.isDark)
		}
	}
}

func TestLightTheme(t *testing.T) {
	theme := &LightTheme{}
	if theme.Name() != "light" {
		t.Errorf("expected name 'light', got '%s'", theme.Name())
	}
	if theme.IsDark() {
		t.Error("light theme should not be dark")
	}
	colors := theme.Colors()
	if colors.Blue != (color.RGBA{R: 0, G: 122, B: 255, A: 255}) {
		t.Errorf("unexpected blue color: %v", colors.Blue)
	}
}

func TestDarkTheme(t *testing.T) {
	theme := &DarkTheme{}
	if theme.Name() != "dark" {
		t.Errorf("expected name 'dark', got '%s'", theme.Name())
	}
	if !theme.IsDark() {
		t.Error("dark theme should be dark")
	}
	colors := theme.Colors()
	if colors.Blue != (color.RGBA{R: 10, G: 132, B: 255, A: 255}) {
		t.Errorf("unexpected blue color: %v", colors.Blue)
	}
}

func TestGlassTheme(t *testing.T) {
	theme := &GlassTheme{}
	if theme.Name() != "glass" {
		t.Errorf("expected name 'glass', got '%s'", theme.Name())
	}
	if theme.IsDark() {
		t.Error("glass theme should not be dark")
	}
}

func TestGetThemeColors(t *testing.T) {
	tests := []string{"light", "dark", "glass", "unknown"}
	for _, name := range tests {
		colors := GetThemeColors(name)
		if colors.Blue == (color.RGBA{}) {
			t.Errorf("theme %s should have blue color", name)
		}
	}
}
