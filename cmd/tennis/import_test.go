package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

// What a real ~/.codex and ~/.claude keep beside their transcripts: a
// history.jsonl at the root, which a lexical walk visits first. Codex also
// writes a session_index.jsonl there. None of it is in a transcript shape.
// Some Claude Code history lines carry a sessionId (see claudeCodeHistory,
// below) and some do not, but none carries a message, and a sessionId alone
// is not a transcript.
const codexHistory = `{"session_id":"S9","ts":1749497108,"text":"what hotel did we stay at in Mexico"}
`

const codexSessionIndex = `{"id":"S9","thread_name":"Hotel in Mexico","updated_at":"2026-06-09T19:25:20Z"}
`

const claudeCodeHistoryNoSessionID = `{"display":"the auth test is flaky","pastedContents":{},"project":"/Users/joe/git/x","timestamp":1786938424866}
`

// A Claude Code transcript as one is written today: bookkeeping records —
// more of them than the sniffer once read — ahead of the first turn.
const claudeCodeBookkeeping = `{"type":"queue-operation","operation":"enqueue","sessionId":"S2","timestamp":"2026-08-01T10:00:00.000Z","content":"hi"}
{"type":"queue-operation","operation":"dequeue","sessionId":"S2","timestamp":"2026-08-01T10:00:00.000Z"}
{"type":"mode","mode":"default","sessionId":"S2"}
{"type":"permission-mode","permissionMode":"default","sessionId":"S2"}
{"type":"ai-title","aiTitle":"Say hello","sessionId":"S2"}
{"type":"file-history-snapshot","messageId":"m0","snapshot":{},"isSnapshotUpdate":false}
{"type":"attachment","uuid":"x0","sessionId":"S2","attachment":{}}
{"type":"user","uuid":"u1","sessionId":"S2","timestamp":"2026-08-01T10:00:01.000Z","message":{"role":"user","content":"hi"}}
`

// A folder of notes holding JSONL that is no transcript but carries an id a
// transcript line does: an event log keyed by uuid, with a type.
var eventLogFolder = map[string]string{
	"README.md":         "# Field notes\nthe heron nests by the culvert",
	"data/a.jsonl":      `{"x":1}` + "\n",
	"data/events.jsonl": `{"type":"page_view","uuid":"e1"}` + "\n",
}

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
	if _, err := importPath(path, format, per, defaultExt, sink, warn, true); err != nil {
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
		// A real ~/.codex or ~/.claude keeps a history.jsonl at its root, in
		// neither transcript shape, and the walk is lexical, so detection has
		// to look past the JSONL it cannot place rather than settle on the
		// first one.
		{"codex transcripts behind root history", map[string]string{
			"history.jsonl":             codexHistory,
			"session_index.jsonl":       codexSessionIndex,
			"sessions/rollout-S9.jsonl": codexSession,
		}, formatCodex},
		{"claude code transcripts behind root history", map[string]string{
			"history.jsonl":          claudeCodeHistoryNoSessionID,
			"projects/repo/S1.jsonl": claudeCodeSession,
		}, formatClaudeCode},
		{"claude code transcript behind bookkeeping", map[string]string{"projects/repo/S2.jsonl": claudeCodeBookkeeping}, formatClaudeCode},
		// The first JSONL a sniffer claims decides the format for the whole
		// source, so a line with an id in it is not enough: it has to be a
		// conversation record.
		{"claude code history alone", map[string]string{"history.jsonl": claudeCodeHistory}, formatFiles},
		{"event log among notes", eventLogFolder, formatFiles},
		{"codex envelope without a timestamp", map[string]string{
			"logs/stream.jsonl": `{"type":"event_msg","payload":{"type":"click"}}` + "\n",
		}, formatFiles},
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
	// The project is the working directory's last element, not the dashed
	// directory name the transcript sits under: it is what a Codex session
	// stores, what a card prints, and what `--where project=tennis` names.
	if got := recs[1].attr("project"); got != "tennis" {
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
	// A side-chain turn inside the session's own transcript is not a
	// subagent's file, and has no file to be named for.
	if _, ok := recs[3].Attrs["subagent"]; ok {
		t.Errorf("an inline sidechain turn got a subagent attribute: %v", recs[3].Attrs)
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
	if got := recs[1].attr("project"); got != "tennis" {
		t.Errorf("project attribute: %q", got)
	}
}

// A transcript that never records where it ran still gets a project: the
// directory it sits under, which is the working directory with its slashes
// turned to dashes.
func TestImportClaudeCodeProjectFallsBackToDirectory(t *testing.T) {
	session := strings.ReplaceAll(claudeCodeSession, `"cwd":"/Users/joe/git/tennis",`, "")
	recs := collect(t, writeZip(t, "sessions.zip", map[string]string{
		"projects/-Users-joe-git-tennis/S1.jsonl": session,
	}), formatAuto, perTurn)
	if len(recs) != 4 {
		t.Fatalf("got %d documents, want 4: %+v", len(recs), recs)
	}
	if got := recs[1].attr("cwd"); got != "" {
		t.Errorf("cwd attribute should be absent, got %q", got)
	}
	if got := recs[1].attr("project"); got != "-Users-joe-git-tennis" {
		t.Errorf("project attribute: %q", got)
	}
}

// The desktop app runs most sessions in a worktree under .claude/worktrees,
// where the working directory's last element is the worktree's name. Filing
// those under their own names would leave `--where project=tennis` matching
// only the sessions run at the repo root.
func TestImportClaudeCodeInWorktree(t *testing.T) {
	session := strings.ReplaceAll(claudeCodeSession,
		`"cwd":"/Users/joe/git/tennis","gitBranch":"main"`,
		`"cwd":"/Users/joe/git/tennis/.claude/worktrees/trusting-ardinghelli-d93106","gitBranch":"claude/trusting-ardinghelli-d93106"`)
	recs := collect(t, writeZip(t, "sessions.zip", map[string]string{
		"projects/-Users-joe-git-tennis--claude-worktrees-trusting-ardinghelli-d93106/S1.jsonl": session,
	}), formatAuto, perTurn)
	if len(recs) != 4 {
		t.Fatalf("got %d documents, want 4: %+v", len(recs), recs)
	}
	if got := recs[1].attr("project"); got != "tennis" {
		t.Errorf("project attribute: %q", got)
	}
	if got := recs[1].attr("worktree"); got != "trusting-ardinghelli-d93106" {
		t.Errorf("worktree attribute: %q", got)
	}
	if got := recs[1].attr("cwd"); got != "/Users/joe/git/tennis/.claude/worktrees/trusting-ardinghelli-d93106" {
		t.Errorf("cwd attribute: %q", got)
	}
	if got := recs[1].attr("branch"); got != "claude/trusting-ardinghelli-d93106" {
		t.Errorf("branch attribute: %q", got)
	}
}

func TestProjectOf(t *testing.T) {
	for _, tc := range []struct {
		cwd, project, worktree string
	}{
		{"/Users/joe/git/tennis", "tennis", ""},
		{"/Users/joe/git/tennis/.claude/worktrees/trusting-ardinghelli-d93106", "tennis", "trusting-ardinghelli-d93106"},
		{"/Users/joe/git/tennis/.claude/worktrees/trusting-ardinghelli-d93106/cmd/tennis", "tennis", "trusting-ardinghelli-d93106"},
		{"/Users/joe/git/tennis/.claude/worktrees/", "worktrees", ""},
		{"/Users/joe/git/gx", "gx", ""},
	} {
		project, worktree := projectOf(tc.cwd)
		if project != tc.project || worktree != tc.worktree {
			t.Errorf("projectOf(%q) = %q, %q; want %q, %q", tc.cwd, project, worktree, tc.project, tc.worktree)
		}
	}
}

// Where a transcript is stored names its project when no line says where it
// ran. A subagent's is stored under its session, in the same project.
func TestCCProjectDir(t *testing.T) {
	for _, tc := range []struct {
		stored   string
		subagent bool
		want     string
	}{
		{"projects/-Users-joe-git-tennis/S1.jsonl", false, "-Users-joe-git-tennis"},
		{"/Users/joe/.claude/projects/-Users-joe-git-tennis/S1.jsonl", false, "-Users-joe-git-tennis"},
		{"S1.jsonl", false, ""},
		{"projects/-Users-joe-git-tennis/S1/subagents/agent-x.jsonl", true, "-Users-joe-git-tennis"},
		{"-Users-joe-git-tennis/S1/subagents/workflows/wf_1/agent-x.jsonl", true, "-Users-joe-git-tennis"},
		{"S1/subagents/agent-x.jsonl", true, ""},
		{"/Users/joe/backup/agent-x.jsonl", true, "backup"},
	} {
		if got := ccProjectDir(tc.stored, tc.subagent); got != tc.want {
			t.Errorf("ccProjectDir(%q, %v) = %q, want %q", tc.stored, tc.subagent, got, tc.want)
		}
	}
}

// A session moves — into a subdirectory, into a worktree, into another repo —
// and every line records where it is at the time. It is filed under where it
// started, and the directory, project and worktree all come from that one line,
// so they cannot disagree.
func TestImportClaudeCodeKeepsWhereTheSessionStarted(t *testing.T) {
	const tennis, worktree = "/Users/joe/git/tennis", "/Users/joe/git/tennis/.claude/worktrees/wt-a"
	for _, tc := range []struct {
		name                 string
		cwds                 []string
		project, wt, wantCWD string
	}{
		{"into a subdirectory", []string{tennis, tennis + "/www"}, "tennis", "", tennis},
		{"into a worktree", []string{tennis, worktree}, "tennis", "", tennis},
		{"out of a worktree", []string{worktree, "/Users/joe/git/gx"}, "tennis", "wt-a", worktree},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			for i, cwd := range tc.cwds {
				fmt.Fprintf(&b, `{"type":"user","uuid":"u%d","sessionId":"S1","cwd":%q,"message":{"role":"user","content":"turn %d"}}`+"\n", i, cwd, i)
			}
			recs := collect(t, writeTree(t, map[string]string{"-Users-joe-git-tennis/S1.jsonl": b.String()}), formatClaudeCode, perTurn)
			if len(recs) != len(tc.cwds) {
				t.Fatalf("got %d documents, want %d", len(recs), len(tc.cwds))
			}
			for _, r := range recs {
				if got := r.attr("project"); got != tc.project {
					t.Errorf("%s: project %q, want %q", r.ID, got, tc.project)
				}
				if got := r.attr("worktree"); got != tc.wt {
					t.Errorf("%s: worktree %q, want %q", r.ID, got, tc.wt)
				}
				if got := r.attr("cwd"); got != tc.wantCWD {
					t.Errorf("%s: cwd %q, want %q", r.ID, got, tc.wantCWD)
				}
			}
		})
	}
}

// A session that ran a subagent, as Claude Code stores it: the session's
// transcript named for it, and the subagent's in a directory named for it
// beside that, every line on the side chain and carrying the session's ID.
// The subagent ran after the session moved into www, and its own lines say so.
const claudeCodeParent = `{"type":"user","uuid":"u1","sessionId":"S1","timestamp":"2026-08-01T10:00:00.000Z","cwd":"/Users/joe/git/tennis","gitBranch":"main","message":{"role":"user","content":"the auth test is flaky"}}
{"type":"assistant","uuid":"a1","sessionId":"S1","timestamp":"2026-08-01T10:00:05.000Z","cwd":"/Users/joe/git/tennis/www","gitBranch":"main","message":{"role":"assistant","content":[{"type":"text","text":"sending a subagent after the token clock"}]}}
{"type":"ai-title","aiTitle":"Fixing the flaky auth test","sessionId":"S1"}
`

const claudeCodeSubagent = `{"type":"user","uuid":"su1","isSidechain":true,"sessionId":"S1","timestamp":"2026-08-01T10:00:06.000Z","cwd":"/Users/joe/git/tennis/www","gitBranch":"main","message":{"role":"user","content":"find the race in the token clock"}}
{"type":"assistant","uuid":"sa1","isSidechain":true,"sessionId":"S1","timestamp":"2026-08-01T10:00:09.000Z","cwd":"/Users/joe/git/tennis/www","gitBranch":"main","message":{"role":"assistant","content":[{"type":"text","text":"the refresh window races the clock skew"}]}}
`

var claudeCodeWithSubagent = map[string]string{
	"-Users-joe-git-tennis/S1.jsonl":                   claudeCodeParent,
	"-Users-joe-git-tennis/S1/subagents/agent-x.jsonl": claudeCodeSubagent,
}

// A subagent's turns belong to the session that ran it: its ID, its title, the
// place it started. What sets them apart is the subagent attribute, naming the
// file, which only they carry.
func TestImportClaudeCodeSubagent(t *testing.T) {
	src := writeTree(t, claudeCodeWithSubagent)
	sink := &docSink{}
	var recs []docRecord
	sink.capture = func(id, text string, attrs map[string]any) {
		recs = append(recs, docRecord{ID: id, Text: text, Attrs: attrs})
	}
	rep, err := importPath(src, formatAuto, perTurn, defaultExt, sink, func(string) {}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := rep["conversations"]; got != 1 {
		t.Errorf("a session and its subagent are one conversation, report says %v", got)
	}
	if len(recs) != 4 {
		t.Fatalf("got %d documents, want 4: %+v", len(recs), recs)
	}
	subs := 0
	for _, r := range recs {
		if got := r.attr("session"); got != "S1" {
			t.Errorf("%s: session %q, want the parent's S1", r.ID, got)
		}
		if got := r.attr("title"); got != "Fixing the flaky auth test" {
			t.Errorf("%s: title %q, want the session's", r.ID, got)
		}
		if got := r.attr("project"); got != "tennis" {
			t.Errorf("%s: project %q, want where the session started", r.ID, got)
		}
		if got := r.attr("cwd"); got != "/Users/joe/git/tennis" {
			t.Errorf("%s: cwd %q, want where the session started", r.ID, got)
		}
		sub, onSide := r.Attrs["subagent"], strings.HasSuffix(r.attr("role"), "/subagent")
		if onSide != (sub != nil) {
			t.Errorf("%s: role %q but subagent attribute %v", r.ID, r.attr("role"), sub)
		}
		if sub != nil {
			subs++
			if sub != "agent-x" {
				t.Errorf("%s: subagent %v, want the file's name agent-x", r.ID, sub)
			}
			if !strings.HasPrefix(r.ID, "claude-code:S1:s") {
				t.Errorf("subagent turn ID %q, want the session's ID and the message's", r.ID)
			}
		}
	}
	if subs != 2 {
		t.Errorf("%d documents carry the subagent attribute, want the subagent's 2", subs)
	}

	// At conversation granularity the session and its subagent are a document
	// each. Under one ID, whichever was written second replaced the other.
	recs = collect(t, src, formatAuto, perConversation)
	ids := map[string]docRecord{}
	for _, r := range recs {
		ids[r.ID] = r
	}
	if len(recs) != 2 || len(ids) != 2 {
		t.Fatalf("want two distinct documents, got %+v", recs)
	}
	main, sub := ids["claude-code:S1"], ids["claude-code:S1:agent-x"]
	if !strings.Contains(main.Text, "the auth test is flaky") || main.Attrs["subagent"] != nil {
		t.Errorf("the session's document: %+v", main)
	}
	if !strings.Contains(sub.Text, "races the clock skew") || sub.attr("subagent") != "agent-x" || sub.attr("session") != "S1" {
		t.Errorf("the subagent's document: %+v", sub)
	}
}

// The session's card describes the session. A subagent's run is part of it
// and gets no card of its own, which was an untitled stub beside the real one.
func TestImportClaudeCodeSubagentHasNoCard(t *testing.T) {
	dir := t.TempDir()
	sum := &recordingSummarizer{}
	w := addCards(t, writeTree(t, claudeCodeWithSubagent), formatClaudeCode, "", sum, dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || w.written != 1 || len(sum.seen) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("want one card for the session, got %v (written %d, summarized %d)", names, w.written, len(sum.seen))
	}
	if !strings.Contains(entries[0].Name(), "fixing-the-flaky-auth-test") {
		t.Errorf("the card should be the session's: %s", entries[0].Name())
	}
}

// A subagent's transcript handed over on its own has no subagents directory
// to be recognized by. Its lines still say what it is: every one on the side
// chain, under a session that is not the one the file is named for.
func TestImportClaudeCodeLoneSubagentFile(t *testing.T) {
	src := writeTree(t, map[string]string{"agent-x.jsonl": claudeCodeSubagent})
	recs := collect(t, filepath.Join(src, "agent-x.jsonl"), formatClaudeCode, perTurn)
	if len(recs) != 2 {
		t.Fatalf("got %d documents, want 2", len(recs))
	}
	for _, r := range recs {
		if r.attr("subagent") != "agent-x" || r.attr("session") != "S1" {
			t.Errorf("%s: subagent %q session %q, want agent-x in S1", r.ID, r.attr("subagent"), r.attr("session"))
		}
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

// The fallback: a zip that is not an export at all is still worth indexing —
// every file in it with text, read by whatever reads its kind, and nothing
// said about the photos. A hidden file is not what anyone meant.
func TestImportPlainZipOfFiles(t *testing.T) {
	path := writeZip(t, "notes.zip", map[string]string{
		"notes/auth.md":                   "# Session handling\nkeep the user signed in",
		"notes/budget.xlsx":               string(testWorkbook(t)),
		"notes/memo.docx":                 string(testDocx(t, `<w:p><w:r><w:t>Rent is due on the 1st</w:t></w:r></w:p>`)),
		"notes/page.html":                 "<html><body><p>Hotel &amp; taxi</p></body></html>",
		"notes/script.go":                 "package main",
		"notes/logo.png":                  "\x89PNG\x00\x01",
		"notes/blob.bin":                  "\x00\x01\x02",
		"notes/.secret.md":                "not for the index",
		"notes/.cache/x.md":               "nor this",
		"notes/node_modules/lib/index.js": "module.exports = {}",
	})
	var warnings []string
	recs := collectWarn(t, path, formatAuto, perTurn, func(msg string) { warnings = append(warnings, msg) })
	got := map[string]string{}
	for _, r := range recs {
		got[strings.TrimPrefix(r.ID, path+"!notes/")] = r.Text
	}
	for name, want := range map[string]string{
		"auth.md": "keep the user signed in", "budget.xlsx": "Rent (monthly)", "memo.docx": "Rent is due on the 1st",
		"page.html": "Hotel & taxi", "script.go": "package main",
	} {
		if !strings.Contains(got[name], want) {
			t.Errorf("%s: indexed as %q, want it to carry %q", name, got[name], want)
		}
	}
	for _, name := range []string{"logo.png", "blob.bin", ".secret.md", ".cache/x.md", "node_modules/lib/index.js"} {
		if _, ok := got[name]; ok {
			t.Errorf("%s should not have been indexed", name)
		}
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "blob.bin") {
		t.Errorf("want exactly one warning, about the unknown binary; got %v", warnings)
	}
	if len(recs) == 0 || recs[0].attr("name") == "" || recs[0].attr("path") == "" {
		t.Errorf("file attributes missing: %+v", recs)
	}
}

// writeTree lays out files under a fresh directory and returns it.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// codeProject is a code checkout as add meets one: a little source, and a great
// deal that nobody there wrote — dependencies, build output, and what the
// .gitignore files name.
var codeProject = map[string]string{
	"README.md":                                "# Tennis\nsearch what you said",
	"src/main.go":                              "package main",
	"src/generated/api.go":                     "package api", // "/generated" is anchored to the root
	"keep.log":                                 "brought back by !keep.log",
	"docs/final.md":                            "the plan",
	".gitignore":                               "*.log\n!keep.log\n/generated\ncoverage/\n",
	"docs/.gitignore":                          "draft.md\n",
	"debug.log":                                "a gitignored file",
	"docs/draft.md":                            "a gitignored file in a nested .gitignore",
	"generated/api.go":                         "package api",
	"coverage/index.html":                      "<p>87%</p>",
	"coverage/lcov/report.txt":                 "nor anything under it",
	"node_modules/left-pad/index.js":           "module.exports = pad",
	"src/node_modules/x/index.js":              "module.exports = x",
	"vendor/github.com/x/y/y.go":               "package y",
	"target/debug/build.txt":                   "cargo output",
	"dist/app.js":                              "bundled",
	"build/out.txt":                            "built",
	"__pycache__/m.cpython-312.pyc":            "bytecode",
	"venv/lib/python3.12/site-packages/six.py": "import sys",
	".cache/node_modules/z.js":                 "under a dot directory: not even counted",
}

// Pointed at a code project, add reads what the people there wrote. The
// dependency and build folders are passed over without being entered, what
// the .gitignore files name is passed over too, and each is counted rather
// than announced: a folder of node_modules is not a folder of mistakes.
func TestImportFilesPassesOverDependenciesAndIgnored(t *testing.T) {
	dir := writeTree(t, codeProject)
	var warnings []string
	sink := &docSink{}
	got := map[string]bool{}
	sink.capture = func(id, _ string, _ map[string]any) {
		rel, _ := filepath.Rel(dir, id)
		got[filepath.ToSlash(rel)] = true
	}
	rep, err := importPath(dir, formatAuto, perTurn, defaultExt, sink, func(msg string) { warnings = append(warnings, msg) }, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"README.md", "src/main.go", "src/generated/api.go", "keep.log", "docs/final.md"}
	if len(got) != len(want) {
		t.Errorf("indexed %v, want exactly %v", sortedKeys(got), want)
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("%s should have been indexed; got %v", name, sortedKeys(got))
		}
	}
	if len(warnings) != 0 {
		t.Errorf("passing over a folder is not worth a warning: %v", warnings)
	}
	// debug.log and docs/draft.md; node_modules twice, vendor, target, dist,
	// build, __pycache__ and site-packages, then coverage and generated.
	if rep["skipped_files"] != 2 || rep["skipped_dirs"] != 10 {
		t.Errorf("skipped_files %v, skipped_dirs %v; want 2 and 10", rep["skipped_files"], rep["skipped_dirs"])
	}

	// --ext narrows what is read, not what is passed over.
	sink = &docSink{capture: func(string, string, map[string]any) {}}
	if rep, err = importPath(dir, formatAuto, perTurn, ".js", sink, func(string) {}, true); err == nil {
		t.Fatalf("every .js file is in a passed-over folder, so nothing should be indexed: %v", rep)
	} else if !strings.Contains(err.Error(), "0 skipped, 8 folders passed over") {
		t.Errorf("the error should say where the files went: %v", err)
	}
}

// The folder a person names is the folder they mean, whatever it is called.
func TestImportFilesReadsANamedBuildFolder(t *testing.T) {
	root := filepath.Join(t.TempDir(), "build")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("what the build does"), 0o644); err != nil {
		t.Fatal(err)
	}
	if recs := collect(t, root, formatAuto, perTurn); len(recs) != 1 {
		t.Errorf("got %d documents from the named folder, want 1", len(recs))
	}
}

// A .gitignore is a statement about a code project, not about transcripts.
// A ~/.claude kept in git that ignores projects/ still has its sessions read,
// because those are what pointing tennis at it asks for.
func TestImportClaudeCodeIgnoresGitignore(t *testing.T) {
	dir := writeTree(t, map[string]string{
		".gitignore": "projects/\n*.jsonl\n",
		"projects/-Users-joe-git-tennis/S1.jsonl": claudeCodeSession,
	})
	if recs := collect(t, dir, formatAuto, perTurn); len(recs) != 4 {
		t.Errorf("got %d documents, want the session's 4", len(recs))
	}
}

// seed is add --files with the namespace first, and passes over the same
// folders.
func TestSeedPassesOverDependenciesAndIgnored(t *testing.T) {
	cache := ndjsonTestCache(t)
	t.Setenv("TENNIS_CACHE", cache)
	dir := writeTree(t, codeProject)
	dbPath := filepath.Join(t.TempDir(), "seed.sqlite")
	out, err := captureStdout(t, func() error {
		return cmdSeed([]string{"notes", dir, "--db", dbPath, "--json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	res := decodeImportResult(t, out)
	if res["written"] != float64(5) || res["skipped_files"] != float64(2) || res["skipped_dirs"] != float64(10) {
		t.Errorf("seed: %v; want 5 written, 2 files and 10 folders skipped", res)
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
	cache := ndjsonTestCache(t)
	t.Setenv("TENNIS_CACHE", cache)
	t.Setenv("TENNIS_CARDS", t.TempDir())
	// No key, so no card reaches an API and the card counts do not depend on
	// whether one did.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
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
	cards := res["cards"]
	if cards == float64(0) {
		t.Fatalf("first import wrote no cards: %v", res)
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
	// And so must its cards: with a key, each one written again is a call.
	if res["cards"] != float64(0) || res["cards_unchanged"] != cards {
		t.Errorf("repeat import: want cards=0 cards_unchanged=%v, got %v", cards, res)
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
	cache := ndjsonTestCache(t)
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

// Files get cards too. An add of a folder holding a note, a spreadsheet, a
// Word file and — where this machine can read one — a PDF leaves one card per
// file, named after the file so a re-add lands on the same card, and with no
// model each card opens with the start of the file. The summarizer note is
// printed, because it applies.
func TestAddFilesWritesCards(t *testing.T) {
	cache := ndjsonTestCache(t)
	t.Setenv("TENNIS_CACHE", cache)
	t.Setenv("TENNIS_NS", "")
	// No key, so the note is the deterministic one and no card reaches an API.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	cardDir := filepath.Join(t.TempDir(), "cards")
	t.Setenv("TENNIS_CARDS", cardDir)
	dbPath := filepath.Join(t.TempDir(), "add.sqlite")

	notes := t.TempDir()
	files := map[string][]byte{
		"auth.md":     []byte("# Session handling\nkeep the user signed in"),
		"budget.xlsx": testWorkbook(t),
		"memo.docx":   testDocx(t, `<w:p><w:r><w:t>Rent is due on the 1st</w:t></w:r></w:p>`),
		"photo.jpg":   []byte("\xff\xd8\xff"),
	}
	pdf := tool("pdftotext") != "" || runtime.GOOS == "darwin"
	if pdf {
		files["plan.pdf"] = testPDF(t)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(notes, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	wantCards := len(files) - 1 // the photo gets none

	// Not --json: that mode is quiet about the summarizer, and what the
	// human-facing run says is part of what is under test.
	add := func(args ...string) string {
		t.Helper()
		stderr, err := captureStderr(t, func() error {
			_, err := captureStdout(t, func() error {
				return cmdAdd(append([]string{"--db", dbPath}, args...))
			})
			return err
		})
		if err != nil {
			t.Fatalf("add %v: %v\nstderr: %s", args, err, stderr)
		}
		return stderr
	}
	cardNames := func() []string {
		t.Helper()
		entries, err := os.ReadDir(cardDir)
		if err != nil {
			t.Fatalf("card directory: %v", err)
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return names
	}
	cardFor := func(stem string) string {
		t.Helper()
		for _, n := range cardNames() {
			if strings.HasPrefix(n, stem) {
				body, err := os.ReadFile(filepath.Join(cardDir, n))
				if err != nil {
					t.Fatal(err)
				}
				return string(body)
			}
		}
		t.Fatalf("no card named %s*: %v", stem, cardNames())
		return ""
	}

	stderr := add("--files", notes)
	if !strings.Contains(stderr, "no ANTHROPIC_API_KEY or OPENAI_API_KEY") {
		t.Errorf("files have cards, so an add with no key should say so:\n%s", stderr)
	}
	if strings.Contains(stderr, "photo.jpg") {
		t.Errorf("a photo should be passed over without comment:\n%s", stderr)
	}
	if names := cardNames(); len(names) != wantCards {
		t.Fatalf("%d files left %d cards: %v", wantCards, len(names), names)
	}
	for stem, want := range map[string]string{
		"budget-xlsx-": "Rent (monthly)", "memo-docx-": "Rent is due on the 1st", "auth-md-": "keep the user signed in",
	} {
		if body := cardFor(stem); !strings.Contains(body, "source: file") || !strings.Contains(body, want) {
			t.Errorf("card %s* is missing %q:\n%s", stem, want, body)
		}
	}
	if pdf {
		if body := cardFor("plan-pdf-"); !strings.Contains(body, "Hotel Esencia in Tulum") {
			t.Errorf("the PDF's card does not carry its text:\n%s", body)
		}
	}

	// The cells are searchable, with the attributes a caller needs to get
	// back to the file.
	out, err := captureStdout(t, func() error {
		return cmdSearch([]string{"--db", dbPath, "--json", "monthly rent"})
	})
	if err != nil {
		t.Fatalf("search: %v\noutput: %s", err, out)
	}
	var results []map[string]any
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("search --json did not parse: %v\noutput: %s", err, out)
	}
	var found bool
	for _, r := range results {
		if attrs, _ := r["attributes"].(map[string]any); attrs["name"] == "budget.xlsx" {
			found = true
		}
	}
	if !found {
		t.Errorf("the spreadsheet was not among the hits: %v", results)
	}

	// A re-add lands on the same cards rather than growing the folder, and
	// --no-cards still leaves it alone.
	add("--files", notes)
	if names := cardNames(); len(names) != wantCards {
		t.Errorf("re-adding the folder changed the cards: %v", names)
	}
	stderr = add("--no-cards", "--files", notes)
	if strings.Contains(stderr, "API_KEY") {
		t.Errorf("--no-cards still talked about the summarizer:\n%s", stderr)
	}
	if names := cardNames(); len(names) != wantCards {
		t.Errorf("--no-cards changed the cards: %v", names)
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
// reader is handed it along with the transcripts it sits beside, and must
// decline it quietly rather than report every line as a parse error.
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

// A session that ran a subagent, added for real. At conversation granularity
// the database holds two documents, where it held one because the second
// write replaced the first; there is one card, the session's; and ls lists the
// session once, under its title. That title holds when a subagent's turns
// carry none, as they do when the subagent's file is added on its own.
func TestAddSessionWithSubagent(t *testing.T) {
	cache := ndjsonTestCache(t)
	t.Setenv("TENNIS_CACHE", cache)
	cardDir := filepath.Join(t.TempDir(), "cards")
	t.Setenv("TENNIS_CARDS", cardDir)
	t.Setenv("TENNIS_NS", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	dbPath := filepath.Join(t.TempDir(), "add.sqlite")
	src := writeTree(t, claudeCodeWithSubagent)

	add := func(args ...string) map[string]any {
		t.Helper()
		out, err := captureStdout(t, func() error {
			return cmdAdd(append([]string{"--db", dbPath, "--json"}, args...))
		})
		if err != nil {
			t.Fatalf("add %v: %v\noutput: %s", args, err, out)
		}
		return decodeImportResult(t, out)
	}
	ls := func(args ...string) []map[string]any {
		t.Helper()
		out, err := captureStdout(t, func() error {
			return cmdLS(append([]string{"--db", dbPath, "--json", "-n", "0"}, args...))
		})
		if err != nil {
			t.Fatalf("ls %v: %v\noutput: %s", args, err, out)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("ls --json: %v\noutput: %s", err, out)
		}
		return rows
	}
	wantOneSession := func(rows []map[string]any, docs float64) {
		t.Helper()
		if len(rows) != 1 || rows[0]["key"] != "S1" || rows[0]["documents"] != docs {
			t.Fatalf("ls: want the one session S1 with %v documents, got %v", docs, rows)
		}
		if attrs, _ := rows[0]["attributes"].(map[string]any); attrs["title"] != "Fixing the flaky auth test" {
			t.Errorf("ls: the session's row should carry its title, got %v", rows[0])
		}
	}

	if res := add("--per", "conversation", src); res["written"] != float64(2) {
		t.Errorf("add --per conversation: want written=2, got %v", res)
	}
	docs := ls("--docs")
	got := map[any]bool{}
	for _, d := range docs {
		got[d["id"]] = true
	}
	if len(docs) != 2 || !got["claude-code:S1"] || !got["claude-code:S1:agent-x"] {
		t.Errorf("ls --docs: want the session's document and the subagent's, got %v", docs)
	}
	wantOneSession(ls(), 2)

	const turns = "turns"
	add("--ns", turns, src)
	wantOneSession(ls("--ns", turns), 4)
	add("--ns", turns, filepath.Join(src, "-Users-joe-git-tennis", "S1", "subagents", "agent-x.jsonl"))
	wantOneSession(ls("--ns", turns), 4)

	entries, err := os.ReadDir(cardDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("want only the session's card, got %v", names)
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

// A path that fails after one that succeeded must not cost the first its
// documents. The sink batches writes and the card writer runs ahead of it, so
// returning on the first path error used to leave cards in ~/tennis for
// conversations the database never received — a re-run was the only way to
// make them searchable. Now what was read goes in, the report covers it, and
// the error is what main exits nonzero on.
func TestAddKeepsEarlierPathsWhenALaterOneFails(t *testing.T) {
	cache := ndjsonTestCache(t)
	t.Setenv("TENNIS_CACHE", cache)
	cardDir := t.TempDir()
	t.Setenv("TENNIS_CARDS", cardDir)
	dbPath := filepath.Join(t.TempDir(), "add.sqlite")
	const ns = "lost"

	good := writeZip(t, "claude-export.zip", map[string]string{"conversations.json": claudeExport})
	// Nothing in here is indexable: detection falls through to plain files,
	// a photo has no text to index, and a plain-files import that finds
	// nothing is an error.
	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, "photo.png"), []byte("\x89PNG\x00\x01"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error {
		return cmdAdd([]string{"--db", dbPath, "--json", "--ns", ns, good, bad})
	})
	if err == nil {
		t.Fatal("a path that imports nothing must still fail the command")
	}
	if !strings.Contains(err.Error(), "no indexable files") {
		t.Errorf("the error should be the bad path's, got %q", err)
	}

	// The report describes what landed — the good path alone — and names the
	// failure, so stdout on its own does not read as a clean run.
	res := decodeImportResult(t, out)
	if res["written"] != float64(2) {
		t.Errorf("written: want the good path's 2, got %v", res["written"])
	}
	sources, _ := res["sources"].([]any)
	if len(sources) != 1 {
		t.Fatalf("sources should list the good path alone, got %v", res["sources"])
	}
	if src := sources[0].(map[string]any); src["path"] != good {
		t.Errorf("source path: %v, want %s", src["path"], good)
	}
	if msg, _ := res["error"].(string); !strings.Contains(msg, "no indexable files") {
		t.Errorf("the report should carry the error, got %v", res["error"])
	}

	// In the database, not just in the report.
	out, err = captureStdout(t, func() error {
		return cmdLS([]string{"--db", dbPath, "--json", "--docs", "--ns", ns})
	})
	if err != nil {
		t.Fatalf("ls: %v\noutput: %s", err, out)
	}
	var docs []map[string]any
	if err := json.Unmarshal([]byte(out), &docs); err != nil {
		t.Fatalf("ls --json did not parse: %v\noutput: %s", err, out)
	}
	if len(docs) != 2 {
		t.Errorf("ls --docs: got %d documents, want the good path's 2: %v", len(docs), docs)
	}

	// And every card on disk describes a conversation that is actually there.
	cards, _ := filepath.Glob(filepath.Join(cardDir, "*.md"))
	if len(cards) != 1 {
		t.Errorf("cards: got %d, want 1 for the one conversation imported: %v", len(cards), cards)
	}
}

// The other way round — the bad path first, nothing read before it — is the
// plain error it always was, with no report in front of it: an "imported 0"
// line would dress the mistake up as a partial success.
func TestAddBadOnlyPathPrintsNoReport(t *testing.T) {
	cache := ndjsonTestCache(t)
	t.Setenv("TENNIS_CACHE", cache)
	t.Setenv("TENNIS_CARDS", t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "add.sqlite")

	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, "photo.png"), []byte("\x89PNG\x00\x01"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error {
		return cmdAdd([]string{"--db", dbPath, "--json", "--ns", "lost", bad})
	})
	if err == nil {
		t.Fatal("a lone path that imports nothing must fail the command")
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("nothing was read, so nothing should be reported; stdout was %q", out)
	}
}

// A folder of notes can hold JSONL that passes for a transcript — a fixture,
// a stub — and detection, which settles on the first such file, then reads
// the whole folder as transcripts and finds no conversation in it. That is a
// wrong guess, not an empty source: the folder is read as plain files after
// all, and the run says so. Named explicitly, the format is taken at its word.
func TestAddReadsPlainFilesWhenNoTranscriptTurnsUp(t *testing.T) {
	cache := ndjsonTestCache(t)
	t.Setenv("TENNIS_CACHE", cache)
	t.Setenv("TENNIS_CARDS", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	for _, tc := range []struct {
		name, flag, reading, stub string
	}{
		// A turn that is nothing but a tool result, which is not indexed.
		{"claude code", "--claude-code", "reading Claude Code session transcripts",
			`{"type":"user","uuid":"t1","sessionId":"S1","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}` + "\n"},
		// Telemetry, and nothing said.
		{"codex", "--codex", "reading Codex session transcripts",
			`{"timestamp":"2026-06-09T19:25:07.716Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeTree(t, map[string]string{
				"README.md":              "# Field notes\nthe heron nests by the culvert",
				"fixtures/session.jsonl": tc.stub,
			})
			dbPath := filepath.Join(t.TempDir(), "add.sqlite")
			add := func(args ...string) (string, error) {
				t.Helper()
				return captureStderr(t, func() error {
					_, err := captureStdout(t, func() error {
						return cmdAdd(append([]string{"--db", dbPath, "--ns", "notes", "--no-cards"}, args...))
					})
					return err
				})
			}

			stderr, err := add(dir)
			if err != nil {
				t.Fatalf("add: %v\nstderr: %s", err, stderr)
			}
			if !strings.Contains(stderr, tc.reading) || !strings.Contains(stderr, "no transcripts after all; reading plain files") {
				t.Errorf("the run should say what it guessed and that it read plain files instead:\n%s", stderr)
			}

			out, err := captureStdout(t, func() error {
				return cmdLS([]string{"--db", dbPath, "--json", "--docs", "--ns", "notes"})
			})
			if err != nil {
				t.Fatalf("ls: %v\noutput: %s", err, out)
			}
			var docs []map[string]any
			if err := json.Unmarshal([]byte(out), &docs); err != nil {
				t.Fatalf("ls --json did not parse: %v\noutput: %s", err, out)
			}
			var readme bool
			for _, d := range docs {
				if d["id"] == filepath.Join(dir, "README.md") {
					readme = true
				}
			}
			if !readme {
				t.Errorf("README.md was not indexed: %v", docs)
			}

			stderr, err = add(tc.flag, dir)
			if err == nil || !strings.Contains(err.Error(), "nothing to import") {
				t.Errorf("%s names the format, so a source with no conversation in it is an error; got %v\nstderr: %s", tc.flag, err, stderr)
			}
			if strings.Contains(stderr, "reading plain files") {
				t.Errorf("%s should not fall back to plain files:\n%s", tc.flag, stderr)
			}
		})
	}
}
