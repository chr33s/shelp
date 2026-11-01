package index

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func executable(t testing.TB, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestIndexLifecycle(t *testing.T) {
	ctx := context.Background()
	first, second := t.TempDir(), t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "index.db")
	executable(t, first, "git", "echo 'Usage: git version control history commits'")
	executable(t, first, "find", "echo 'Usage: find locate large files by size'")
	executable(t, second, "git", "echo 'shadowed command should never win'")
	executable(t, second, "grep", "echo 'Usage: grep search patterns in text'")
	if err := os.WriteFile(filepath.Join(first, "ignored"), []byte("not executable"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(first, "find"), filepath.Join(first, "locate")); err != nil {
		t.Fatal(err)
	}
	opts := Options{DBPath: dbPath, PATH: first + string(os.PathListSeparator) + second, Workers: 4, Timeout: time.Second}
	n, err := Build(ctx, opts)
	if err != nil || n != 4 {
		t.Fatalf("Build: %d, %v", n, err)
	}
	for _, query := range []string{"git", "gi", "gti", "history"} {
		results, err := Search(ctx, dbPath, query, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(results) == 0 || results[0].Name != "git" || results[0].Path != filepath.Join(first, "git") {
			t.Fatalf("query %q: %+v", query, results)
		}
	}
	results, err := Search(ctx, dbPath, "gr", 10)
	if err != nil || len(results) == 0 || results[0].Name != "grep" {
		t.Fatalf("prefix: %+v %v", results, err)
	}
	db, err := open(ctx, dbPath, true)
	if err != nil {
		t.Fatal(err)
	}
	// Unchanged entries must retain metadata even if another command changes.
	if _, err = db.Exec(`UPDATE commands SET updated=123`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err = Build(ctx, opts); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(ctx, dbPath)
	if err != nil || s.Updated != 123 || s.Embeddings != 4 {
		t.Fatalf("incremental: %+v %v", s, err)
	}
	if err = os.Remove(filepath.Join(second, "grep")); err != nil {
		t.Fatal(err)
	}
	executable(t, first, "find", "echo 'Usage: find freshuniqueterm'")
	if _, err = Build(ctx, opts); err != nil {
		t.Fatal(err)
	}
	db, err = open(ctx, dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM commands_fts WHERE commands_fts MATCH 'patterns'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale FTS: %d %v", count, err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM commands_fts WHERE commands_fts MATCH 'freshuniqueterm'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("updated FTS: %d %v", count, err)
	}
	var updated int64
	if err = db.QueryRow(`SELECT updated FROM commands WHERE name='git'`).Scan(&updated); err != nil || updated != 123 {
		t.Fatalf("unchanged update: %d %v", updated, err)
	}
	opts.Full = true
	if _, err = Build(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT updated FROM commands WHERE name='git'`).Scan(&updated); err != nil || updated == 123 {
		t.Fatalf("full update: %d %v", updated, err)
	}
}
func TestHelpFallbackAndBounds(t *testing.T) {
	dir := t.TempDir()
	executable(t, dir, "man", `printf 'manual for %s\n' "$4"`)
	t.Setenv("PATH", dir)
	flag := executable(t, dir, "flags", `if [ "$1" = -h ]; then echo 'Usage: flags alternate'; else exit 1; fi`)
	if got := captureHelp(context.Background(), flag, time.Second); got != "Usage: flags alternate" {
		t.Fatal(got)
	}
	listing := executable(t, dir, "listing", `if [ "$1" = -h ]; then echo file.txt; else exit 1; fi`)
	if got := captureHelp(context.Background(), listing, time.Second); got != "manual for listing" {
		t.Fatalf("indexed non-help output: %q", got)
	}
	manual := executable(t, dir, "manual", "exit 1")
	if got := captureHelp(context.Background(), manual, time.Second); got != "manual for manual" {
		t.Fatal(got)
	}
	noisy := executable(t, dir, "noisy", `while :; do printf 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n'; done`)
	start := time.Now()
	got := captureHelp(context.Background(), noisy, 100*time.Millisecond)
	if time.Since(start) > time.Second || len(got) > 12<<10 {
		t.Fatalf("unbounded help: %s %d", time.Since(start), len(got))
	}
	if got := clean("\x1b[31mN\bNAME\x1b[0m\x00"); got != "NAME" {
		t.Fatalf("clean: %q", got)
	}
}
func TestCancelledBuildPreservesSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(t.TempDir(), "index.db")
	executable(t, dir, "tool", "echo 'Usage: tool original'")
	opts := Options{DBPath: path, PATH: dir, Workers: 2, Timeout: time.Second}
	if _, err := Build(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	executable(t, dir, "tool", "/bin/sleep 10")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Build(ctx, opts); err == nil {
		t.Fatal("expected cancellation")
	}
	results, err := Search(context.Background(), path, "original", 1)
	if err != nil || len(results) != 1 || !strings.Contains(results[0].Help, "original") {
		t.Fatalf("snapshot lost: %+v %v", results, err)
	}
}
func TestLegacyMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE commands(name TEXT PRIMARY KEY,path TEXT,help TEXT,help_hash TEXT,updated INTEGER);
 CREATE VIRTUAL TABLE commands_fts USING fts5(name,help,content='commands',content_rowid='rowid');
 INSERT INTO commands VALUES('legacy','/bin/legacy','legacy keyword','1',1)`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = open(context.Background(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM commands_fts WHERE commands_fts MATCH 'keyword'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("migration: %d %v", count, err)
	}
}
func TestQueryAndInputErrors(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "missing.db")
	if _, err := Search(ctx, path, "x", 10); err == nil {
		t.Fatal("missing index accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("search created DB")
	}
	if _, err := Build(ctx, Options{DBPath: path, Workers: 1, Timeout: time.Second}); err == nil {
		t.Fatal("empty PATH accepted")
	}
	dir := t.TempDir()
	executable(t, dir, "tool", "echo 'Usage: tool text'")
	if _, err := Build(ctx, Options{DBPath: path, PATH: dir, Workers: 1, Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`" OR *`, "'", ":", "NEAR(text)", "你好", ""} {
		if _, err := Search(ctx, path, q, 10); err != nil {
			t.Fatalf("query %q: %v", q, err)
		}
	}
	if _, err := Search(ctx, path, "x", 0); err == nil {
		t.Fatal("invalid limit accepted")
	}
}
func BenchmarkIndex1000(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(b.TempDir(), "index.db")
	for i := 0; i < 1000; i++ {
		executable(b, dir, fmt.Sprintf("tool%04d", i), fmt.Sprintf("echo 'Usage: tool%04d search files network documents category%d'", i, i%20))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Build(context.Background(), Options{DBPath: path, PATH: dir, Workers: 32, Timeout: 2 * time.Second, Full: true}); err != nil {
			b.Fatal(err)
		}
	}
}

// A captures the old tool while B replaces or deletes it. A must publish the
// help it actually embedded, even though that help matched its initial cache.
func TestOverlappingBuildsPublishConsistentSnapshot(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(fmt.Sprintf("removed=%t", remove), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			dirA, dirB, state := t.TempDir(), t.TempDir(), t.TempDir()
			dbPath := filepath.Join(state, "index.db")
			executable(t, dirA, "tool", "echo 'Usage: tool oldkeyword'")
			executable(t, dirA, "zgate", "echo 'Usage: gate initial'")
			opts := Options{DBPath: dbPath, PATH: dirA, Workers: 1, Timeout: 8 * time.Second}
			if _, err := Build(ctx, opts); err != nil {
				t.Fatal(err)
			}
			ready, release := filepath.Join(state, "ready"), filepath.Join(state, "release")
			executable(t, dirA, "zgate", fmt.Sprintf("touch %q\nwhile [ ! -f %q ]; do /bin/sleep 0.01; done\necho 'Usage: gate changed'", ready, release))
			finished := make(chan error, 1)
			var report Report
			go func() { defer close(finished); var err error; report, err = BuildReport(ctx, opts); finished <- err }()
			defer func() {
				cancel()
				select {
				case <-finished:
				case <-time.After(time.Second):
				}
			}()
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				select {
				case err := <-finished:
					t.Fatalf("build A ended before capture barrier: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(5 * time.Millisecond):
				}
			}
			if !remove {
				executable(t, dirB, "tool", "echo 'Usage: tool newkeyword'")
			}
			other := opts
			other.PATH = dirB
			if _, err := Build(ctx, other); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-finished:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			expectedNew, expectedChanged := 1, 1
			if remove {
				expectedNew, expectedChanged = 2, 0
			}
			if report.New != expectedNew || report.Changed != expectedChanged || report.Removed != 0 {
				t.Fatalf("summary does not reflect replaced snapshot: %+v", report)
			}
			db, err := open(ctx, dbPath, false)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var help, path string
			var vectors int
			if err = db.QueryRow(`SELECT help,path FROM commands WHERE name='tool'`).Scan(&help, &path); err != nil {
				t.Fatal(err)
			}
			if help != "Usage: tool oldkeyword" || path != filepath.Join(dirA, "tool") {
				t.Fatalf("published stale foreign snapshot: %q %q", help, path)
			}
			if err = db.QueryRow(`SELECT count(*) FROM embeddings WHERE name='tool'`).Scan(&vectors); err != nil || vectors != 1 {
				t.Fatalf("embedding missing: %d %v", vectors, err)
			}
			var count int
			if err = db.QueryRow(`SELECT count(*) FROM commands_fts WHERE commands_fts MATCH 'newkeyword'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("FTS retains other snapshot: %d %v", count, err)
			}
		})
	}
}
