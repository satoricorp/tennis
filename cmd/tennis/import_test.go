package main

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- fixtures ---------------------------------------------------------------

// A ChatGPT export in miniature: a message graph with a root node carrying no
// message, a hidden system message, and one branch.
const chatgptExport = `[
  {
    "title": "Deploy timeout",
    "conversation_id": "c1",
    "create_time": 1712345678.5,
    "mapping": {
      "root": {"message": null, "parent": null, "children": ["n1"]},
      "n1": {"message": {"id": "m1", "author": {"role": "system"}, "content": {"content_type": "text", "parts": ["you are helpful"]},
             "metadata": {"is_visually_hidden_from_conversation": true}}, "parent": "root", "children": ["n2"]},
      "n2": {"message": {"id": "m2", "author": {"role": "user"}, "create_time": 1712345679,
             "content": {"content_type": "text", "parts": ["the deploy failed with a connection timeout"]}}, "parent": "n1", "children": ["n3"]},
      "n3": {"message": {"id": "m3", "author": {"role": "assistant"},
             "content": {"content_type": "multimodal_text", "parts": [{"content_type": "image_asset_pointer"}, {"text": "retry with exponential backoff"}]}},
             "parent": "n2", "children": []},
      "orphan": {"message": {"id": "m4", "author": {"role": "user"},
             "content": {"content_type": "code", "text": "SELECT 1"}}, "parent": "gone", "children": []}
    }
  }
]`

const claudeExport = `[
  {
    "uuid": "cc1",
    "name": "Session cookies",
    "created_at": "2026-01-02T03:04:05.123456Z",
    "chat_messages": [
      {"uuid": "u1", "sender": "human", "created_at": "2026-01-02T03:04:06.000000Z", "text": "",
       "content": [{"type": "text", "text": "keep me signed in between sessions"}],
       "attachments": [{"file_name": "auth.md", "extracted_content": "TOKEN_TTL is 900 seconds"}]},
      {"uuid": "a1", "sender": "assistant", "content": [], "text": "use a session cookie with a refresh token"}
    ]
  }
]`

const claudeProjects = `[
  {"uuid": "p1", "name": "tennis", "description": "local hybrid search",
   "docs": [{"uuid": "d1", "filename": "spec.md", "content": "chunks overlap by 100 characters"}]}
]`

const claudeCodeSession = `{"type":"summary","summary":"Fixing the flaky auth test","leafUuid":"L1"}
{"type":"user","uuid":"u1","sessionId":"S1","timestamp":"2026-08-01T10:00:00.000Z","cwd":"/Users/joe/git/tennis","gitBranch":"main","message":{"role":"user","content":"the auth test is flaky"}}
{"type":"assistant","uuid":"a1","sessionId":"S1","timestamp":"2026-08-01T10:00:05.000Z","message":{"role":"assistant","content":[{"type":"text","text":"look at the token refresh window"},{"type":"tool_use","name":"Read","input":{"file":"auth.go"}}]}}
{"type":"user","uuid":"t1","sessionId":"S1","message":{"role":"user","content":[{"type":"tool_result","content":"package auth\nfunc Refresh() {}\n"}]}}
{"type":"assistant","uuid":"s1","isSidechain":true,"sessionId":"S1","message":{"role":"assistant","content":[{"type":"text","text":"subagent found the race in the clock"}]}}
{"type":"user","uuid":"meta1","isMeta":true,"sessionId":"S1","message":{"role":"user","content":"<command-name>/clear</command-name>"}}
`

// A Codex rollout in miniature. Every record shares the {timestamp, type,
// payload} envelope, the conversation is carried on the event_msg channel, and
// the response_item channel repeats it with the harness preamble prepended —
// which is exactly what must not be indexed.
const codexSession = `{"timestamp":"2026-06-09T19:25:07.702Z","type":"session_meta","payload":{"id":"S9","timestamp":"2026-06-09T19:24:52.635Z","cwd":"/Users/joe/git/gx","originator":"Codex Desktop"}}
{"timestamp":"2026-06-09T19:25:07.716Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}
{"timestamp":"2026-06-09T19:25:07.719Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions\nnever index me"}]}}
{"timestamp":"2026-06-09T19:25:08.000Z","type":"event_msg","payload":{"type":"user_message","client_id":"c1","message":"what hotel did we stay at in Mexico"}}
{"timestamp":"2026-06-09T19:25:20.000Z","type":"event_msg","payload":{"type":"agent_message","message":"You stayed at Hotel Esencia in Tulum.","phase":"commentary"}}
{"timestamp":"2026-06-09T19:25:21.000Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{}"}}
{"timestamp":"2026-06-09T19:25:22.000Z","type":"event_msg","payload":{"type":"token_count","total":123}}
`

// writeZip builds a zip from a name -> content map and returns its path.
func writeZip(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := zip.NewWriter(f)
	for name, body := range files {
		e, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

// collect runs an archive through detection and the matching adapter, and
// returns the documents that would have been written.
func collect(t *testing.T, path, format, per string) []docRecord {
	t.Helper()
	return collectWarn(t, path, format, per, func(string) {})
}

// collectWarn is collect with the warnings visible, for the cases where what
// the import declined to say is the thing under test.
func collectWarn(t *testing.T, path, format, per string, warn func(string)) []docRecord {
	t.Helper()
	sink := &docSink{}
	var recs []docRecord
	sink.capture = func(id, text string, attrs map[string]any) {
		recs = append(recs, docRecord{ID: id, Text: text, Attrs: attrs})
	}
	if _, err := importPath(path, format, per, ".md,.txt", sink, warn, true); err != nil {
		t.Fatalf("importPath(%s, %s): %v", path, format, err)
	}
	return recs
}

type docRecord struct {
	ID    string
	Text  string
	Attrs map[string]any
}

func (r docRecord) attr(k string) string {
	s, _ := r.Attrs[k].(string)
	return s
}

// --- detection --------------------------------------------------------------

func TestDetectFormats(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"chatgpt export", map[string]string{"conversations.json": chatgptExport, "chat.html": "<html>"}, formatChatGPT},
		{"claude export", map[string]string{"conversations.json": claudeExport, "users.json": "[]"}, formatClaude},
		{"claude code transcripts", map[string]string{"projects/repo/S1.jsonl": claudeCodeSession}, formatClaudeCode},
		// Both agent formats are directories of JSONL, so the sniffers have to
		// tell them apart rather than settle for "looks like a transcript".
		{"codex transcripts", map[string]string{"sessions/rollout-S9.jsonl": codexSession}, formatCodex},
		{"plain files", map[string]string{"notes/a.md": "# hello", "notes/b.txt": "world"}, formatFiles},
		{"nested export still found", map[string]string{"export-2026/conversations.json": claudeExport}, formatClaude},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, err := openArchive(writeZip(t, "export.zip", c.files))
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			pl, err := a.detect(formatAuto)
			if err != nil {
				t.Fatal(err)
			}
			if pl.format != c.want {
				t.Errorf("detected %q, want %q", pl.format, c.want)
			}
		})
	}
}

// A payload at the root must win over a copy buried in a backup folder,
// otherwise a re-export saved inside an older one silently imports the wrong
// history.
func TestDetectPrefersShallowestPayload(t *testing.T) {
	a, err := openArchive(writeZip(t, "export.zip", map[string]string{
		"conversations.json":            claudeExport,
		"old/backup/conversations.json": chatgptExport,
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	pl, err := a.detect(formatAuto)
	if err != nil {
		t.Fatal(err)
	}
	if pl.format != formatClaude || pl.payload != "conversations.json" {
		t.Errorf("got %+v, want the root claude export", pl)
	}
}

func TestSniffConversationsRejectsNonExports(t *testing.T) {
	for _, in := range []string{`{"not":"an array"}`, `[]`, `[{"id":"x"}]`, `not json`} {
		if got, err := sniffConversations(strings.NewReader(in)); err == nil {
			t.Errorf("sniff(%q) = %q, want an error", in, got)
		}
	}
}

// --- format adapters --------------------------------------------------------

func TestImportChatGPT(t *testing.T) {
	recs := collect(t, writeZip(t, "chatgpt.zip", map[string]string{"conversations.json": chatgptExport}), formatAuto, perTurn)

	if len(recs) != 3 {
		t.Fatalf("got %d documents, want 3 (hidden system message dropped, root node has none): %+v", len(recs), recs)
	}
	// Depth-first from the root keeps reading order; the orphaned node comes
	// after, rather than being lost.
	wantIDs := []string{"chatgpt:c1:m2", "chatgpt:c1:m3", "chatgpt:c1:m4"}
	for i, want := range wantIDs {
		if recs[i].ID != want {
			t.Errorf("document %d: id %q, want %q", i, recs[i].ID, want)
		}
	}
	if !strings.Contains(recs[0].Text, "connection timeout") {
		t.Errorf("first turn text: %q", recs[0].Text)
	}
	// A multimodal message: the image pointer contributes nothing, the text
	// part is all there is to index.
	if recs[1].Text != "retry with exponential backoff" {
		t.Errorf("multimodal turn text: %q", recs[1].Text)
	}
	// content_type "code" carries its body in "text", not in "parts".
	if recs[2].Text != "SELECT 1" {
		t.Errorf("code turn text: %q", recs[2].Text)
	}
	if got := recs[0].attr("role"); got != "user" {
		t.Errorf("role: %q", got)
	}
	if got := recs[0].attr("session"); got != "c1" {
		t.Errorf("session: %q", got)
	}
	if got := recs[0].attr("title"); got != "Deploy timeout" {
		t.Errorf("title: %q", got)
	}
	if got := recs[0].attr("created"); !strings.HasPrefix(got, "2024-04-05T") {
		t.Errorf("created: %q, want the epoch converted to RFC3339", got)
	}
}

func TestImportClaude(t *testing.T) {
	recs := collect(t, writeZip(t, "claude.zip", map[string]string{
		"conversations.json": claudeExport,
		"projects.json":      claudeProjects,
	}), formatAuto, perTurn)

	if len(recs) != 4 {
		t.Fatalf("got %d documents, want 4 (2 messages + project description + project doc): %+v", len(recs), recs)
	}
	// "human" is normalized so that --where role=user means one thing across
	// every source.
	if got := recs[0].attr("role"); got != "user" {
		t.Errorf("sender human should normalize to user, got %q", got)
	}
	// An attachment is part of what was said, and carries the rare terms.
	if !strings.Contains(recs[0].Text, "TOKEN_TTL") || !strings.Contains(recs[0].Text, "keep me signed in") {
		t.Errorf("attachment content missing from turn: %q", recs[0].Text)
	}
	// The second message has empty content blocks and a flat "text" field.
	if recs[1].Text != "use a session cookie with a refresh token" {
		t.Errorf("flat-text fallback: %q", recs[1].Text)
	}
	if got := recs[0].attr("created"); got != "2026-01-02T03:04:06Z" {
		t.Errorf("created: %q, want normalized RFC3339", got)
	}

	var projectDocs int
	for _, r := range recs {
		if r.attr("kind") == "project_doc" {
			projectDocs++
			if r.attr("project") != "tennis" {
				t.Errorf("project attribute: %q", r.attr("project"))
			}
		}
	}
	if projectDocs != 2 {
		t.Errorf("project documents: got %d, want 2", projectDocs)
	}
}

func TestImportClaudeCode(t *testing.T) {
	recs := collect(t, writeZip(t, "sessions.zip", map[string]string{
		"projects/-Users-joe-git-tennis/S1.jsonl": claudeCodeSession,
	}), formatAuto, perTurn)

	if len(recs) != 4 {
		t.Fatalf("got %d documents, want 4 (summary, user, assistant, subagent): %+v", len(recs), recs)
	}
	if got := recs[0].attr("role"); got != "summary" {
		t.Errorf("first document role: %q", got)
	}
	if got := recs[0].attr("title"); got != "Fixing the flaky auth test" {
		t.Errorf("the summary should become the session title, got %q", got)
	}
	if recs[1].ID != "claude-code:S1:u1" {
		t.Errorf("document id: %q", recs[1].ID)
	}
	if got := recs[1].attr("branch"); got != "main" {
		t.Errorf("branch attribute: %q", got)
	}
	if got := recs[1].attr("project"); got != "-Users-joe-git-tennis" {
		t.Errorf("project attribute: %q", got)
	}
	// A message whose only block is a tool_use keeps its text and drops the
	// tool call; a message that is nothing but a tool_result is dropped whole.
	if recs[2].Text != "look at the token refresh window" {
		t.Errorf("assistant text: %q", recs[2].Text)
	}
	for _, r := range recs {
		if strings.Contains(r.Text, "package auth") {
			t.Errorf("tool_result content was indexed: %q", r.Text)
		}
		if strings.Contains(r.Text, "/clear") {
			t.Errorf("isMeta line was indexed: %q", r.Text)
		}
	}
	if got := recs[3].attr("role"); got != "assistant/subagent" {
		t.Errorf("sidechain role: %q", got)
	}
}

func TestImportCodex(t *testing.T) {
	recs := collect(t, writeZip(t, "codex.zip", map[string]string{
		"sessions/rollout-2026-06-09T14-24-52-S9.jsonl": codexSession,
	}), formatAuto, perTurn)

	if len(recs) != 2 {
		t.Fatalf("got %d documents, want 2 (one user, one assistant): %+v", len(recs), recs)
	}
	// session_meta wins over the filename, so a renamed rollout still updates
	// the documents it wrote last time instead of duplicating them.
	if recs[0].ID != "codex:S9:line4" {
		t.Errorf("document id: %q", recs[0].ID)
	}
	if got := recs[0].attr("source"); got != formatCodex {
		t.Errorf("source attribute: %q", got)
	}
	if got := recs[0].attr("role"); got != "user" {
		t.Errorf("first document role: %q", got)
	}
	if got := recs[1].attr("role"); got != "assistant" {
		t.Errorf("second document role: %q", got)
	}
	if got := recs[0].attr("cwd"); got != "/Users/joe/git/gx" {
		t.Errorf("cwd attribute: %q", got)
	}
	if got := recs[0].attr("project"); got != "gx" {
		t.Errorf("project attribute: %q", got)
	}
	// A rollout names nothing, so the first thing asked has to serve as the label.
	if got := recs[0].attr("title"); got != "what hotel did we stay at in Mexico" {
		t.Errorf("title should come from the first user message, got %q", got)
	}
	if got := recs[0].attr("created"); got != "2026-06-09T19:25:08Z" {
		t.Errorf("created attribute: %q", got)
	}
	for _, r := range recs {
		if strings.Contains(r.Text, "AGENTS.md") || strings.Contains(r.Text, "never index me") {
			t.Errorf("the response_item preamble was indexed: %q", r.Text)
		}
		if strings.Contains(r.Text, "token_count") || strings.Contains(r.Text, "shell") {
			t.Errorf("telemetry was indexed: %q", r.Text)
		}
	}
}

// ~/.codex holds history.jsonl and session_index.jsonl next to the rollouts.
// They share the extension and nothing else, and pointing at the directory
// must not turn them into an error or into documents.
func TestImportCodexIgnoresNonRollouts(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"rollout-S9.jsonl":    codexSession,
		"history.jsonl":       `{"session_id":"S9","ts":1781033107,"text":"ls -la"}` + "\n",
		"session_index.jsonl": `{"id":"S9","path":"/tmp/rollout-S9.jsonl"}` + "\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	recs := collect(t, dir, formatCodex, perTurn)
	if len(recs) != 2 {
		t.Fatalf("got %d documents, want only the rollout's 2: %+v", len(recs), recs)
	}
	for _, r := range recs {
		if strings.Contains(r.Text, "ls -la") {
			t.Errorf("history.jsonl was indexed: %q", r.Text)
		}
	}
}

// A directory of transcripts is the same import as a zip of them — this is the
// shape ~/.claude/projects already has on disk.
func TestImportClaudeCodeFromDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "-Users-joe-git-tennis"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "-Users-joe-git-tennis", "S1.jsonl"), []byte(claudeCodeSession), 0o644); err != nil {
		t.Fatal(err)
	}
	recs := collect(t, dir, formatAuto, perTurn)
	if len(recs) != 4 {
		t.Fatalf("got %d documents, want 4: %+v", len(recs), recs)
	}
	if got := recs[1].attr("cwd"); got != "/Users/joe/git/tennis" {
		t.Errorf("cwd attribute: %q", got)
	}
}

func TestImportPerConversation(t *testing.T) {
	recs := collect(t, writeZip(t, "chatgpt.zip", map[string]string{"conversations.json": chatgptExport}), formatAuto, perConversation)
	if len(recs) != 1 {
		t.Fatalf("got %d documents, want 1 per conversation: %+v", len(recs), recs)
	}
	if recs[0].ID != "chatgpt:c1" {
		t.Errorf("id: %q", recs[0].ID)
	}
	if got := recs[0].attr("kind"); got != "conversation" {
		t.Errorf("kind: %q", got)
	}
	for _, want := range []string{"# Deploy timeout", "## user", "connection timeout", "## assistant", "exponential backoff"} {
		if !strings.Contains(recs[0].Text, want) {
			t.Errorf("rendered transcript is missing %q:\n%s", want, recs[0].Text)
		}
	}
	if n, _ := recs[0].Attrs["messages"].(int); n != 3 {
		t.Errorf("messages attribute: %v, want 3", recs[0].Attrs["messages"])
	}
}

// The fallback: a zip that is not an export at all is still worth indexing,
// under the same rules seed applies to a directory.
func TestImportPlainZipOfFiles(t *testing.T) {
	path := writeZip(t, "notes.zip", map[string]string{
		"notes/auth.md":  "# Session handling\nkeep the user signed in",
		"notes/logo.png": "\x00\x01binary",
		"notes/skip.go":  "package main",
	})
	recs := collect(t, path, formatAuto, perTurn)
	if len(recs) != 1 {
		t.Fatalf("got %d documents, want 1 (.png is binary, .go is not in --ext): %+v", len(recs), recs)
	}
	if want := path + "!notes/auth.md"; recs[0].ID != want {
		t.Errorf("id: %q, want %q", recs[0].ID, want)
	}
	if got := recs[0].attr("name"); got != "auth.md" {
		t.Errorf("name attribute: %q", got)
	}
}

// A directory's documents must land on the absolute path seed would use, so
// importing a folder and later seeding it updates one document instead of
// storing the same file twice under two IDs.
func TestImportDirectoryIDsMatchSeed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	recs := collect(t, dir, formatAuto, perTurn)
	if len(recs) != 1 {
		t.Fatalf("got %d documents, want 1", len(recs))
	}
	if want := filepath.Join(dir, "a.md"); recs[0].ID != want {
		t.Errorf("id: %q, want %q", recs[0].ID, want)
	}
}

func TestImportRejectsBadFlags(t *testing.T) {
	if err := validFormat("gemini"); err == nil {
		t.Error("unknown --format should be rejected before anything is written")
	}
	if _, err := validPer("paragraph"); err == nil {
		t.Error("unknown --per should be rejected")
	}
	if got, _ := validPer("message"); got != perTurn {
		t.Errorf("--per message should mean turn, got %q", got)
	}
}

func TestImportEmptySourceIsAnError(t *testing.T) {
	sink := &docSink{capture: func(string, string, map[string]any) {}}
	path := writeZip(t, "empty.zip", map[string]string{"readme.rst": "nothing indexable here"})
	if _, err := importPath(path, formatAuto, perTurn, ".md,.txt", sink, func(string) {}, true); err == nil {
		t.Error("an import that indexed nothing must be an error, not a silent success")
	}
}

func TestNormalizeTime(t *testing.T) {
	cases := map[string]string{
		"2026-01-02T03:04:05.123456Z": "2026-01-02T03:04:05Z",
		"2026-01-02T03:04:05Z":        "2026-01-02T03:04:05Z",
		"":                            "",
		"whenever":                    "whenever", // kept verbatim: wrong-looking beats missing
	}
	for in, want := range cases {
		if got := normalizeTime(in); got != want {
			t.Errorf("normalizeTime(%q) = %q, want %q", in, got, want)
		}
	}
	sec := 1712345678.5
	if got := epochTime(&sec); got != "2024-04-05T19:34:38Z" {
		t.Errorf("epochTime = %q", got)
	}
	if got := epochTime(nil); got != "" {
		t.Errorf("epochTime(nil) = %q", got)
	}
}

// --- end to end -------------------------------------------------------------

// The whole path through the real binary's code: a zip in, documents in
// SQLite, attributes back out of match --json. Needs the embedding model, so
// it skips cleanly when the weights are absent, like the rest of the suite.
func TestImportEndToEnd(t *testing.T) {
	cache := putTestCache(t)
	t.Setenv("TENNIS_CACHE", cache)
	t.Setenv("TENNIS_CARDS", t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "import.sqlite")
	const ns = "history"

	archivePath := writeZip(t, "claude-export.zip", map[string]string{
		"conversations.json": claudeExport,
		"projects.json":      claudeProjects,
	})

	out, err := captureStdout(t, func() error {
		return cmdImport([]string{"--db", dbPath, "--json", ns, archivePath})
	})
	if err != nil {
		t.Fatalf("import: %v\noutput: %s", err, out)
	}
	res := decodeImportResult(t, out)
	if res["written"] != float64(4) || res["skipped"] != float64(0) || res["failed"] != float64(0) {
		t.Fatalf("first import: want written=4 skipped=0 failed=0, got %v", res)
	}
	sources, _ := res["sources"].([]any)
	if len(sources) != 1 {
		t.Fatalf("sources: %v", res["sources"])
	}
	if src := sources[0].(map[string]any); src["format"] != formatClaude {
		t.Errorf("reported format: %v", src["format"])
	}

	// Re-importing the same archive must be free: same IDs, same content, so
	// every document is recognized and none is embedded again.
	out, err = captureStdout(t, func() error {
		return cmdImport([]string{"--db", dbPath, "--json", ns, archivePath})
	})
	if err != nil {
		t.Fatalf("import (repeat): %v\noutput: %s", err, out)
	}
	res = decodeImportResult(t, out)
	if res["written"] != float64(0) || res["skipped"] != float64(4) {
		t.Errorf("repeat import: want written=0 skipped=4, got %v", res)
	}

	// And the point of all of it: the history is searchable, with the
	// attributes a caller needs to jump back to the conversation.
	out, err = captureStdout(t, func() error {
		return cmdMatch([]string{"--db", dbPath, "--json", "-n", "10", ns, "keep me signed in"})
	})
	if err != nil {
		t.Fatalf("match: %v\noutput: %s", err, out)
	}
	var results []map[string]any
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("match --json did not parse: %v\noutput: %s", err, out)
	}
	if len(results) == 0 {
		t.Fatal("no matches for imported history")
	}
	var found bool
	for _, r := range results {
		if r["id"] != "claude:cc1:u1" {
			continue
		}
		found = true
		attrs, ok := r["attributes"].(map[string]any)
		if !ok {
			t.Fatalf("hit has no attributes: %v", r)
		}
		if attrs["session"] != "cc1" || attrs["role"] != "user" || attrs["source"] != formatClaude {
			t.Errorf("attributes did not round-trip: %v", attrs)
		}
	}
	if !found {
		t.Errorf("the imported user turn was not among the matches: %v", results)
	}
}

func decodeImportResult(t *testing.T, out string) map[string]any {
	t.Helper()
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("import --json output did not parse: %v\noutput: %s", err, out)
	}
	return res
}

// The site's two commands, run for real: add a source without naming a
// namespace, then ask a question without naming one, and get the answer back.
// If these ever diverge, the demo on the front page searches an empty index.
func TestAddAndSearchDefaultNamespace(t *testing.T) {
	cache := putTestCache(t)
	t.Setenv("TENNIS_CACHE", cache)
	t.Setenv("TENNIS_CARDS", t.TempDir())
	t.Setenv("TENNIS_NS", "")
	dbPath := filepath.Join(t.TempDir(), "add.sqlite")

	archivePath := writeZip(t, "codex.zip", map[string]string{
		"sessions/rollout-S9.jsonl": codexSession,
	})

	out, err := captureStdout(t, func() error {
		return cmdAdd([]string{"--db", dbPath, "--json", "--codex", archivePath})
	})
	if err != nil {
		t.Fatalf("add: %v\noutput: %s", err, out)
	}
	res := decodeImportResult(t, out)
	if res["written"] != float64(2) {
		t.Fatalf("add: want written=2, got %v", res)
	}
	sources, _ := res["sources"].([]any)
	if len(sources) != 1 {
		t.Fatalf("sources: %v", res["sources"])
	}
	if src := sources[0].(map[string]any); src["format"] != formatCodex {
		t.Errorf("--codex should select the codex format, got %v", src["format"])
	}

	out, err = captureStdout(t, func() error {
		return cmdSearch([]string{"--db", dbPath, "--json", "what hotel did we stay at in Mexico"})
	})
	if err != nil {
		t.Fatalf("search: %v\noutput: %s", err, out)
	}
	var results []map[string]any
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("search --json did not parse: %v\noutput: %s", err, out)
	}
	if len(results) == 0 {
		t.Fatal("search found nothing in the namespace add just wrote to")
	}
	attrs, ok := results[0]["attributes"].(map[string]any)
	if !ok || attrs["source"] != formatCodex {
		t.Errorf("top hit did not come from the added session: %v", results[0])
	}
}

// Two source flags is a question with no right answer, and picking one would
// import the archive as the wrong thing. It has to fail before the namespace
// is created, or a typo leaves an empty namespace bound to an embedder.
func TestAddRejectsConflictingSources(t *testing.T) {
	err := cmdAdd([]string{"--codex", "--chatgpt", "/nonexistent"})
	if err == nil {
		t.Fatal("two source flags should be an error")
	}
	if !strings.Contains(err.Error(), "pick one") {
		t.Errorf("error should say what to do about it, got %q", err)
	}
	if err := cmdAdd([]string{"--codex", "--format", "files", "/nonexistent"}); err == nil {
		t.Error("a source flag contradicting --format should be an error")
	}
	if err := cmdAdd([]string{"--codex"}); err == nil {
		t.Error("add with no path should be a usage error")
	}
}

// Resuming a Codex session writes a second session_meta naming the session it
// forked from. Letting that one win files the whole transcript under its
// parent, where its line numbers collide with the parent's own turns and
// overwrite them — 62 documents vanished from a real ~/.codex this way.
const codexResumedSession = `{"timestamp":"2026-05-31T17:39:52.000Z","type":"session_meta","payload":{"id":"CHILD","timestamp":"2026-05-31T17:39:52.000Z","cwd":"/Users/joe/git/tennis"}}
{"timestamp":"2026-05-31T17:39:52.000Z","type":"session_meta","payload":{"id":"PARENT","timestamp":"2026-05-30T15:51:43.000Z","cwd":"/Users/joe/git/other"}}
{"timestamp":"2026-05-31T17:40:00.000Z","type":"event_msg","payload":{"type":"user_message","message":"pick up where we left off"}}
`

func TestImportCodexResumedSessionKeepsItsOwnID(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rollout-child.jsonl"), []byte(codexResumedSession), 0o644); err != nil {
		t.Fatal(err)
	}
	recs := collect(t, dir, formatCodex, perTurn)
	if len(recs) != 1 {
		t.Fatalf("got %d documents, want 1: %+v", len(recs), recs)
	}
	if got := recs[0].attr("session"); got != "CHILD" {
		t.Errorf("a resumed session must keep its own id, got %q", got)
	}
	if !strings.HasPrefix(recs[0].ID, "codex:CHILD:") {
		t.Errorf("document id should be namespaced by the child session: %q", recs[0].ID)
	}
	// The lineage is worth keeping, just not as the identity.
	if got := recs[0].attr("forked_from"); got != "PARENT" {
		t.Errorf("forked_from attribute: %q", got)
	}
	// The opening record describes this session, not the one it forked from.
	if got := recs[0].attr("cwd"); got != "/Users/joe/git/tennis" {
		t.Errorf("cwd should come from the first session_meta, got %q", got)
	}
}

// ~/.claude holds history.jsonl beside the transcripts: the record of prompts
// typed, with epoch-millisecond timestamps and a sessionId on every line. The
// sessionId makes it sniff as a transcript, so the reader gets handed it and
// must decline it quietly rather than report every line as a parse error.
const claudeCodeHistory = `{"display":"ok, commit to main","pastedContents":{},"timestamp":1786938424866,"project":"/Users/joe/git/yeet","sessionId":"04b2d7d3-2a75-4486-b837-3e0d01992d76"}
{"display":"now push it","pastedContents":{},"timestamp":1786938500000,"project":"/Users/joe/git/yeet","sessionId":"04b2d7d3-2a75-4486-b837-3e0d01992d76"}
`

func TestImportClaudeCodeSkipsHistoryFile(t *testing.T) {
	// The real shape of ~/.claude: history.jsonl at the top, transcripts one
	// directory down.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "history.jsonl"), []byte(claudeCodeHistory), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "projects", "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "projects", "repo", "S1.jsonl"), []byte(claudeCodeSession), 0o644); err != nil {
		t.Fatal(err)
	}

	var warnings []string
	recs := collectWarn(t, dir, formatClaudeCode, perTurn, func(s string) { warnings = append(warnings, s) })
	if len(warnings) != 0 {
		t.Errorf("a numeric timestamp is not a parse failure, got warnings: %v", warnings)
	}
	if len(recs) == 0 {
		t.Fatal("the transcript beside history.jsonl should still import")
	}
	// history.jsonl records prompts under `display`, not `message`, so nothing
	// in it is a turn — and its text must not reach the index by another door.
	for _, r := range recs {
		if strings.Contains(r.Text, "ok, commit to main") {
			t.Errorf("history.jsonl content leaked into a document: %q", r.Text)
		}
		if !strings.HasPrefix(r.ID, "claude-code:") {
			t.Errorf("unexpected document id %q", r.ID)
		}
	}
}

// A transcript whose timestamp arrives as a number still dates its turns.
func TestClaudeCodeNumericTimestampBecomesADate(t *testing.T) {
	dir := t.TempDir()
	line := `{"type":"user","uuid":"u1","sessionId":"s1","timestamp":1786938424866,"message":{"role":"user","content":"hello"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	recs := collect(t, dir, formatClaudeCode, perTurn)
	if len(recs) != 1 {
		t.Fatalf("got %d documents, want 1", len(recs))
	}
	if got := recs[0].attr("created"); !strings.HasPrefix(got, "2026-") {
		t.Errorf("epoch milliseconds should become an RFC3339 date, got %q", got)
	}
}
