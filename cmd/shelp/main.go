package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/chr33s/shelp/internal/index"
	"github.com/chr33s/shelp/internal/nl"
	"github.com/chr33s/shelp/shells"
	"github.com/spf13/cobra"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := newRoot().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "shelp:", err)
		os.Exit(1)
	}
}
func newRoot() *cobra.Command {
	var dbPath string
	root := &cobra.Command{Use: "shelp [query]", Args: cobra.ArbitraryArgs, Short: "Offline CLI help search and shell suggestions", SilenceUsage: true, SilenceErrors: true}
	root.CompletionOptions.DisableDefaultCmd = true
	root.Example = `  shelp git                  # search installed tools
  shelp "find large files"    # semantic search, offline
  shelp "?find large files"   # suggest a command with on-device AI
  shelp index                # create or refresh the index
  shelp setup zsh            # show shell setup instructions`
	root.Long = "Search installed CLI tools by name or meaning. Prefix a query with ? to opt into AI suggestions.\nUse 'shelp search <query>' when a query matches a subcommand name."
	root.PersistentFlags().StringVar(&dbPath, "db", os.Getenv("SHELP_DB"), "SQLite index path (default: $XDG_DATA_HOME/shelp/index.db or ~/.local/share/shelp/index.db)")
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if dbPath != "" {
			return nil
		}
		var err error
		dbPath, err = index.DefaultPath()
		return err
	}
	var full bool
	var path string
	var workers int
	var helpTimeout time.Duration
	build := &cobra.Command{Use: "index", Short: "Create or refresh your command index", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		report, err := index.BuildReport(cmd.Context(), index.Options{DBPath: dbPath, PATH: path, Workers: workers, Timeout: helpTimeout, Full: full, Progress: indexProgress(cmd.ErrOrStderr())})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Indexed %d commands: %d new, %d changed, %d removed; %d without help.\n", report.Total, report.New, report.Changed, report.Removed, report.Unavailable)
		return err
	}}
	build.Flags().BoolVar(&full, "full", false, "Rebuild all stored help and embeddings")
	build.Flags().StringVar(&path, "path", os.Getenv("PATH"), "Path-list to scan (defaults to PATH)")
	build.Flags().Lookup("path").DefValue = "$PATH"
	build.Flags().IntVar(&workers, "workers", 32, "Concurrent help capture workers (1–128)")
	build.Flags().DurationVar(&helpTimeout, "timeout", 2*time.Second, "Timeout for each command's help attempts and its man fallback")
	var limit int
	var jsonOutput bool
	search := &cobra.Command{Use: "search <query>", Short: "Search installed tools by name or meaning", Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		results, err := index.Search(cmd.Context(), dbPath, strings.Join(args, " "), limit)
		if err != nil {
			return err
		}
		if jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(results)
		}
		return printResults(cmd.OutOrStdout(), results)
	}}
	search.Flags().IntVarP(&limit, "limit", "n", 10, "Maximum results (1–1000)")
	search.Flags().BoolVar(&jsonOutput, "json", false, "Print structured results")
	root.Flags().IntVarP(&limit, "limit", "n", 10, "Maximum search results (1–1000)")
	root.Flags().BoolVar(&jsonOutput, "json", false, "Print structured search results")
	var fm string
	var nlTimeout, ttl time.Duration
	// Hidden inherited flags preserve existing invocations, including ? queries.
	root.PersistentFlags().StringVar(&fm, "fm", "fm", "Foundation Models executable")
	root.PersistentFlags().DurationVar(&nlTimeout, "nl-timeout", 30*time.Second, "On-device suggestion timeout")
	root.PersistentFlags().DurationVar(&ttl, "nl-ttl", 15*time.Minute, "Natural-language cache TTL (0 disables caching)")
	suggest := func(cmd *cobra.Command, query string) ([]string, error) {
		return nl.Suggest(cmd.Context(), query, nl.Options{DBPath: dbPath, Executable: fm, Timeout: nlTimeout, TTL: ttl})
	}
	natural := &cobra.Command{Use: "nl <natural language>", Short: "Suggest commands using on-device fm; never execute them", Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		lines, err := suggest(cmd, strings.Join(args, " "))
		if err != nil {
			return err
		}
		return printLines(cmd.OutOrStdout(), lines)
	}}
	// Advanced settings belong in nl help, but remain accepted before commands.
	for _, name := range []string{"fm", "nl-timeout", "nl-ttl"} {
		_ = root.PersistentFlags().MarkHidden(name)
	}
	natural.Flags().StringVar(&fm, "fm", "fm", "Foundation Models executable")
	natural.Flags().DurationVar(&nlTimeout, "nl-timeout", 30*time.Second, "On-device suggestion timeout")
	natural.Flags().DurationVar(&ttl, "nl-ttl", 15*time.Minute, "Suggestion cache TTL (0 disables caching)")
	root.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return cmd.Help()
		}
		if nl.Requested(strings.Join(args, " ")) {
			return natural.RunE(cmd, args)
		}
		return search.RunE(cmd, args)
	}
	var shell string
	var completeLimit int
	complete := &cobra.Command{Use: "complete [query]", Hidden: true, Short: "Print one shell suggestion per line", Args: cobra.ArbitraryArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if shell != "" && shell != "zsh" && shell != "bash" {
			return fmt.Errorf("unsupported shell %q (use zsh or bash)", shell)
		}
		query := strings.Join(args, " ")
		if nl.Requested(query) {
			lines, err := suggest(cmd, query)
			if err != nil {
				return err
			}
			return printLines(cmd.OutOrStdout(), lines)
		}
		// v1 completes command names, not arbitrary shell syntax or arguments.
		if len(strings.Fields(query)) > 1 {
			return nil
		}
		results, err := index.Search(cmd.Context(), dbPath, query, completeLimit)
		if err != nil {
			return err
		}
		lines := make([]string, 0, len(results))
		for _, r := range results {
			lines = append(lines, shellQuote(r.Name))
		}
		return printLines(cmd.OutOrStdout(), lines)
	}}
	complete.Flags().StringVar(&shell, "shell", "", "Shell output format: zsh or bash")
	complete.Flags().IntVarP(&completeLimit, "limit", "n", 10, "Maximum suggestions (1–1000)")
	init := &cobra.Command{Use: "init <zsh|bash>", Hidden: true, Short: "Print shell integration to source", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		var content string
		switch args[0] {
		case "zsh":
			content = shells.Zsh
		case "bash":
			content = shells.Bash
		default:
			return fmt.Errorf("unsupported shell %q", args[0])
		}
		_, err := io.WriteString(cmd.OutOrStdout(), content)
		return err
	}}
	setup := &cobra.Command{Use: "setup <zsh|bash>", Short: "Show how to enable shell suggestions", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		file := ""
		note := ""
		switch args[0] {
		case "zsh":
			file = "~/.zshrc"
			note = " (after compinit for Tab completion)"
		case "bash":
			file = "~/.bashrc"
			note = " (Bash 4+)"
		default:
			return fmt.Errorf("unsupported shell %q (use zsh or bash)", args[0])
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "1. Create or refresh your index:\n   shelp index\n\n2. Add this to %s%s:\n   eval \"$(shelp init %s)\"\n\n3. Start a new shell. Type a prefix and press Ctrl-X Ctrl-H to cycle suggestions.\n   Prefix a question with ? to request AI suggestions. Review before pressing Enter.\n", file, note, args[0])
		return err
	}}
	status := &cobra.Command{Use: "status", Short: "Show local index statistics", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		s, err := index.Inspect(cmd.Context(), dbPath)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Index: %s\nCommands: %d\nEmbeddings: %d\nVocabulary: %d terms\nModel: local TF-IDF + LSA (up to 64 dimensions)\n", s.Path, s.Commands, s.Embeddings, s.Terms)
		if err == nil && s.Updated > 0 {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Updated: %s\n", time.Unix(s.Updated, 0).Format(time.RFC3339))
		}
		return err
	}}
	root.AddCommand(build, search, complete, natural, init, setup, status)
	return root
}
func printLines(w io.Writer, lines []string) error {
	for _, line := range lines {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

// printResults writes "name<TAB>description" lines. On a terminal the names
// are padded into a column instead; pipes keep the documented tab format.
func printResults(w io.Writer, results []index.Result) error {
	width := 0
	if f, ok := w.(*os.File); ok && terminal(f) {
		for _, r := range results {
			width = max(width, min(len(r.Name), 24))
		}
	}
	for _, r := range results {
		var err error
		if width > 0 {
			_, err = fmt.Fprintf(w, "%-*s  %s\n", width, r.Name, describe(r))
		} else {
			_, err = fmt.Fprintf(w, "%s\t%s\n", r.Name, describe(r))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func terminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// describe prefers the one-line summary ("find – walk a file hierarchy" becomes
// "walk a file hierarchy") over raw help, which for man pages starts with the
// page header.
func describe(r index.Result) string {
	if r.Summary != "" {
		return snippet(r.Summary)
	}
	return snippet(r.Help)
}

func snippet(help string) string {
	text := strings.Join(strings.Fields(help), " ")
	r := []rune(text)
	if len(r) > 160 {
		return string(r[:157]) + "..."
	}
	return text
}
func shellQuote(name string) string {
	if name != "" && !strings.ContainsAny(name, " \t\r\n'\"\\$`;&|<>(){}[]*?!~#") {
		return name
	}
	return "'" + strings.ReplaceAll(name, "'", "'\\''") + "'"
}
