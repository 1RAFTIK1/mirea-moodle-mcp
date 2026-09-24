package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type req struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

var out = json.NewEncoder(os.Stdout) // Encode пишет JSON + '\n' — ровно формат stdio-транспорта

func reply(id json.RawMessage, result any) {
	out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func fail(id json.RawMessage, code int, msg string) {
	out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
}

func text(t string, isErr bool) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": t}}, "isError": isErr}
}

var tools = []map[string]any{{
	"name":        "shout",
	"description": "Переводит текст в верхний регистр. Используй, когда пользователь просит «прокричать» фразу.",
	"inputSchema": map[string]any{
		"type":       "object",
		"properties": map[string]any{"text": map[string]any{"type": "string", "description": "что прокричать"}},
		"required":   []string{"text"},
	},
}}

func main() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var q req
		if json.Unmarshal(sc.Bytes(), &q) != nil || len(q.ID) == 0 {
			continue // мусор или notification — не отвечаем
		}
		switch q.Method {
		case "initialize":
			reply(q.ID, map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "mini", "version": "0.1"},
			})
		case "ping":
			reply(q.ID, map[string]any{})
		case "tools/list":
			reply(q.ID, map[string]any{"tools": tools})
		case "tools/call":
			var p struct {
				Name string `json:"name"`
				Args struct {
					Text string `json:"text"`
				} `json:"arguments"`
			}
			json.Unmarshal(q.Params, &p)
			if p.Name != "shout" {
				fail(q.ID, -32602, "unknown tool "+p.Name)
				continue
			}
			if p.Args.Text == "" {
				reply(q.ID, text("пустой text", true)) // ошибка инструмента — через isError
				continue
			}
			reply(q.ID, text(strings.ToUpper(p.Args.Text)+"!!!", false))
		default:
			fail(q.ID, -32601, "method not found: "+q.Method)
		}
	}
	fmt.Fprintln(os.Stderr, "stdin closed, bye") // логи — только в stderr
}
