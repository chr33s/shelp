package index

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type Status struct {
	Path       string `json:"path"`
	Commands   int    `json:"commands"`
	Embeddings int    `json:"embeddings"`
	Terms      int    `json:"terms"`
	Updated    int64  `json:"updated"`
}

func Inspect(ctx context.Context, path string) (Status, error) {
	s := Status{Path: path}
	db, err := open(ctx, path, false)
	if err != nil {
		return s, err
	}
	defer db.Close()
	err = db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM commands),(SELECT count(*) FROM embeddings),(SELECT count(*) FROM terms),(SELECT coalesce(max(updated),0) FROM commands)`).Scan(&s.Commands, &s.Embeddings, &s.Terms, &s.Updated)
	return s, err
}
func Cached(ctx context.Context, path, key string) ([]string, error) {
	db, err := open(ctx, path, false)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var raw string
	err = db.QueryRowContext(ctx, `SELECT response FROM nl_cache WHERE key=? AND expires>?`, key, time.Now().Unix()).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result []string
	err = json.Unmarshal([]byte(raw), &result)
	return result, err
}
func Cache(ctx context.Context, path, key string, lines []string, ttl time.Duration) error {
	db, err := open(ctx, path, true)
	if err != nil {
		return err
	}
	defer db.Close()
	raw, err := json.Marshal(lines)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO nl_cache(key,response,expires) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET response=excluded.response,expires=excluded.expires`, key, string(raw), time.Now().Add(ttl).Unix())
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM nl_cache WHERE expires<=? OR key NOT IN (SELECT key FROM nl_cache ORDER BY expires DESC,key LIMIT 100)`, time.Now().Unix())
	if err != nil {
		return err
	}
	return tx.Commit()
}
