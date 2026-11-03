package nl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

const defaultOllamaHost = "http://127.0.0.1:11434"

// ollamaURL normalises an Ollama address ("", "localhost:11434" or a URL) and
// accepts only loopback hosts: help excerpts can contain private paths, and
// shelp never sends them off the machine.
func ollamaURL(raw string) (string, error) {
	if raw == "" {
		raw = defaultOllamaHost
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("invalid Ollama address %q; use http://127.0.0.1:11434", raw)
	}
	host := u.Hostname()
	// OLLAMA_HOST is often a bind address (0.0.0.0); the server is then
	// reachable on loopback.
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
		u.Host = net.JoinHostPort(host, u.Port())
		if u.Port() == "" {
			u.Host = host
		}
	}
	if ip := net.ParseIP(host); !(host == "localhost" || (ip != nil && ip.IsLoopback())) {
		return "", fmt.Errorf("Ollama address %q is not on this machine; shelp only sends help excerpts to a loopback address (localhost, 127.0.0.1 or ::1)", raw)
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(host, "11434")
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host+u.Path, "/"), nil
}

// ollamaChat asks a local Ollama model for suggestions with the same
// instructions as fm, deterministically (temperature 0).
func ollamaChat(ctx context.Context, host, model, prompt string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"model":  model,
		"stream": false,
		"messages": []map[string]string{
			{"role": "system", "content": instructions},
			{"role": "user", "content": prompt},
		},
		"options": map[string]any{"temperature": 0},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	// No proxy: a loopback request must not be routed through HTTP_PROXY.
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("Ollama suggestions timed out: %w", ctx.Err())
		}
		return "", fmt.Errorf("could not reach Ollama at %s (is `ollama serve` running?): %w", host, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var reply struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(data, &reply)
	if resp.StatusCode != http.StatusOK {
		msg := reply.Error
		if msg == "" {
			msg = strings.TrimSpace(string(data))
		}
		if resp.StatusCode == http.StatusNotFound {
			return "", fmt.Errorf("Ollama model %q is not available: %s (run `ollama pull %s`)", model, msg, model)
		}
		return "", fmt.Errorf("Ollama suggestions failed (%s): %s", resp.Status, msg)
	}
	if reply.Message.Content == "" {
		return "", fmt.Errorf("Ollama returned no suggestion")
	}
	return reply.Message.Content, nil
}

// stripFence removes one surrounding Markdown code fence (```sh ... ```).
func stripFence(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) >= 2 && strings.HasPrefix(strings.TrimSpace(lines[0]), "```") && strings.TrimSpace(lines[len(lines)-1]) == "```" {
		return strings.Join(lines[1:len(lines)-1], "\n")
	}
	return out
}
