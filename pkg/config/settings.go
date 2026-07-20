package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type Theme string

const (
	ThemeLight Theme = "light"
	ThemeDark  Theme = "dark"
	ThemeAuto  Theme = "auto"
	ThemeGlass Theme = "glass"
)

type Language string

const (
	LangEn Language = "en"
	LangZh Language = "zh"
)

type Settings struct {
	General    GeneralSettings    `json:"general"`
	Appearance AppearanceSettings `json:"appearance"`
	Security   SecuritySettings   `json:"security"`
	Advanced   AdvancedSettings   `json:"advanced"`
	mu         sync.RWMutex
}

type GeneralSettings struct {
	Language   Language `json:"language"`
	Port       int      `json:"port"`
	PluginsDir string   `json:"plugins_dir"`
	DataDir    string   `json:"data_dir"`
	AutoStart  bool     `json:"auto_start"`
}

type AppearanceSettings struct {
	Theme       Theme `json:"theme"`
	FontSize    int   `json:"font_size"`
	CompactMode bool  `json:"compact_mode"`
	Animations  bool  `json:"animations"`
}

type SecuritySettings struct {
	RequireAuth bool `json:"require_auth"`
	TokenExpiry int  `json:"token_expiry_hours"`
	MaxAttempts int  `json:"max_login_attempts"`
	LockoutMins int  `json:"lockout_minutes"`
}

type AdvancedSettings struct {
	DebugMode      bool   `json:"debug_mode"`
	MetricsEnabled bool   `json:"metrics_enabled"`
	LogLevel       string `json:"log_level"`
}

func DefaultSettings() *Settings {
	return &Settings{
		General: GeneralSettings{
			Language:   LangEn,
			Port:       19527,
			PluginsDir: "./plugins",
			DataDir:    "./data",
			AutoStart:  false,
		},
		Appearance: AppearanceSettings{
			Theme:       ThemeGlass,
			FontSize:    15,
			CompactMode: false,
			Animations:  true,
		},
		Security: SecuritySettings{
			RequireAuth: true,
			TokenExpiry: 24,
			MaxAttempts: 5,
			LockoutMins: 15,
		},
		Advanced: AdvancedSettings{
			DebugMode:      false,
			MetricsEnabled: true,
			LogLevel:       "info",
		},
	}
}

type SettingsManager struct {
	settings *Settings
	filePath string
	mu       sync.RWMutex
}

func NewSettingsManager(filePath string) *SettingsManager {
	return &SettingsManager{
		settings: DefaultSettings(),
		filePath: filePath,
	}
}

func (m *SettingsManager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return m.saveLocked()
		}
		return err
	}

	return json.Unmarshal(data, m.settings)
}

func (m *SettingsManager) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveLocked()
}

func (m *SettingsManager) saveLocked() error {
	dir := filepath.Dir(m.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(m.settings, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(m.filePath, data, 0644)
}

func (m *SettingsManager) Get() *Settings {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.settings
}

func (m *SettingsManager) Update(fn func(*Settings)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(m.settings)
}

func (m *SettingsManager) SetLanguage(lang Language) {
	m.Update(func(s *Settings) {
		s.General.Language = lang
	})
}

func (m *SettingsManager) SetTheme(theme Theme) {
	m.Update(func(s *Settings) {
		s.Appearance.Theme = theme
	})
}

func (m *SettingsManager) SetFontSize(size int) {
	m.Update(func(s *Settings) {
		s.Appearance.FontSize = size
	})
}

func (m *SettingsManager) SetAnimations(enabled bool) {
	m.Update(func(s *Settings) {
		s.Appearance.Animations = enabled
	})
}
