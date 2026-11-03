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
	t.Setenv("MANPATH", t.TempDir()) // fake commands must not resolve to system man pages
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
	executable(t, dir, "man", `case "$4" in listing|manual|documented) printf 'manual for %s\n' "$4";; *) exit 1;; esac`)
	t.Setenv("PATH", dir)
	flag := executable(t, dir, "flags", `if [ "$1" = -h ]; then echo 'Usage: flags alternate'; else echo 'unknown option, try -h' >&2; exit 1; fi`)
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

func mustGetwd(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestHelpProbeSafety(t *testing.T) {
	dir, marks := t.TempDir(), t.TempDir()
	executable(t, dir, "man", `case "$4" in documented) echo 'manual for documented';; *) exit 1;; esac`)
	t.Setenv("PATH", dir)
	ctx := context.Background()
	ran := func(name string) bool {
		_, err := os.Stat(filepath.Join(marks, name))
		return err == nil
	}
	// Commands with a man page are never executed.
	documented := executable(t, dir, "documented", "touch "+marks+"/documented; echo 'Usage: ran'")
	if got := captureHelp(ctx, documented, time.Second); got != "manual for documented" || ran("documented") {
		t.Fatalf("man page not preferred: %q ran=%v", got, ran("documented"))
	}
	// Denylisted names and sbin directories are never executed.
	wall := executable(t, dir, "wall", "touch "+marks+"/wall; echo 'Usage: wall'")
	sbin := filepath.Join(t.TempDir(), "sbin")
	if err := os.Mkdir(sbin, 0755); err != nil {
		t.Fatal(err)
	}
	admin := executable(t, sbin, "admin", "touch "+marks+"/admin; echo 'Usage: admin'")
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(wall, alias); err != nil {
		t.Fatal(err)
	}
	unsafe := []string{wall, admin, alias}
	for _, name := range []string{"perlbug5.34", "install-tool", "update_firmware", "usbdiagnose", "dev-server", "release.sh", "probe.d"} {
		unsafe = append(unsafe, executable(t, dir, name, "touch "+marks+"/unsafe; echo 'Usage: unsafe'"))
	}
	dtrace := filepath.Join(dir, "walker")
	if err := os.WriteFile(dtrace, []byte("#!/usr/sbin/dtrace -s\n"), 0755); err != nil {
		t.Fatal(err)
	}
	unsafe = append(unsafe, dtrace)
	for _, bin := range unsafe {
		if got := captureHelp(ctx, bin, time.Second); got != "" {
			t.Fatalf("%s probed: %q", bin, got)
		}
	}
	if ran("wall") || ran("admin") || ran("unsafe") {
		t.Fatal("unsafe command executed")
	}
	// -h runs only when --help advertises it.
	silent := executable(t, dir, "silent", `[ "$1" = -h ] && touch `+marks+`/h; exit 1`)
	if got := captureHelp(ctx, silent, time.Second); got != "" || ran("h") {
		t.Fatalf("unadvertised -h probed: %q ran=%v", got, ran("h"))
	}
	// Probes run in a throwaway directory, HOME and XDG dirs with empty stdin,
	// and never pass a bare word.
	home, tmp := t.TempDir(), t.TempDir()
	t.Setenv("TMPDIR", tmp) // private, so other packages' concurrent probes don't show up below
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	cwd, prev := t.TempDir(), mustGetwd(t)
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	writer := executable(t, dir, "writer", `touch stray "$HOME/.dotfile" "$TMPDIR/tmpfile"; mkdir -p "$XDG_CONFIG_HOME"; touch "$XDG_CONFIG_HOME/conf"; [ "$1" = help ] && touch `+marks+`/help; read line && touch `+marks+`/stdin; exit 1`)
	captureHelp(ctx, writer, time.Second)
	for _, d := range []string{cwd, home} {
		if entries, _ := os.ReadDir(d); len(entries) != 0 {
			t.Fatalf("probe wrote to %s: %v", d, entries)
		}
	}
	if leftovers, _ := filepath.Glob(filepath.Join(tmp, "shelp-probe-*")); len(leftovers) != 0 {
		t.Fatalf("sandbox not removed: %v", leftovers)
	}
	if ran("help") || ran("stdin") {
		t.Fatalf("help=%v stdin=%v", ran("help"), ran("stdin"))
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
	// Version 2 stems: "keywords" matches "keyword".
	if err = db.QueryRow(`SELECT count(*) FROM commands_fts WHERE commands_fts MATCH 'keywords'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("stemming: %d %v", count, err)
	}
	var version int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("version: %d %v", version, err)
	}
}

func TestMultiWordQueryPrefersNamedCommand(t *testing.T) {
	t.Setenv("MANPATH", t.TempDir())
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "index.db")
	// Mirrors the real failure: shelp's help quotes the query verbatim and
	// short help texts mention "large files"/"large objects".
	executable(t, dir, "shelp", `echo 'Usage: shelp "find large files"  # semantic search'`)
	executable(t, dir, "find", `echo 'Usage: find walk a file hierarchy and evaluate an expression for each file'`)
	executable(t, dir, "findrule", `echo 'Usage: findrule command line wrapper to File::Find::Rule'`)
	executable(t, dir, "git-lfs", `echo 'Usage: git lfs managing and versioning large files'`)
	executable(t, dir, "vacuumlo", `echo 'Usage: vacuumlo removes unreferenced large objects'`)
	executable(t, dir, "sdiff", `echo 'Usage: sdiff side-by-side merge of file differences'`)
	if _, err := Build(ctx, Options{DBPath: dbPath, PATH: dir, Workers: 2, Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	results, err := Search(ctx, dbPath, "find large files", 10)
	if err != nil || len(results) == 0 || results[0].Name != "find" {
		t.Fatalf("find large files: %+v %v", results, err)
	}
	for _, r := range results {
		if r.Name == "shelp" {
			t.Fatal("shelp indexed itself")
		}
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

func TestXcodeShimsWithoutToolAreNotRun(t *testing.T) {
	dir, marks := t.TempDir(), t.TempDir()
	executable(t, dir, "man", "exit 1")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	shims := t.TempDir()
	prevDirs, prevTool := shimDirs, developerTool
	t.Cleanup(func() { shimDirs, developerTool = prevDirs, prevTool })
	shimDirs = []string{shims}
	developerTool = func(name string) bool { return name == "present" }
	// Real shims are Mach-O files linking libxcselect.dylib; the marker string
	// is what detection looks for.
	shim := func(name string) string {
		return executable(t, shims, name, "# links /usr/lib/libxcselect.dylib\ntouch "+marks+"/"+name+"; echo 'Usage: "+name+" [options]'")
	}
	if got := captureHelp(context.Background(), shim("pagestuff"), time.Second); got != "" {
		t.Fatalf("shim with missing tool probed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(marks, "pagestuff")); err == nil {
		t.Fatal("shim with missing tool executed")
	}
	if got := captureHelp(context.Background(), shim("present"), time.Second); got != "Usage: present [options]" {
		t.Fatalf("shim with installed tool: %q", got)
	}
	// Ordinary executables in the shim directory are unaffected.
	plain := executable(t, shims, "plain", "echo 'Usage: plain [options]'")
	if got := captureHelp(context.Background(), plain, time.Second); got != "Usage: plain [options]" {
		t.Fatalf("plain: %q", got)
	}
}
