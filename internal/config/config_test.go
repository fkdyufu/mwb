// internal/config/config_test.go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(`
host = "192.168.1.50"
key = "MySecurityKey1234"
name = "testbox"
port = 15100
`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "192.168.1.50" {
		t.Errorf("host = %q, want %q", cfg.Host, "192.168.1.50")
	}
	if cfg.Key != "MySecurityKey1234" {
		t.Errorf("key = %q", cfg.Key)
	}
	if cfg.Name != "testbox" {
		t.Errorf("name = %q", cfg.Name)
	}
	if cfg.Port != 15100 {
		t.Errorf("port = %d, want 15100", cfg.Port)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(`
host = "10.0.0.1"
key = "SomeKeyHere!1234"
`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 15100 {
		t.Errorf("default port = %d, want 15100", cfg.Port)
	}
	if cfg.Name == "" {
		t.Error("name should default to hostname")
	}
	if !cfg.Clipboard {
		t.Error("clipboard sync should default to enabled")
	}
	if cfg.ClipboardMaxFileMB != 20 {
		t.Errorf("default clipboard_max_file_mb = %d, want 20", cfg.ClipboardMaxFileMB)
	}
}

func TestLoadConfigClipboardMaxFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	for content, want := range map[string]int{
		"clipboard_max_file_mb = 0":  0,
		"clipboard_max_file_mb = 64": 64,
		"clipboard_max_file_mb = -1": -1,
	} {
		if err := os.WriteFile(path, []byte("host = \"10.0.0.1\"\nkey = \"SomeKeyHere!1234\"\n"+content+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if want < 0 {
			if err == nil {
				t.Errorf("%s: expected error", content)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", content, err)
		}
		if cfg.ClipboardMaxFileMB != want {
			t.Errorf("%s: got %d", content, cfg.ClipboardMaxFileMB)
		}
	}
}

func TestLoadConfigClipboardDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(`
host = "10.0.0.1"
key = "SomeKeyHere!1234"
clipboard = false
`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Clipboard {
		t.Error("clipboard = false should disable clipboard sync")
	}
}

func TestLoadConfigValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	// Missing host
	if err := os.WriteFile(path, []byte(`key = "SomeKeyHere!1234"`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("should fail without host")
	}

	// Missing key
	if err := os.WriteFile(path, []byte(`host = "10.0.0.1"`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("should fail without key")
	}
}
