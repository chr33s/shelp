package nl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chr33s/shelp/internal/index"
)

func TestSuggestionsContractCacheAndInvalidation(t *testing.T) {
	t.Setenv("MANPATH", t.TempDir()) // fake commands must not resolve to system man pages
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(t.TempDir(), "index.db")
	if err := os.WriteFile(filepath.Join(dir, "find"), []byte("#!/bin/sh\necho 'Usage: find files -size +100M'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	build := index.Options{DBPath: path, PATH: dir, Workers: 1, Timeout: time.Second}
	if _, err := index.Build(ctx, build); err != nil {
		t.Fatal(err)
	}
	fm := filepath.Join(t.TempDir(), "fm")
	argsFile := fm + ".args"
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsFile + "'\nprintf '%s\\n' 'find . -type f -size +100M'\n"
	if err := os.WriteFile(fm, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	opts := Options{DBPath: path, Executable: fm, Backend: "fm", Timeout: time.Second, TTL: time.Minute}
	got, err := Suggest(ctx, "?find large files", opts)
	if err != nil || len(got) != 1 || got[0] != "find . -type f -size +100M" {
		t.Fatalf("suggest: %v %v", got, err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"respond\n--model\nsystem\n--no-stream", "--greedy", "Platform:", "--instructions", "--text", "User intent: find large files", "Usage: find files"} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("fm arguments lack %q: %s", want, args)
		}
	}
	if err = os.WriteFile(fm, []byte("#!/bin/sh\necho unavailable >&2\nexit 69\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err = Suggest(ctx, "find large files", opts); err != nil {
		t.Fatal("cache missed", err)
	}
	opts.TTL = 0
	if _, err = Suggest(ctx, "find large files", opts); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatal("missing fm error", err)
	}
	opts.TTL = time.Minute
	build.Full = true
	if _, err = index.Build(ctx, build); err != nil {
		t.Fatal(err)
	}
	if _, err = Suggest(ctx, "find large files", opts); err == nil {
		t.Fatal("index rebuild did not invalidate NL cache")
	}
}
func TestOutputAndExplicitTrigger(t *testing.T) {
	for _, out := range []string{"", "```sh\nls\n```", "ls\ncat\ngit\nfind", "ls\x1b[31m", "# explanation"} {
		if _, err := parse(out, "fm"); err == nil {
			t.Fatalf("accepted %q", out)
		}
	}
	for _, q := range []string{"?files", "  ?find large files"} {
		if !Requested(q) {
			t.Fatalf("not prose: %q", q)
		}
	}
	for _, q := range []string{"git", "find large files", "show git history", "find . -type f", "git log --all", "show foo/bar files"} {
		if Requested(q) {
			t.Fatalf("unexpected prose: %q", q)
		}
	}
}
func TestCacheExpiry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(t.TempDir(), "index.db")
	if _, err := index.Build(context.Background(), index.Options{DBPath: path, PATH: dir, Workers: 1, Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := index.Cache(context.Background(), path, "expired", []string{"ls"}, -time.Second); err != nil {
		t.Fatal(err)
	}
	if got, err := index.Cached(context.Background(), path, "expired"); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
}
