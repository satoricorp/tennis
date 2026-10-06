package tennis

import (
	"context"
	"strings"
	"testing"
)

// listTestNS builds a namespace holding two conversations at turn granularity
// plus a loose file, which is the shape `ls` actually has to render.
func listTestNS(t *testing.T) *Namespace {
	t.Helper()
	db := openTest(t)
	ns, err := db.CreateNamespace(context.Background(), "main", NamespaceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	docs := []Document{
		{ID: "chatgpt:c1:m1", Text: "does ITOT trigger the wash sale rule", Attributes: map[string]any{
			"kind": "message", "source": "chatgpt", "session": "c1",
			"title": "Wash sales", "created": "2026-08-16T14:32:05Z", "role": "user"}},
		{ID: "chatgpt:c1:m2", Text: "different index, generally fine", Attributes: map[string]any{
			"kind": "message", "source": "chatgpt", "session": "c1",
			"title": "Wash sales", "created": "2026-08-16T14:33:00Z", "role": "assistant"}},
		{ID: "claude-code:s9:m1", Text: "rotate the signing key", Attributes: map[string]any{
			"kind": "message", "source": "claude-code", "session": "s9",
			"title": "Key rotation", "created": "2026-08-17T09:00:00Z", "role": "user"}},
		{ID: "/notes/a.md", Text: "a plain file with no session", Attributes: map[string]any{
			"kind": "file", "name": "a.md", "modified": "2026-08-16T20:00:00Z"}},
	}
	if _, err := ns.Write(context.Background(), docs); err != nil {
		t.Fatal(err)
	}
	return ns
}

// TestGroupsCollapsesTurns is what makes a listing readable: import stores one
// document per turn, so without grouping "what conversations do I have" is
// answered with one row per message.
func TestGroupsCollapsesTurns(t *testing.T) {
	ns := listTestNS(t)
	ctx := context.Background()

	groups, err := ns.Groups(ctx, "session", ListOptions{}, "title", "source")
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 3 {
		t.Fatalf("got %d groups, want 2 sessions and the file on its own", len(groups))
	}

	// Newest first: the Claude Code session is a day later than the ChatGPT one.
	if groups[0].Key != "s9" {
		t.Errorf("first group is %q, want the most recent session", groups[0].Key)
	}
	if groups[0].Attributes["title"] != "Key rotation" {
		t.Errorf("group title = %v, want the constant title of its turns", groups[0].Attributes["title"])
	}
	if groups[0].Attributes["source"] != "claude-code" {
		t.Errorf("group source = %v", groups[0].Attributes["source"])
	}

	var chatgpt GroupInfo
	for _, g := range groups {
		if g.Key == "c1" {
			chatgpt = g
		}
	}
	if chatgpt.Documents != 2 {
		t.Errorf("group c1 holds %d documents, want both of its turns", chatgpt.Documents)
	}
	if chatgpt.Chunks < 2 {
		t.Errorf("group c1 reports %d chunks, want at least one per turn", chatgpt.Chunks)
	}
}

func TestGroupsRespectsFilterAndOrder(t *testing.T) {
	ns := listTestNS(t)
	ctx := context.Background()

	groups, err := ns.Groups(ctx, "session", ListOptions{Filter: Eq("source", "chatgpt")}, "title")
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Key != "c1" {
		t.Fatalf("filtered groups = %+v, want only c1", groups)
	}

	asc, err := ns.Groups(ctx, "session", ListOptions{Asc: true}, "title")
	if err != nil {
		t.Fatal(err)
	}
	// The file has no created date, and sorts last either way rather than
	// opening an oldest-first listing.
	if asc[0].Key != "c1" || asc[len(asc)-1].Key != "/notes/a.md" {
		t.Errorf("oldest-first listing runs %q … %q, want c1 first and the file last", asc[0].Key, asc[len(asc)-1].Key)
	}
}

// TestGroupsKeepsFiles is the regression test for a listing that dropped every
// document without a session: after adding a folder of notes, ls said the
// namespace was empty. A file is a row of its own, and with a fallback sort it
// takes its place by date among the sessions.
func TestGroupsKeepsFiles(t *testing.T) {
	ns := listTestNS(t)
	ctx := context.Background()

	groups, err := ns.Groups(ctx, "session", ListOptions{SortFallback: "modified"}, "title", "source")
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, g := range groups {
		keys = append(keys, g.Key)
	}
	// The file was modified the evening of the ChatGPT conversation, before
	// the next morning's Claude Code session.
	if strings.Join(keys, " ") != "s9 /notes/a.md c1" {
		t.Fatalf("groups ran %v, want the file between the two sessions by date", keys)
	}

	file := groups[1]
	if !file.Ungrouped || file.Documents != 1 || file.Chunks < 1 {
		t.Errorf("file row = %+v, want an ungrouped row of one document", file)
	}
	if file.Attributes["name"] != "a.md" || file.Attributes["modified"] != "2026-08-16T20:00:00Z" {
		t.Errorf("file row attributes = %v, want the file's own", file.Attributes)
	}
	if _, ok := file.Attributes["created"]; ok {
		t.Errorf("file row reports a created date it does not have: %v", file.Attributes)
	}
	for _, g := range []GroupInfo{groups[0], groups[2]} {
		if g.Ungrouped {
			t.Errorf("session %s is marked ungrouped", g.Key)
		}
		if _, ok := g.Attributes["kind"]; ok {
			t.Errorf("session %s carries per-document attributes: %v", g.Key, g.Attributes)
		}
	}

	// The flat listing interleaves the same way.
	infos, err := ns.List(ctx, ListOptions{SortFallback: "modified"})
	if err != nil {
		t.Fatal(err)
	}
	if infos[1].ID != "/notes/a.md" {
		t.Errorf("second document is %q, want the file, by its modified date", infos[1].ID)
	}

	sessions, files, err := ns.CountGroups(ctx, "session", nil)
	if err != nil {
		t.Fatal(err)
	}
	if sessions != 2 || files != 1 {
		t.Errorf("CountGroups = %d, %d; want 2 sessions and 1 file", sessions, files)
	}
}

// TestGroupsOnlyFiles is the namespace the README's quick start leaves
// behind: a folder of notes and nothing else.
func TestGroupsOnlyFiles(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	ns, err := db.CreateNamespace(ctx, "notes", NamespaceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	docs := []Document{
		{ID: "/notes/auth.md", Text: "keep the user signed in", Attributes: map[string]any{
			"kind": "file", "name": "auth.md", "modified": "2026-08-10T09:00:00Z"}},
		{ID: "/notes/config.md", Text: "merge the file over the defaults", Attributes: map[string]any{
			"kind": "file", "name": "config.md", "modified": "2026-08-12T09:00:00Z"}},
	}
	if _, err := ns.Write(ctx, docs); err != nil {
		t.Fatal(err)
	}

	groups, err := ns.Groups(ctx, "session", ListOptions{SortFallback: "modified"}, "title", "source")
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].Key != "/notes/config.md" || groups[1].Key != "/notes/auth.md" {
		t.Fatalf("groups = %+v, want both files, newest first", groups)
	}
	for _, g := range groups {
		if !g.Ungrouped || g.Documents != 1 {
			t.Errorf("%s = %+v, want an ungrouped row of one", g.Key, g)
		}
	}

	sessions, files, err := ns.CountGroups(ctx, "session", nil)
	if err != nil {
		t.Fatal(err)
	}
	if sessions != 0 || files != 2 {
		t.Errorf("CountGroups = %d, %d; want no sessions and 2 files", sessions, files)
	}

	// A filter applies to files as it does to sessions.
	page, err := ns.Groups(ctx, "session", ListOptions{Filter: Eq("name", "auth.md")})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].Key != "/notes/auth.md" {
		t.Errorf("filtered groups = %+v, want only auth.md", page)
	}
}

// TestGroupsKeepsSessionAndIDApart: a document with no session is grouped by
// its ID, which must not fold it into a session that happens to share the
// value.
func TestGroupsKeepsSessionAndIDApart(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	ns, err := db.CreateNamespace(ctx, "clash", NamespaceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	docs := []Document{
		{ID: "x", Text: "a document with no session", Attributes: map[string]any{"name": "x"}},
		{ID: "y", Text: "a turn in a session named after the other document", Attributes: map[string]any{"session": "x"}},
	}
	if _, err := ns.Write(ctx, docs); err != nil {
		t.Fatal(err)
	}
	groups, err := ns.Groups(ctx, "session", ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].Ungrouped == groups[1].Ungrouped {
		t.Errorf("groups = %+v, want the session and the document as separate rows", groups)
	}
}

// TestListReturnsNoText is the reason this exists rather than reusing Query: a
// listing must stay cheap on an archive that is mostly transcript.
func TestListReturnsNoText(t *testing.T) {
	ns := listTestNS(t)
	infos, err := ns.List(context.Background(), ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 4 {
		t.Fatalf("got %d documents, want 4", len(infos))
	}
	for _, info := range infos {
		if info.Chars <= 0 {
			t.Errorf("%s reports %d chars; the length should still be known", info.ID, info.Chars)
		}
		if info.Chunks <= 0 {
			t.Errorf("%s reports %d chunks", info.ID, info.Chunks)
		}
	}
	// The document with no created date sorts last rather than first, or a
	// listing would open with every loose file.
	if infos[len(infos)-1].ID != "/notes/a.md" {
		t.Errorf("last document is %q, want the one with no created date", infos[len(infos)-1].ID)
	}
}

func TestListLimitAndCount(t *testing.T) {
	ns := listTestNS(t)
	ctx := context.Background()

	infos, err := ns.List(ctx, ListOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Errorf("limit 2 returned %d", len(infos))
	}
	page2, err := ns.List(ctx, ListOptions{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 2 || page2[0].ID == infos[0].ID {
		t.Errorf("offset did not advance the page: %v then %v", infos[0].ID, page2[0].ID)
	}

	all, err := ns.List(ctx, ListOptions{Limit: -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Errorf("unbounded listing returned %d, want 4", len(all))
	}

	n, err := ns.Count(ctx, Eq("source", "chatgpt"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("Count = %d, want 2", n)
	}
	groups, ungrouped, err := ns.CountGroups(ctx, "session", nil)
	if err != nil {
		t.Fatal(err)
	}
	if groups != 2 || ungrouped != 1 {
		t.Errorf("CountGroups = %d, %d; want 2 sessions and the 1 file without one", groups, ungrouped)
	}
}

// TestListRejectsHostileSortKey: the sort attribute is interpolated into a JSON
// path, so it is the one input here that must not be taken on trust.
func TestListRejectsHostileSortKey(t *testing.T) {
	ns := listTestNS(t)
	ctx := context.Background()
	for _, key := range []string{"created') OR 1=1 --", "a b", ""} {
		if key == "" {
			continue // empty means "use the default"
		}
		if _, err := ns.List(ctx, ListOptions{SortBy: key}); err == nil {
			t.Errorf("SortBy %q was accepted", key)
		}
		if _, err := ns.List(ctx, ListOptions{SortFallback: key}); err == nil {
			t.Errorf("SortFallback %q was accepted", key)
		}
		if _, err := ns.Groups(ctx, key, ListOptions{}); err == nil {
			t.Errorf("group attribute %q was accepted", key)
		}
	}
}
