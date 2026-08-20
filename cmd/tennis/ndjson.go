package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/satoricorp/tennis"
)

// maxNDJSONLineSize bounds a single NDJSON line. Synthetic documents (event
// lines, chat messages, claims) are small; this is generous headroom rather
// than a real expected size, matching the spirit of maxSeedFileSize.
const maxNDJSONLineSize = 10 << 20

// runNDJSON ingests documents that do not exist as files — the case the
// archive readers do not cover. yeet's event lines, chat turns, and claims are
// produced by a program, not read off disk, so the input is NDJSON on stdin
// rather than a path: one JSON object per line, {"id": "...", "text": "...",
// "attributes": {...}}.
//
// It writes straight to ns.Write rather than going through importPath, because
// these documents are not conversations: there is nothing to detect, no turns
// to split, and no card to render. Everything else — auto-create on first use,
// the embedder flags, the content-hash skip — is shared with the rest of add.
func runNDJSON(nsName string, o importOpts) error {
	db, err := open(o.dbPath, o.asJSON)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()

	ns, err := db.Namespace(ctx, nsName)
	switch {
	case errors.Is(err, tennis.ErrNamespaceNotFound):
		// Same ergonomics as the rest of add: create on first use, bind the
		// embedder here, enforce it on every write and query after.
		ns, err = db.CreateNamespace(ctx, nsName, tennis.NamespaceOptions{
			Model: o.model, OpenAIModel: o.openaiModel, ChunkSize: o.chunkSize,
		})
		if err != nil {
			return err
		}
		if !o.asJSON {
			fmt.Fprintf(os.Stderr, "tennis: created namespace %q bound to %s\n", nsName, ns.EmbedderID())
		}
	case err != nil:
		return err
	}

	docs, failed := readNDJSONDocs(os.Stdin, os.Stderr)

	res := &tennis.WriteResult{}
	if len(docs) > 0 {
		res, err = ns.Write(ctx, docs)
		if err != nil {
			return err
		}
	}

	if o.asJSON {
		if err := emit(map[string]any{
			"written": res.Written, "skipped": res.Skipped, "chunks": res.Chunks, "failed": failed,
		}); err != nil {
			return err
		}
	} else {
		fmt.Printf("added %d, skipped %d unchanged, %d chunks in %q", res.Written, res.Skipped, res.Chunks, nsName)
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

// ndjsonLine is the NDJSON contract: one document per line, text required,
// attributes optional and arbitrary JSON.
type ndjsonLine struct {
	ID         string         `json:"id"`
	Text       string         `json:"text"`
	Attributes map[string]any `json:"attributes"`
}

// parseNDJSONDoc decodes one NDJSON line into a Document. The error carries no
// line number — readNDJSONDocs adds that, since it is the one with a line
// counter.
func parseNDJSONDoc(raw []byte) (tennis.Document, error) {
	var in ndjsonLine
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

// readNDJSONDocs scans NDJSON from r, one document per line. A malformed line is
// reported to errOut with its 1-based line number and counted as failed, but
// never stops the scan — yeet may be piping thousands of synthetic documents,
// and one bad line should cost that line, not the batch. Blank lines are
// skipped silently; they are formatting, not content.
func readNDJSONDocs(r io.Reader, errOut io.Writer) ([]tennis.Document, int) {
	var docs []tennis.Document
	failed := 0

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxNDJSONLineSize)

	line := 0
	for scanner.Scan() {
		line++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		doc, err := parseNDJSONDoc(raw)
		if err != nil {
			failed++
			fmt.Fprintf(errOut, "tennis: ndjson: line %d: %v\n", line, err)
			continue
		}
		docs = append(docs, doc)
	}
	if err := scanner.Err(); err != nil {
		failed++
		fmt.Fprintf(errOut, "tennis: ndjson: reading input: %v\n", err)
	}
	return docs, failed
}
