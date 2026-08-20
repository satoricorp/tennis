#!/usr/bin/env bash
# Renders dist/manifest.json: what the current release is, and where each
# platform's archive lives. Consumed by the website's install snippet and by
# anything that wants to check the published version without a GitHub API call.
#
#   VERSION=0.1.0 TENNIS_DOWNLOAD_HOST=get.example.sh scripts/render-manifest.sh dist
set -euo pipefail

dist_dir="${1:-dist}"
version="${VERSION:?VERSION is required}"
host="${TENNIS_DOWNLOAD_HOST:?TENNIS_DOWNLOAD_HOST is required}"
published_at="${PUBLISHED_AT:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

version="${version#v}"
base_url="https://${host%/}"
checksums="$dist_dir/checksums.txt"
[[ -f "$checksums" ]] || { echo "no $checksums — run goreleaser first" >&2; exit 1; }

assets="{}"
for archive in "$dist_dir"/tennis_"$version"_*.tar.gz; do
  [[ -f "$archive" ]] || continue
  base="$(basename "$archive")"
  platform="${base#tennis_"${version}"_}"
  platform="${platform%.tar.gz}"
  goos="${platform%_*}"
  goarch="${platform##*_}"

  # goreleaser writes one checksums.txt for the whole release; take this
  # archive's line rather than recomputing, so the manifest and the file the
  # installer verifies against can never disagree.
  sha="$(awk -v want="$base" '$2 == want || $2 == "*" want {print $1}' "$checksums")"
  [[ -n "$sha" ]] || { echo "$base missing from checksums.txt" >&2; exit 1; }
  size="$(wc -c < "$archive" | tr -d ' ')"

  assets="$(jq -n --argjson acc "$assets" \
    --arg key "$goos/$goarch" \
    --arg url "$base_url/v$version/$base" \
    --arg sha "$sha" --argjson size "$size" \
    '$acc + {($key): {url: $url, sha256: $sha, size: $size}}')"
done

[[ "$(jq 'length' <<<"$assets")" -gt 0 ]] || {
  echo "no archives for version $version in $dist_dir" >&2; exit 1
}

jq -n --arg version "$version" --arg published_at "$published_at" \
  --arg install_url "$base_url/install.sh" \
  --arg install_command "curl -fsSL $base_url/install.sh | sh" \
  --argjson assets "$assets" \
  '{schema_version: 1, version: $version, published_at: $published_at,
    install_url: $install_url, install_command: $install_command,
    assets: $assets}' > "$dist_dir/manifest.json"

echo "Wrote $dist_dir/manifest.json"
