package embed

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ModelSpec describes a built-in static model and where to fetch it.
type ModelSpec struct {
	Name string // the name users type, and half of the Embedder ID
	Repo string // Hugging Face repo holding model.safetensors and tokenizer.json
	Dims int    // vector width, verified against the weights at load
	MB   int    // approximate download size, so the first-run message is honest
}

// Builtins are the static models tennis knows how to fetch.
//
// The default is the retrieval-tuned one. The base models score higher on MTEB
// overall but that average is dominated by classification and clustering tasks
// that have nothing to do with search; on retrieval specifically, which is the
// only thing tennis does, potion-retrieval-32M wins.
var Builtins = map[string]ModelSpec{
	"potion-retrieval-32M": {Name: "potion-retrieval-32M", Repo: "minishlab/potion-retrieval-32M", Dims: 512, MB: 123},
	"potion-base-8M":       {Name: "potion-base-8M", Repo: "minishlab/potion-base-8M", Dims: 256, MB: 29},
}

// DefaultModel is what a namespace gets when it does not ask for anything else.
const DefaultModel = "potion-retrieval-32M"

// CacheDir is where downloaded weights live. Honors TENNIS_CACHE so tests and
// sandboxes can redirect it without touching the user's real cache.
func CacheDir() string {
	if d := os.Getenv("TENNIS_CACHE"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "tennis")
	}
	return filepath.Join(home, ".cache", "tennis")
}

// Load returns a static embedder, downloading the model on first use.
//
// progress, if non-nil, is called with human-readable status. It exists so the
// CLI can say what is happening during a 123MB download instead of appearing
// to hang, while the SDK stays silent by default.
func Load(ctx context.Context, name string, progress func(string)) (*Static, error) {
	spec, ok := Builtins[name]
	if !ok {
		return nil, fmt.Errorf("unknown built-in model %q (have: %s)", name, strings.Join(builtinNames(), ", "))
	}
	dir := filepath.Join(CacheDir(), "models", spec.Name)
	weights := filepath.Join(dir, "model.safetensors")
	tokenizerPath := filepath.Join(dir, "tokenizer.json")

	need := !exists(weights) || !exists(tokenizerPath)
	if need {
		if progress != nil {
			progress(fmt.Sprintf("downloading %s (~%dMB, one time) to %s", spec.Name, spec.MB, dir))
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		for _, f := range []struct{ name, dst string }{
			{"model.safetensors", weights},
			{"tokenizer.json", tokenizerPath},
		} {
			url := fmt.Sprintf("https://huggingface.co/%s/resolve/main/%s", spec.Repo, f.name)
			if err := download(ctx, url, f.dst); err != nil {
				return nil, fmt.Errorf("fetching %s: %w", f.name, err)
			}
		}
		if progress != nil {
			progress("model ready")
		}
	}
	return NewStatic(spec, weights, tokenizerPath)
}

func builtinNames() []string {
	names := make([]string, 0, len(Builtins))
	for k := range Builtins {
		names = append(names, k)
	}
	return names
}

func exists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Size() > 0
}

// download writes url to dst via a temporary file, so an interrupted transfer
// leaves no half-written model that would look cached and then fail to parse.
func download(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".partial-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}
