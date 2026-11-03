package index

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// manPage renders text the way `man -P cat` prints it after clean().
func manPage(title, names, summary, body string) string {
	return title + "                     General Commands Manual                    " + title + "\n\n" +
		"NAME\n     " + names + " – " + summary + "\n\nSYNOPSIS\n     " + strings.Fields(names)[0] + " [options]\n\n" +
		"DESCRIPTION\n     " + body + "\n\nmacOS 27.2                     June 1, 2026                     macOS 27.2"
}

func TestHelpSummaryAndBody(t *testing.T) {
	grep := manPage("GREP(1)", "grep, egrep, zgrep,\n     bzegrep", "file pattern searcher", "The grep utility searches files.")
	summary, names := helpSummary(grep)
	if summary != "file pattern searcher" || strings.Join(names, ",") != "grep,egrep,zgrep,bzegrep" {
		t.Fatalf("man summary: %q %q", summary, names)
	}
	body := searchBody(grep)
	for _, boilerplate := range []string{"General Commands Manual", "NAME", "SYNOPSIS", "macOS 27.2"} {
		if strings.Contains(body, boilerplate) {
			t.Fatalf("body kept %q: %q", boilerplate, body)
		}
	}
	if !strings.Contains(body, "file pattern searcher") || !strings.Contains(body, "searches files") {
		t.Fatalf("body lost content: %q", body)
	}
	for help, want := range map[string]string{
		"Usage: uv [OPTIONS] <COMMAND>\n\nAn extremely fast Python package manager.\n\nCommands:\n  run": "An extremely fast Python package manager.",
		"usage: tool [-a] [-b]\n            [-c file]\n\n  -a  all\nDescribe the tool in words":          "Describe the tool in words",
		"tmux: unknown option -- -\nusage: tmux [-2CDlNuVv]":                                             "",
		"pico(1) General Commands Manual pico(1)\n\nName\n     pico - simple text editor\n":              "simple text editor",
		"": "",
	} {
		if got, _ := helpSummary(help); got != want {
			t.Errorf("helpSummary(%q) = %q, want %q", help, got, want)
		}
	}
	if got := searchBody("Usage: wc [-l] file\nword count"); strings.Contains(strings.ToLower(got), "usage") {
		t.Fatalf("usage label kept: %q", got)
	}
}

func TestAliasGroups(t *testing.T) {
	grep := manPage("GREP(1)", "grep, egrep, bzegrep", "file pattern searcher", "x")
	ln := manPage("LN(1)", "link, ln", "make links", "x")
	groups := aliasGroups([]command{
		{Name: "bzegrep", Help: grep}, {Name: "egrep", Help: grep}, {Name: "grep", Help: grep},
		{Name: "link", Help: ln}, {Name: "ln", Help: ln},
		{Name: "aa", Help: "same"}, {Name: "a", Help: "same"},
		{Name: "pg_dump", Help: "v17"}, {Name: "pg_dump-18", Help: "v18"}, {Name: "json_pp5.34", Help: "j"}, {Name: "json_pp", Help: "k"},
		{Name: "x264", Help: "enc"}, {Name: "solo", Help: "unique"}, {Name: "nohelp"}, {Name: "nohelp2"},
	})
	for name, want := range map[string]string{
		"bzegrep": "grep", "egrep": "grep", "grep": "grep", // page title wins
		"link": "ln", "ln": "ln", // title LN(1) beats NAME order "link, ln"
		"aa": "a", "a": "a", // no man metadata: shortest
		"pg_dump-18": "pg_dump", "json_pp5.34": "json_pp", // versioned installs join the base
		"x264": "x264", "solo": "solo", "nohelp": "nohelp", "nohelp2": "nohelp2",
	} {
		if groups[name] != want {
			t.Errorf("group[%s] = %q, want %q", name, groups[name], want)
		}
	}
}

// TestRelevanceFixtures locks in ranking behaviour with realistic man pages,
// independent of the machine's installed tools.
func TestRelevanceFixtures(t *testing.T) {
	t.Setenv("MANPATH", t.TempDir())
	dir := t.TempDir()
	pages := map[string]string{
		"grep":     manPage("GREP(1)", "grep, egrep, zgrep, bzegrep", "file pattern searcher", "The grep utility searches any given input files, selecting lines that match one or more patterns."),
		"du":       manPage("DU(1)", "du", "display disk usage statistics", "The du utility displays the file system block usage for each file argument."),
		"df":       manPage("DF(1)", "df", "display free disk space", "The df utility displays statistics about the amount of free disk space on the mounted file system."),
		"pwd":      manPage("PWD(1)", "pwd", "return working directory name", "The pwd utility writes the absolute pathname of the current working directory to the standard output."),
		"wc":       manPage("WC(1)", "wc", "word, line, character, and byte count", "The wc utility displays the number of lines, words, and bytes contained in each input file."),
		"git":      manPage("GIT(1)", "git", "the stupid content tracker", "Git is a fast, scalable, distributed revision control system."),
		"git-lfs":  manPage("GIT-LFS(1)", "git-lfs", "Work with large files in Git repositories", "Git LFS is a system for managing and versioning large files."),
		"textutil": manPage("TEXTUTIL(1)", "textutil", "text utility", "textutil can be used to manipulate text files of various formats, using the mechanisms provided by the Cocoa text system."),
		"perldoc":  manPage("PERLDOC(1)", "perldoc", "Look up Perl documentation in Pod format", "perldoc looks up documentation in .pod format that is embedded in the perl installation tree or in a perl script."),
		// Commands named after everyday words must not win on the name alone.
		"file":  manPage("FILE(1)", "file", "determine file type", "file tests each argument and changes nothing; see the permissions of the file with ls."),
		"chmod": manPage("CHMOD(1)", "chmod", "change file modes or Access Control Lists", "The chmod utility modifies the file mode bits (permissions) of the listed files."),
		"make":  manPage("MAKE(1)", "make", "maintain program dependencies", "make changes into each directory given with -C directory and makes the targets in it."),
		"mkdir": manPage("MKDIR(1)", "mkdir", "make directories", "The mkdir utility creates the directories named as operands."),
	}
	for name, help := range pages {
		path := filepath.Join(dir, name+".txt")
		if err := os.WriteFile(path, []byte(help), 0644); err != nil {
			t.Fatal(err)
		}
		executable(t, dir, name, "cat '"+path+"'")
	}
	for _, alias := range []string{"egrep", "zgrep", "bzegrep"} {
		executable(t, dir, alias, "cat '"+filepath.Join(dir, "grep.txt")+"'")
	}
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "index.db")
	if _, err := Build(ctx, Options{DBPath: dbPath, PATH: dir, Workers: 4, Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	for query, want := range map[string]string{
		"search text in files":    "grep",
		"show disk usage":         "du",
		"free disk space":         "df",
		"print working directory": "pwd",
		"count lines":             "wc",
		"git":                     "git",
		"gi":                      "git",
		"zgrep":                   "zgrep",
		"change file permissions": "chmod",
		"make a directory":        "mkdir",
		"determine file type":     "file",
		"file":                    "file",
	} {
		results, err := Search(ctx, dbPath, query, 5)
		if err != nil || len(results) == 0 || results[0].Name != want {
			names := []string{}
			for _, r := range results {
				names = append(names, r.Name)
			}
			t.Errorf("%q: got %v, want %s first (%v)", query, names, want, err)
		}
	}
	results, err := Search(ctx, dbPath, "file pattern searcher", 10)
	if err != nil {
		t.Fatal(err)
	}
	grepLike := 0
	for _, r := range results {
		switch r.Name {
		case "grep", "egrep", "zgrep", "bzegrep":
			grepLike++
			if r.Name != "grep" {
				t.Errorf("alias shown instead of canonical: %s", r.Name)
			}
		}
	}
	if grepLike != 1 {
		t.Errorf("aliases not collapsed: %d grep entries", grepLike)
	}
}

func TestSchemaV2UpgradesOnSearch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	help := manPage("DU(1)", "du", "display disk usage statistics", "block usage")
	if _, err = db.Exec(`CREATE TABLE commands(name TEXT PRIMARY KEY,path TEXT,help TEXT,help_hash TEXT,updated INTEGER);
 CREATE VIRTUAL TABLE commands_fts USING fts5(name,help,content='commands',content_rowid='rowid',tokenize='porter unicode61');
 CREATE TABLE embeddings(name TEXT PRIMARY KEY REFERENCES commands(name) ON DELETE CASCADE, vector BLOB NOT NULL);
 CREATE TABLE terms(term TEXT PRIMARY KEY,idf REAL NOT NULL,vector BLOB NOT NULL);
 CREATE TABLE nl_cache(key TEXT PRIMARY KEY,response TEXT NOT NULL,expires INTEGER NOT NULL);
 PRAGMA user_version=2;`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO commands VALUES('du','/usr/bin/du',?,'h',1)`, help); err != nil {
		t.Fatal(err)
	}
	db.Close()
	// A read-only search upgrades the old index in place instead of failing.
	results, err := Search(context.Background(), path, "disk usage", 5)
	if err != nil || len(results) != 1 || results[0].Name != "du" {
		t.Fatalf("search after upgrade: %+v %v", results, err)
	}
	// Its terms and embeddings predate the current tokenizer until reindexed.
	if outdated, err := Outdated(context.Background(), path); err != nil || !outdated {
		t.Fatalf("migrated index must report outdated model: %v %v", outdated, err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	var summary, grp string
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("version %d %v", version, err)
	}
	if err = db.QueryRow(`SELECT summary,grp FROM commands WHERE name='du'`).Scan(&summary, &grp); err != nil || summary != "display disk usage statistics" || grp != "du" {
		t.Fatalf("derived columns: %q %q %v", summary, grp, err)
	}
}

func TestModelVersionForcesRetrain(t *testing.T) {
	t.Setenv("MANPATH", t.TempDir())
	ctx := context.Background()
	dir := t.TempDir()
	executable(t, dir, "tool", "echo 'Usage: tool does things'")
	dbPath := filepath.Join(t.TempDir(), "index.db")
	opts := Options{DBPath: dbPath, PATH: dir, Workers: 1, Timeout: time.Second}
	if _, err := Build(ctx, opts); err != nil {
		t.Fatal(err)
	}
	db, err := open(ctx, dbPath, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`UPDATE meta SET value='old'; DELETE FROM terms`); err != nil {
		t.Fatal(err)
	}
	report, err := BuildReport(ctx, opts)
	if err != nil || report.Changed != 0 {
		t.Fatalf("retrain must not count unchanged help as changed: %+v %v", report, err)
	}
	var model string
	var terms int
	if err = db.QueryRow(`SELECT value FROM meta WHERE key='model'`).Scan(&model); err != nil || model != modelVersion {
		t.Fatalf("model %q %v", model, err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM terms`).Scan(&terms); err != nil || terms == 0 {
		t.Fatalf("not retrained: %d %v", terms, err)
	}
	if outdated, err := Outdated(ctx, dbPath); err != nil || outdated {
		t.Fatalf("retrained index reported outdated: %v %v", outdated, err)
	}
}

func TestManPagesSkipsMissingLookups(t *testing.T) {
	man := t.TempDir()
	if err := os.MkdirAll(filepath.Join(man, "man1"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"documented.1.gz", "CA.pl.1ssl", "other.8"} {
		if err := os.WriteFile(filepath.Join(man, "man1", f), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("MANPATH", man)
	pages := manPages(context.Background())
	if pages == nil {
		t.Skip("manpath unavailable")
	}
	for _, name := range []string{"documented", "CA.pl", "other"} {
		if !pages[name] {
			t.Errorf("missing %s in %v", name, pages)
		}
	}
	// With a known page list, man is not spawned for commands without a page.
	dir, marks := t.TempDir(), t.TempDir()
	executable(t, dir, "man", "touch "+marks+"/man; exit 1")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	tool := executable(t, dir, "undocumented", "echo 'Usage: undocumented'")
	if got := captureHelpIn(context.Background(), "", pages, tool, time.Second); got != "Usage: undocumented" {
		t.Fatalf("help: %q", got)
	}
	if _, err := os.Stat(filepath.Join(marks, "man")); err == nil {
		t.Fatal("man spawned for a command without a page")
	}
	// man still gets the last word when the probe finds nothing: it resolves
	// pages the directory scan misses, and sbin tools are never probed.
	executable(t, dir, "man", `[ "$4" = sbintool ] && echo 'SBINTOOL(8) manual'`)
	sbin := filepath.Join(t.TempDir(), "sbin")
	if err := os.Mkdir(sbin, 0755); err != nil {
		t.Fatal(err)
	}
	if got := captureHelpIn(context.Background(), "", pages, executable(t, sbin, "sbintool", "exit 1"), time.Second); got != "SBINTOOL(8) manual" {
		t.Fatalf("man fallback after probe: %q", got)
	}
	// A MANPATH listing no pages is treated as unknown, not as "no pages".
	t.Setenv("MANPATH", t.TempDir())
	if got := manPages(context.Background()); got != nil {
		t.Fatalf("empty man path must return nil: %v", got)
	}
}
