package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
)

// Minimal MCP (JSON-RPC 2.0 over newline-delimited stdio), no deps.

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcErr struct {
	Code int    `json:"code"`
	Msg  string `json:"message"`
}

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

type tool struct {
	Name   string         `json:"name"`
	Desc   string         `json:"description"`
	Schema map[string]any `json:"inputSchema"`
	Ann    map[string]any `json:"annotations,omitempty"`
	fn     func(a json.RawMessage) (any, error)
}

type server struct {
	tools []*tool
	idx   map[string]*tool
	mu    sync.Mutex
	wg    sync.WaitGroup
	w     *bufio.Writer
}

func newServer(ts []*tool) *server {
	s := &server{tools: ts, idx: map[string]*tool{}}
	for _, t := range ts {
		s.idx[t.Name] = t
	}
	return s
}

func (s *server) send(v any) {
	b, _ := json.Marshal(v)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.w.Write(b)
	s.w.WriteByte('\n')
	s.w.Flush()
}

func (s *server) serve(r io.Reader, w io.Writer) error {
	s.w = bufio.NewWriter(w)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		ln := sc.Bytes()
		if len(ln) == 0 {
			continue
		}
		var q rpcReq
		if err := json.Unmarshal(ln, &q); err != nil {
			s.send(rpcResp{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcErr{-32700, "parse error"}})
			continue
		}
		if len(q.ID) == 0 { // notification
			continue
		}
		s.wg.Add(1)
		go func() { defer s.wg.Done(); s.handle(q) }()
	}
	s.wg.Wait()
	return sc.Err()
}

func (s *server) handle(q rpcReq) {
	res, e := s.dispatch(q)
	s.send(rpcResp{JSONRPC: "2.0", ID: q.ID, Result: res, Error: e})
}

func (s *server) dispatch(q rpcReq) (any, *rpcErr) {
	switch q.Method {
	case "initialize":
		var p struct {
			PV string `json:"protocolVersion"`
		}
		json.Unmarshal(q.Params, &p)
		pv := p.PV
		if pv == "" {
			pv = "2025-06-18"
		}
		return map[string]any{
			"protocolVersion": pv,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "mirea-moodle", "version": version},
			"instructions":    serverInstr,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.tools}, nil
	case "tools/call":
		var p struct {
			Name string          `json:"name"`
			Args json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(q.Params, &p); err != nil {
			return nil, &rpcErr{-32602, "bad params"}
		}
		t := s.idx[p.Name]
		if t == nil {
			return nil, &rpcErr{-32602, "unknown tool " + p.Name}
		}
		if len(p.Args) == 0 || string(p.Args) == "null" {
			p.Args = json.RawMessage("{}")
		}
		out, err := safeCall(t, p.Args)
		if err != nil {
			return textRes(err.Error(), true), nil
		}
		var txt string
		if str, ok := out.(string); ok {
			txt = str
		} else {
			b, _ := json.MarshalIndent(out, "", " ")
			txt = string(b)
		}
		return textRes(txt, false), nil
	case "resources/list":
		return map[string]any{"resources": []any{}}, nil
	case "prompts/list":
		return map[string]any{"prompts": []any{}}, nil
	}
	return nil, &rpcErr{-32601, "method not found: " + q.Method}
}

func safeCall(t *tool, a json.RawMessage) (out any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
			fmt.Fprintln(os.Stderr, "panic in", t.Name, r)
		}
	}()
	return t.fn(a)
}

func textRes(t string, isErr bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": t}},
		"isError": isErr,
	}
}

const serverInstr = `Tools for the student's own MIREA Moodle (online-edu.mirea.ru).
Typical flow: list_courses -> course_contents(course_id) -> get_activity(url) -> download_file / read_text.
Deadlines: deadlines. Overview page: dashboard.
submit_assignment saves a draft; finalize=true sends the work for grading and usually cannot be undone: always confirm with the user first.
If a tool says the session expired, tell the user to log in at online-edu.mirea.ru in the browser and run: mirea-moodle-mcp cookie <MoodleSession>.
All times are Europe/Moscow.`
