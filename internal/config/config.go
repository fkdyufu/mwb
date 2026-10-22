// internal/config/config.go
package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Host string `toml:"host"`
	Key  string `toml:"key"`
	Name string `toml:"name"`
	Port int    `toml:"port"`
	// Clipboard enables clipboard sync with the peer (default true).
	Clipboard bool `toml:"clipboard"`
	// ClipboardMaxFileMB bounds the copied files, and contents too big for the
	// message connection, transferred in either direction (default 20, 0
	// disables these transfers).
	ClipboardMaxFileMB int `toml:"clipboard_max_file_mb"`
}

func Load(path string) (*Config, error) {
	cfg := Config{Clipboard: true, ClipboardMaxFileMB: 20}
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	if cfg.Host == "" {
		return nil, fmt.Errorf("config: host is required")
	}
	if cfg.Key == "" {
		return nil, fmt.Errorf("config: key is required")
	}
	if cfg.ClipboardMaxFileMB < 0 {
		return nil, fmt.Errorf("config: clipboard_max_file_mb must not be negative")
	}
	if cfg.Port == 0 {
		cfg.Port = 15100
	}
	if cfg.Name == "" {
		hostname, _ := os.Hostname()
		if hostname == "" {
			hostname = "linux"
		}
		cfg.Name = hostname
	}
	if len(cfg.Name) > 15 {
		cfg.Name = cfg.Name[:15]
	}
	return &cfg, nil
}

func (c *Config) MessagePort() int {
	return c.Port + 1
}
