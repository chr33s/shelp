package index

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

func DefaultPath() (string, error) {
	if p := os.Getenv("SHELP_DB"); p != "" {
		return p, nil
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "shelp", "index.db"), nil
}

func open(ctx context.Context, path string, write bool) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	mode := "ro"
	if write {
		mode = "rwc"
		if err = os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
			return nil, err
		}
	} else if _, err = os.Stat(absolute); err != nil {
		if os.IsNotExist(err) {
			hint := "shelp index"
			defaultPath, defaultErr := DefaultPath()
			defaultAbsolute, absErr := filepath.Abs(defaultPath)
			if defaultErr != nil || absErr != nil || absolute != defaultAbsolute {
				hint += " --db '" + strings.ReplaceAll(absolute, "'", "'\\''") + "'"
			}
			return nil, fmt.Errorf("no index found at %s\nRun: %s", absolute, hint)
		}
		return nil, fmt.Errorf("open index: %w", err)
	}
	uri := url.URL{Scheme: "file", Path: absolute}
	q := uri.Query()
	q.Set("mode", mode)
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	uri.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = db.PingContext(ctx); err == nil && write {
		err = migrate(ctx, db)
	} else if err == nil {
		// Searches against an index from an older shelp upgrade it in place
		// (fast: no help capture or training) instead of failing on new columns.
		var outdated bool
		if outdated, err = needsMigration(ctx, db); err == nil && outdated {
			db.Close()
			var upgraded *sql.DB
			if upgraded, err = open(ctx, path, true); err != nil {
				return nil, fmt.Errorf("upgrade index %s: %w\nRun: shelp index", absolute, err)
			}
			upgraded.Close()
			return open(ctx, path, false)
		}
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// schemaVersion history: 1 initial; 2 porter-stemmed FTS; 3 summary/body
// columns weighted separately in FTS, alias groups and the meta table.
const schemaVersion = 3

// modelVersion changes whenever tokenization or document text for embeddings
// changes; a build retrains when the stored value differs.
const modelVersion = "3"

// migrate brings any older schema to schemaVersion. Every statement is
// idempotent, so the same steps upgrade empty, legacy, v1 and v2 indexes.
func migrate(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > schemaVersion {
		return fmt.Errorf("index schema %d is newer than supported version %d", version, schemaVersion)
	}
	if version == schemaVersion {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `
 CREATE TABLE IF NOT EXISTS commands (
 name TEXT PRIMARY KEY, path TEXT, help TEXT, help_hash TEXT, updated INTEGER);
 CREATE TABLE IF NOT EXISTS embeddings (
 name TEXT PRIMARY KEY REFERENCES commands(name) ON DELETE CASCADE,
 vector BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS terms(term TEXT PRIMARY KEY,idf REAL NOT NULL,vector BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS nl_cache(key TEXT PRIMARY KEY,response TEXT NOT NULL,expires INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY,value TEXT NOT NULL);`); err != nil {
		return err
	}
	columns := map[string]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT name FROM pragma_table_info('commands')`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	rows.Close()
	for _, column := range []string{"summary", "body", "grp"} {
		if !columns[column] {
			if _, err = tx.ExecContext(ctx, `ALTER TABLE commands ADD COLUMN `+column+` TEXT NOT NULL DEFAULT ''`); err != nil {
				return err
			}
		}
	}
	// Derived columns are computed in Go, so existing rows are filled here.
	var commands []command
	rows, err = tx.QueryContext(ctx, `SELECT name,COALESCE(help,'') FROM commands`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var c command
		if err = rows.Scan(&c.Name, &c.Help); err != nil {
			rows.Close()
			return err
		}
		commands = append(commands, c)
	}
	rows.Close()
	// Drop the old FTS index first so the row updates below do not re-index
	// each row in a table that is rebuilt anyway.
	if _, err = tx.ExecContext(ctx, `
 DROP TRIGGER IF EXISTS commands_ai;
 DROP TRIGGER IF EXISTS commands_ad;
 DROP TRIGGER IF EXISTS commands_au;
 DROP TABLE IF EXISTS commands_fts;`); err != nil {
		return err
	}
	groups := aliasGroups(commands)
	for _, c := range commands {
		summary, _ := helpSummary(c.Help)
		if _, err = tx.ExecContext(ctx, `UPDATE commands SET summary=?,body=?,grp=? WHERE name=?`, summary, searchBody(c.Help), groups[c.Name], c.Name); err != nil {
			return err
		}
	}
	// FTS triggers only fire when indexed columns change; grp updates skip them.
	if _, err = tx.ExecContext(ctx, `
 CREATE VIRTUAL TABLE commands_fts USING fts5(name,summary,body,content='commands',content_rowid='rowid',tokenize='porter unicode61');
 CREATE TRIGGER commands_ai AFTER INSERT ON commands BEGIN
 INSERT INTO commands_fts(rowid,name,summary,body) VALUES(new.rowid,new.name,new.summary,new.body); END;
 CREATE TRIGGER commands_ad AFTER DELETE ON commands BEGIN
 INSERT INTO commands_fts(commands_fts,rowid,name,summary,body) VALUES('delete',old.rowid,old.name,old.summary,old.body); END;
 CREATE TRIGGER commands_au AFTER UPDATE OF name,summary,body ON commands BEGIN
 INSERT INTO commands_fts(commands_fts,rowid,name,summary,body) VALUES('delete',old.rowid,old.name,old.summary,old.body);
 INSERT INTO commands_fts(rowid,name,summary,body) VALUES(new.rowid,new.name,new.summary,new.body); END;
 INSERT INTO commands_fts(commands_fts) VALUES('rebuild');
 PRAGMA user_version=3;`); err != nil {
		return err
	}
	return tx.Commit()
}

// Outdated reports whether the index was built by a shelp with a different
// tokenizer or document layout. Search still works (the schema is upgraded on
// open), but semantic matches are weaker until `shelp index` retrains.
func Outdated(ctx context.Context, path string) (bool, error) {
	db, err := open(ctx, path, false)
	if err != nil {
		return false, err
	}
	defer db.Close()
	var stored string
	err = db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='model'`).Scan(&stored)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	return stored != modelVersion, nil
}

// needsMigration reports whether a read-only open must upgrade the schema first.
func needsMigration(ctx context.Context, db *sql.DB) (bool, error) {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return false, err
	}
	return version < schemaVersion, nil
}
