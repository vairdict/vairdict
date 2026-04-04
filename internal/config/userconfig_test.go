package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoadUserConfig(t *testing.T) {
	// Use a temp dir as XDG_CONFIG_HOME.
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	cfg := &UserConfig{APIKey: "sk-test-123"}
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Verify file permissions.
	path := filepath.Join(tmp, "vairdict", "config.yaml")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("permissions = %o, want 0600", info.Mode().Perm())
	}

	// Load it back.
	loaded, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.APIKey != "sk-test-123" {
		t.Errorf("api_key = %q, want %q", loaded.APIKey, "sk-test-123")
	}
}

func TestLoadUserConfig_NotExists(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.APIKey != "" {
		t.Errorf("api_key = %q, want empty", cfg.APIKey)
	}
}

func TestResolveAPIKey_EnvTakesPrecedence(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("ANTHROPIC_API_KEY", "sk-from-env")

	// Save a different key in config.
	_ = SaveUserConfig(&UserConfig{APIKey: "sk-from-file"})

	key := ResolveAPIKey()
	if key != "sk-from-env" {
		t.Errorf("key = %q, want sk-from-env", key)
	}
}

func TestResolveAPIKey_FallsBackToFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("ANTHROPIC_API_KEY", "")

	_ = SaveUserConfig(&UserConfig{APIKey: "sk-from-file"})

	key := ResolveAPIKey()
	if key != "sk-from-file" {
		t.Errorf("key = %q, want sk-from-file", key)
	}
}

func TestResolveAPIKey_Empty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "")

	key := ResolveAPIKey()
	if key != "" {
		t.Errorf("key = %q, want empty", key)
	}
}
