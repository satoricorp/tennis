// Command basic is the README's Go example, kept compilable so it cannot rot.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/satoricorp/tennis"
)

func main() {
	ctx := context.Background()

	db, err := tennis.Open(filepath.Join(os.TempDir(), "tennis-example.sqlite"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	ns, err := db.Namespace(ctx, "agents")
	if err != nil {
		// The embedder is chosen here, once, and bound to the namespace forever.
		ns, err = db.CreateNamespace(ctx, "agents", tennis.NamespaceOptions{})
		if err != nil {
			log.Fatal(err)
		}
	}

	if _, err := ns.Write(ctx, []tennis.Document{
		{ID: "a1", Text: "make the login flow remember the user between sessions",
			Attributes: map[string]any{"status": "merged", "cost": 4}},
		{ID: "a2", Text: "write a parser for TOML configuration files",
			Attributes: map[string]any{"status": "open", "cost": 8}},
	}); err != nil {
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
		fmt.Printf("%.4f  %s  (kw#%d sem#%d)\n  %s\n", r.Score, r.ID, r.KeywordRank, r.SemanticRank, r.Text)
	}
}
