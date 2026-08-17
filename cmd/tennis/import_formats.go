package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/satoricorp/tennis"
)

// The adapters. Each one turns a vendor's export into the same conversation
// shape and hands it to the sink; nothing downstream of here knows which
// service the history came from.

// streamArray walks a top-level JSON array one element at a time.
//
// A real ChatGPT export is a single JSON array hundreds of megabytes long.
// Unmarshaling that whole file to iterate it would cost more memory than the
// index it produces, so elements are decoded individually and released.
//
// fn's error is fatal — it only ever comes from the database write. A single
// malformed element is fn's own business to warn about and skip, because one
// unreadable conversation out of nine hundred should cost that conversation.
func streamArray(r io.Reader, what string, fn func(i int, raw json.RawMessage) error) (int, error) {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return 0, fmt.Errorf("%s: %w", what, err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return 0, fmt.Errorf("%s: expected a JSON array", what)
	}
	n := 0
	for dec.More() {
		var raw json.RawMessage
		// A syntax error here is fatal by necessity: the decoder's position in
		// the stream is undefined afterwards, so there is nothing to resume.
		if err := dec.Decode(&raw); err != nil {
			return n, fmt.Errorf("%s: %w", what, err)
		}
		if err := fn(n, raw); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// --- ChatGPT ---------------------------------------------------------------

type chatgptConv struct {
	ID             string   `json:"id"`
	ConversationID string   `json:"conversation_id"`
	Title          string   `json:"title"`
	CreateTime     *float64 `json:"create_time"`
	Mapping        map[string]struct {
		Message  *chatgptMessage `json:"message"`
		Parent   *string         `json:"parent"`
		Children []string        `json:"children"`
	} `json:"mapping"`
}

type chatgptMessage struct {
	ID     string `json:"id"`
	Author struct {
		Role string `json:"role"`
	} `json:"author"`
	CreateTime *float64       `json:"create_time"`
	Content    chatgptContent `json:"content"`
	Metadata   map[string]any `json:"metadata"`
}

// chatgptContent covers every content_type in one struct because they differ
// only in which field holds the words: "text" and "multimodal_text" fill
// parts, "code" and "execution_output" fill text, and the custom-instructions
// record fills its own two.
type chatgptContent struct {
	ContentType      string            `json:"content_type"`
	Parts            []json.RawMessage `json:"parts"`
	Text             string            `json:"text"`
	UserProfile      string            `json:"user_profile"`
	UserInstructions string            `json:"user_instructions"`
}

func importChatGPT(r io.Reader, per string, sink *docSink, warn func(string)) (int, int, error) {
	failed := 0
	n, err := streamArray(r, "conversations.json", func(i int, raw json.RawMessage) error {
		var c chatgptConv
		if err := json.Unmarshal(raw, &c); err != nil {
			failed++
			warn(fmt.Sprintf("conversation %d: %v", i+1, err))
			return nil
		}
		return c.normalize(i).emit(per, sink)
	})
	return n, failed, err
}

func (c chatgptConv) normalize(idx int) conversation {
	id := firstNonEmpty(c.ConversationID, c.ID, "conversation-"+strconv.Itoa(idx))
	return conversation{
		source: formatChatGPT,
		id:     id,
		title:  strings.TrimSpace(c.Title),
		create: epochTime(c.CreateTime),
		turns:  c.walkMapping(),
	}
}

// walkMapping flattens ChatGPT's message graph into reading order.
//
// The export is a tree, not a list: every edit or regeneration forks a branch,
// and the mapping keeps all of them. Depth-first from the roots keeps each
// branch contiguous and, unlike sorting by create_time, still produces a
// stable order when timestamps are missing — which they are on system nodes.
// Abandoned branches are kept: you asked the question, so you should be able
// to find it again.
func (c chatgptConv) walkMapping() []turn {
	ids := sortedKeys(c.Mapping)
	var roots, dangling []string
	for _, id := range ids {
		n := c.Mapping[id]
		if n.Parent == nil || *n.Parent == "" {
			roots = append(roots, id)
			continue
		}
		if _, ok := c.Mapping[*n.Parent]; !ok {
			// A parent missing from the export: the subtree is real but
			// detached, so it trails the thread rather than leading it.
			dangling = append(dangling, id)
		}
	}

	var (
		out   []turn
		seen  = make(map[string]bool, len(c.Mapping))
		stack []string
	)
	// Iterative rather than recursive: a long chat is a chain thousands of
	// nodes deep, and this is a stack that grows on the heap.
	drain := func(from []string) {
		for i := len(from) - 1; i >= 0; i-- {
			stack = append(stack, from[i])
		}
		for len(stack) > 0 {
			id := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seen[id] {
				continue
			}
			seen[id] = true
			n, ok := c.Mapping[id]
			if !ok {
				continue
			}
			if n.Message != nil {
				if t, ok := chatgptTurn(id, *n.Message); ok {
					out = append(out, t)
				}
			}
			for i := len(n.Children) - 1; i >= 0; i-- {
				stack = append(stack, n.Children[i])
			}
		}
	}
	drain(roots)
	drain(dangling)

	// Anything the walk still never reached — a cycle, most likely — holds real
	// messages too. Appending it in sorted order keeps the import lossless and
	// the result deterministic.
	var unreached []string
	for _, id := range ids {
		if !seen[id] {
			unreached = append(unreached, id)
		}
	}
	drain(unreached)
	return out
}

func chatgptTurn(nodeID string, m chatgptMessage) (turn, bool) {
	// Tool plumbing and system scaffolding are marked hidden by the exporter.
	// They were never on screen, and indexing them buries the words that were.
	if hidden, ok := m.Metadata["is_visually_hidden_from_conversation"].(bool); ok && hidden {
		return turn{}, false
	}
	text := strings.TrimSpace(chatgptText(m.Content))
	if text == "" {
		return turn{}, false
	}
	return turn{
		id:      firstNonEmpty(m.ID, nodeID),
		role:    firstNonEmpty(m.Author.Role, "unknown"),
		text:    text,
		created: epochTime(m.CreateTime),
	}, true
}

func chatgptText(c chatgptContent) string {
	var parts []string
	for _, raw := range c.Parts {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if s = strings.TrimSpace(s); s != "" {
				parts = append(parts, s)
			}
			continue
		}
		// A multimodal part is an object. The ones carrying words carry them
		// under "text"; the rest are image pointers with nothing to index.
		var obj struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &obj) == nil {
			if t := strings.TrimSpace(obj.Text); t != "" {
				parts = append(parts, t)
			}
		}
	}
	if len(parts) == 0 {
		for _, s := range []string{c.Text, c.UserProfile, c.UserInstructions} {
			if s = strings.TrimSpace(s); s != "" {
				parts = append(parts, s)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// --- Claude ----------------------------------------------------------------

type claudeConv struct {
	UUID         string          `json:"uuid"`
	Name         string          `json:"name"`
	CreatedAt    string          `json:"created_at"`
	ChatMessages []claudeMessage `json:"chat_messages"`
}

type claudeMessage struct {
	UUID      string `json:"uuid"`
	Text      string `json:"text"`
	Sender    string `json:"sender"`
	CreatedAt string `json:"created_at"`
	Content   []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	} `json:"content"`
	Attachments []struct {
		FileName         string `json:"file_name"`
		ExtractedContent string `json:"extracted_content"`
	} `json:"attachments"`
}

func importClaude(r io.Reader, per string, sink *docSink, warn func(string)) (int, int, error) {
	failed := 0
	n, err := streamArray(r, "conversations.json", func(i int, raw json.RawMessage) error {
		var c claudeConv
		if err := json.Unmarshal(raw, &c); err != nil {
			failed++
			warn(fmt.Sprintf("conversation %d: %v", i+1, err))
			return nil
		}
		return c.normalize(i).emit(per, sink)
	})
	return n, failed, err
}

func (c claudeConv) normalize(idx int) conversation {
	conv := conversation{
		source: formatClaude,
		id:     firstNonEmpty(c.UUID, "conversation-"+strconv.Itoa(idx)),
		title:  strings.TrimSpace(c.Name),
		create: normalizeTime(c.CreatedAt),
	}
	for i, m := range c.ChatMessages {
		text := claudeText(m)
		if text == "" {
			continue
		}
		role := m.Sender
		if role == "human" {
			// "human" and "user" are the same speaker under two names; picking
			// one means --where role=user works across every source.
			role = "user"
		}
		conv.turns = append(conv.turns, turn{
			id:      firstNonEmpty(m.UUID, "m"+strconv.Itoa(i)),
			role:    firstNonEmpty(role, "unknown"),
			text:    text,
			created: normalizeTime(m.CreatedAt),
		})
	}
	return conv
}

func claudeText(m claudeMessage) string {
	var parts []string
	for _, b := range m.Content {
		for _, s := range []string{b.Text, b.Thinking} {
			if s = strings.TrimSpace(s); s != "" {
				parts = append(parts, s)
			}
		}
	}
	if len(parts) == 0 {
		// Older exports carry the message body in a flat "text" field and no
		// content blocks at all.
		if s := strings.TrimSpace(m.Text); s != "" {
			parts = append(parts, s)
		}
	}
	// A pasted file is part of what was said, and it is usually the part with
	// the rare terms in it — exactly what keyword ranking is good at.
	for _, a := range m.Attachments {
		if s := strings.TrimSpace(a.ExtractedContent); s != "" {
			parts = append(parts, strings.TrimSpace(a.FileName+"\n"+s))
		}
	}
	return strings.Join(parts, "\n")
}

type claudeProject struct {
	UUID        string `json:"uuid"`
	Name        string `json:"name"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
	Docs        []struct {
		UUID      string `json:"uuid"`
		Filename  string `json:"filename"`
		Content   string `json:"content"`
		CreatedAt string `json:"created_at"`
	} `json:"docs"`
}

// importClaudeProjects indexes the knowledge files attached to projects. They
// live in their own file in the export and would otherwise be the one part of
// it that never became searchable.
func importClaudeProjects(r io.Reader, sink *docSink, warn func(string)) (int, int, error) {
	failed := 0
	n, err := streamArray(r, "projects.json", func(i int, raw json.RawMessage) error {
		var p claudeProject
		if err := json.Unmarshal(raw, &p); err != nil {
			failed++
			warn(fmt.Sprintf("project %d: %v", i+1, err))
			return nil
		}
		id := firstNonEmpty(p.UUID, "project-"+strconv.Itoa(i))
		base := map[string]any{
			"source": formatClaude, "kind": "project_doc", "project": p.Name, "session": id,
		}
		if t := normalizeTime(p.CreatedAt); t != "" {
			base["created"] = t
		}
		if d := strings.TrimSpace(p.Description); d != "" {
			attrs := copyAttrs(base)
			attrs["name"] = "description"
			if err := sink.add(tennis.Document{
				ID: formatClaude + ":project:" + id, Text: d, Attributes: attrs,
			}); err != nil {
				return err
			}
		}
		for j, d := range p.Docs {
			text := strings.TrimSpace(d.Content)
			if text == "" {
				continue
			}
			attrs := copyAttrs(base)
			attrs["name"] = d.Filename
			if t := normalizeTime(d.CreatedAt); t != "" {
				attrs["created"] = t
			}
			if err := sink.add(tennis.Document{
				ID:         formatClaude + ":project:" + id + ":" + firstNonEmpty(d.UUID, "doc"+strconv.Itoa(j)),
				Text:       text,
				Attributes: attrs,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return n, failed, err
}

// --- Claude Code -----------------------------------------------------------

type ccLine struct {
	Type        string     `json:"type"`
	UUID        string     `json:"uuid"`
	LeafUUID    string     `json:"leafUuid"`
	SessionID   string     `json:"sessionId"`
	Timestamp   string     `json:"timestamp"`
	CWD         string     `json:"cwd"`
	GitBranch   string     `json:"gitBranch"`
	IsMeta      bool       `json:"isMeta"`
	IsSidechain bool       `json:"isSidechain"`
	Summary     string     `json:"summary"`
	AITitle     string     `json:"aiTitle"`
	Message     *ccMessage `json:"message"`
}

type ccMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// importClaudeCode reads local agent transcripts: one JSONL file per session,
// as written under ~/.claude/projects.
func importClaudeCode(a *archive, per string, sink *docSink, warn func(string)) (int, int, error) {
	sessions, failed := 0, 0
	for _, e := range a.entries {
		if !strings.EqualFold(path.Ext(e.path), ".jsonl") {
			continue
		}
		f, err := a.open(e.path)
		if err != nil {
			failed++
			warn(err.Error())
			continue
		}
		conv, bad := readClaudeCodeSession(f, e.path, a, warn)
		f.Close()
		failed += bad
		if len(conv.turns) == 0 {
			continue
		}
		sessions++
		if err := conv.emit(per, sink); err != nil {
			return sessions, failed, err
		}
	}
	return sessions, failed, nil
}

func readClaudeCodeSession(r io.Reader, entry string, a *archive, warn func(string)) (conversation, int) {
	conv := conversation{
		source: formatClaudeCode,
		id:     strings.TrimSuffix(path.Base(entry), path.Ext(entry)),
		extra:  map[string]any{},
	}
	// The transcripts sit one directory down per project, and that directory
	// name is the only record of which repo a session belonged to.
	if dir := path.Dir(entry); dir != "." && dir != "/" {
		conv.extra["project"] = path.Base(dir)
	} else if !a.isZip {
		conv.extra["project"] = path.Base(a.root)
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxImportLineSize)

	failed, n := 0, 0
	for sc.Scan() {
		n++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var l ccLine
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			failed++
			warn(fmt.Sprintf("%s: line %d: %v", entry, n, err))
			continue
		}
		if l.IsMeta {
			continue
		}
		if l.SessionID != "" {
			conv.id = l.SessionID
		}
		if l.CWD != "" {
			conv.extra["cwd"] = l.CWD
		}
		if l.GitBranch != "" {
			conv.extra["branch"] = l.GitBranch
		}
		if conv.create == "" {
			conv.create = normalizeTime(l.Timestamp)
		}

		// A session names itself as it goes, on its own line type. That title is
		// the best label a result can carry, and the last one written is the
		// one that saw the whole session.
		if s := strings.TrimSpace(l.AITitle); s != "" {
			conv.title = s
			continue
		}
		if l.Type == "summary" {
			if s := strings.TrimSpace(l.Summary); s != "" {
				if conv.title == "" {
					conv.title = s
				}
				conv.turns = append(conv.turns, turn{
					id:   firstNonEmpty(l.LeafUUID, l.UUID, "summary"+strconv.Itoa(n)),
					role: "summary", text: s, created: normalizeTime(l.Timestamp),
				})
			}
			continue
		}
		if l.Message == nil {
			continue
		}
		text := ccText(l.Message.Content)
		if text == "" {
			continue
		}
		role := firstNonEmpty(l.Message.Role, l.Type, "unknown")
		if l.IsSidechain {
			// A subagent's turns are real content but not the main thread;
			// naming the role is what lets a search exclude or isolate them.
			role += "/subagent"
		}
		conv.turns = append(conv.turns, turn{
			id:      firstNonEmpty(l.UUID, "line"+strconv.Itoa(n)),
			role:    role,
			text:    text,
			created: normalizeTime(l.Timestamp),
		})
	}
	if err := sc.Err(); err != nil {
		failed++
		warn(fmt.Sprintf("%s: %v", entry, err))
	}
	return conv, failed
}

// ccText pulls the words out of a transcript message.
//
// Only text and thinking blocks are indexed. A transcript's tool_use and
// tool_result blocks are mostly whole file reads and command output — far more
// bytes than the conversation itself — and letting them in would mean every
// search ranked file contents that already live on disk, over what was
// actually said about them.
func ccText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var blocks []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		var got string
		switch b.Type {
		case "text":
			got = b.Text
		case "thinking":
			got = b.Thinking
		}
		if got = strings.TrimSpace(got); got != "" {
			parts = append(parts, got)
		}
	}
	return strings.Join(parts, "\n")
}

// --- shared helpers --------------------------------------------------------

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func copyAttrs(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+2)
	for k, v := range m {
		out[k] = v
	}
	return out
}
