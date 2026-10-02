package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInRoots(t *testing.T) {
	h := fakeHome(t)
	dl := filepath.Join(h, "Downloads")
	os.MkdirAll(dl, 0o755)
	os.MkdirAll(filepath.Join(h, ".ssh"), 0o700)
	os.WriteFile(filepath.Join(h, ".ssh", "id"), []byte("k"), 0o600)
	os.WriteFile(filepath.Join(dl, "r.pdf"), []byte("x"), 0o644)
	symlinked := os.Symlink(filepath.Join(h, ".ssh"), filepath.Join(dl, "keys")) == nil // needs privileges on Windows

	ok := []string{filepath.Join(dl, "r.pdf"), "~/Downloads/r.pdf", filepath.Join(dl, "new", "dir", "f.txt")}
	bad := []string{
		filepath.Join(h, ".ssh", "id"),         // outside roots
		filepath.Join(dl, "..", ".ssh", "id"),  // traversal
		filepath.Join(dl, "keys", "id"),        // symlink escaping the root
		filepath.Join(dl, ".env"),              // hidden inside root
		filepath.Join(dl, "proj", ".git", "x"), // hidden segment
		filepath.Join(h, "outside.txt"),
	}
	for _, p := range ok {
		if _, err := inRoots(p); err != nil {
			t.Errorf("want ok %s: %v", p, err)
		}
	}
	for _, p := range bad {
		if !symlinked && strings.Contains(p, "keys") {
			continue
		}
		if _, err := inRoots(p); err == nil {
			t.Errorf("want deny %s", p)
		}
	}
}

func TestRootsConfig(t *testing.T) {
	h := fakeHome(t)
	vuz := filepath.Join(h, "vuz")
	os.MkdirAll(vuz, 0o755)
	if _, err := inRoots(filepath.Join(vuz, "a.ipynb")); err == nil {
		t.Fatal("vuz must be denied by default")
	}
	if err := cmdRoots([]string{"add", vuz}); err != nil {
		t.Fatal(err)
	}
	if _, err := inRoots(filepath.Join(vuz, "a.ipynb")); err != nil {
		t.Fatal(err)
	}
}

func TestSaveFile(t *testing.T) {
	d := t.TempDir()
	p1, _, err := saveFile(strings.NewReader("one"), d, "a.pdf", false)
	if err != nil {
		t.Fatal(err)
	}
	p2, _, _ := saveFile(strings.NewReader("two"), d, "a.pdf", false)
	if filepath.Base(p1) != "a.pdf" || filepath.Base(p2) != "a (1).pdf" {
		t.Fatalf("names: %s %s", p1, p2)
	}
	p3, _, _ := saveFile(strings.NewReader("three"), d, "a.pdf", true)
	if b, _ := os.ReadFile(p3); p3 != p1 || string(b) != "three" {
		t.Fatalf("overwrite: %s %s", p3, b)
	}
	if p, _, _ := saveFile(strings.NewReader("x"), d, "../.bashrc", false); filepath.Dir(p) != d || strings.HasPrefix(filepath.Base(p), ".") {
		t.Fatalf("name sanitize: %s", p)
	}
	// failed stream leaves nothing behind
	_, _, err = saveFile(io.MultiReader(strings.NewReader("half"), errReader{}), d, "b.pdf", false)
	if err == nil {
		t.Fatal("want error")
	}
	left, _ := filepath.Glob(filepath.Join(d, "*b.pdf*"))
	parts, _ := filepath.Glob(filepath.Join(d, ".mirea-*"))
	if len(left)+len(parts) != 0 {
		t.Fatalf("leftovers: %v %v", left, parts)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("network down") }

func TestSubmitGuards(t *testing.T) {
	fakeHome(t)
	s := newSess("https://example.invalid")
	if _, err := s.submit(1, []string{"/etc/passwd"}, "", false, true); err == nil || !strings.Contains(err.Error(), "вне разрешённых") {
		t.Fatalf("roots guard: %v", err)
	}
	remoteMode, allowSubmit = true, false
	defer func() { remoteMode = false }()
	if _, err := s.submit(1, nil, "hi", false, true); err == nil || !strings.Contains(err.Error(), "HTTP-режиме") {
		t.Fatalf("remote guard: %v", err)
	}
}
