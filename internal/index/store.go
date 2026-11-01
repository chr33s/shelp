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
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return fmt.Errorf("index schema %d is newer than supported version 1", version)
	}
	if version == 1 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
 CREATE TABLE IF NOT EXISTS commands (
 name TEXT PRIMARY KEY, path TEXT, help TEXT, help_hash TEXT, updated INTEGER);
 CREATE VIRTUAL TABLE IF NOT EXISTS commands_fts USING fts5(name,help,content='commands',content_rowid='rowid');
 CREATE TABLE IF NOT EXISTS embeddings (
 name TEXT PRIMARY KEY REFERENCES commands(name) ON DELETE CASCADE,
 vector BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS terms(term TEXT PRIMARY KEY,idf REAL NOT NULL,vector BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS nl_cache(key TEXT PRIMARY KEY,response TEXT NOT NULL,expires INTEGER NOT NULL);
 CREATE TRIGGER IF NOT EXISTS commands_ai AFTER INSERT ON commands BEGIN
 INSERT INTO commands_fts(rowid,name,help) VALUES(new.rowid,new.name,new.help); END;
 CREATE TRIGGER IF NOT EXISTS commands_ad AFTER DELETE ON commands BEGIN
 INSERT INTO commands_fts(commands_fts,rowid,name,help) VALUES('delete',old.rowid,old.name,old.help); END;
 CREATE TRIGGER IF NOT EXISTS commands_au AFTER UPDATE ON commands BEGIN
 INSERT INTO commands_fts(commands_fts,rowid,name,help) VALUES('delete',old.rowid,old.name,old.help);
 INSERT INTO commands_fts(rowid,name,help) VALUES(new.rowid,new.name,new.help); END;
 INSERT INTO commands_fts(commands_fts) VALUES('rebuild');
 PRAGMA user_version=1;
 `)
	if err != nil {
		return err
	}
	return tx.Commit()
}
