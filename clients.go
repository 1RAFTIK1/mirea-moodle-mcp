package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Registration of the server in popular MCP clients.
//
// Every client speaks the same MCP over stdio; they differ only in where the
// config lives and how it is shaped. Three shapes cover almost everyone:
//   mcpServers  {"mcpServers": {"name": {"command": "..."}}}            Claude Desktop, Cursor, Windsurf, Gemini CLI, LM Studio, Cline
//   vscode      {"servers": {"name": {"type": "stdio", "command": "..."}}}
//   zed         {"context_servers": {"name": {"command": "...", "args": [], "env": {}}}}
// plus Codex (TOML) and clients that have their own "mcp add" CLI.

const srvName = "mirea-moodle"

type mcpClient struct {
	ID, Name string
	kind     string                    // mcpServers | vscode | zed | codex | cli
	path     func() string             // config file (empty: none)
	cli      func(exe string) []string // preferred if the binary is in PATH
	detect   func() bool
	note     string
}

func home() string { h, _ := os.UserHomeDir(); return h }

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func inPath(b string) bool { _, err := exec.LookPath(b); return err == nil }

func appData() string {
	if a := os.Getenv("APPDATA"); a != "" {
		return a
	}
	return filepath.Join(home(), "AppData", "Roaming")
}

// osPath picks a per-OS path.
func osPath(mac, win, linux string) string {
	switch runtime.GOOS {
	case "darwin":
		return mac
	case "windows":
		return win
	}
	return linux
}

func vscodeUser() string {
	return osPath(filepath.Join(home(), "Library", "Application Support", "Code", "User"),
		filepath.Join(appData(), "Code", "User"),
		filepath.Join(home(), ".config", "Code", "User"))
}

func dirOfP(f func() string) func() bool {
	return func() bool { return exists(filepath.Dir(f())) }
}

var clients = []*mcpClient{
	{ID: "claude-desktop", Name: "Claude Desktop", kind: "mcpServers",
		path: func() string {
			return osPath(filepath.Join(home(), "Library", "Application Support", "Claude", "claude_desktop_config.json"),
				filepath.Join(appData(), "Claude", "claude_desktop_config.json"),
				filepath.Join(home(), ".config", "Claude", "claude_desktop_config.json"))
		}},
	{ID: "claude-code", Name: "Claude Code", kind: "cli",
		cli: func(exe string) []string {
			return []string{"claude", "mcp", "add", "--scope", "user", srvName, "--", exe}
		},
		detect: func() bool { return inPath("claude") }},
	{ID: "cursor", Name: "Cursor", kind: "mcpServers",
		path: func() string { return filepath.Join(home(), ".cursor", "mcp.json") }},
	{ID: "vscode", Name: "VS Code (Copilot agent)", kind: "vscode",
		path: func() string { return filepath.Join(vscodeUser(), "mcp.json") },
		cli: func(exe string) []string {
			b, _ := json.Marshal(map[string]any{"name": srvName, "type": "stdio", "command": exe})
			return []string{"code", "--add-mcp", string(b)}
		}},
	{ID: "cline", Name: "Cline (VS Code)", kind: "mcpServers",
		path: func() string {
			return filepath.Join(vscodeUser(), "globalStorage", "saoudrizwan.claude-dev", "settings", "cline_mcp_settings.json")
		}},
	{ID: "windsurf", Name: "Windsurf", kind: "mcpServers",
		path: func() string { return filepath.Join(home(), ".codeium", "windsurf", "mcp_config.json") }},
	{ID: "gemini", Name: "Gemini CLI", kind: "mcpServers",
		path: func() string { return filepath.Join(home(), ".gemini", "settings.json") }},
	{ID: "codex", Name: "OpenAI Codex CLI", kind: "codex",
		path: func() string {
			if h := os.Getenv("CODEX_HOME"); h != "" {
				return filepath.Join(h, "config.toml")
			}
			return filepath.Join(home(), ".codex", "config.toml")
		},
		cli: func(exe string) []string { return []string{"codex", "mcp", "add", srvName, "--", exe} }},
	{ID: "lmstudio", Name: "LM Studio", kind: "mcpServers",
		path: func() string { return filepath.Join(home(), ".lmstudio", "mcp.json") }},
	{ID: "zed", Name: "Zed", kind: "zed",
		path: func() string {
			return osPath(filepath.Join(home(), ".config", "zed", "settings.json"),
				filepath.Join(appData(), "Zed", "settings.json"),
				filepath.Join(home(), ".config", "zed", "settings.json"))
		}},
}

func init() {
	for _, c := range clients {
		if c.detect == nil && c.path != nil {
			c.detect = dirOfP(c.path)
			if c.cli != nil {
				p, cl := c.path, c.cli
				c.detect = func() bool { return exists(filepath.Dir(p())) || inPath(cl("")[0]) }
			}
		}
	}
}

func findClient(id string) *mcpClient {
	id = strings.ToLower(strings.TrimSpace(id))
	alias := map[string]string{"claude": "claude-desktop", "code": "vscode", "copilot": "vscode", "vs-code": "vscode", "gemini-cli": "gemini", "lm-studio": "lmstudio"}
	if a, ok := alias[id]; ok {
		id = a
	}
	for _, c := range clients {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func selfExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return exe, nil
}

// snippet returns the config fragment for manual installation.
func snippet(c *mcpClient, exe string) string {
	var v any
	switch c.kind {
	case "vscode":
		v = map[string]any{"servers": map[string]any{srvName: map[string]any{"type": "stdio", "command": exe}}}
	case "zed":
		v = map[string]any{"context_servers": map[string]any{srvName: map[string]any{"command": exe, "args": []string{}, "env": map[string]string{}}}}
	case "codex":
		return fmt.Sprintf("[mcp_servers.%s]\ncommand = %s\n", srvName, tomlStr(exe))
	case "cli":
		return strings.Join(quoteArgs(c.cli(exe)), " ")
	default:
		v = map[string]any{"mcpServers": map[string]any{srvName: map[string]any{"command": exe}}}
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func quoteArgs(a []string) []string {
	out := make([]string, len(a))
	for i, s := range a {
		if strings.ContainsAny(s, " \"'{}") {
			s = "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
		out[i] = s
	}
	return out
}

// install registers the server in one client. Returns a human message.
func install(c *mcpClient) (string, error) {
	exe, err := selfExe()
	if err != nil {
		return "", err
	}
	// 1. the client's own CLI, if available
	if c.cli != nil && inPath(c.cli(exe)[0]) {
		a := c.cli(exe)
		out, err := exec.Command(a[0], a[1:]...).CombinedOutput()
		if err == nil {
			return "через " + a[0] + ": " + strings.TrimSpace(firstLine(string(out))), nil
		}
		if c.path == nil {
			return "", fmt.Errorf("%s: %v: %s", a[0], err, strings.TrimSpace(string(out)))
		}
		// fall through to editing the file
	}
	if c.path == nil {
		return "", fmt.Errorf("%s не найден в PATH; выполни вручную:\n  %s", c.cli(exe)[0], snippet(c, exe))
	}
	p := c.path()
	switch c.kind {
	case "codex":
		return p, writeCodex(p, exe)
	case "vscode":
		return p, writeJSON(p, "servers", map[string]any{"type": "stdio", "command": exe}, c, exe)
	case "zed":
		return p, writeJSON(p, "context_servers", map[string]any{"command": exe, "args": []string{}, "env": map[string]string{}}, c, exe)
	default:
		return p, writeJSON(p, "mcpServers", map[string]any{"command": exe}, c, exe)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func backup(p string, b []byte) {
	os.WriteFile(fmt.Sprintf("%s.bak-%s", p, time.Now().Format("20060102-150405")), b, 0o600)
}

func fileMode(p string) os.FileMode {
	if st, err := os.Stat(p); err == nil {
		return st.Mode().Perm()
	}
	return 0o600
}

// writeJSON sets cfg[key][srvName] = entry, keeping everything else.
func writeJSON(p, key string, entry map[string]any, c *mcpClient, exe string) error {
	cfg := map[string]any{}
	b, err := os.ReadFile(p)
	if err == nil && len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return fmt.Errorf("%s не чистый JSON (комментарии?) — не трогаю его. Добавь вручную:\n%s", p, snippet(c, exe))
		}
		backup(p, b)
	} else if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	m, _ := cfg[key].(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	m[srvName] = entry
	cfg[key] = m
	out, _ := json.MarshalIndent(cfg, "", "  ")
	return os.WriteFile(p, append(out, '\n'), fileMode(p))
}

func tomlStr(s string) string {
	if !strings.Contains(s, "'") {
		return "'" + s + "'" // literal string: no escaping (Windows paths)
	}
	b, _ := json.Marshal(s)
	return string(b)
}

var reTomlHdr = regexp.MustCompile(`(?m)^\s*\[`)

// writeCodex replaces or appends the [mcp_servers.mirea-moodle] table.
func writeCodex(p, exe string) error {
	b, _ := os.ReadFile(p)
	s := string(b)
	if len(b) > 0 {
		backup(p, b)
	} else if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	sec := fmt.Sprintf("[mcp_servers.%s]\ncommand = %s\n", srvName, tomlStr(exe))
	hdr := "[mcp_servers." + srvName + "]"
	if i := strings.Index(s, hdr); i >= 0 {
		rest := s[i+len(hdr):]
		end := len(s)
		if loc := reTomlHdr.FindStringIndex(rest); loc != nil {
			end = i + len(hdr) + loc[0]
		}
		s = s[:i] + sec + "\n" + strings.TrimLeft(s[end:], "\n")
	} else {
		if s != "" && !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		if s != "" {
			s += "\n"
		}
		s += sec
	}
	return os.WriteFile(p, []byte(s), fileMode(p))
}

// ---- CLI ----

func cmdClients() {
	exe, _ := selfExe()
	fmt.Println("Клиенты MCP (✓ — найден на этом компьютере):")
	for _, c := range clients {
		mark := "  "
		if c.detect() {
			mark = "✓ "
		}
		where := ""
		if c.path != nil {
			where = c.path()
		} else if c.cli != nil {
			where = "команда " + c.cli(exe)[0]
		}
		fmt.Printf("  %s%-15s %-26s %s\n", mark, c.ID, c.Name, where)
	}
	fmt.Println(`
mirea-moodle-mcp install <id>      подключить к клиенту
mirea-moodle-mcp install detected  ко всем найденным
mirea-moodle-mcp config <id>       показать фрагмент конфига для ручной вставки
mirea-moodle-mcp http              для ChatGPT, claude.ai и других удалённых клиентов (см. docs/CLIENTS.md)`)
}

func cmdInstall(arg string) error {
	var list []*mcpClient
	switch arg {
	case "", "detected", "all":
		for _, c := range clients {
			if c.detect() {
				list = append(list, c)
			}
		}
		if len(list) == 0 {
			return errors.New("не нашёл установленных клиентов; укажи явно: mirea-moodle-mcp install <id> (список: mirea-moodle-mcp clients)")
		}
	default:
		c := findClient(arg)
		if c == nil {
			return fmt.Errorf("неизвестный клиент %q (список: mirea-moodle-mcp clients)", arg)
		}
		list = []*mcpClient{c}
	}
	failed := 0
	for _, c := range list {
		msg, err := install(c)
		if err != nil {
			failed++
			fmt.Printf("✗ %s: %v\n", c.Name, err)
			continue
		}
		fmt.Printf("✓ %s — %s\n", c.Name, msg)
	}
	fmt.Println("Перезапусти клиент(ы), чтобы они увидели сервер.")
	if failed > 0 && failed == len(list) {
		return errors.New("ни один клиент не настроен")
	}
	return nil
}

func cmdConfig(arg string) error {
	exe, _ := selfExe()
	c := findClient(arg)
	if c == nil {
		ids := []string{}
		for _, c := range clients {
			ids = append(ids, c.ID)
		}
		sort.Strings(ids)
		return fmt.Errorf("укажи клиент: %s", strings.Join(ids, ", "))
	}
	if c.path != nil {
		fmt.Println("# файл:", c.path())
	}
	fmt.Println(snippet(c, exe))
	return nil
}
