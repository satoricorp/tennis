package tennis

import (
	"context"
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
			"kind": "file", "name": "a.md"}},
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
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2 — the file has no session and must not become one", len(groups))
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
	if asc[0].Key != "c1" {
		t.Errorf("oldest-first listing starts at %q, want c1", asc[0].Key)
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
	groups, err := ns.CountGroups(ctx, "session", nil)
	if err != nil {
		t.Fatal(err)
	}
	if groups != 2 {
		t.Errorf("CountGroups = %d, want 2", groups)
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
		if _, err := ns.Groups(ctx, key, ListOptions{}); err == nil {
			t.Errorf("group attribute %q was accepted", key)
		}
	}
}
