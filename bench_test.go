package tennis

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// BenchmarkHybridQuery is the number behind the README's performance claims.
// 1,000 documents, a few chunks each, full hybrid query including query
// embedding, both rankers, and fusion.
func BenchmarkHybridQuery(b *testing.B) {
	db, err := Open(b.TempDir() + "/bench.sqlite")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	ns, err := db.CreateNamespace(ctx, "bench", NamespaceOptions{})
	if err != nil {
		b.Fatal(err)
	}

	topics := []string{
		"retry logic with exponential backoff for the http client",
		"login session cookies and token refresh flow",
		"parsing configuration files with strict validation",
		"streaming large uploads to object storage",
		"scheduling background jobs with a cron syntax",
	}
	docs := make([]Document, 1000)
	for i := range docs {
		t := topics[i%len(topics)]
		docs[i] = Document{
			ID:   fmt.Sprintf("doc-%d", i),
			Text: strings.Repeat(fmt.Sprintf("Note %d about %s. ", i, t), 12),
			Attributes: map[string]any{
				"idx": i, "topic": t[:10],
			},
		}
	}
	if _, err := ns.Write(ctx, docs); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := ns.Query(ctx, Query{Text: "keep me signed in between sessions", TopK: 10})
		if err != nil {
			b.Fatal(err)
		}
		if len(res) == 0 {
			b.Fatal("no results")
		}
	}
}
