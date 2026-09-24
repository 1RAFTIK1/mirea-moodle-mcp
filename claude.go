package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// claudeConfig returns the Claude Desktop config path for this OS.
func claudeConfig() (string, error) {
	h, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(h, "Library", "Application Support", "Claude", "claude_desktop_config.json"), nil
	case "windows":
		a := os.Getenv("APPDATA")
		if a == "" {
			return "", errors.New("не найден %APPDATA%")
		}
		return filepath.Join(a, "Claude", "claude_desktop_config.json"), nil
	default:
		return filepath.Join(h, ".config", "Claude", "claude_desktop_config.json"), nil
	}
}

// registerClaude adds/updates the "mirea-moodle" entry, keeping everything else.
func registerClaude() (string, error) {
	p, err := claudeConfig()
	if err != nil {
		return "", err
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	cfg := map[string]any{}
	if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return "", fmt.Errorf("%s повреждён (%v) — поправь вручную", p, err)
		}
		bak := fmt.Sprintf("%s.bak-%s", p, time.Now().Format("20060102-150405"))
		os.WriteFile(bak, b, 0o600)
	} else if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	srv, _ := cfg["mcpServers"].(map[string]any)
	if srv == nil {
		srv = map[string]any{}
	}
	srv["mirea-moodle"] = map[string]any{"command": exe}
	cfg["mcpServers"] = srv
	b, _ := json.MarshalIndent(cfg, "", "  ")
	return p, os.WriteFile(p, b, 0o600)
}
