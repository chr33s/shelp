package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func run(args ...string) (string, error) {
	out, _, err := runStreams(args...)
	return out, err
}
func TestCLI(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(t.TempDir(), "index.db")
	for name, help := range map[string]string{"git": "Usage: git commit history", "find": "Usage: find files size"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\necho '"+help+"'\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := run("--db", db, "index", "--path", dir); err != nil || out != "Indexed 2 commands: 2 new, 0 changed, 0 removed; 0 without help.\n" {
		t.Fatal(out, err)
	}
	checks := []struct {
		args []string
		want string
	}{
		{[]string{"git"}, "git\tUsage: git"},
		{[]string{"find large files"}, "find\tUsage: find"},
		{[]string{"--json", "git"}, `"name":"git"`},
		{[]string{"setup", "zsh"}, `eval "$(shelp init zsh)"`},
		{[]string{"setup", "bash"}, "~/.bashrc"},
		{[]string{"search", "git"}, "git\tUsage: git"},
		{[]string{"search", "--json", "files"}, `"name":"find"`},
		{[]string{"complete", "--shell", "zsh", "git"}, "git\n"},
		{[]string{"status"}, "Commands: 2"},
		{[]string{"init", "zsh"}, "zle -N _shelp_widget"},
		{[]string{"init", "bash"}, "complete -F _shelp_complete"},
	}
	for _, check := range checks {
		out, err := run(append([]string{"--db", db}, check.args...)...)
		if err != nil || !strings.Contains(out, check.want) {
			t.Fatalf("%v: %q %v", check.args, out, err)
		}
	}
	for _, args := range [][]string{{"init", "fish"}, {"complete", "--shell", "fish", "git"}, {"search"}, {"search", "--limit", "0", "git"}, {"index", "--workers", "0"}} {
		if _, err := run(append([]string{"--db", db}, args...)...); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if out, err := run("--db", db, "complete", "--", "git log"); err != nil || out != "" {
		t.Fatal(out, err)
	}
}
func TestShellQuote(t *testing.T) {
	for name, want := range map[string]string{"git": "git", "my tool": "'my tool'", "a'b": "'a'\\''b'", "$(touch marker)": "'$(touch marker)'"} {
		if got := shellQuote(name); got != want {
			t.Fatalf("%q: %q", name, got)
		}
	}
}

func runStreams(args ...string) (string, string, error) {
	cmd := newRoot()
	var out, diagnostics bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&diagnostics)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), diagnostics.String(), err
}

func TestExplicitAIAndDefaultSearch(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(t.TempDir(), "index.db")
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\necho 'Usage: git show commit history'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := run("--db", db, "index", "--path", dir); err != nil {
		t.Fatal(err)
	}
	// An unavailable model must never affect ordinary queries, even prose.
	missingFM := filepath.Join(t.TempDir(), "missing-fm")
	for _, args := range [][]string{{"show git history"}, {"show", "git", "history"}, {"search", "show git history"}, {"complete", "show git history"}} {
		if _, err := run(append([]string{"--db", db, "--fm", missingFM}, args...)...); err != nil {
			t.Fatalf("query unexpectedly invoked fm: %v: %v", args, err)
		}
	}
	fm := filepath.Join(t.TempDir(), "fm")
	if err := os.WriteFile(fm, []byte("#!/bin/sh\necho 'git log --oneline'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"?show git history"}, {"nl", "show git history"}, {"complete", "?show git history"}} {
		out, err := run(append([]string{"--db", db, "--fm", fm, "--nl-ttl", "0"}, args...)...)
		if err != nil || out != "git log --oneline\n" {
			t.Fatalf("explicit AI %v: %q %v", args, out, err)
		}
	}
	// Advanced flags work after the explicit command as well as before it.
	if out, err := run("--db", db, "nl", "--fm", fm, "--nl-ttl", "0", "show git history"); err != nil || out != "git log --oneline\n" {
		t.Fatal(out, err)
	}
}

func TestHelpAndFirstRun(t *testing.T) {
	help, err := run("--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []string{"--fm", "--nl-timeout", "--nl-ttl", "  complete ", "  init "} {
		if strings.Contains(help, hidden) {
			t.Fatalf("root help exposes advanced plumbing %q:\n%s", hidden, help)
		}
	}
	for _, visible := range []string{"shelp git", "shelp setup zsh", "--limit", "--json"} {
		if !strings.Contains(help, visible) {
			t.Fatalf("help missing %q", visible)
		}
	}
	if out, err := run(); err != nil || out != help {
		t.Fatal("empty invocation should show help", out, err)
	}
	if out, err := run("nl", "--help"); err != nil || !strings.Contains(out, "--fm") || !strings.Contains(out, "--nl-ttl") {
		t.Fatal(out, err)
	}
	if _, err := run("--db", filepath.Join(t.TempDir(), "missing.db"), "git"); err == nil || !strings.Contains(err.Error(), "Run: shelp index") {
		t.Fatal(err)
	}
	if _, err := run("setup", "fish"); err == nil {
		t.Fatal("unknown setup shell accepted")
	}
}

func TestIndexProgressAndSummary(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(t.TempDir(), "index.db")
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\necho 'Usage: "+text+"'\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	write("tool", "initial")
	write("removed", "old")
	args := []string{"--db", db, "index", "--path", dir}
	out, progress, err := runStreams(args...)
	if err != nil || !strings.Contains(out, "2 new, 0 changed, 0 removed; 0 without help") {
		t.Fatal(out, err)
	}
	for _, phase := range []string{"Scanning PATH", "Capturing help: 0/2", "Capturing help: 2/2", "Training embeddings", "Saving index"} {
		if !strings.Contains(progress, phase) {
			t.Fatalf("missing progress %q: %s", phase, progress)
		}
	}
	out, progress, err = runStreams(args...)
	if err != nil || !strings.Contains(out, "0 new, 0 changed, 0 removed") || strings.Contains(progress, "Training") {
		t.Fatal(out, progress, err)
	}
	out, progress, err = runStreams(append(args, "--full")...)
	if err != nil || !strings.Contains(out, "0 new, 0 changed, 0 removed") || !strings.Contains(progress, "Training embeddings") {
		t.Fatal(out, progress, err)
	}
	write("tool", "changed")
	write("added", "new")
	if err = os.Remove(filepath.Join(dir, "removed")); err != nil {
		t.Fatal(err)
	}
	out, _, err = runStreams(args...)
	if err != nil || !strings.Contains(out, "1 new, 1 changed, 1 removed") {
		t.Fatal(out, err)
	}
	// An isolated PATH makes a missing man fallback deterministic.
	t.Setenv("PATH", dir)
	if err = os.WriteFile(filepath.Join(dir, "silent"), []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	out, _, err = runStreams(args...)
	if err != nil || !strings.Contains(out, "1 without help") {
		t.Fatal(out, err)
	}
}
