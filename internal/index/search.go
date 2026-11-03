package index

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/chr33s/shelp/internal/embedding"
)

type Result struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Help string `json:"help"`
	// Summary is the one-line description (man NAME line or first descriptive
	// --help line); empty when the help has none.
	Summary string  `json:"summary,omitempty"`
	Score   float64 `json:"score"`
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
	// Each content word becomes an FTS group with its agent-noun form, so
	// "search" also matches grep's "file pattern searcher". Stop words such as
	// "in" match nearly everything and are left to the name/embedding signals.
	var words, groups []string
	for _, t := range terms {
		if embedding.StopWord(t) || len(t) < 2 {
			continue
		}
		group := `"` + t + `"`
		if len(t) >= 4 && !strings.HasSuffix(strings.ToLower(t), "er") {
			group = `("` + t + `" OR "` + strings.TrimSuffix(t, "s") + `er")`
		}
		words = append(words, t)
		groups = append(groups, group)
	}
	// summaryHits[name] lists the groups matching name's one-line summary.
	summaryHits := map[string][]int{}
	if len(groups) == 1 {
		ranked, err := ftsNames(ctx, tx, groups[0])
		if err != nil {
			return nil, err
		}
		for rank, m := range ranked {
			keyword[m.name] = 30 / (1 + float64(rank)*0.1)
		}
	} else if len(groups) > 1 {
		ranked, err := ftsNames(ctx, tx, strings.Join(groups, " OR "))
		if err != nil {
			return nil, err
		}
		// Documents covering more query words rank first; bm25 orders ties.
		// Otherwise one rare word in a short help text beats all of them.
		covered := map[string]int{}
		for i, g := range groups {
			matches, err := ftsNames(ctx, tx, g)
			if err != nil {
				return nil, err
			}
			for _, m := range matches {
				covered[m.name]++
				if m.inSummary {
					summaryHits[m.name] = append(summaryHits[m.name], i)
				}
			}
		}
		sort.SliceStable(ranked, func(i, j int) bool { return covered[ranked[i].name] > covered[ranked[j].name] })
		for rank, m := range ranked {
			// The one-line summary says what a command does; matching query
			// words there outweighs matches buried in option lists. Single words
			// are usually name lookups ("git"), where it would lift git-lfs over git.
			keyword[m.name] = 30/(1+float64(rank)*0.1) + 30*float64(len(summaryHits[m.name]))/float64(len(groups))
		}
	}
	model := embedding.Model{}
	for token := range embedding.Tokens(query) {
		// Training folds "searcher" into "search"; look up the same variants.
		for _, word := range embedding.Variants(token) {
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
			break
		}
	}
	queryVector := model.Embed(query)
	rows, err := tx.QueryContext(ctx, `SELECT c.name,c.path,c.grp,e.vector FROM commands c LEFT JOIN embeddings e ON c.name=e.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Result, 0)
	group, nameScore := map[string]string{}, map[string]float64{}
	for rows.Next() {
		var r Result
		var raw []byte
		var grp string
		if err = rows.Scan(&r.Name, &r.Path, &grp, &raw); err != nil {
			return nil, err
		}
		if grp == "" {
			grp = r.Name
		}
		group[r.Name] = grp
		nameScore[r.Name] = lexical(r.Name, query)
		if len(terms) > 1 {
			nameScore[r.Name] = max(nameScore[r.Name], wordName(r.Name, words, summaryHits[r.Name]))
		}
		r.Score = nameScore[r.Name] + keyword[r.Name]
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
		if result[i].Score != result[j].Score {
			return result[i].Score > result[j].Score
		}
		// Among equal scores (aliases sharing help), the canonical name first.
		ci, cj := group[result[i].Name] == result[i].Name, group[result[j].Name] == result[j].Name
		if ci != cj {
			return ci
		}
		return result[i].Name < result[j].Name
	})
	// Aliases sharing one help text (grep, egrep, zgrep...) appear once, at the
	// group's best score. The canonical name is shown unless the query matched
	// an alias by name, so "zgrep" still shows zgrep.
	paths := map[string]string{}
	for _, r := range result {
		paths[r.Name] = r.Path
	}
	seen := map[string]bool{}
	unique := result[:0]
	for _, r := range result {
		g := group[r.Name]
		if seen[g] {
			continue
		}
		seen[g] = true
		if p, ok := paths[g]; ok && g != r.Name && nameScore[r.Name] == 0 {
			r.Name, r.Path = g, p
		}
		unique = append(unique, r)
	}
	result = unique
	if len(result) > limit {
		result = result[:limit]
	}
	for i := range result {
		if err = tx.QueryRowContext(ctx, `SELECT substr(help,1,1200),summary FROM commands WHERE name=?`, result[i].Name).Scan(&result[i].Help, &result[i].Summary); err != nil {
			return nil, err
		}
	}
	return result, tx.Commit()
}

type ftsMatch struct {
	name string
	// inSummary is set when a query phrase occurs in the summary column.
	inSummary bool
}

func ftsNames(ctx context.Context, tx *sql.Tx, match string) ([]ftsMatch, error) {
	// bm25 weighted on the summary column alone is non-zero only when a phrase
	// occurs there, which avoids a separate "{summary} :" query per group.
	rows, err := tx.QueryContext(ctx, `SELECT name,bm25(commands_fts,0,1,0) FROM commands_fts WHERE commands_fts MATCH ? ORDER BY bm25(commands_fts,5,4,1),name`, match)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var matches []ftsMatch
	for rows.Next() {
		var m ftsMatch
		var summaryScore float64
		if err = rows.Scan(&m.name, &summaryScore); err != nil {
			return nil, err
		}
		m.inSummary = summaryScore < 0
		matches = append(matches, m)
	}
	return matches, rows.Err()
}

// wordName scores a name against the content words of a multi-word query:
// "find large files" names find exactly, so find must not depend on help text
// alone. A name that is also an everyday word (file, make, test) counts fully
// only when the command's summary matches another query word (otherwise the
// FTS name column alone credits it), so "change file
// permissions" does not lift file over chmod. A prefix counts only when it is
// most of the name ("print" for printenv), not "text" for textutil.
func wordName(name string, words []string, summaryHits []int) float64 {
	name = strings.ToLower(name)
	best := 0.0
	for i, w := range words {
		w = strings.ToLower(w)
		switch {
		case name == w:
			if len(words) == 1 || slices.ContainsFunc(summaryHits, func(j int) bool { return j != i }) {
				return 60
			}
			// The FTS name column already credits the name; no extra boost.
		case len(w) >= 3 && strings.HasPrefix(name, w) && 2*len(w) > len(name):
			best = 20
		}
	}
	return best
}

func lexical(name, query string) float64 {
	name, query = strings.ToLower(name), strings.ToLower(query)
	switch {
	case query == "":
		return 0
	case name == query:
		// Typing a command's exact name must find it first; keyword and
		// embedding signals together stay below this margin.
		return 200
	case strings.HasPrefix(name, query):
		// Shorter completions first: "gi" prefers git over git-upload-pack.
		return 60 + 20*float64(len(query))/float64(len(name))
	case strings.Contains(name, query):
		return 50
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
