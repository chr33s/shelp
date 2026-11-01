package index

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
				c.Help = captureHelp(ctx, c.Path, opts.Timeout)
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
	changed := opts.Full || len(commands) != len(cache)
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
	for i, c := range commands {
		texts[i] = c.Name + "\n" + c.Help
	}
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
		if _, err = tx.ExecContext(ctx, `INSERT INTO commands(name,path,help,help_hash,updated) VALUES(?,?,?,?,?)
    ON CONFLICT(name) DO UPDATE SET path=excluded.path,help=excluded.help,help_hash=excluded.help_hash,updated=excluded.updated
    WHERE ? OR commands.help_hash IS NOT excluded.help_hash OR commands.path IS NOT excluded.path`, c.Name, c.Path, c.Help, c.Hash, now, opts.Full); err != nil {
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
func captureHelp(parent context.Context, bin string, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var fallback string
	for _, flag := range []string{"--help", "-h", "help"} {
		if ctx.Err() != nil {
			break
		}
		out, stderr, err := process.Run(ctx, 12<<10, bin, flag)
		text := clean(out + "\n" + stderr)
		if len(text) > 12<<10 {
			text = text[:12<<10]
			text = strings.ToValidUTF8(text, "")
		}
		lower := strings.ToLower(text)
		helpLike := strings.Contains(lower, "usage:") || strings.Contains(lower, "synopsis") || strings.Contains(lower, "options:") || strings.Contains(lower, "commands:")
		if text != "" && err == nil && (flag == "--help" || helpLike) {
			return text
		}
		// A short error/usage message is a fallback, not a substitute for a man page.
		// In particular, -h often means human-readable output rather than help.
		if helpLike {
			fallback = text
		}
	}
	if parent.Err() == nil {
		manCtx, manCancel := context.WithTimeout(parent, timeout)
		defer manCancel()
		out, _, err := process.Run(manCtx, 12<<10, "man", "-P", "cat", "--", filepath.Base(bin))
		if err == nil && clean(out) != "" {
			return clean(out)
		}
	}
	return fallback
}
