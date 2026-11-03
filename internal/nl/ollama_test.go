package nl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chr33s/shelp/internal/index"
)

func indexedDB(t *testing.T) string {
	t.Helper()
	t.Setenv("MANPATH", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "find"), []byte("#!/bin/sh\necho 'Usage: find files -size +100M'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "index.db")
	if _, err := index.Build(context.Background(), index.Options{DBPath: path, PATH: dir, Workers: 1, Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultBackendPerPlatform(t *testing.T) {
	prev := goos
	t.Cleanup(func() { goos = prev })
	goos = "linux"
	opts := Options{}
	if err := resolveBackend(&opts); err == nil || !strings.Contains(err.Error(), "--nl-backend ollama") || !strings.Contains(err.Error(), "Offline search") {
		t.Fatalf("linux default: %v", err)
	}
	goos = "darwin"
	opts = Options{}
	if err := resolveBackend(&opts); err != nil || opts.Backend != "fm" {
		t.Fatalf("darwin default: %q %v", opts.Backend, err)
	}
	for _, bad := range []Options{{Backend: "cloud"}, {Backend: "ollama"}, {Backend: "ollama", Model: "m", OllamaHost: "http://example.com:11434"}} {
		if err := resolveBackend(&bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

func TestMissingFMExplainsAlternatives(t *testing.T) {
	path := indexedDB(t)
	_, err := Suggest(context.Background(), "?find large files", Options{DBPath: path, Backend: "fm", Executable: filepath.Join(t.TempDir(), "fm"), Timeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "macOS 27") || !strings.Contains(err.Error(), "--nl-backend ollama") {
		t.Fatalf("missing fm: %v", err)
	}
}

func TestOllamaURL(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "http://127.0.0.1:11434",
		"localhost":              "http://localhost:11434",
		"127.0.0.1:8080":         "http://127.0.0.1:8080",
		"http://[::1]:11434/":    "http://[::1]:11434",
		"0.0.0.0":                "http://127.0.0.1:11434",
		"http://0.0.0.0:11434":   "http://127.0.0.1:11434",
		"https://localhost:9443": "https://localhost:9443",
	} {
		if got, err := ollamaURL(in); err != nil || got != want {
			t.Errorf("ollamaURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"http://192.168.1.5:11434", "example.com", "ftp://localhost", "http://"} {
		if _, err := ollamaURL(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestOllamaBackend(t *testing.T) {
	path := indexedDB(t)
	var got struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct{ Role, Content string }
		Options  map[string]float64
	}
	reply := "```sh\nfind . -type f -size +100M\n```"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		if got.Model == "missing" {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"model \"missing\" not found, try pulling it first"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"role": "assistant", "content": reply}})
	}))
	defer server.Close()
	opts := Options{DBPath: path, Backend: "ollama", Model: "llama3.2", OllamaHost: server.URL, Timeout: 5 * time.Second}
	lines, err := Suggest(context.Background(), "?find large files", opts)
	if err != nil || len(lines) != 1 || lines[0] != "find . -type f -size +100M" {
		t.Fatalf("suggest: %v %v", lines, err)
	}
	if got.Model != "llama3.2" || got.Stream || got.Options["temperature"] != 0 || len(got.Messages) != 2 ||
		got.Messages[0].Content != instructions || !strings.Contains(got.Messages[1].Content, "Usage: find files") {
		t.Fatalf("request: %+v", got)
	}
	// Invalid model output is still rejected after unfencing.
	reply = "Here are some options:\n1. find\n2. du\n3. ls\n4. fd"
	if _, err = Suggest(context.Background(), "?find big files", opts); err == nil || !strings.Contains(err.Error(), "Ollama") {
		t.Fatalf("prose output: %v", err)
	}
	opts.Model = "missing"
	if _, err = Suggest(context.Background(), "?find large files", opts); err == nil || !strings.Contains(err.Error(), "ollama pull missing") {
		t.Fatalf("missing model: %v", err)
	}
	opts.Model, opts.OllamaHost = "llama3.2", "http://127.0.0.1:1"
	if _, err = Suggest(context.Background(), "?find large files", opts); err == nil || !strings.Contains(err.Error(), "ollama serve") {
		t.Fatalf("unreachable: %v", err)
	}
}

func TestOllamaDoesNotFollowRedirects(t *testing.T) {
	path := indexedDB(t)
	forwarded := false
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded = true
	}))
	defer remote.Close()
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, remote.URL+"/api/chat", http.StatusTemporaryRedirect)
	}))
	defer local.Close()
	opts := Options{DBPath: path, Backend: "ollama", Model: "llama3.2", OllamaHost: local.URL, Timeout: 5 * time.Second}
	if _, err := Suggest(context.Background(), "?find large files", opts); err == nil {
		t.Fatal("redirect response accepted")
	}
	if forwarded {
		t.Fatal("prompt forwarded to the redirect target")
	}
}
