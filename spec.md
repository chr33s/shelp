# shelp Spec

## Purpose

Go CLI + thin shell glue that indexes installed binaries and their `--help`/man text, enables fast fuzzy + semantic (embedding) autocomplete, and optionally turns natural-language queries into command suggestions via macOS 27 `fm`.

## Goals

- Scan `$PATH`, capture help text, and store it locally (SQLite).
- Embeddings for semantic search over help.
- Fast completion (prefix/fuzzy + vector).
- Optional NL mode using on-device `fm`.
- Single binary; minimal shell scripts for zsh/bash.

## Architecture

```text
shelp [query] # offline search; ?query explicitly requests on-device AI
├── index     # scan + embed
├── search    # fuzzy + vector
├── complete  # shell-facing suggestions
├── nl        # fm-backed natural language
├── setup     # print shell setup instructions
└── status    # inspect the local index
```

Shell plugins only register completers that call `shelp complete`.

## Data Model (SQLite)

- `commands`: `name TEXT PK`, `path TEXT`, `help TEXT`, `help_hash TEXT`, `updated INTEGER`, `summary TEXT` (man `NAME` line or first descriptive `--help` line), `body TEXT` (help without page headers/footers, section headings and `Usage:` labels), `grp TEXT` (canonical name of commands sharing identical help, or of a versioned install's base command).
- `meta`: `key TEXT PK`, `value TEXT`; `model` records the embedding model version, and a mismatch forces retraining.
- `embeddings`: `name TEXT PK`, `vector BLOB` (little-endian float32 array, up to 64 dimensions).
- FTS5 virtual table on `name`, `summary`, `body` (bm25 weights 5, 4, 1) with `porter unicode61` stemming. Schema version 3; older indexes are migrated on the next `shelp index` or search.
- `terms`: term vocabulary, IDF weights, and float32 projection vectors for query embeddings.
- `nl_cache`: prompt keys, cached suggestion lines, and expiration timestamps.

## Indexing

1. Walk `$PATH`, keeping executable files (skip directories and obvious non-CLIs). Skip shelp itself (by name and by the running executable's resolved path); its help quotes example queries.
2. Read `man -P cat` first (2-second timeout); a command with a man page is never executed. Otherwise run `bin --help`, and `bin -h` only when the `--help` output advertises `-h`, sharing a separate 2-second timeout. Probes get stdin `/dev/null`; the working directory, `HOME`, `TMPDIR` and XDG base directories point into a throwaway sandbox under one per-run root that is removed when indexing ends. Never pass a bare positional word. Never execute commands in `sbin` directories, on the side-effect denylist (broadcast, mail, power, firmware, audio, GUI, daemon tools such as `wall`, `mail`, `shutdown`, `say`, `open`, `osascript`, including versioned names like `perlbug5.34`), matching installer/updater/diagnose/flash/server/`.sh`/`.d` name patterns, or DTrace scripts, including via symlinks; they rely on man pages alone. On macOS, a `/usr/bin` developer-tool shim (links `libxcselect.dylib`; not `xcrun`/`xcode-select`) runs only if `xcode-select -p` names an existing directory and `xcrun --find <name>` succeeds; otherwise running it would open the developer-tools install dialog.
3. Limit captured help to 12 KiB, remove terminal escapes/overstrikes, and hash with SHA-256. Retain unchanged records and skip training when the corpus is unchanged.
4. Generate an embedding of the name, the summary (repeated three times), and the body. Tokens are Porter-stemmed, help boilerplate words are stop words, and agent nouns fold into their base when the corpus has it ("searcher" → "search"). Corpus or model-version changes retrain the shared model and refresh command vectors. Look up man page names once from `manpath` and only run `man` for commands that have a page.
5. Upsert into SQLite.
6. Run in parallel (goroutines + semaphore). Incremental by default; provide a full rebuild flag.
7. Report scanning, help-capture counts, training, and saving progress on stderr.
8. On success, print a stdout summary with total, new, changed, removed, and unavailable-help counts. Count actual content/path changes; a forced rebuild does not make unchanged commands "changed". Compare changes with the snapshot replaced during atomic publication.

### Local embeddings

- Use TF-IDF and randomized truncated SVD (LSA), implemented in Go with Gonum.
- Learn up to 64 dimensions from up to 4,096 corpus terms; persist the term projection and command vectors in SQLite.
- No model server, weight download, or model-generation fallback is required for indexing/search.
- Corpus-trained semantics cover relationships present in installed help. Unknown vocabulary falls back to lexical matching.

## Search / Completion

- Input: current token / buffer.
- Hybrid score:
  - Name match: exact (dominant), prefix scaled by query/name length, substring, subsequence, typo. In multi-word queries, a word equal to the command name scores as a name match, and a prefix covering most of the name scores lower.
  - FTS keyword over name/summary/body, ranking help that covers more query words first, then bm25; query words also match their "-er" form; stop words are skipped. Multi-word queries add a bonus for summary matches.
  - Cosine similarity on embeddings.
- Collapse commands in one `grp` to a single result at the group's best score, shown under the canonical name unless an alias matched by name.
- Return the top N names + short help snippet.
- Relevance is tracked by a fixture suite and an opt-in check against an installed index (`SHELP_EVAL_DB=… go test ./internal/index -run RealIndex -v`).
- `shelp complete --shell zsh <query>` outputs lines for `compadd`.

## Natural Language

- Trigger: query explicitly starts with `?`, or the user runs `shelp nl`.
- Ordinary prose always stays offline; do not infer AI intent from wording or length.
- Bare CLI queries use the same offline search as `shelp search`.
- Shell completion without `?` handles command-name tokens; multiword shell input receives no candidates.
- Prompt `fm respond` with:
  - User intent.
  - Up to eight relevant help snippets (from hybrid search).
  - Instruction: “Output 1–3 concrete commands only, one per line.”
- Use `--model system --no-stream --greedy`, provide the platform, preserve synopsis argument ordering, and default unspecified locations to the current directory.
- Cache recent NL results for 15 minutes by default, capped at 100 entries and invalidated when the index changes. A zero TTL bypasses caching.
- On-device only; no cloud.

## Shell Integration

- `shelp setup zsh` / `shelp setup bash` prints first-run indexing and shell configuration instructions without editing files.
- `shelp init zsh` / `shelp init bash` remains available to print the sourceable snippet.
- Hide `init` and `complete` from ordinary root help; keep both callable.
- Cycling refreshes when the buffer or cursor position changes, preserving the suffix after the cursor.
- Zsh accepts already-ranked fuzzy and semantic matches without prefix filtering.
- Zsh: custom widget or `compdef` that calls the binary.
- Bash: `complete -F` function.
- Loading shell integration binds Ctrl-X Ctrl-H for suggestion cycling. Bash requires 4+; Bash 5 adds initial-command Tab completion.

## CLI Surface

```text
shelp <query>                         # default offline search
shelp "?<intent>"                     # explicit on-device AI
shelp index [--full] [--path PATH_LIST]
shelp setup zsh|bash
shelp search <query>                  # compatible explicit search
shelp init zsh|bash                   # sourceable shell glue
shelp complete [--shell zsh|bash] <query>
shelp nl <natural language>
shelp status
```

### Help and first-run behavior

- No arguments shows help with everyday examples and commands.
- `--limit` and `--json` apply to default and explicit search.
- Advanced `--fm`, `--nl-timeout`, and `--nl-ttl` settings appear in `nl --help`, not root help. Existing invocations with these flags before a command remain compatible, and `?` queries can use them.
- Named subcommands take precedence over bare search. Use `shelp search <query>` to search a command name that collides with a subcommand.
- Missing-index errors include a copyable `Run: shelp index` instruction, preserving an explicit database override when needed.

## Non-goals (v1)

- Full argument/flag parsing from help.
- Cross-machine sync.
- Replacing shell history.

## Dependencies

- Go 1.22+.
- `modernc.org/sqlite` (pure Go).
- `gonum.org/v1/gonum` for local embedding training.
- `fm` present for the NL path (macOS 27+).

## Success Criteria

- Index ~1,000 binaries in <30 seconds on a typical Mac.
- Completion latency <50 ms (no NL).
- NL suggestions useful for common intents (“find large files”, “show git history”).
- Pure local; works offline.
