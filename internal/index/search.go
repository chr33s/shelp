package index

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/chr33s/shelp/internal/embedding"
)

type Result struct {
	Name  string  `json:"name"`
	Path  string  `json:"path"`
	Help  string  `json:"help"`
	Score float64 `json:"score"`
}

func Search(ctx context.Context, path, query string, limit int) ([]Result, error) {
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("limit must be between 1 and 1000")
	}
	query = strings.TrimSpace(query)
	if len(query) > 4096 {
		return nil, fmt.Errorf("query exceeds 4096 bytes")
	}
	db, err := open(ctx, path, false)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	keyword := map[string]float64{}
	terms := strings.FieldsFunc(query, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	if len(terms) > 0 {
		for i, t := range terms {
			terms[i] = `"` + t + `"`
		}
		rows, err := tx.QueryContext(ctx, `SELECT name FROM commands_fts WHERE commands_fts MATCH ? ORDER BY bm25(commands_fts,5,1),name`, strings.Join(terms, " OR "))
		if err != nil {
			return nil, err
		}
		rank := 0
		for rows.Next() {
			var name string
			if err = rows.Scan(&name); err != nil {
				rows.Close()
				return nil, err
			}
			keyword[name] = 30 / (1 + float64(rank)*0.1)
			rank++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	model := embedding.Model{}
	for word := range embedding.Tokens(query) {
		var t embedding.Term
		var raw []byte
		err = tx.QueryRowContext(ctx, `SELECT idf,vector FROM terms WHERE term=?`, word).Scan(&t.IDF, &raw)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		t.Vector, err = embedding.Decode(raw)
		if err != nil {
			return nil, err
		}
		model[word] = t
	}
	queryVector := model.Embed(query)
	rows, err := tx.QueryContext(ctx, `SELECT c.name,c.path,e.vector FROM commands c LEFT JOIN embeddings e ON c.name=e.name`)

	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Result, 0)
	for rows.Next() {
		var r Result
		var raw []byte
		if err = rows.Scan(&r.Name, &r.Path, &raw); err != nil {
			return nil, err
		}
		r.Score = lexical(r.Name, query) + keyword[r.Name]
		if queryVector != nil && len(raw) > 0 {
			v, err := embedding.Decode(raw)
			if err != nil {
				return nil, fmt.Errorf("corrupt embedding for %s: %w", r.Name, err)
			}
			cosine, err := embedding.Cosine(queryVector, v)
			if err != nil {
				return nil, err
			}
			if cosine > 0 {
				r.Score += 40 * cosine
			}
		}
		if query == "" || r.Score > 0 {
			result = append(result, r)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	sort.Slice(result, func(i, j int) bool {
		if result[i].Score == result[j].Score {
			return result[i].Name < result[j].Name
		}
		return result[i].Score > result[j].Score
	})
	if len(result) > limit {
		result = result[:limit]
	}
	for i := range result {
		if err = tx.QueryRowContext(ctx, `SELECT substr(help,1,1200) FROM commands WHERE name=?`, result[i].Name).Scan(&result[i].Help); err != nil {
			return nil, err
		}
	}
	return result, tx.Commit()
}

func lexical(name, query string) float64 {
	name, query = strings.ToLower(name), strings.ToLower(query)
	switch {
	case query == "":
		return 0
	case name == query:
		return 100
	case strings.HasPrefix(name, query):
		return 80
	case strings.Contains(name, query):
		return 60
	}
	a, b := []rune(name), []rune(query)
	// Subsequence matching tolerates omitted letters; edit distance catches typos.
	j := 0
	for _, r := range a {
		if j < len(b) && r == b[j] {
			j++
		}
	}
	if j == len(b) {
		return 40 * float64(len(b)) / float64(len(a))
	}
	if len(a) > 128 || len(b) > 128 {
		return 0
	}
	maxEdits := 1
	if len(b) >= 5 {
		maxEdits = 2
	}
	if d := distance(a, b); d <= maxEdits {
		return 30 - float64(d)*5
	}
	return 0
}
func distance(a, b []rune) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	var before []int
	for i, x := range a {
		next := make([]int, len(b)+1)
		next[0] = i + 1
		for j, y := range b {
			cost := 0
			if x != y {
				cost = 1
			}
			next[j+1] = min(prev[j+1]+1, next[j]+1, prev[j]+cost)
			if i > 0 && j > 0 && x == b[j-1] && a[i-1] == y {
				next[j+1] = min(next[j+1], before[j-1]+1)
			}
		}
		before, prev = prev, next
	}
	return prev[len(b)]
}
