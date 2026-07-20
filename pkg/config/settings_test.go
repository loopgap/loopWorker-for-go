package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultSettings(t *testing.T) {
	s := DefaultSettings()
	if s == nil {
		t.Fatal("default settings should not be nil")
	}
	if s.General.Language != "en" {
		t.Errorf("expected language en, got %s", s.General.Language)
	}
	if s.General.Port != 19527 {
		t.Errorf("expected port 19527, got %d", s.General.Port)
	}
	if s.Appearance.Theme != "glass" {
		t.Errorf("expected theme glass, got %s", s.Appearance.Theme)
	}
	if s.Appearance.FontSize != 15 {
		t.Errorf("expected font size 15, got %d", s.Appearance.FontSize)
	}
	if !s.Appearance.Animations {
		t.Error("animations should be enabled by default")
	}
	if !s.Security.RequireAuth {
		t.Error("require auth should be true by default")
	}
}

func TestNewSettingsManager(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "settings.json")
	mgr := NewSettingsManager(filePath)
	if mgr == nil {
		t.Fatal("settings manager should not be nil")
	}
}

func TestSettingsManagerLoad(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "settings.json")
	mgr := NewSettingsManager(filePath)

	if err := mgr.Load(); err != nil {
		t.Fatalf("load settings: %v", err)
	}

	settings := mgr.Get()
	if settings.General.Port != 19527 {
		t.Errorf("expected port 19527, got %d", settings.General.Port)
	}
}

func TestSettingsManagerSave(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "settings.json")
	mgr := NewSettingsManager(filePath)

	if err := mgr.Save(); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Error("settings file should exist after save")
	}
}

func TestSettingsManagerSetLanguage(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "settings.json")
	mgr := NewSettingsManager(filePath)
	_ = mgr.Load()

	mgr.SetLanguage("zh")
	if mgr.Get().General.Language != "zh" {
		t.Errorf("expected language zh, got %s", mgr.Get().General.Language)
	}
}

func TestSettingsManagerSetTheme(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "settings.json")
	mgr := NewSettingsManager(filePath)
	_ = mgr.Load()

	mgr.SetTheme("dark")
	if mgr.Get().Appearance.Theme != "dark" {
		t.Errorf("expected theme dark, got %s", mgr.Get().Appearance.Theme)
	}
}

func TestSettingsManagerSetFontSize(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "settings.json")
	mgr := NewSettingsManager(filePath)
	_ = mgr.Load()

	mgr.SetFontSize(18)
	if mgr.Get().Appearance.FontSize != 18 {
		t.Errorf("expected font size 18, got %d", mgr.Get().Appearance.FontSize)
	}
}

func TestSettingsManagerSetAnimations(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "settings.json")
	mgr := NewSettingsManager(filePath)
	_ = mgr.Load()

	mgr.SetAnimations(false)
	if mgr.Get().Appearance.Animations {
		t.Error("animations should be disabled")
	}
}

func TestSettingsManagerUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "settings.json")
	mgr := NewSettingsManager(filePath)
	_ = mgr.Load()

	mgr.Update(func(s *Settings) {
		s.General.Port = 9090
		s.Appearance.Theme = "light"
	})

	if mgr.Get().General.Port != 9090 {
		t.Errorf("expected port 9090, got %d", mgr.Get().General.Port)
	}
	if mgr.Get().Appearance.Theme != "light" {
		t.Errorf("expected theme light, got %s", mgr.Get().Appearance.Theme)
	}
}

func TestSettingsManagerPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "settings.json")

	mgr1 := NewSettingsManager(filePath)
	_ = mgr1.Load()
	mgr1.SetLanguage("zh")
	mgr1.SetTheme("dark")
	_ = mgr1.Save()

	mgr2 := NewSettingsManager(filePath)
	_ = mgr2.Load()

	if mgr2.Get().General.Language != "zh" {
		t.Errorf("expected language zh after reload, got %s", mgr2.Get().General.Language)
	}
	if mgr2.Get().Appearance.Theme != "dark" {
		t.Errorf("expected theme dark after reload, got %s", mgr2.Get().Appearance.Theme)
	}
}

func TestThemeConstants(t *testing.T) {
	if ThemeLight != "light" {
		t.Errorf("expected ThemeLight 'light', got '%s'", ThemeLight)
	}
	if ThemeDark != "dark" {
		t.Errorf("expected ThemeDark 'dark', got '%s'", ThemeDark)
	}
	if ThemeAuto != "auto" {
		t.Errorf("expected ThemeAuto 'auto', got '%s'", ThemeAuto)
	}
	if ThemeGlass != "glass" {
		t.Errorf("expected ThemeGlass 'glass', got '%s'", ThemeGlass)
	}
}

func TestLanguageConstants(t *testing.T) {
	if LangEn != "en" {
		t.Errorf("expected LangEn 'en', got '%s'", LangEn)
	}
	if LangZh != "zh" {
		t.Errorf("expected LangZh 'zh', got '%s'", LangZh)
	}
}
