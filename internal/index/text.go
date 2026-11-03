package index

import (
	"regexp"
	"sort"
	"strings"
)

// manHeader matches man page header lines such as
// "GREP(1)   General Commands Manual   GREP(1)".
var manHeader = regexp.MustCompile(`^\S+\([0-9][0-9A-Za-z]*\)\s.*\S+\([0-9][0-9A-Za-z]*\)$`)

// columns splits a line on runs of two or more spaces, as man lays out
// headers and footers.
var columns = regexp.MustCompile(`\s{2,}`)

// pageFrame reports a man header or footer: "GREP(1) ... GREP(1)" or
// "macOS 27.2   June 1, 2026   macOS 27.2", whose outer columns repeat.
func pageFrame(line string) bool {
	if manHeader.MatchString(line) {
		return true
	}
	parts := columns.Split(line, -1)
	return len(parts) == 3 && parts[0] == parts[2] && parts[0] != ""
}

// manTitle captures the page name from a man header's first token.
var manTitle = regexp.MustCompile(`^(\S+)\([0-9][0-9A-Za-z]*\)`)

// usageLabel matches the "Usage:" label that starts most --help output.
var usageLabel = regexp.MustCompile(`(?i)\busage:`)

// probeError matches a tool rejecting the help flag rather than describing itself.
var probeError = regexp.MustCompile(`(?i)\b(unknown|illegal|invalid|unrecognized|unsupported) (option|flag|argument|command)|\berror\b`)

// summaryDash separates names from the one-line summary in a man NAME section.
var summaryDash = regexp.MustCompile(`\s(?:-|–|—|\\-)\s`)

func trimmedLines(help string) []string {
	lines := strings.Split(help, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return lines
}

// heading reports a man section heading: a short line in capitals such as
// NAME, SEE ALSO or EXIT STATUS, or "Name" as pico's page writes it.
func heading(line string) bool {
	if line == "" || len(line) > 30 || strings.ContainsAny(line, ".,:;()[]-") {
		return false
	}
	if line == strings.ToUpper(line) && strings.ToUpper(line) != strings.ToLower(line) {
		return true
	}
	switch strings.ToLower(line) {
	case "name", "synopsis", "description", "options":
		return true
	}
	return false
}

// helpSummary extracts what a command does in one line, and the names a man
// page lists for it. Man pages state it in NAME ("grep, egrep – file pattern
// searcher"); --help output usually after the usage line.
func helpSummary(help string) (summary string, names []string) {
	lines := trimmedLines(help)
	for i, line := range lines {
		if !strings.EqualFold(line, "name") {
			continue
		}
		var parts []string
		for _, next := range lines[i+1:] {
			if next == "" || heading(next) {
				break
			}
			parts = append(parts, next)
		}
		text := strings.Join(parts, " ")
		if loc := summaryDash.FindStringIndex(text); loc != nil {
			for _, n := range strings.Split(text[:loc[0]], ",") {
				if n = strings.TrimSpace(n); n != "" {
					names = append(names, n)
				}
			}
			return limitSummary(text[loc[1]:]), names
		}
		return limitSummary(text), nil
	}
	inUsage := false
	for _, line := range lines {
		lower := strings.ToLower(line)
		switch {
		case line == "":
			inUsage = false
		case strings.HasPrefix(lower, "usage") || pageFrame(line) || heading(line):
			inUsage = strings.HasPrefix(lower, "usage")
		case inUsage, strings.HasPrefix(line, "-"), len(strings.Fields(line)) < 3, probeError.MatchString(line):
			// Synopsis continuation, option rows and fragments are not a summary.
		default:
			return limitSummary(line), nil
		}
	}
	return "", nil
}

func limitSummary(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = strings.ToValidUTF8(s[:200], "")
	}
	return s
}

// searchBody is the help text without man headers/footers, section headings
// and "Usage:" labels, which every page shares and which drown real terms.
func searchBody(help string) string {
	lines := trimmedLines(help)
	out := lines[:0]
	for _, line := range lines {
		if line == "" || pageFrame(line) || heading(line) {
			continue
		}
		out = append(out, usageLabel.ReplaceAllString(line, ""))
	}
	return strings.Join(out, "\n")
}

// aliasGroups maps each command to the canonical name of the commands that
// share its exact help (grep, egrep, zgrep... share one man page). The
// canonical name is the first name the page lists that is installed, else the
// shortest, so results show grep rather than bzegrep.
func aliasGroups(commands []command) map[string]string {
	byHelp := map[string][]int{}
	for i, c := range commands {
		if c.Help != "" {
			byHelp[c.Help] = append(byHelp[c.Help], i)
		}
	}
	groups := make(map[string]string, len(commands))
	for _, c := range commands {
		groups[c.Name] = c.Name
	}
	for help, members := range byHelp {
		if len(members) < 2 {
			continue
		}
		installed := map[string]bool{}
		names := make([]string, 0, len(members))
		for _, i := range members {
			installed[commands[i].Name] = true
			names = append(names, commands[i].Name)
		}
		// The page title ("LN(1)") names the page's own command, which beats
		// the NAME list order ("link, ln – make links").
		canonical := ""
		if m := manTitle.FindStringSubmatch(strings.TrimSpace(help)); m != nil {
			for _, n := range names {
				if strings.EqualFold(n, m[1]) {
					canonical = n
				}
			}
		}
		_, listed := helpSummary(help)
		for _, n := range listed {
			if canonical == "" && installed[n] {
				canonical = n
			}
		}
		if canonical == "" {
			sort.Slice(names, func(a, b int) bool {
				if len(names[a]) != len(names[b]) {
					return len(names[a]) < len(names[b])
				}
				return names[a] < names[b]
			})
			canonical = names[0]
		}
		for _, n := range names {
			groups[n] = canonical
		}
	}
	// Versioned installs (pg_dump-18, json_pp5.34) join their unversioned
	// command's group when it is installed, even if their help differs slightly.
	for _, c := range commands {
		if m := versionSuffix.FindStringSubmatch(c.Name); m != nil {
			if base, ok := groups[m[1]]; ok && groups[c.Name] == c.Name {
				groups[c.Name] = base
			}
		}
	}
	return groups
}

// versionSuffix splits "name5.34", "name-18" or "name3.14" into name and a
// dotted or dashed version; bare digits (x264, md5) are part of the name.
var versionSuffix = regexp.MustCompile(`^(.*[A-Za-z_])(?:-[0-9]+(?:\.[0-9]+)*|[0-9]+\.[0-9]+(?:\.[0-9]+)*)$`)
