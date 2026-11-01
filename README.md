# shelp

shelp indexes installed CLI tools and their help/man text in SQLite. Run
`shelp <query>` to search by command name, typo, keyword, or meaning, then insert
suggestions into Bash or Zsh. Optional natural-language suggestions use
macOS 27's on-device `fm`.
Search and embeddings work offline, without a model server.

## Install

Go 1.22+ is required to build. Shell integration supports Zsh and Bash 4+;
Bash 5 adds initial-command Tab completion. Python, SQLite's CLI, a C compiler,
and downloaded model weights are not runtime dependencies. `fm` is only needed
for natural-language suggestions.

### With Go

```sh
go install github.com/chr33s/shelp/cmd/shelp@latest
```

Keep Go's binary installation directory on `PATH`.

### From source

Run these commands from the repository directory:

```sh
go build -o bin/shelp ./cmd/shelp
mkdir -p ~/.local/bin
cp bin/shelp ~/.local/bin/shelp
```

Dependencies are pinned in `go.mod` and verified by `go.sum`. Shell adapters are
embedded in the binary; no separate scripts need to be installed.

### Enable shell suggestions

Add the binary's directory to `PATH`, then run `shelp setup zsh` or
`shelp setup bash` for instructions. Setup prints guidance without modifying
shell files. Build an index with `shelp index`, then add one of these to your
shell configuration:

```zsh
# ~/.zshrc, after compinit for Tab completion
eval "$(shelp init zsh)"
```

```bash
# ~/.bashrc
eval "$(shelp init bash)"
```

Start a new shell to load the integration. Loading it binds **Ctrl-X Ctrl-H**
and registers the Tab completion behavior described below. Suggestions are
inserted into the editing buffer and never executed automatically.

## Use

```sh
shelp git                           # search installed tools
shelp "find large files"            # semantic search, offline
shelp "?find large files"           # ask on-device AI for a command
shelp --limit 20 "git history"
shelp --json "git history"          # structured results with scores
shelp index                         # create or refresh the index
shelp index --full                   # force a rebuild
shelp index --path /usr/bin:/bin     # replace the index with this path-list
shelp setup zsh                      # show shell setup instructions
shelp status
```

Running `shelp` without arguments shows help. Bare queries always search offline
unless they start with `?`; quote that prefix to prevent shell globbing.
The explicit `shelp search <query>` and `shelp nl <intent>` commands remain
available. Use `shelp search status`, for example, to search for a name that
collides with a subcommand. AI settings are documented in `shelp nl --help`;
`complete` and `init` remain callable shell plumbing, hidden from normal help.

### Index progress

Indexing reports scanning, help-capture counts, training, and saving on stderr.
On success, stdout contains a summary such as:

```text
Indexed 120 commands: 5 new, 2 changed, 1 removed; 3 without help.
```

New, changed, and removed counts describe differences from the replaced index.
"Without help" counts indexed commands with no captured help or man text.
An unchanged run skips training and saving; `--full` rebuilds even when there
are no content changes, without counting unchanged tools as changed.

Indexing runs executables with `--help`, `-h`, or `help`, then tries `man -P cat`
if needed. Use a trusted PATH or limit the scan with `--path`. Each run indexes
its selected path-list as a complete collection; tools outside that collection
are removed from the previous index.

### Ranking and matching

Search combines exact/prefix command names, substring/subsequence matching,
typo tolerance, FTS5 keywords, and embedding cosine similarity. Name matches
have the strongest weight; equal scores are ordered by command name. Search
and completion return up to 10 results by default; `--limit N` accepts 1–1,000.

Embeddings use a local TF-IDF + latent semantic analysis (LSA) model implemented
in Go with Gonum. It learns up to 64 dimensions from up to 4,096 terms in the
installed help corpus. It learns relationships present in that corpus rather
than a pretrained language model's broad synonym knowledge. Unknown vocabulary
falls back to lexical search. No weights are downloaded or network requests
made for indexing and search.

### Output formats

`shelp <query>` and `search` print command names and short help snippets
separated by a tab.
`search --json` returns names, executable paths, scores, and help excerpts of up
to 1,200 characters. `complete` prints shell-quoted command names, or complete
command lines for natural-language input, one per line.

`complete` accepts a token or a buffer. A leading `?` explicitly selects
natural-language mode. Ordinary prose never invokes AI. Multiword input without
`?` receives no shell-completion candidates; use `shelp "show git history"` to
search that text offline. V1 does not parse arbitrary shell syntax, complete
flags, or replace shell history.

## Configuration and privacy

Configuration uses CLI flags and environment variables. The index location is
selected in this order:

1. `--db /path/to/index.db`
2. `SHELP_DB`
3. `$XDG_DATA_HOME/shelp/index.db`
4. `~/.local/share/shelp/index.db`

The database contains executable names and paths, captured help, embeddings,
and cached natural-language suggestions. The default database directory is
created with mode 0700. Help output and cached suggestions can contain local
paths or other private information; shelp does not redact them or sync the
index to another machine.

```sh
shelp index --workers 16 --timeout 2s
shelp --db /path/to/index.db status
shelp nl --nl-ttl 0 'show git history'
shelp nl --nl-timeout 60s 'find large files'
```

Capture defaults to 32 workers, with a supported range of 1–128. Each command's
help attempts share a 2-second deadline; man fallback gets a separate deadline
of the same duration. Output is limited to 12 KiB, with terminal escapes and
man-page overstrikes removed. A missing index produces an error directing you
to run `shelp index`, retaining `--db` in the suggested command when needed;
search does not create an empty database.

## Zsh and Bash behavior

Type a command prefix or `?find large files`, then press **Ctrl-X Ctrl-H**.
Repeated presses cycle the returned candidates. Editing the buffer or moving
the cursor starts a new search. The widget completes the text before the cursor
and preserves the suffix after it. Review the suggestion before pressing Enter.

Zsh registers initial-command Tab completion when `compdef` is available.
Already-ranked candidates bypass Zsh's prefix filter, so fuzzy and semantic
matches remain available. Load the integration after `compinit` to enable this.

Bash 5 registers initial-command Tab completion. Bash 4 supports empty-line
Tab completion and the cycling key. Existing shell argument completers remain
responsible for flags and arguments; shelp does not install history hooks.

## Optional natural-language suggestions

NL mode retrieves up to eight relevant help excerpts and invokes
`fm respond --model system --no-stream --greedy`. It supplies the operating
system and asks for one to three command lines with correct synopsis ordering
and the current directory as the default search location.

Invoke it explicitly with `shelp "?find large files"` or
`shelp nl "find large files"`. Advanced `--fm`, `--nl-timeout`, and `--nl-ttl`
flags appear in `nl --help`. Existing invocations with those flags before a
command, or alongside a `?` query, remain supported.

This requires a supported Mac with the on-device model available and Apple's
`fm` license accepted. Inspect those prerequisites with:

```sh
fm license --status
fm models
```

shelp does not accept legal terms or switch to a cloud model. Missing models,
license errors, and timeouts are reported. Ordinary search and completion work
without `fm`; use `--fm /path/to/fm` to select another executable explicitly.

Recent NL results are cached in SQLite for 15 minutes, bounded to 100 entries
and invalidated when the index changes. `--nl-ttl 0` bypasses caching;
`--nl-timeout` defaults to 30 seconds. Generated commands still need review for
the intended task, and shelp never executes them.

## Development and verification

```sh
go test ./...
go test -race ./...
go vet ./...
```

Tests cover PATH precedence, help/man fallback and output limits, incremental
and full builds, overlapping index publication, stale FTS removal, legacy schema
migration, cancellation, local embedding ranking, float32 storage, NL invocation
and caching, default-search routing, explicit AI opt-in, setup guidance, and
index progress/summary output. Shell tests cover candidate cycling and cursor
movement in Bash and Zsh. A real Zsh PTY test checks fuzzy and semantic Tab
completion; that test requires Zsh and Python 3 and skips when either is absent.

Automated NL tests use a controlled executable. Live checks on macOS 27 with
AFM 3 Core Advanced also verified the spec's two example intents: “find large
files” produced `find . -type f -size +100M` (plus `du -a .`), and “show git
history” produced `git log --oneline`. The reviewed commands were checked in
disposable file/repository fixtures; repeated NL completion verified the cache.

## Performance and database updates

PATH scanning and help capture use bounded worker pools. The first executable
in PATH wins, including symlinks to regular executable files. Directories,
hidden files, non-executable files, and common library/object extensions are
skipped.

SHA-256 detects help changes. Unchanged records retain their timestamps, and an
unchanged corpus skips model training. Corpus changes retrain the shared model
and refresh vectors; `--full` forces a rebuild. Publication uses one SQLite
transaction and compares records against the current database before upserting,
so overlapping builds publish consistent help and embeddings. Cancellation, scan errors, or training errors leave the previous published
snapshot intact.

Legacy scaffold databases migrate on indexing, including an FTS rebuild.
Search reads one database snapshot, loads only relevant term projections, and
fetches help excerpts only for the top results.

Run the reproducible benchmarks with:

```sh
go test ./internal/index -run '^$' -bench BenchmarkIndex1000 -benchtime=1x
go build -o /tmp/shelp ./cmd/shelp
python3 scripts/benchmark.py /tmp/shelp --check
```

Measured on an Apple M5 Max with 1,000 controlled CLI fixtures, each emitting
roughly 8 KiB of help:

| Operation | Time |
| --- | ---: |
| Index 1,000 commands | 17.5 s |
| Completion, full CLI process, median | 9.2 ms |
| Completion, full CLI process, p95 | 9.7 ms |
| Completion, full CLI process, maximum | 10.0 ms |

Completion measurements cover 50 process invocations. The spec targets indexing
about 1,000 binaries in under 30 seconds and non-NL completion under 50 ms on a
typical Mac. Real PATH latency depends on how many tools time out or need man
fallback; these fixture results are not a guarantee for arbitrary programs.

## Design and scope

See [the specification](spec.md) for the architecture, command contracts, and
acceptance criteria. V1 focuses on local command discovery and suggestions;
full argument parsing, cross-machine sync, and replacing shell history are
outside its scope.
