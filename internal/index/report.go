package index

import (
	"context"
	"database/sql"
)

// Progress callbacks are serialized, including during concurrent help capture.
type Progress struct {
	Stage       string
	Done, Total int
}
type Report struct{ Total, New, Changed, Removed, Unavailable int }
type record struct{ hash, path string }
type recordReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadRecords(ctx context.Context, db recordReader) (map[string]record, error) {
	rows, err := db.QueryContext(ctx, `SELECT name,help_hash,path FROM commands`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := map[string]record{}
	for rows.Next() {
		var name string
		var r record
		if err = rows.Scan(&name, &r.hash, &r.path); err != nil {
			return nil, err
		}
		records[name] = r
	}
	return records, rows.Err()
}
func summarize(commands []command, records map[string]record) Report {
	report := Report{Total: len(commands), Removed: len(records)}
	for _, c := range commands {
		old, exists := records[c.Name]
		if !exists {
			report.New++
		} else {
			report.Removed--
			if old.hash != c.Hash || old.path != c.Path {
				report.Changed++
			}
		}
		if c.Help == "" {
			report.Unavailable++
		}
	}
	return report
}
