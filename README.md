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

Tennis is a CLI for indexing and searching context locally. Context can include files, chat history, and so on, and it embeds and stores everything locally in SQLite. Each search is a merged hybrid of keyword and semantic searches.

# Getting Started

## Install

Tennis runs on macOS and Linux.

**Option 1: with Go** (needs Go 1.25 or newer)

```bash
go install github.com/satoricorp/tennis/cmd/tennis@latest
```

This puts `tennis` in `$(go env GOPATH)/bin`. That folder must be on your `PATH`.


**Option 2: build from source**

```bash
git clone https://github.com/satoricorp/tennis
cd tennis
CGO_ENABLED=0 go build -o tennis ./cmd/tennis
```

NOTE: `CGO_ENABLED=0` makes one self-contained binary with no C libraries.

```bash
tennis version
```

The first time you index something, Tennis downloads it (about 123MB) into `~/.cache/tennis`. It also uses the network to summarize cards when `ANTHROPIC_API_KEY` or `OPENAI_API_KEY` is set, and for every `add` and `search` in an `--openai` namespace.

To read PDFs on Linux, also install `pdftotext` (for example `sudo apt install poppler-utils`). On a Mac, Tennis defaults to `pdftotext`, with the Spotlight importer as a fallback.

# Examples

## Basic Example

Index a folder of notes:

```bash
$ tennis add ~/Documents/notes
tennis: created namespace "context" bound to builtin:potion-retrieval-32M
tennis: summarizing cards with anthropic:claude-opus-5
tennis: /Users/you/Documents/notes: reading plain files
imported 3, skipped 0 unchanged, 3 chunks in "context", 3 cards in /Users/you/tennis
```

Without `ANTHROPIC_API_KEY` or `OPENAI_API_KEY`, that line is `no ANTHROPIC_API_KEY or OPENAI_API_KEY; cards will carry the opening message instead of a summary`.

What happened:

- Tennis created a **namespace** called `context`. A namespace is a separate collection of documents, and `context` is the default one.
- It read the 3 files, split them into chunks, and stored them in `~/.tennis/db.sqlite`.
- It wrote one markdown summary **card** per file into `~/tennis`. These are human readable markdown files you can explore to understand what you've indexed without any tools.

Run search:

```bash
$ tennis search "keep me signed in"
> # Session handling

  Make the login flow remember the user between sessions. The session cookie is
  set with an expiry so the browser keeps it after the tab closes.

  auth.md [2026-10-08] 0.0328
```

You get the best match, printed in full. The last line includes the file name, date the file was edited, and a confidence score.

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

Note the tag in these examples. `kw#1` denotes that keyword search ranked higher than semantic search, where `sem#2` means semantic search ranked it second.

Running `add` multiple times is non-destructive. Tennis skips any files that 1. it already knows about and 2. haven't changed:

```bash
$ tennis add ~/Documents/notes
tennis: summarizing cards with anthropic:claude-opus-5
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

Some notes on what these commands do.

Chat history is stored one message per document, so a search finds the exact message. `tennis ls` groups messages back into one row per conversation.

Every document has attributes you can filter with `--where`. Join comparisons with commas (all must match): `=`, `!=`, `>`, `>=`, `<`, `<=`. Quote a value that contains a comma: `--where 'path="/Users/you/Budget, 2026.md"'`.

| Documents | Attributes |
|---|---|
| Files | `path`, `name`, `size`, `modified` |
| Chat messages | `source`, `session`, `role`, `title`, `created` |
| Claude Code and Codex | also `project` (the repo the session started in), `cwd`, and `worktree` when there was one |
| Claude Code | also `branch`; a subagent's turns have `subagent` |

Every file/chat gets its own markdown card. With `ANTHROPIC_API_KEY` or `OPENAI_API_KEY` set, the card contains a short summary otherwise its the header of the document.

| Setting | Default | Change with |
|---|---|---|
| Format | detected from the contents | `--chatgpt`, `--claude`, `--claude-code`, `--codex`, `--files` |
| Database | `~/.tennis/db.sqlite` | `--db` or `$TENNIS_DB` |
| Namespace | `context` | `--ns` or `$TENNIS_NS` |
| Cards | `~/tennis` | `--cards <dir>` or `--no-cards` |

Run `tennis --help` for the main commands and `tennis --agents` for every command.

# Details

## What tennis is great for

- **Working offline.** Tennis runs on your machine and never makes network calls.
- **Searching files and chat history locally** It finds exact terms (names, IDs, error codes) and paraphrases in the same search.
- **Free, local knowledge base.** Write documents in and query them back through the Go library, a local HTTP API, or the CLI with `--json`.
- **Speed.** On an Apple M4, a search over about 117,000 chunks (74,000 chat messages) takes about 2 seconds.

## What tennis is not great for

- **Millions of documents.** Tennis is not optimized for massive document stores, and is ideal for individual power users.
- **A shared server.** Tennis doesn't have any authentication features, and should stay sandboxed on a single machine.
- **Chinese, Japanese, and Korean languages.** The keyword index can't split those languages into words, however, semantic search should work. Follow along here: [issue #1](https://github.com/satoricorp/tennis/issues/1).
- **Images, audio, and video.** Tennis indexes text only.
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
| `GET` | `/v1/namespaces` | none | list of namespaces with counts, or `null` when there are none |
| `POST` | `/v1/namespaces/{ns}/write` | `{"documents": [{"id", "text", "attributes"}]}` | `{"written", "skipped", "chunks"}` |
| `POST` | `/v1/namespaces/{ns}/query` | `{"text", "top_k", "mode", "where"}` | `{"results": [...]}` |
| `POST` | `/v1/namespaces/{ns}/delete` | `{"ids": [...]}` | `{"deleted": n}` |

Namespaces are created on the first write. `where` uses the same syntax as the CLI's `--where`.

```bash
curl -s localhost:8817/v1/namespaces/notes/write -d '{"documents": [{"id": "a1", "text": "make the login flow remember the user"}]}'
curl -s localhost:8817/v1/namespaces/notes/query -d '{"text": "keep me signed in", "top_k": 5}'
```

**3. The CLI from scripts.** Add `--json` to `search`, `ls`, `add`, or `rm` for machine-readable output on stdout. `ns list` and `ns rm` honor it too. `ns create` prints plain text. To add documents that aren't files, pipe newline-delimited JSON into `add --ndjson`, one document per line:

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

## Specifics about the Tennis vector architecture

An **embedding** is a list of numbers that represents what a piece of text means. Texts with similar meanings get similar lists. Tennis stores one embedding per chunk of text and compares them with the embedding of your query.

The default, [`potion-retrieval-32M`](https://huggingface.co/minishlab/potion-retrieval-32M) from Minish Lab, is a table of one 512-number vector per vocabulary token. Tennis splits the text into tokens, looks each one up, averages the vectors, and scales the result to length 1.

| Model | Numbers per vector | Use it with |
|---|---|---|
| `potion-retrieval-32M` | 512 | the default |
| `potion-base-8M` | 256 | `--model potion-base-8M` |
| `text-embedding-3-small` | 1536 | `--openai text-embedding-3-small` |
| `text-embedding-3-large` | 3072 | `--openai text-embedding-3-large` |

On the MTEB retrieval benchmark, `potion-retrieval-32M` scores 35.06, against 42.92 for `all-MiniLM-L6-v2`. Keyword search covers much of that gap on exact names, IDs, and code. `potion-base-8M` is the same kind of model, smaller (29MB) and less accurate. The OpenAI models need `OPENAI_API_KEY` and send your text to OpenAI.

The built-in model downloads once from Hugging Face into `~/.cache/tennis/models/`. Tennis checks the file against the SHA-256 checksum in [`embed/models.go`](embed/models.go) and refuses any non-matching files. A namespace requires the model it was created with, and opening it with another model throws an error (i.e. the vectors can't be compared). To switch, create a new namespace and add the files again.

Each document is split into chunks of about 1,000 characters that overlap by 100. Tennis attempts to find boundaries on paragraphs or sentences when possible. Documents, attributes, and chunks live in one SQLite file, `~/.tennis/db.sqlite`, with each vector stored as 32-bit floats.

A search compares the query with every chunk that passes the filters. Time grows with the number of chunks.

## Specifics about tennis search

Every search runs a keyword search and a semantic search, then merges both.

Keyword search uses [BM25](https://en.wikipedia.org/wiki/Okapi_BM25) through SQLite's FTS5 index. Words are stemmed, so "running" matches "run", and the query's words are joined with OR. Common words such as "the" are dropped, and search operators in the query are plain text. Meaning-based search compares the query's vector with every chunk's vector.

Each search keeps a document's best-matching chunk and the top 100 documents. The merged score is reciprocal rank fusion: `1 / (60 + rank)` from each search that found the document, added together. Ranks are used because BM25 and cosine similarity are on different scales. First in both scores 2/61 ≈ 0.0328. First in one scores 1/61 ≈ 0.0164. The text shown is that best chunk, preferring the meaning-based one when both searches found the document.

| Mode | What runs |
|---|---|
| `--mode hybrid` | both searches (the default) |
| `--mode keyword` | BM25 only |
| `--mode semantic` | the vector search only |

`--where` runs before either search. The CLI shows 1 result (`-k` for more). The Go library returns 10 (`TopK`). `tennis search --where ...` with no query prints every match in full, oldest first.

# Contributing

You need Go 1.25 or newer, on macOS or Linux.

1. Clone the repository and download the model the tests use. Without it, about 50 tests are skipped.

   ```bash
   git clone https://github.com/satoricorp/tennis
   cd tennis
   scripts/fetch-test-model.sh
   ```

2. Make your change. Ideally, you run the CI checks locally:

   ```bash
   go vet ./...
   test -z "$(gofmt -l $(git ls-files '*.go'))"
   go test ./...
   CGO_ENABLED=0 go build -o tennis ./cmd/tennis
   ```

3. Write good commits. If you're using Claude, please read and review your code, and make sure your commit message reflects what's changed and why.

File structure:

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

Questions and bugs go in [an issue](https://github.com/satoricorp/tennis/issues).

# Thanks

- [Minish Lab](https://github.com/MinishLab/model2vec), for model2vec and the models that make embeddings possible.
- [SQLite](https://sqlite.org) and its [FTS5](https://sqlite.org/fts5.html) extension for keyword search.
- [sugarme/tokenizer](https://github.com/sugarme/tokenizer), for tokenizers in Go.
- [modernc.org/sqlite](https://gitlab.com/cznic/sqlite), for SQLite in Go.
