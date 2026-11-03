package index

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/chr33s/shelp/internal/embedding"
	"github.com/chr33s/shelp/internal/process"
)

type Options struct {
	DBPath   string
	PATH     string
	Workers  int
	Timeout  time.Duration
	Full     bool
	Progress func(Progress)
}
type command struct{ Name, Path, Help, Hash string }

// Run retains the original entry point with conservative defaults.
func Run(ctx context.Context) error {
	path, err := DefaultPath()
	if err != nil {
		return err
	}
	n, err := Build(ctx, Options{DBPath: path, PATH: os.Getenv("PATH"), Workers: 8, Timeout: 2 * time.Second})
	if err == nil {
		fmt.Printf("Indexed %d commands\n", n)
	}
	return err
}

// Build captures help concurrently and publishes one complete snapshot atomically.
// Commands removed from PATH are removed only after a successful scan.
func Build(ctx context.Context, opts Options) (int, error) {
	report, err := BuildReport(ctx, opts)
	return report.Total, err
}

// BuildReport returns changes relative to the snapshot replaced at publication.
func BuildReport(ctx context.Context, opts Options) (Report, error) {
	progress := func(stage string, done, total int) {
		if opts.Progress != nil {
			opts.Progress(Progress{Stage: stage, Done: done, Total: total})
		}
	}
	if opts.Workers < 1 || opts.Workers > 128 {
		return Report{}, fmt.Errorf("workers must be between 1 and 128")
	}
	if opts.Timeout <= 0 {
		return Report{}, fmt.Errorf("help timeout must be positive")
	}
	progress("scan", 0, 0)
	commands, err := discover(ctx, opts.PATH, opts.Workers)
	if err != nil {
		return Report{}, err
	}
	db, err := open(ctx, opts.DBPath, true)
	if err != nil {
		return Report{}, err
	}
	defer db.Close()
	cache, err := loadRecords(ctx, db)
	if err != nil {
		return Report{}, err
	}
	// One root for every probe sandbox, removed when indexing ends, so files
	// written late by detached children (telemetry, caches) are also cleaned.
	probeRoot, err := os.MkdirTemp("", "shelp-index-")
	if err != nil {
		return Report{}, err
	}
	defer os.RemoveAll(probeRoot)
	// Spawning man for a missing page costs ~100 ms; on most machines a few
	// hundred commands have none, so look pages up once from the man path.
	pages := manPages(ctx)
	progress("capture", 0, len(commands))
	var progressMu sync.Mutex
	completed := 0

	jobs := make(chan int, len(commands))
	for i := range commands {
		jobs <- i
	}
	close(jobs)
	var wg sync.WaitGroup
	for w := 0; w < opts.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				c := &commands[i]
				c.Help = captureHelpIn(ctx, probeRoot, pages, c.Path, opts.Timeout)
				c.Hash = fmt.Sprintf("%x", sha256.Sum256([]byte(c.Name+"\n"+c.Help)))
				progressMu.Lock()
				completed++
				progress("capture", completed, len(commands))
				progressMu.Unlock()
			}
		}()
	}
	wg.Wait()
	if err = ctx.Err(); err != nil {
		return Report{}, err
	}
	var storedModel string
	if err = db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='model'`).Scan(&storedModel); err != nil && err != sql.ErrNoRows {
		return Report{}, err
	}
	// A new tokenizer or document layout needs retraining even if no help changed.
	changed := opts.Full || len(commands) != len(cache) || storedModel != modelVersion
	for _, c := range commands {
		old, ok := cache[c.Name]
		if !ok || old.hash != c.Hash || old.path != c.Path {
			changed = true
		}
	}
	if !changed {
		return summarize(commands, cache), nil
	}
	progress("train", 0, len(commands))
	texts := make([]string, len(commands))
	summaries := make([]string, len(commands))
	bodies := make([]string, len(commands))
	for i, c := range commands {
		summaries[i], _ = helpSummary(c.Help)
		bodies[i] = searchBody(c.Help)
		// Repeating the summary weights what a command does over its option list.
		texts[i] = c.Name + "\n" + strings.Repeat(summaries[i]+"\n", 3) + bodies[i]
	}
	groups := aliasGroups(commands)
	model, vectors, err := embedding.Train(ctx, texts)
	if err != nil {
		return Report{}, err
	}

	progress("save", 0, len(commands))
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Report{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `CREATE TEMP TABLE active_names(name TEXT PRIMARY KEY); DELETE FROM embeddings; DELETE FROM terms; DELETE FROM nl_cache;`); err != nil {
		return Report{}, err
	}
	// The earlier cache can be stale after concurrent help capture/training.
	cache, err = loadRecords(ctx, tx)
	if err != nil {
		return Report{}, err
	}
	now := time.Now().Unix()
	for i, c := range commands {
		if _, err = tx.ExecContext(ctx, `INSERT INTO active_names(name) VALUES(?)`, c.Name); err != nil {
			return Report{}, err
		}
		// Compare against the current row under the publication write lock, not
		// the cache read before capture: another build may have changed/deleted it.
		if _, err = tx.ExecContext(ctx, `INSERT INTO commands(name,path,help,help_hash,updated,summary,body,grp) VALUES(?,?,?,?,?,?,?,?)
    ON CONFLICT(name) DO UPDATE SET path=excluded.path,help=excluded.help,help_hash=excluded.help_hash,updated=excluded.updated,summary=excluded.summary,body=excluded.body
    WHERE ? OR commands.help_hash IS NOT excluded.help_hash OR commands.path IS NOT excluded.path OR commands.summary IS NOT excluded.summary OR commands.body IS NOT excluded.body`, c.Name, c.Path, c.Help, c.Hash, now, summaries[i], bodies[i], groups[c.Name], opts.Full); err != nil {
			return Report{}, err
		}
		// Alias groups depend on every command, so refresh them even for unchanged rows.
		if _, err = tx.ExecContext(ctx, `UPDATE commands SET grp=? WHERE name=? AND grp IS NOT ?`, groups[c.Name], c.Name, groups[c.Name]); err != nil {
			return Report{}, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO embeddings(name,vector) VALUES(?,?)`, c.Name, embedding.Encode(vectors[i])); err != nil {
			return Report{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM commands WHERE name NOT IN (SELECT name FROM active_names); DROP TABLE active_names;`); err != nil {
		return Report{}, err
	}
	for word, term := range model {
		if _, err = tx.ExecContext(ctx, `INSERT INTO terms(term,idf,vector) VALUES(?,?,?)`, word, term.IDF, embedding.Encode(term.Vector)); err != nil {
			return Report{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('model',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, modelVersion); err != nil {
		return Report{}, err
	}

	if err = tx.Commit(); err != nil {
		return Report{}, err
	}
	return summarize(commands, cache), nil
}

func discover(ctx context.Context, pathEnv string, workers int) ([]command, error) {
	if pathEnv == "" {
		return nil, fmt.Errorf("PATH is empty; refusing to replace the index")
	}
	dirs := filepath.SplitList(pathEnv)
	self := selfPath()
	found := make([][]command, len(dirs))
	errs := make([]error, len(dirs))
	jobs := make(chan int, len(dirs))
	for i := range dirs {
		jobs <- i
	}
	close(jobs)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				dir := dirs[i]
				if dir == "" {
					dir = "."
				}
				entries, err := os.ReadDir(dir)
				if os.IsNotExist(err) {
					continue
				}
				if err != nil {
					errs[i] = fmt.Errorf("scan PATH directory %s: %w", dir, err)
					continue
				}
				for _, e := range entries {
					if e.IsDir() || strings.HasPrefix(e.Name(), ".") || strings.IndexFunc(e.Name(), unicode.IsControl) >= 0 {
						continue
					}
					switch filepath.Ext(e.Name()) {
					case ".dylib", ".so", ".dll", ".a", ".o", ".plist":
						continue
					}
					bin, err := filepath.Abs(filepath.Join(dir, e.Name()))
					if err != nil {
						errs[i] = err
						break
					}
					info, err := os.Stat(bin)
					if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
						continue
					}
					// shelp's own help quotes example queries and would outrank real tools.
					if e.Name() == "shelp" || (self != "" && resolved(bin) == self) {
						continue
					}
					found[i] = append(found[i], command{Name: e.Name(), Path: bin})
				}
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var result []command
	for i, list := range found {
		if errs[i] != nil {
			return nil, errs[i]
		}
		for _, c := range list {
			if !seen[c.Name] {
				seen[c.Name] = true
				result = append(result, c)
			}
		}
	}
	return result, nil
}

// manPages lists page names under every man*/ directory of `manpath`, which
// honours MANPATH and PATH-relative man directories as man does. It returns
// nil when the path is unavailable, and callers then ask man for every command.
func manPages(ctx context.Context) map[string]bool {
	mctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, _, err := process.Run(mctx, 64<<10, "manpath")
	if err != nil {
		return nil
	}
	pages := map[string]bool{}
	for _, dir := range filepath.SplitList(strings.TrimSpace(out)) {
		sections, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, section := range sections {
			if !strings.HasPrefix(section.Name(), "man") {
				continue
			}
			entries, err := os.ReadDir(filepath.Join(dir, section.Name()))
			if err != nil {
				continue
			}
			for _, e := range entries {
				name := e.Name()
				for _, ext := range []string{".gz", ".bz2", ".xz", ".Z", ".zst"} {
					name = strings.TrimSuffix(name, ext)
				}
				// "grep.1" and "CA.pl.1ssl" name grep and CA.pl.
				if i := strings.LastIndexByte(name, '.'); i > 0 {
					pages[name[:i]] = true
				}
			}
		}
	}
	return pages
}

// selfPath is the resolved path of the running executable, or "" if unknown.
func selfPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return resolved(exe)
}

func resolved(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return path
}

var terminalEscape = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)

func clean(text string) string {
	var runes []rune
	for _, r := range text {
		if r == '\b' {
			if len(runes) > 0 {
				runes = runes[:len(runes)-1]
			}
		} else {
			runes = append(runes, r)
		}
	}
	text = string(runes)
	text = terminalEscape.ReplaceAllString(strings.ToValidUTF8(text, ""), "")
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, text))
}

// unsafeProbe lists commands that act on unknown arguments or empty stdin
// (broadcast, mail, power, firmware, audio, GUI, daemons, file-moving dev
// scripts) rather than printing help. They are indexed from their man page
// only and never executed.
var unsafeProbe = map[string]bool{
	"shutdown": true, "reboot": true, "halt": true, "poweroff": true, "fasthalt": true, "fastboot": true,
	"wall": true, "write": true, "talk": true, "mesg": true, "biff": true,
	"mail": true, "mailx": true, "Mail": true, "sendmail": true, "mailq": true, "newaliases": true,
	"perlbug": true, "perlthanks": true,
	"say": true, "afplay": true, "open": true, "osascript": true, "osacompile": true,
	"pmset": true, "caffeinate": true, "screencapture": true, "screenshot2": true, "logout": true, "login": true,
	"launchctl": true, "kill": true, "killall": true, "pkill": true, "script": true,
	"lp": true, "lpr": true, "logger": true, "tmutil": true, "diskutil": true, "nvram": true,
	"softwareupdate": true, "systemsetup": true, "networksetup": true, "systemctl": true,
	"init": true, "telinit": true, "loginctl": true,
	"usbcfwflasher": true, "IOMFB_FDR_Loader": true, "devmodectl": true, "rvictl": true,
	"keychain-access": true, "javaws": true, "jconsole": true, "jcontrol": true, "policytool": true,
	"rmid": true, "rmiregistry": true, "orbd": true, "tnameserv": true, "servertool": true, "jstatd": true,
	"code-tunnel": true, "zed": true, "ksdiff": true, "trasher": true, "patcheck": true,
	"murge": true, "codiff": true, "bisect-create": true, "splash": true,
}

// unsafeName matches name patterns for installers, updaters, firmware tools,
// diagnostics collectors, servers and shell/DTrace scripts, which commonly
// ignore --help and act immediately.
var unsafeName = regexp.MustCompile(`(?i)(^(un)?install|^update[-_]|diagnose$|flash|-?server$|\.sh$|\.d$)`)

// probeAllowed reports whether bin may be executed to capture help.
// Admin directories (sbin), denylisted names and patterns, and DTrace scripts
// (where -h writes <name>.h) are never run; symlink targets are checked too.
func probeAllowed(bin string) bool {
	paths := []string{bin}
	if target, err := filepath.EvalSymlinks(bin); err == nil && target != bin {
		paths = append(paths, target)
	}
	for _, p := range paths {
		name := filepath.Base(p)
		unversioned := strings.TrimRight(name, "0123456789.") // perlbug5.34 -> perlbug
		if filepath.Base(filepath.Dir(p)) == "sbin" || unsafeProbe[name] || unsafeProbe[unversioned] || unsafeName.MatchString(name) || dtraceScript(p) {
			return false
		}
		if xcodeShim(p) && !developerTool(name) {
			return false
		}
	}
	return true
}

// shimDirs holds macOS developer-tool shims: /usr/bin/git, make, pagestuff...
var shimDirs = []string{"/usr/bin"}

// xcodeShim reports an Apple developer-tool shim. Shims link libxcselect and
// forward to the tool in the active Xcode or Command Line Tools; when that
// tool is missing, running the shim opens the "install developer tools"
// dialog, which cannot help for tools no package ships (pagestuff).
func xcodeShim(path string) bool {
	if !slices.Contains(shimDirs, filepath.Dir(path)) {
		return false
	}
	switch filepath.Base(path) {
	case "xcrun", "xcode-select": // the selector tools themselves
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20))
	return err == nil && bytes.Contains(data, []byte("libxcselect.dylib"))
}

var (
	developerDirOnce sync.Once
	developerDirOK   bool
)

// developerTool reports whether a shim's real tool exists, without prompting:
// with no developer directory selected, even xcrun opens the install dialog,
// so it is only asked once `xcode-select -p` succeeds.
var developerTool = func(name string) bool {
	developerDirOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, _, err := process.Run(ctx, 4<<10, "xcode-select", "-p")
		if err == nil {
			info, statErr := os.Stat(strings.TrimSpace(out))
			developerDirOK = statErr == nil && info.IsDir()
		}
	})
	if !developerDirOK {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := process.Run(ctx, 4<<10, "xcrun", "--find", name)
	return err == nil
}

func dtraceScript(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 128)
	n, _ := f.Read(head)
	line, _, _ := strings.Cut(string(head[:n]), "\n")
	return strings.HasPrefix(line, "#!") && strings.Contains(line, "dtrace")
}

// advertisesH matches a standalone -h flag, as in "[-h]" or "use -h for help".
var advertisesH = regexp.MustCompile(`(^|[\s\[|,('"])-h($|[\s\]|,)'":=])`)

func helpLike(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "usage:") || strings.Contains(lower, "synopsis") || strings.Contains(lower, "options:") || strings.Contains(lower, "commands:")
}

func bound(text string) string {
	if len(text) > 12<<10 {
		text = strings.ToValidUTF8(text[:12<<10], "")
	}
	return text
}

// captureHelp prefers the man page and only executes bin when none exists.
// Probes run in a throwaway directory (also HOME, TMPDIR, XDG dirs) with
// /dev/null stdin so stray files never reach the caller's cwd or home.
func captureHelp(parent context.Context, bin string, timeout time.Duration) string {
	return captureHelpIn(parent, "", nil, bin, timeout)
}

// captureHelpIn creates probe sandboxes under root ("" means the system temp
// dir). pages lists names with a man page; nil means unknown, so man is asked.
func captureHelpIn(parent context.Context, root string, pages map[string]bool, bin string, timeout time.Duration) string {
	if parent.Err() != nil {
		return ""
	}
	if name := filepath.Base(bin); pages == nil || pages[name] {
		manCtx, manCancel := context.WithTimeout(parent, timeout)
		out, _, err := process.Run(manCtx, 12<<10, "man", "-P", "cat", "--", name)
		manCancel()
		if text := bound(clean(out)); err == nil && text != "" {
			return text
		}
	}
	if parent.Err() != nil || !probeAllowed(bin) {
		return ""
	}
	sandbox, err := os.MkdirTemp(root, "shelp-probe-")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(sandbox)
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	// Only --help is probed blindly. -h is run only when --help output
	// advertises it: elsewhere it can mean halt (shutdown), header generation
	// (dtrace scripts write <name>.h) or human-readable output. A bare
	// positional word is never passed (mail help, script help).
	out, stderr, err := process.RunIn(ctx, sandbox, 12<<10, bin, "--help")
	text := bound(clean(out + "\n" + stderr))
	if text != "" && err == nil {
		return text
	}
	var fallback string
	if helpLike(text) {
		fallback = text
	}
	if ctx.Err() == nil && advertisesH.MatchString(text) {
		out, stderr, err = process.RunIn(ctx, sandbox, 12<<10, bin, "-h")
		if hText := bound(clean(out + "\n" + stderr)); helpLike(hText) {
			// A short error/usage message is a fallback when nothing better exists.
			if err == nil {
				return hText
			}
			fallback = hText
		}
	}
	return fallback
}
