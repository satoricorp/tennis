<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset=".github/assets/tennis-logo-dark.png">
    <img src=".github/assets/tennis-logo-light.png" alt="Tennis" width="226">
  </picture>
</p>

<p align="center">
  <a href="https://github.com/satoricorp/tennis/actions/workflows/ci.yml"><img src="https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/satoricorp/tennis/badges/tests.json" alt="Tests passing"></a>
  <a href="https://github.com/satoricorp/tennis/actions/workflows/ci.yml"><img src="https://github.com/satoricorp/tennis/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
  <a href="https://github.com/satoricorp/tennis/releases"><img src="https://img.shields.io/github/v/release/satoricorp/tennis" alt="Latest release"></a>
  <a href="https://pkg.go.dev/github.com/satoricorp/tennis"><img src="https://pkg.go.dev/badge/github.com/satoricorp/tennis.svg" alt="Go reference"></a>
  <img src="https://img.shields.io/github/go-mod/go-version/satoricorp/tennis" alt="Go version">
  <a href="LICENSE"><img src="https://img.shields.io/github/license/satoricorp/tennis" alt="License"></a>
</p>

# Tennis

Tennis is a command-line tool and Go library for searching text on your own computer. You give it files or your AI chat history, and it stores everything in one SQLite file. Each search looks for your exact words and for passages with the same meaning, then merges both into one ranked list, with no server and no API key.

[Documentation](https://satoricorp.github.io/tennis/docs) · [Discord](https://discord.gg/JpAggvxJJ)

# Getting Started

## Install

Tennis runs on macOS and Linux, on Intel/AMD (`amd64`) and ARM (`arm64`). Pick one of these three ways.

**Option 1: with Go** (needs Go 1.25 or newer)

```bash
go install github.com/satoricorp/tennis/cmd/tennis@latest
```

This puts `tennis` in `$(go env GOPATH)/bin`. That folder must be on your `PATH`.

**Option 2: download a release**

```bash
VERSION=0.2.0                                               # the newest version on the Releases page
OS=$(uname -s | tr '[:upper:]' '[:lower:]')                 # darwin or linux
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')   # amd64 or arm64
curl -fL -o tennis.tar.gz "https://github.com/satoricorp/tennis/releases/download/v${VERSION}/tennis_${VERSION}_${OS}_${ARCH}.tar.gz"
tar -xzf tennis.tar.gz tennis
mkdir -p ~/.local/bin && mv tennis ~/.local/bin/            # any folder on your PATH works
```

**Option 3: build from source**

```bash
git clone https://github.com/satoricorp/tennis
cd tennis
CGO_ENABLED=0 go build -o tennis ./cmd/tennis
```

`CGO_ENABLED=0` makes one self-contained binary with no C libraries. Tennis needs none.

Check that it worked:

```bash
tennis version
```

The first time you add something, Tennis downloads its embedding model once (about 123MB) into `~/.cache/tennis`. After that, search never uses the network.

To read PDFs on Linux, also install `pdftotext` (for example `sudo apt install poppler-utils`). On a Mac, Tennis uses `pdftotext` if you have it and the built-in Spotlight importer if you don't.

# Examples

## Basic Example

Index a folder of notes:

```bash
$ tennis add ~/Documents/notes
tennis: created namespace "context" bound to builtin:potion-retrieval-32M
tennis: no ANTHROPIC_API_KEY or OPENAI_API_KEY; cards will carry the opening message instead of a summary
tennis: /Users/you/Documents/notes: reading plain files
imported 3, skipped 0 unchanged, 3 chunks in "context", 3 cards in /Users/you/tennis
```

What happened:

- Tennis created a **namespace** called `context`. A namespace is a separate collection of documents, and `context` is the default one.
- It read the 3 files, split them into chunks, and stored them in `~/.tennis/db.sqlite`.
- It wrote one markdown summary **card** per file into `~/tennis`. The line about API keys is only about those cards; search never needs a key.

Search it:

```bash
$ tennis search "keep me signed in"
> # Session handling

  Make the login flow remember the user between sessions. The session cookie is
  set with an expiry so the browser keeps it after the tab closes.

  auth.md [2026-10-08] 0.0328
```

You get the best match, printed in full. Under it are the file name, the file's date, and the score.

Ask for more results with `-k`:

```bash
$ tennis search "keep me signed in" -k 3
 1. auth.md [2026-10-08] 0.0328  [kw#1 sem#1]
    # Session handling

    Make the login flow remember the user between sessions. The session cookie
    is set with an expiry so the browser keeps it after the tab closes.

 2. retry.md [2026-10-08] 0.0161  [sem#2]
    # Retries

    Failed requests back off exponentially, doubling the delay each attempt up
    to a cap of thirty seconds.

 3. config.md [2026-10-08] 0.0159  [sem#3]
    # Configuration

    Write a parser for TOML configuration files. Values from the file are merged
    over the defaults.
```

The tag at the end of each line says which search found the result. `kw#1` means keyword search ranked it first. `sem#2` means meaning-based search ranked it second. `auth.md` was found by both, so it scores highest. The other two were found only by meaning, so treat them as weaker matches.

Run `add` again and unchanged files are skipped:

```bash
$ tennis add ~/Documents/notes
tennis: no ANTHROPIC_API_KEY or OPENAI_API_KEY; cards will carry the opening message instead of a summary
tennis: /Users/you/Documents/notes: reading plain files
imported 0, skipped 3 unchanged, 0 chunks in "context"
```

List what is stored:

```bash
$ tennis ls
DATE             SOURCE         DOCS  TITLE
2026-10-08 14:09 file              1  auth.md
2026-10-08 14:09 file              1  config.md
2026-10-08 14:09 file              1  retry.md

3 files
```

## Full Example

This walks through the main features in order. Replace the paths with your own.

```bash
# 1. Import AI chat history. Tennis detects the format from the contents.
tennis add ~/.claude                            # Claude Code sessions on this machine
tennis add ~/.codex                             # Codex sessions on this machine
tennis add ~/Downloads/chatgpt-export.zip       # a ChatGPT data export
tennis add ~/Downloads/claude-export.zip        # a Claude.ai data export

# 2. Keep work documents in their own namespace.
tennis add ~/work/handbook --ns work

# 3. See what is stored, newest first.
tennis ls
tennis ls chatgpt                               # only one source
tennis ls --ns work

# 4. Search, and narrow the search with attribute filters.
tennis search "token refresh" -k 5
tennis search "token refresh" --where project=tennis,branch=main
tennis search "retry logic" --where role=user   # only things you typed
tennis search "vacation policy" --ns work

# 5. Choose one kind of search.
tennis search "ERR_CONN_RESET" --mode keyword   # exact words only
tennis search "the app forgets me" --mode semantic

# 6. Read a whole conversation: give a filter and no query.
tennis search --where session=7f3a2c10

# 7. Get JSON for scripts.
tennis search "token refresh" -k 5 --json

# 8. Delete one document, then a whole namespace.
tennis rm ~/work/handbook/old-policy.md --ns work
tennis ns rm work
```

Some notes on what these commands do:

- **Formats.** `add` works out what a path is. If it guesses wrong, name it with `--chatgpt`, `--claude`, `--claude-code`, `--codex`, or `--files`.
- **Turns.** Chat history is stored one message per document, so a search finds the exact message. `tennis ls` groups messages back into one row per conversation.
- **Attributes.** Every document carries attributes you can filter on with `--where`. Files have `path`, `name`, `size`, `modified`. Chat messages have `source`, `session`, `role`, `title`, `created`. Claude Code and Codex messages also have `project` (the repo the session started in), `cwd`, and `worktree` when there was one. Claude Code messages also have `branch`, and a subagent's turns have `subagent`.
- **Filters.** `--where` takes `key=value`, `key!=value`, `key>value`, `key>=value`, `key<value`, `key<=value`, joined with commas (all must match). Put a value in double quotes when it contains a comma: `--where 'path="/Users/you/Budget, 2026.md"'`.
- **Cards.** Every file and conversation also gets a markdown card in `~/tennis`. Set `ANTHROPIC_API_KEY` or `OPENAI_API_KEY` and each card gets a short summary written by a model. Without a key, the card holds the opening lines instead. A card's last line is the command that prints the whole record. Use `--no-cards` to skip cards, or `--cards <dir>` to put them elsewhere.
- **Defaults.** The database is `~/.tennis/db.sqlite` (change it with `--db` or `$TENNIS_DB`). The namespace is `context` (change it with `--ns` or `$TENNIS_NS`).

Run `tennis --help` for the main commands and `tennis --agents` for every command.

# Details

## What tennis is great for

- **Searching your own files on one machine.** It finds exact terms (names, IDs, error codes) and paraphrases in the same search.
- **Searching AI chat history.** It reads ChatGPT and Claude exports and the local Claude Code and Codex history, and none of it leaves your machine.
- **Giving a program or AI agent a local memory.** Write documents in and query them back through the Go library, a local HTTP API, or the CLI with `--json`.
- **Working offline.** The model runs on your machine. Search never calls an API.
- **Collections up to about 100,000 chunks.** On an Apple M4, a search over about 117,000 chunks (74,000 chat messages) takes about 2 seconds.

## What tennis is not great for

- **Millions of documents, or answers in milliseconds.** Meaning-based search compares the query with every stored chunk. Time grows in step with the size of the collection.
- **A shared server.** The HTTP API has no authentication. It is meant for programs on the same machine.
- **Telling you when nothing matches.** There is no relevance cutoff. A search always returns up to `-k` results, even weak ones. Use the score and the `kw#`/`sem#` tags to judge.
- **Chinese, Japanese, and Korean keyword search.** The keyword index can't split those languages into words, so keyword search finds nothing in them. Meaning-based search still works. This is tracked in [issue #1](https://github.com/satoricorp/tennis/issues/1).
- **Images, audio, and video.** Tennis indexes text only. A scanned PDF with no text layer has nothing to index.
- **The best possible meaning-based results.** The built-in model is small and fast, but weaker than large hosted models. You can switch a namespace to OpenAI embeddings (see below).
- **Windows.** Only macOS and Linux are supported.

## Embedding tennis with your application

There are three ways to use Tennis from your own code.

**1. Go library.** This is the same code as [`examples/basic`](examples/basic/main.go):

```go
import "github.com/satoricorp/tennis"

db, err := tennis.Open("~/.tennis/db.sqlite")
if err != nil {
    log.Fatal(err)
}
defer db.Close()

// Create a namespace once. Later, open it with db.Namespace(ctx, "agents").
ns, err := db.CreateNamespace(ctx, "agents", tennis.NamespaceOptions{})

// Write documents. Writing an existing ID replaces that document.
_, err = ns.Write(ctx, []tennis.Document{
    {ID: "a1", Text: "make the login flow remember the user between sessions",
        Attributes: map[string]any{"status": "merged", "cost": 4}},
})

// Search.
results, err := ns.Query(ctx, tennis.Query{
    Text:   "keep me signed in",
    TopK:   5,                             // default 10
    Mode:   tennis.Hybrid,                 // or tennis.Keyword, tennis.Semantic
    Filter: tennis.Eq("status", "merged"), // also NotEq, In, Gt, Gte, Lt, Lte, Glob, And, Or
})
for _, r := range results {
    fmt.Println(r.ID, r.Score, r.KeywordRank, r.SemanticRank, r.Text)
}
```

`Namespace` also has `Delete`, `Get`, `List`, and `Count`. The [API reference](https://pkg.go.dev/github.com/satoricorp/tennis) lists everything.

**2. Local HTTP API.** For any other language. Start the server:

```bash
tennis serve                        # listens on 127.0.0.1:8817
```

| Method | Path | Request body | Response |
|---|---|---|---|
| `GET` | `/health` | none | `{"status": "ok", "version": ..., "db": ...}` |
| `GET` | `/v1/namespaces` | none | list of namespaces with counts |
| `POST` | `/v1/namespaces/{ns}/write` | `{"documents": [{"id", "text", "attributes"}]}` | `{"written", "skipped", "chunks"}` |
| `POST` | `/v1/namespaces/{ns}/query` | `{"text", "top_k", "mode", "where"}` | `{"results": [...]}` |
| `POST` | `/v1/namespaces/{ns}/delete` | `{"ids": [...]}` | `{"deleted": n}` |

The first write to a namespace creates it. `where` uses the same syntax as the CLI's `--where`.

```bash
curl -s localhost:8817/v1/namespaces/notes/write -d '{"documents": [{"id": "a1", "text": "make the login flow remember the user"}]}'
curl -s localhost:8817/v1/namespaces/notes/query -d '{"text": "keep me signed in", "top_k": 5}'
```

**3. The CLI from scripts.** Add `--json` to `search`, `ls`, `add`, `rm`, or `ns` for machine-readable output on stdout. To add documents that aren't files, pipe newline-delimited JSON into `add --ndjson`, one document per line:

```bash
echo '{"id": "e1", "text": "deploy failed with a connection timeout", "attributes": {"kind": "event"}}' \
  | tennis add --ndjson --ns agents
```

## Supported file types

| What | Files | Read by | Where it works |
|---|---|---|---|
| Plain text | any file whose contents are text: `.md`, `.txt`, `.go`, `.json`, `.csv`, a `Makefile`, … | Tennis | everywhere |
| Office | `.docx`, `.pptx`, `.xlsx` | Tennis | everywhere |
| Web pages and e-books | `.html`, `.htm`, `.xhtml`, `.epub` | Tennis | everywhere |
| PDF | `.pdf` | `pdftotext`, or Spotlight on a Mac | macOS; Linux with `pdftotext` installed |
| Rich text and older Word | `.rtf`, `.rtfd`, `.doc`, `.odt`, `.webarchive` | macOS `textutil` | macOS |
| Apple iWork, older Office, email | `.pages`, `.numbers`, `.key`, `.xls`, `.ppt`, `.ods`, `.odp`, `.eml`, `.emlx` | macOS Spotlight | macOS |
| ChatGPT history | a data export, as a `.zip` or unzipped folder | Tennis | everywhere |
| Claude.ai history | a data export, as a `.zip` or unzipped folder, including project files | Tennis | everywhere |
| Claude Code history | `~/.claude`, including subagent transcripts | Tennis | everywhere |
| Codex history | `~/.codex` | Tennis | everywhere |
| Program output | newline-delimited JSON on stdin, with `add --ndjson` | Tennis | everywhere |

When you add a folder, Tennis skips these:

- **Silently:** images, audio, video, archives, fonts, databases, empty files, dotfiles, and anything inside a folder whose name starts with a dot (such as `.git`).
- **Silently, counted in the summary:** `node_modules`, `vendor`, `target`, `dist`, `build`, `__pycache__`, `site-packages`, application bundles, symbolic links to folders, and anything a `.gitignore` inside the folder lists. The folder you name yourself is always read.
- **With a note on stderr:** other binary files, text files over 10MB, documents over 100MB, and formats with no reader on your machine.

Use `--ext .go,.md` to read only some extensions.

## Specifics about the vector architecture

An **embedding** is a list of numbers that represents what a piece of text means. Texts with similar meanings get similar lists. Tennis stores one embedding per chunk of text and compares them with the embedding of your query.

- **Model.** The default is [`potion-retrieval-32M`](https://huggingface.co/minishlab/potion-retrieval-32M) from Minish Lab. It is a *static* model: a table holding one 512-number vector for each token in its vocabulary. To embed a text, Tennis splits it into tokens, looks up each token's vector, averages them, and scales the result to length 1. No neural network runs, so Tennis needs no machine-learning runtime or C libraries.
- **Download.** The model downloads once from Hugging Face into `~/.cache/tennis/models/`. Tennis checks the file against a SHA-256 checksum pinned in [`embed/models.go`](embed/models.go) and refuses a file that doesn't match.
- **Quality.** On the MTEB retrieval benchmark the model scores 35.06, against 42.92 for the common `all-MiniLM-L6-v2`. Keyword search covers much of that gap, because the queries a small model handles worst (exact names, IDs, code) are the ones keyword search handles best.
- **Other models.** `--model potion-base-8M` is smaller (256 numbers per vector, 29MB) and less accurate. `--openai text-embedding-3-small` (1536 numbers) or `--openai text-embedding-3-large` (3072) uses OpenAI instead. That needs `OPENAI_API_KEY` and sends your text to OpenAI.
- **One model per namespace.** A namespace records its model and vector size when it is created. Opening it with a different model is an error, because vectors from different models cannot be compared. To change models, create a new namespace and add your files again.
- **Chunks.** Each document is split into chunks of about 1,000 characters that overlap by 100. Splits fall on paragraph or sentence boundaries when possible. Each chunk gets its own vector, because the average of a long text's tokens says little about any one part of it.
- **Storage.** Everything lives in one SQLite file, `~/.tennis/db.sqlite` by default. Documents, their attributes (as JSON), and their chunks are ordinary tables. Each chunk's vector is stored next to its text as 32-bit floats.
- **Comparing vectors.** There is no approximate index. A search computes the cosine similarity between the query vector and every chunk vector in the namespace that passes the filters. Results are exact, and time grows in step with the number of chunks.
- **Re-adding.** Each document stores a hash of its text and attributes. A document whose hash hasn't changed is skipped, not embedded again.

## Specifics about tennis search

Every search runs two searches and merges them.

- **Keyword search** uses [BM25](https://en.wikipedia.org/wiki/Okapi_BM25) through SQLite's FTS5 full-text index. Words are reduced to their stems, so "running" matches "run". The query's words are joined with OR. Very common words such as "the" and "in" are dropped. Search operators typed in the query are treated as plain text.
- **Meaning-based search** compares the query's vector with every chunk's vector, as described above.
- **Each document counts once.** Both searches keep each document's best-matching chunk and take the top 100 documents.
- **Merging** uses reciprocal rank fusion. A document's score is `1 / (60 + rank)` for each search that found it, added together. It uses ranks rather than raw scores because BM25 scores and cosine similarities are on different scales. Ranked first by both searches, a document scores 2/61 ≈ 0.0328. Ranked first by only one, it scores 1/61 ≈ 0.0164.
- **The text shown** is the document's best-matching chunk, preferring the meaning-based search's choice when both found it.
- **Modes.** `--mode hybrid` (the default) runs both searches. `--mode keyword` runs only BM25, and `--mode semantic` runs only the vector search.
- **Filters** from `--where` run before either search, so a filtered search only looks at matching documents.
- **Result count.** The CLI shows 1 result by default (`-k` for more). The Go library returns 10 by default (`TopK`).
- **Reading instead of searching.** `tennis search --where ...` with no query prints every matching document in full, oldest first, instead of ranking anything.

# Contributing

You need Go 1.25 or newer, on macOS or Linux.

1. Clone the repository and download the model the tests use. Without it, about 50 tests are skipped.

   ```bash
   git clone https://github.com/satoricorp/tennis
   cd tennis
   scripts/fetch-test-model.sh
   ```

2. Make your change, then run the same checks CI runs:

   ```bash
   go vet ./...
   test -z "$(gofmt -l $(git ls-files '*.go'))"
   go test ./...
   CGO_ENABLED=0 go build -o tennis ./cmd/tennis
   ```

3. Write the commit subject as `area: what changed`, in lowercase, where the area is `cli`, `docs`, `www`, or `infra`. Use the body to explain why.

Where things are:

| Path | What it is |
|---|---|
| `*.go` (repository root) | the Go library: storage, chunking, search, filters |
| `embed/` | the embedding models: built-in and OpenAI |
| `summarize/` | the model calls that write card summaries |
| `cmd/tennis/` | the command-line tool: importers, file readers, cards, the HTTP server |
| `examples/basic/` | the Go example above, compiled by CI so it stays correct |
| `docs/` | the documentation site (Next.js): `cd docs && npm install && npm run dev` |
| `www/` | the project website (Next.js) |
| `infra/` | the download host for releases (AWS CDK) |
| `scripts/` | the installer and CI helper scripts |

Ask questions in [Discord](https://discord.gg/JpAggvxJJ) or [open an issue](https://github.com/satoricorp/tennis/issues).

# Thanks

- [Minish Lab](https://github.com/MinishLab/model2vec), for model2vec and the potion models that make embeddings possible without a neural network.
- [modernc.org/sqlite](https://gitlab.com/cznic/sqlite), for SQLite in pure Go, which keeps Tennis a single binary.
- [SQLite](https://sqlite.org) and its [FTS5](https://sqlite.org/fts5.html) extension, which does the keyword search.
- [sugarme/tokenizer](https://github.com/sugarme/tokenizer), for Hugging Face tokenizers in Go.
- [sqlite-vec](https://github.com/asg017/sqlite-vec) by Alex Garcia, for the idea that vectors can live in a SQLite file you own.
- Cormack, Clarke, and Büttcher, for [reciprocal rank fusion](https://plg.uwaterloo.ca/~gvcormac/cormacksigir09-rrf.pdf).
- [Poppler](https://poppler.freedesktop.org), for `pdftotext`.
