#!/bin/sh
# Puts the built-in embedding model where the model-backed tests look for it.
#
# Without it those tests skip themselves, which in CI meant the whole root
# package and every end-to-end CLI test never ran. The download goes through
# tennis itself, so it is checked against the SHA-256 pinned in
# embed/models.go exactly as a user's first run is; a changed upstream file
# fails here instead of being tested against.
#
#   scripts/fetch-test-model.sh
#
# Run from the repository root. Safe to run again: an existing copy is kept.
set -eu

model=potion-retrieval-32M
cache="testdata/cache"
dir="$cache/models/$model"

if [ ! -s "$dir/model.safetensors" ] || [ ! -s "$dir/tokenizer.json" ]; then
  scratch="$(mktemp -d)"
  trap 'rm -rf "$scratch"' EXIT
  # Creating a namespace bound to the model is what makes tennis fetch it.
  TENNIS_CACHE="$PWD/$cache" go run ./cmd/tennis ns create warm \
    --model "$model" --db "$scratch/warm.sqlite"
fi

# embed's reference test reads the weights from testdata/ directly.
cp "$dir/model.safetensors" "$dir/tokenizer.json" testdata/
echo "test model ready in $dir"
