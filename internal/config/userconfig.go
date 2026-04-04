package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// UserConfig holds user-level settings stored in ~/.config/vairdict/config.yaml.
// These are secrets and preferences that should never be checked into a repo.
type UserConfig struct {
	APIKey string `yaml:"api_key"`
}

// userConfigDir returns the path to the vairdict config directory.
// It checks XDG_CONFIG_HOME first, then falls back to os.UserConfigDir().
func userConfigDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "vairdict"), nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolving user config directory: %w", err)
	}
	return filepath.Join(configDir, "vairdict"), nil
}

// UserConfigPath returns the full path to the user config file.
func UserConfigPath() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// LoadUserConfig reads the user config from ~/.config/vairdict/config.yaml.
// Returns an empty UserConfig (not an error) if the file does not exist.
func LoadUserConfig() (*UserConfig, error) {
	path, err := UserConfigPath()
	if err != nil {
		return &UserConfig{}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &UserConfig{}, nil
		}
		return nil, fmt.Errorf("reading user config: %w", err)
	}

	var cfg UserConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing user config: %w", err)
	}

	return &cfg, nil
}

// SaveUserConfig writes the user config to ~/.config/vairdict/config.yaml
// with 0600 permissions (user-only read/write).
func SaveUserConfig(cfg *UserConfig) error {
	dir, err := userConfigDir()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling user config: %w", err)
	}

	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing user config: %w", err)
	}

	return nil
}

// ResolveAPIKey returns the API key from the first available source:
// 1. ANTHROPIC_API_KEY environment variable
// 2. ~/.config/vairdict/config.yaml api_key field
// Returns empty string if neither is set.
func ResolveAPIKey() string {
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		return key
	}

	userCfg, err := LoadUserConfig()
	if err != nil {
		return ""
	}

	return userCfg.APIKey
}
