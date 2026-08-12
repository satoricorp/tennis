package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/satoricorp/tennis"
	"github.com/satoricorp/tennis/embed"
)

// maxPutLineSize bounds a single NDJSON line. Synthetic documents (event
// lines, chat messages, claims) are small; this is generous headroom rather
// than a real expected size, matching the spirit of maxSeedFileSize.
const maxPutLineSize = 10 << 20

// cmdPut ingests documents that do not exist as files — the case seed does
// not cover. yeet's event lines, chat turns, and claims are produced by a
// program, not read off disk, so the input is NDJSON on stdin rather than a
// path: one JSON object per line, {"id": "...", "text": "...", "attributes":
// {...}}. Everything else — auto-create on first use, the embedder flags, the
// content-hash skip — is shared with seed via the same ns.Write call.
func cmdPut(args []string) error {
	fs_ := flag.NewFlagSet("put", flag.ExitOnError)
	dbPath := fs_.String("db", defaultDB(), "database file")
	asJSON := fs_.Bool("json", false, "machine-readable output")
	model := fs_.String("model", "", "built-in model for a new namespace (default "+embed.DefaultModel+")")
	openaiModel := fs_.String("openai", "", "use an OpenAI model instead of the built-in one (requires OPENAI_API_KEY)")
	chunkSize := fs_.Int("chunk", 0, "chunk size in characters for a new namespace")
	pos, err := parseInterleaved(fs_, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: tennis put <namespace> < documents.ndjson")
	}
	nsName := pos[0]

	db, err := open(*dbPath, *asJSON)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()

	ns, err := db.Namespace(ctx, nsName)
	switch {
	case errors.Is(err, tennis.ErrNamespaceNotFound):
		// Same ergonomics as seed: create on first use, bind the embedder here,
		// enforce it on every write and query after.
		ns, err = db.CreateNamespace(ctx, nsName, tennis.NamespaceOptions{
			Model: *model, OpenAIModel: *openaiModel, ChunkSize: *chunkSize,
		})
		if err != nil {
			return err
		}
		if !*asJSON {
			fmt.Fprintf(os.Stderr, "tennis: created namespace %q bound to %s\n", nsName, ns.EmbedderID())
		}
	case err != nil:
		return err
	}

	docs, failed := readPutDocs(os.Stdin, os.Stderr)

	res := &tennis.WriteResult{}
	if len(docs) > 0 {
		res, err = ns.Write(ctx, docs)
		if err != nil {
			return err
		}
	}

	if *asJSON {
		if err := emit(map[string]any{
			"written": res.Written, "skipped": res.Skipped, "chunks": res.Chunks, "failed": failed,
		}); err != nil {
			return err
		}
	} else {
		fmt.Printf("put %d, skipped %d unchanged, %d chunks in %q", res.Written, res.Skipped, res.Chunks, nsName)
		if failed > 0 {
			fmt.Printf(" (%d lines failed)", failed)
		}
		fmt.Println()
	}
	if failed > 0 {
		return fmt.Errorf("%d line(s) failed to parse", failed)
	}
	return nil
}

// putLine is the NDJSON contract: one document per line, text required,
// attributes optional and arbitrary JSON.
type putLine struct {
	ID         string         `json:"id"`
	Text       string         `json:"text"`
	Attributes map[string]any `json:"attributes"`
}

// parsePutDoc decodes one NDJSON line into a Document. The error carries no
// line number — readPutDocs adds that, since it is the one with a line
// counter.
func parsePutDoc(raw []byte) (tennis.Document, error) {
	var in putLine
	if err := json.Unmarshal(raw, &in); err != nil {
		return tennis.Document{}, err
	}
	if in.ID == "" {
		return tennis.Document{}, fmt.Errorf(`missing "id"`)
	}
	if in.Text == "" {
		return tennis.Document{}, fmt.Errorf(`missing "text"`)
	}
	return tennis.Document{ID: in.ID, Text: in.Text, Attributes: in.Attributes}, nil
}

// readPutDocs scans NDJSON from r, one document per line. A malformed line is
// reported to errOut with its 1-based line number and counted as failed, but
// never stops the scan — yeet may be piping thousands of synthetic documents,
// and one bad line should cost that line, not the batch. Blank lines are
// skipped silently; they are formatting, not content.
func readPutDocs(r io.Reader, errOut io.Writer) ([]tennis.Document, int) {
	var docs []tennis.Document
	failed := 0

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxPutLineSize)

	line := 0
	for scanner.Scan() {
		line++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		doc, err := parsePutDoc(raw)
		if err != nil {
			failed++
			fmt.Fprintf(errOut, "tennis: put: line %d: %v\n", line, err)
			continue
		}
		docs = append(docs, doc)
	}
	if err := scanner.Err(); err != nil {
		failed++
		fmt.Fprintf(errOut, "tennis: put: reading input: %v\n", err)
	}
	return docs, failed
}
