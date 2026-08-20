# tennis

**Local search that actually finds things. One binary, one file, no server.**

Search your notes, docs, or agent history by keyword *and* by meaning at the same time — so "keep me signed in" finds the page about session cookies, even though they share no words.

For the database-inclined: this is the sqlite-vec idea — vectors living in a SQLite file you own — plus the parts you can't easily get from sqlite-vec alone: BM25 keyword search fused with semantic search into one ranking, and the embedding model itself shipped inside the binary, so semantic search works with no pipeline and no API key. Details in [Compared to sqlite-vec](#compared-to-sqlite-vec).

- **Nothing to install.** One static binary. No Python, no Node, no Docker, no Homebrew step, no database extension to compile. Download and run.
- **Works offline, costs nothing.** The embedding model ships with it and runs on your machine. No API key, no network, no per-query bill.
- **Finds things two ways at once.** BM25 keyword search (SQLite FTS5) catches names, IDs, and rare terms. Semantic search (embedding vectors, cosine) catches paraphrases. You get one list fusing both by reciprocal rank.
- **Your data stays a file.** Everything lives in one SQLite database you can copy, back up, inspect with `sqlite3`, or delete.
- **Same on Mac and Linux.** Identical binary behavior, no platform-specific setup.
- **Use it however you want.** Command line, Go library, or a local HTTP API for every other language.
- **Won't lie to you.** If something changes that would make results silently wrong, it stops and tells you instead.

```bash
tennis add ~/Documents/notes
tennis search "keep me signed in"
```

📖 **[Documentation](https://satoricorp.github.io/tennis/docs)** · [Discord](https://discord.gg/JpAggvxJJ) · [llms.txt](https://satoricorp.github.io/tennis/docs/llms.txt)

---

## Install

Grab a binary from [Releases](https://github.com/satoricorp/tennis/releases) — it really is one file:

```bash
curl -sL https://github.com/satoricorp/tennis/releases/download/v0.1.0/tennis_0.1.0_$(uname -s | tr '[:upper:]' '[:lower:]')_$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/').tar.gz | tar xz tennis
```

Or with Go:

```bash
go install github.com/satoricorp/tennis/cmd/tennis@latest
```

Or build it:

```bash
git clone https://github.com/satoricorp/tennis && cd tennis && CGO_ENABLED=0 go build -o tennis ./cmd/tennis
```

`CGO_ENABLED=0` is not a suggestion — it is the whole point. tennis uses a pure-Go SQLite driver and a static embedding model, so the result is one self-contained binary with no shared libraries to find at runtime.

On first use it downloads the embedding model once (~123MB) to `~/.cache/tennis`. After that it never touches the network.

---

## Quick start

```bash
# index a directory (creates the namespace on first run)
$ tennis add ~/Documents/notes
tennis: created namespace "context" bound to builtin:potion-retrieval-32M
tennis: ~/Documents/notes: reading plain files
imported 3, skipped 0 unchanged, 3 chunks in "context"

# search it — one answer, in full
$ tennis search "keep me signed in"
> # Session handling
  Make the login flow remember the user between sessions. The session cookie
  is set with an expiry so the browser keeps it after the tab closes.

  auth.md [2026-08-10] 0.0328

# ask for more when comparing is the point
$ tennis search "keep me signed in" -k 2
 1. auth.md [2026-08-10] 0.0328  [kw#1 sem#1]
    # Session handling
    Make the login flow remember the user between sessions. The session cookie
    is set with an expiry so the browser keeps it after the tab closes.

 2. config.md [2026-08-10] 0.0161  [sem#2]
    # Configuration
    Write a parser for TOML configuration files. Values from the file are
    merged over the defaults.

# re-indexing is nearly free: unchanged files are never re-embedded
$ tennis add ~/Documents/notes
imported 0, skipped 3 unchanged, 0 chunks in "context"
```

One result, printed whole: the words, then where they came from and when. You asked a question, so you get an answer you can actually read, wrapped to your terminal — not ten truncated lines to squint at.

`-k` is how you ask for more, and then every result is numbered — the best one included — because at that point you are reading a ranking rather than an answer. The `[kw#1 sem#1]` tag shows which ranker found each one and where: a hit only one ranker surfaced is a different kind of answer than one both agreed on, and tennis shows you which you got.

Neither command named a namespace, so both used `context`, the default. That is the whole story until you want to keep separate things separate; then `--ns work` on either command, or `$TENNIS_NS`, does it.

Nothing to index yet? Start with the history you already have — point [`add`](#add--index-sessions-or-files) at a ChatGPT or Claude export zip, or at `~/.claude` or `~/.codex`, and search it in one command.

---

## CLI

### `add` — index sessions or files

`add` is the one way in. Point it at an export archive, a directory of local agent transcripts, or a pile of files, and it works out what it was handed:

```bash
tennis add ~/Downloads/chatgpt-export.zip     # a ChatGPT data export
tennis add ~/Downloads/claude-export.zip      # a Claude data export
tennis add ~/.claude                          # local Claude Code sessions
tennis add ~/.codex                           # local Codex sessions
tennis add ./docs                             # a directory tree of files
tennis add ./a.md ./b.md                      # specific files
```

Detection reads the source rather than the filename: the ChatGPT and Claude exports are told apart by the shape of their `conversations.json`, and the two agent transcript formats by the fields on each line of their `.jsonl` — Claude Code writes a `uuid` and `sessionId`, Codex wraps everything in a `{timestamp, type, payload}` envelope. When the guess is wrong, or you would rather be explicit, name the source:

```bash
tennis add --chatgpt ~/Downloads/export.zip
tennis add --claude ~/Downloads/export.zip
tennis add --claude-code ~/.claude
tennis add --codex ~/.codex
tennis add --files ~/Documents/notes
```

A `.zip`, an already-unzipped directory, and a single transcript are all acceptable. Other useful flags:

```bash
tennis add ./src --files --ext .go,.ts,.rs    # pick extensions (default .md,.txt)
tennis add ./docs --chunk 2000                # bigger chunks for a new namespace
tennis add ~/.codex --ns work                 # a namespace other than the default
tennis add ./docs --json                      # machine-readable
```

Importing history exists because the alternative doesn't cover the past. Capturing conversations live — pointing a client at a proxy or a custom base URL — only ever sees traffic from the moment it is configured. For ChatGPT and claude.ai, everything before that is in the export archive and nowhere else, so that archive is the thing tennis reads. Claude Code and Codex are kinder: they keep their transcripts on your disk already, under `~/.claude` and `~/.codex`, and tennis reads them where they sit.

By default each message becomes its own document, which is the unit you tend to remember; `--per conversation` makes each thread one document instead, for when the thread matters more than the line.

```bash
$ tennis add ~/Downloads/claude-export.zip
tennis: created namespace "context" bound to builtin:potion-retrieval-32M
tennis: ~/Downloads/claude-export.zip: reading a Claude export (conversations.json, projects.json)
tennis: 5000 documents in…
imported 11423, skipped 0 unchanged, 14106 chunks in "context"

$ tennis search "that thing about session cookies"
> a session cookie with a refresh token is the usual shape…
  Claude [2025-03-15] 0.0328
```

Every document carries the attributes needed to find its way home — `source`, `session`, `role`, `title`, `created`, and for local sessions `project`, `cwd` and `branch` — so a search can be narrowed the same way any other namespace can:

```bash
tennis search "retry logic" --where role=user
tennis search "flaky test" --where project=tennis,branch=main
tennis search "the timeout" --where source=codex
```

Re-running `add` is an incremental update, and re-adding the same source is free. File documents are keyed by path and skipped when contents and metadata are byte-identical to what is stored — not re-read into the model, not re-indexed — which is why re-adding a large corpus after editing one file takes about as long as indexing one file. Session documents are keyed by the export's own conversation and message IDs, so a fresh export months later writes only what is new.

`add` refuses two kinds of junk when reading plain files, with a note on stderr: files containing binary content (a PDF's raw bytes would index without erroring and quietly pollute every future ranking), and files over 10MB (at that size it's a log or a dataset, not prose). Binary detection is a NUL-byte check in the leading 8KB, the same heuristic git uses; a binary file shorter than that window with no NUL byte in it can slip through and get indexed.

Two things are deliberately left out of session imports. Local transcripts are indexed from their text and thinking, not their tool calls and tool results — those are mostly whole file reads and command output, and letting them in would mean every search ranked file contents above what was said about them. A Codex rollout records each exchange twice, once as raw model traffic carrying the harness preamble and once as the events the interface showed; tennis reads the second, because what you remember saying is what you typed. ChatGPT messages the exporter marked as hidden are skipped for the same reason: they were never on screen.

### `add --ndjson` — ingest documents that aren't files

Every other source names a path; `--ndjson` is for content that never touched disk — event lines, chat turns, agent claims, anything a program produces rather than a file. It reads newline-delimited JSON from stdin, one document per line:

```bash
echo '{"id": "e1", "text": "deploy failed with a connection timeout", "attributes": {"kind": "event", "session": "s1"}}' \
  | tennis add --ndjson --ns agents

tennis add --ndjson --ns agents < events.ndjson        # a whole batch at once
tennis add --ndjson --ns agents --openai text-embedding-3-small < events.ndjson
tennis add --ndjson --ns agents --json < events.ndjson # machine-readable
```

Each line is `{"id": "...", "text": "...", "attributes": {...}}` — `attributes` is optional, everything else follows the rest of `add`: the namespace is created on first use bound to the builtin model (or `--openai <model>`), and a document whose text and attributes are byte-identical to what's stored is skipped rather than re-embedded. No cards are written, because these documents are not conversations.

A line that isn't valid JSON, or is missing `id` or `text`, is reported to stderr with its line number and does not stop the batch; the command exits nonzero if any line failed.

```
$ tennis add --ndjson --ns agents --json < events.ndjson
tennis: ndjson: line 14: missing "id"
{"written": 40, "skipped": 3, "chunks": 51, "failed": 1}
```

### `search` — search

```bash
tennis search "exponential backoff"
tennis search "keep me signed in" -k 5             # top 5, not just the best
tennis search "retry" --mode keyword               # BM25 only
tennis search "retry" --mode semantic              # vectors only
tennis search "auth" --where 'status=merged'       # filter by attribute
tennis search "auth" --where 'cost>5,status!=failed'
tennis search "auth" --ns work                     # a namespace other than the default
tennis search "auth" --json                        # full results with scores and attributes
```

`--json` output includes each hit's stored `attributes`, not just its text and score:

```json
[
  {
    "id": "e1",
    "score": 0.0328,
    "text": "deploy failed with a connection timeout",
    "attributes": {"kind": "event", "session": "s1"},
    "keyword_rank": 1,
    "semantic_rank": 1
  }
]
```

### `ns` — namespaces

```bash
tennis ns list
tennis ns create agents                               # built-in model
tennis ns create agents --openai text-embedding-3-small
tennis ns rm agents                                   # and every document in it
```

```
$ tennis ns list
NAMESPACE            EMBEDDER                               DIMS     DOCS   CHUNKS
notes                builtin:potion-retrieval-32M            512      412     1908
agents               openai:text-embedding-3-small          1536       88      340
```

`ns rm` removes the namespace and everything written to it. It says what that
costs before it does it:

```
$ tennis ns rm agents
tennis: removing "agents" takes its 88 documents (340 chunks) with it
removed "agents"
```

### `rm`, `serve`

```bash
tennis rm /abs/path/to/auth.md                 # delete documents from the default namespace
tennis rm e1 e2 --ns agents                    # ...or from a named one
tennis serve                                   # local HTTP API on 127.0.0.1:8817
```

### Common flags

| Flag | Meaning |
|---|---|
| `--db <path>` | database file (default `~/.tennis/db.sqlite`, or `$TENNIS_DB`) |
| `--ns <name>` | namespace for `add`, `search` and `rm` (default `context`, or `$TENNIS_NS`) |
| `--json` | machine-readable output on stdout; progress goes to stderr |
| `-k <n>` | how many results (`search` only, default 1; `-n` is an alias) |
| `--mode` | `hybrid` (default), `keyword`, `semantic` |
| `--where` | attribute filter: `key=value`, `key>value`, `key!=value`, comma-separated |
| `--format` | override source detection (`add` only): `chatgpt`, `claude`, `claude-code`, `codex`, `files` |
| `--per` | document granularity (`add` only): `turn` (default) or `conversation` |

### Naming the namespace positionally

`add` and `search` take the namespace as a flag so that the common case needs no namespace at all. Three older commands take it as the first argument instead, and still work:

```bash
tennis seed notes ./docs                       # like tennis add --files ./docs --ns notes
tennis import history ~/Downloads/export.zip   # like tennis add ~/Downloads/export.zip --ns history
tennis match notes "keep me signed in"         # like tennis search "keep me signed in" --ns notes
```

`put`, `get`, and `rm` name the namespace positionally too.

---

## Go SDK

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/satoricorp/tennis"
)

func main() {
    ctx := context.Background()

    db, err := tennis.Open("~/.tennis/db.sqlite")
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()

    // The embedder is chosen here, once, and bound to the namespace forever.
    ns, err := db.CreateNamespace(ctx, "agents", tennis.NamespaceOptions{})
    if err != nil {
        log.Fatal(err)
    }

    _, err = ns.Write(ctx, []tennis.Document{
        {
            ID:         "a1",
            Text:       "make the login flow remember the user between sessions",
            Attributes: map[string]any{"status": "merged", "cost": 4},
        },
        {
            ID:         "a2",
            Text:       "write a parser for TOML configuration files",
            Attributes: map[string]any{"status": "open", "cost": 8},
        },
    })
    if err != nil {
        log.Fatal(err)
    }

    results, err := ns.Query(ctx, tennis.Query{
        Text:   "keep me signed in",
        TopK:   5,
        Filter: tennis.Eq("status", "merged"),
    })
    if err != nil {
        log.Fatal(err)
    }

    for _, r := range results {
        fmt.Printf("%.4f  %s  (kw#%d sem#%d)\n  %s\n",
            r.Score, r.ID, r.KeywordRank, r.SemanticRank, r.Text)
    }
}
```

### Opening an existing namespace

```go
ns, err := db.Namespace(ctx, "agents")   // rebuilds the bound embedder
if err != nil {
    log.Fatal(err)   // including: bound to a model you can no longer load
}
fmt.Println(ns.EmbedderID())   // builtin:potion-retrieval-32M
```

### Filters

```go
tennis.Eq("status", "merged")
tennis.NotEq("status", "failed")
tennis.In("status", "merged", "open")
tennis.Gt("cost", 5)
tennis.Gte("cost", 5)
tennis.Lt("cost", 100)
tennis.Lte("cost", 100)
tennis.Glob("path", "*/internal/*")

tennis.And(tennis.Eq("status", "merged"), tennis.Gt("cost", 5))
tennis.Or(tennis.Eq("status", "merged"), tennis.Eq("status", "open"))
```

Filters run before ranking, so a filtered query is faster than an unfiltered one.

### Search modes

```go
ns.Query(ctx, tennis.Query{Text: "retry backoff"})                        // hybrid (default)
ns.Query(ctx, tennis.Query{Text: "retry backoff", Mode: tennis.Keyword})  // BM25 only
ns.Query(ctx, tennis.Query{Text: "retry backoff", Mode: tennis.Semantic}) // vectors only

// tuning knobs — leave these alone unless you are measuring something
ns.Query(ctx, tennis.Query{
    Text:           "retry backoff",
    RRFK:           60,   // fusion constant; lower favors each ranker's top hits
    CandidateDepth: 100,  // chunks per ranker before fusion
})
```

### Deleting and updating

```go
// Writing the same ID again replaces the document and reindexes it.
ns.Write(ctx, []tennis.Document{{ID: "a1", Text: "new text"}})

n, err := ns.Delete(ctx, []string{"a1", "a2"})   // n = how many existed

doc, err := ns.Get(ctx, "a1")
```

---

## HTTP API — for every other language

```bash
tennis serve                       # 127.0.0.1:8817
tennis serve --addr 127.0.0.1:9000
```

```bash
# write
curl -s localhost:8817/v1/namespaces/notes/write -d '{
  "documents": [
    {"id": "a1", "text": "make the login flow remember the user",
     "attributes": {"status": "merged"}}
  ]
}'
# {"written":1,"skipped":0,"chunks":1}

# query
curl -s localhost:8817/v1/namespaces/notes/query -d '{
  "text": "keep me signed in", "top_k": 5, "where": "status=merged"
}'
# {"results":[{"id":"a1","score":0.0164,"text":"...","attributes":{"status":"merged"},"keyword_rank":0,"semantic_rank":1}]}

# list namespaces
curl -s localhost:8817/v1/namespaces

# delete
curl -s localhost:8817/v1/namespaces/notes/delete -d '{"ids": ["a1"]}'
```

Python client, in full:

```python
import requests

BASE = "http://localhost:8817/v1"

def write(ns, docs):
    return requests.post(f"{BASE}/namespaces/{ns}/write", json={"documents": docs}).json()

def match(ns, text, top_k=10, where=""):
    r = requests.post(f"{BASE}/namespaces/{ns}/query",
                      json={"text": text, "top_k": top_k, "where": where})
    r.raise_for_status()
    return r.json()["results"]

write("notes", [{"id": "a1", "text": "make the login flow remember the user"}])
for hit in match("notes", "keep me signed in"):
    print(f'{hit["score"]:.4f}  {hit["id"]}  {hit["text"][:60]}')
```

TypeScript client, in full:

```ts
const BASE = "http://localhost:8817/v1";

export async function write(ns: string, documents: unknown[]) {
  const r = await fetch(`${BASE}/namespaces/${ns}/write`, {
    method: "POST",
    body: JSON.stringify({ documents }),
  });
  if (!r.ok) throw new Error(await r.text());
  return r.json();
}

export async function match(ns: string, text: string, topK = 10, where = "") {
  const r = await fetch(`${BASE}/namespaces/${ns}/query`, {
    method: "POST",
    body: JSON.stringify({ text, top_k: topK, where }),
  });
  if (!r.ok) throw new Error(await r.text());
  return (await r.json()).results;
}

await write("notes", [{ id: "a1", text: "make the login flow remember the user" }]);
console.log(await match("notes", "keep me signed in"));
```

The server binds to loopback and has no authentication, because it is an interface to a local file rather than a service. `--addr` will bind anywhere you tell it to; don't, unless you have put something in front of it.

---

## Embedders

### The built-in model

`potion-retrieval-32M`, a static embedding model: a lookup table of vectors rather than a neural network. Embedding a string is a tokenize, a table lookup, and an average — no inference engine, no ONNX runtime, no cgo. That is what lets tennis be one static binary.

It is genuinely weaker than a large hosted model — roughly 82% of `all-MiniLM-L6-v2` on retrieval benchmarks, which is itself behind OpenAI's. In a hybrid ranking that gap narrows considerably, because the queries a static model handles worst (exact identifiers, rare tokens, code symbols) are exactly the ones BM25 handles best.

It also has no context window, so unlike a transformer-based embedder it will never silently ignore everything past token 512.

`potion-base-8M` (29MB, 256 dims) is available via `--model potion-base-8M` if download size matters more than quality.

### Turning on OpenAI

```bash
export OPENAI_API_KEY=sk-...
tennis ns create notes --openai text-embedding-3-small
```

```go
db.CreateNamespace(ctx, "notes", tennis.NamespaceOptions{
    OpenAIModel: "text-embedding-3-small",
})
```

**You have to ask for this.** tennis will not check for `OPENAI_API_KEY` and quietly use it.

That restraint is deliberate. Vectors from two different models live in different spaces and different dimensions — a cosine between them is not a worse score, it is a meaningless one. If the embedder were chosen by whichever environment variables happened to be set, a namespace indexed with a key present and later queried from a cron job, a different shell, or CI would return confident nonsense. No error, no crash, just wrong answers that look right. That is the worst failure a search tool can have, so it is designed out rather than documented around.

Choosing OpenAI also gives up two of the three things tennis promises: it is no longer offline, and no longer free.

### How the binding is enforced

Every namespace records its embedder's ID and dimension at creation. Every later open verifies the live embedder against those, and refuses on mismatch:

```
namespace "notes" was indexed with builtin:potion-retrieval-32M (512 dims) but the
loaded embedder is openai:text-embedding-3-small (1536 dims); reindex the
namespace or restore the original model
```

To change models, create a new namespace and re-index it. That is a real cost, and it is meant to be — it makes the expensive operation visible instead of letting it happen by accident.

---

## How it works

```
your text
   ├── chunked (~1000 chars, 100 overlap, split on paragraph/sentence boundaries)
   ├── FTS5 index ────────► BM25 ranking ──┐
   └── static embedder ───► cosine ranking ─┴──► reciprocal rank fusion ──► results
                                                 stored in one SQLite file
```

**Why fuse by rank instead of by score.** BM25 scores and cosine similarities are on incomparable scales. Normalizing them into a weighted sum requires inventing an opinion about how a 0.7 cosine compares to a BM25 of −1.3, and that opinion is wrong for some corpus. Positions in a ranking are directly comparable and need no calibration, which is why reciprocal rank fusion is the default everywhere it's used.

**Why brute-force vector search.** There is no approximate index. At 512 dimensions a single core compares a few million vectors per second, so exact search stays comfortably fast well past the point where you would move to a server anyway. Exactness means no recall cliff, no index to tune, and no parameters that quietly degrade results as the corpus grows.

**Why chunking, given the model has no context limit.** A vector is the average of its tokens, so the longer the text the more it converges on the average of the language rather than the meaning of the passage. A ten-page document embeds to roughly nothing in particular. Chunking keeps each vector about one idea.

**Why the FTS triggers matter.** SQLite's external-content FTS5 tables do not follow their source table automatically. Without triggers the index silently drifts on every update and delete — deleted documents keep matching, stale text keeps being returned, and nothing errors. tennis creates those triggers in the first migration, and there is a regression test that updates and deletes a document and asserts the old terms stop matching.

---

## Compared to sqlite-vec

[sqlite-vec](https://github.com/asg017/sqlite-vec) answers "where do my vectors live" — and deliberately leaves everything else to you. tennis makes the same core commitment (your index is one SQLite file you can open with `sqlite3`, copy, back up, or delete) and builds in the layers you would otherwise assemble around it:

| | sqlite-vec | tennis |
|---|---|---|
| Vector storage, exact KNN | yes | yes |
| Embeddings | bring your own | model ships in the binary, runs offline |
| Keyword search | separate FTS5 setup | BM25 built in |
| Hybrid ranking | hand-written fusion SQL | reciprocal rank fusion by default |
| Loading it | compiled C extension (in Go: cgo) | pure Go, `CGO_ENABLED=0`, one static binary |
| Chunking, incremental indexing | yours to write | built in |
| Embedder/index mismatch | silently wrong results | refused at open, by design |

If you want raw SQL over vectors inside a database you already have, use sqlite-vec — it is very good at exactly that. tennis is for when you want the whole retrieval loop — embed, chunk, index, fuse, filter — working in one command.

## What it isn't

- **Not an approximate-nearest-neighbor engine.** Exact scan only. If you have millions of vectors and need sub-millisecond search, use a vector database.
- **Not a server.** No auth, no multi-tenancy, no replication. It's a file.
- **Not a reranker.** Results come from BM25 and cosine fused; there is no cross-encoder pass.
- **Not a relevance threshold.** Semantic search ranks everything, so a query always returns up to `TopK` results even when nothing is a good match. Use the score and the `kw#/sem#` tags to judge; a result found by only one ranker with a low score is usually noise.
- **Not multimodal.** Text only.
- **Not good at CJK keyword search.** The FTS5 tokenizer does not segment Chinese/Japanese/Korean, so the keyword ranker finds nothing for CJK queries; semantic search still works. Fixing this properly (a trigram tokenizer) changes the index schema, so it is tracked as an issue rather than patched quietly.

---

## Development

```bash
go test ./...          # requires the model in testdata/cache; skips cleanly without it
go vet ./...
CGO_ENABLED=0 go build -o tennis ./cmd/tennis
```

The embedder has a correctness test that compares its output against the reference Python `model2vec` implementation vector-for-vector — cosine 1.00000000, max absolute difference 8e-9. A pooling bug or a vocabulary off-by-one produces embeddings that are merely mediocre rather than obviously broken, so this is checked against ground truth rather than eyeballed.

Model downloads are verified against SHA-256 checksums pinned in the source; a changed upstream file is refused rather than silently embedded into every vector the install ever produces.

Tests that need the 123MB model weights skip themselves when the weights are absent, so CI runs green without downloading them; the full suite (including the reference validation and fuzzing) runs wherever the weights exist.

## License

MIT
