package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/satoricorp/tennis"
)

// ls answers "what does tennis actually have" without running a search.
//
// It reads metadata only. A large namespace is tens of megabytes of transcript
// and none of it is needed to list what is in there, so this stays fast at a
// size where search has started to cost real time.
//
// It groups by session rather than listing documents, because import stores one
// document per turn by default: a flat listing answers a question nobody asked,
// thirty thousand rows deep. --docs gives the flat view.
func cmdLS(args []string) error {
	fs_ := flag.NewFlagSet("ls", flag.ExitOnError)
	dbPath := fs_.String("db", defaultDB(), "database file")
	asJSON := fs_.Bool("json", false, "machine-readable output")
	nsName := fs_.String("ns", defaultNamespace, "namespace")
	limit := fs_.Int("n", 25, "how many to list (0 for all)")
	offset := fs_.Int("offset", 0, "skip this many")
	where := fs_.String("where", "", "attribute filter, e.g. source=chatgpt")
	sortBy := fs_.String("sort", "created", "attribute to sort by")
	asc := fs_.Bool("asc", false, "oldest first")
	docs := fs_.Bool("docs", false, "list documents instead of grouping by session")
	pos, err := parseInterleaved(fs_, args)
	if err != nil {
		return err
	}

	// A bare word filters by source, because `tennis ls chatgpt` is what people
	// type before they read the flags.
	filterExpr := *where
	if len(pos) == 1 && filterExpr == "" {
		filterExpr = "source=" + pos[0]
	}

	db, err := open(*dbPath, true)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()

	ns, err := db.Namespace(ctx, resolveNS(*nsName))
	if err != nil {
		return err
	}
	filter, err := parseWhere(filterExpr)
	if err != nil {
		return err
	}
	if *limit == 0 {
		*limit = -1
	}
	opts := tennis.ListOptions{
		Filter: filter, Limit: *limit, Offset: *offset, SortBy: *sortBy, Asc: *asc,
	}

	if *docs {
		return listDocuments(ctx, ns, opts, *offset, *asJSON)
	}
	return listSessions(ctx, ns, opts, *offset, *asJSON)
}

func listSessions(ctx context.Context, ns *tennis.Namespace, opts tennis.ListOptions, offset int, asJSON bool) error {
	groups, err := ns.Groups(ctx, "session", opts, "title", "source")
	if err != nil {
		return err
	}
	if asJSON {
		return emit(groups)
	}
	if len(groups) == 0 {
		fmt.Println("nothing here yet — try: tennis add ~/.claude")
		return nil
	}

	fmt.Printf("%-16s %-12s %6s  %s\n", "DATE", "SOURCE", "DOCS", "TITLE")
	for _, g := range groups {
		title := attrString(g.Attributes, "title")
		if title == "" {
			title = g.Key
		}
		fmt.Printf("%-16s %-12s %6d  %s\n",
			attrDate(g.Attributes, opts.SortBy),
			truncate(attrString(g.Attributes, "source"), 12),
			g.Documents, truncate(title, 60))
	}

	total, err := ns.CountGroups(ctx, "session", opts.Filter)
	if err != nil {
		return err
	}
	printTally(len(groups)+offset, total, "sessions")
	return nil
}

func listDocuments(ctx context.Context, ns *tennis.Namespace, opts tennis.ListOptions, offset int, asJSON bool) error {
	infos, err := ns.List(ctx, opts)
	if err != nil {
		return err
	}
	if asJSON {
		return emit(infos)
	}
	if len(infos) == 0 {
		fmt.Println("nothing here yet — try: tennis add ~/.claude")
		return nil
	}

	fmt.Printf("%-16s %-12s %-8s %6s  %s\n", "DATE", "SOURCE", "KIND", "CHUNKS", "ID")
	for _, info := range infos {
		fmt.Printf("%-16s %-12s %-8s %6d  %s\n",
			attrDate(info.Attributes, opts.SortBy),
			truncate(attrString(info.Attributes, "source"), 12),
			truncate(attrString(info.Attributes, "kind"), 8),
			info.Chunks, truncate(info.ID, 48))
	}

	total, err := ns.Count(ctx, opts.Filter)
	if err != nil {
		return err
	}
	printTally(len(infos)+offset, total, "documents")
	return nil
}

func printTally(shown, total int, what string) {
	if total > shown {
		fmt.Fprintf(os.Stderr, "\n%d of %d %s (--offset %d for more, -n 0 for all)\n", shown, total, what, shown)
		return
	}
	fmt.Fprintf(os.Stderr, "\n%d %s\n", total, what)
}

func attrString(attrs map[string]any, key string) string {
	s, _ := attrs[key].(string)
	return s
}

// attrDate renders whichever timestamp a document carries. Conversations from
// an export have an RFC3339 created date; plain files have a modified time.
func attrDate(attrs map[string]any, key string) string {
	raw := attrs[key]
	if raw == nil {
		raw = attrs["modified"]
	}
	switch v := raw.(type) {
	case string:
		for _, layout := range []string{time.RFC3339, "2006-01-02"} {
			if t, err := time.Parse(layout, v); err == nil {
				return t.Local().Format("2006-01-02 15:04")
			}
		}
		return truncate(v, 16)
	case float64:
		return time.Unix(int64(v), 0).Format("2006-01-02 15:04")
	}
	return ""
}
