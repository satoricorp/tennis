package embed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	// Pinned SHA-256 of each file. The download is HTTPS, but transport
	// security only proves who sent the bytes, not that they are the bytes
	// this release was built against — an upstream re-tag would otherwise be
	// accepted silently, and these bytes get executed as math forever after.
	WeightsSHA, TokenizerSHA string
}

// Builtins are the static models tennis knows how to fetch.
//
// The default is the retrieval-tuned one. The base models score higher on MTEB
// overall but that average is dominated by classification and clustering tasks
// that have nothing to do with search; on retrieval specifically, which is the
// only thing tennis does, potion-retrieval-32M wins.
var Builtins = map[string]ModelSpec{
	"potion-retrieval-32M": {
		Name: "potion-retrieval-32M", Repo: "minishlab/potion-retrieval-32M", Dims: 512, MB: 123,
		WeightsSHA:   "07609e5bd33aad37900b3fd62f4ec96f6daec88ca4d46b9d8b928bfababf6ea0",
		TokenizerSHA: "7d75cbc54318138807c401b0f0c9721117c628b39de8e8e0edb6cb17e0ee7d18",
	},
	"potion-base-8M": {
		Name: "potion-base-8M", Repo: "minishlab/potion-base-8M", Dims: 256, MB: 29,
		WeightsSHA:   "f65d0f325faadc1e121c319e2faa41170d3fa07d8c89abd48ca5358d9a223de2",
		TokenizerSHA: "e67e803f624fb4d67dea1c730d06e1067e1b14d830e2c2202569e3ef0f70bb50",
	},
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
		for _, f := range []struct{ name, dst, sha string }{
			{"model.safetensors", weights, spec.WeightsSHA},
			{"tokenizer.json", tokenizerPath, spec.TokenizerSHA},
		} {
			url := fmt.Sprintf("https://huggingface.co/%s/resolve/main/%s", spec.Repo, f.name)
			if err := download(ctx, url, f.dst, f.sha); err != nil {
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
//
// wantSHA is the pinned SHA-256 of the expected content. HTTPS authenticates
// the server, not the file: if upstream re-tags the artifact, the transfer
// still succeeds and the changed bytes would otherwise be embedded into every
// vector this install ever produces. A mismatch aborts before the rename, so
// nothing unverified ever lands in the cache.
//
// An empty wantSHA is refused rather than treated as "skip verification". A
// spec built without a pinned checksum is a bug, not permission to download
// unverified — the failure mode of silently accepting it is a supply-chain
// hole: a future model spec added without its SHA fields populated would
// download and cache whatever the server returned, no questions asked.
func download(ctx context.Context, url, dst, wantSHA string) error {
	if wantSHA == "" {
		return fmt.Errorf("refusing to download %s: no pinned checksum for this file", url)
	}
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

	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hasher), resp.Body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(hasher.Sum(nil)); got != wantSHA {
		return fmt.Errorf("checksum mismatch for %s:\n  got  %s\n  want %s\nthe upstream file changed or the download was corrupted; refusing to use it", url, got, wantSHA)
	}
	return os.Rename(tmpName, dst)
}
