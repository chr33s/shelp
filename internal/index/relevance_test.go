package index

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// relevanceCases pair natural intents with commands that correctly satisfy them.
// Only cases with at least one expected command installed are scored.
var relevanceCases = []struct {
	query string
	want  []string
}{
	{"search text in files", []string{"grep", "rg", "ag", "ack"}},
	{"show disk usage", []string{"du", "df", "ncdu", "dust"}},
	{"free disk space", []string{"df"}},
	{"count lines words", []string{"wc"}},
	{"count lines", []string{"wc"}},
	{"download a url", []string{"curl", "wget"}},
	{"kill process by name", []string{"pkill", "killall"}},
	{"list running processes", []string{"ps", "top", "htop", "btop"}},
	{"find large files", []string{"find", "fd", "du"}},
	{"compress a directory", []string{"tar", "zip", "gzip", "ditto", "compress"}},
	{"extract archive", []string{"tar", "unzip", "bsdtar"}},
	{"copy files to remote host", []string{"scp", "rsync", "sftp"}},
	{"synchronize directories", []string{"rsync"}},
	{"show network connections", []string{"netstat", "lsof", "ss"}},
	{"change file permissions", []string{"chmod"}},
	{"change file owner", []string{"chown"}},
	{"create symbolic link", []string{"ln"}},
	{"show differences between files", []string{"diff", "sdiff", "colordiff", "delta", "cmp"}},
	{"sort lines", []string{"sort"}},
	{"remove duplicate lines", []string{"uniq"}},
	{"replace text in a stream", []string{"sed", "sd", "perl"}},
	{"first lines of a file", []string{"head"}},
	{"last lines of a file", []string{"tail"}},
	{"query json", []string{"jq", "yq", "gojq"}},
	{"print working directory", []string{"pwd"}},
	{"display a calendar", []string{"cal", "ncal"}},
	{"current date and time", []string{"date"}},
	{"schedule a recurring job", []string{"cron", "crontab", "at", "launchctl"}},
	{"dns lookup", []string{"dig", "nslookup", "host", "dog"}},
	{"test network reachability", []string{"ping", "mtr", "traceroute"}},
	{"print environment variables", []string{"env", "printenv"}},
	{"determine file type", []string{"file"}},
	{"compute checksum", []string{"shasum", "md5", "sha256sum", "cksum", "b2sum"}},
	{"base64 encode", []string{"base64"}},
	{"split a file into pieces", []string{"split", "csplit"}},
	{"edit a text file", []string{"vim", "vi", "nano", "emacs", "pico", "nvim", "hx"}},
	{"terminal multiplexer", []string{"tmux", "screen", "zellij"}},
	{"show manual page", []string{"man"}},
	{"locate a command in path", []string{"which", "whereis", "type", "command"}},
	{"measure command run time", []string{"time", "hyperfine"}},
}

type relevanceScore struct {
	scored, hit1, hit5 int
	mrr                float64
	misses             []string
}

func scoreRelevance(t *testing.T, path string) relevanceScore {
	t.Helper()
	ctx := context.Background()
	db, err := open(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	installed := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM commands`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		installed[name] = true
	}
	rows.Close()
	db.Close()
	var s relevanceScore
	for _, c := range relevanceCases {
		want := map[string]bool{}
		for _, w := range c.want {
			if installed[w] {
				want[w] = true
			}
		}
		if len(want) == 0 {
			continue
		}
		s.scored++
		results, err := Search(ctx, path, c.query, 20)
		if err != nil {
			t.Fatal(err)
		}
		rank := 0
		for i, r := range results {
			if want[r.Name] {
				rank = i + 1
				break
			}
		}
		if rank > 0 {
			s.mrr += 1 / float64(rank)
		}
		if rank == 1 {
			s.hit1++
		}
		if rank > 0 && rank <= 5 {
			s.hit5++
		} else {
			top := make([]string, 0, 3)
			for i := 0; i < len(results) && i < 3; i++ {
				top = append(top, results[i].Name)
			}
			s.misses = append(s.misses, fmt.Sprintf("%s (rank %s; top: %s)", c.query, rankText(rank), strings.Join(top, ", ")))
		}
	}
	if s.scored > 0 {
		s.mrr /= float64(s.scored)
	}
	return s
}

// TestRelevanceRealIndex scores an installed index: SHELP_EVAL_DB=path go test -run RealIndex -v
// It is skipped in CI because results depend on the machine's installed tools.
func TestRelevanceRealIndex(t *testing.T) {
	path := os.Getenv("SHELP_EVAL_DB")
	if path == "" {
		t.Skip("set SHELP_EVAL_DB to an index path")
	}
	if _, err := os.Stat(path); err != nil {
		t.Skip(err)
	}
	s := scoreRelevance(t, path)
	t.Logf("scored %d: MRR %.3f, hit@1 %d (%.0f%%), hit@5 %d (%.0f%%)", s.scored, s.mrr,
		s.hit1, 100*float64(s.hit1)/float64(s.scored), s.hit5, 100*float64(s.hit5)/float64(s.scored))
	for _, m := range s.misses {
		t.Logf("miss@5: %s", m)
	}
}

func rankText(rank int) string {
	if rank == 0 {
		return ">20"
	}
	return fmt.Sprint(rank)
}
