#!/usr/bin/env bash
# Build release archives using Go and standard system archive tools only.
set -euo pipefail

validate_version() {
  if [[ ! "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$ ]]; then
    printf 'Version must be vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-prerelease; got %q\n' "$1" >&2
    return 2
  fi
}

if [[ "${1:-}" == '--validate-version' ]]; then
  if [[ $# != 2 ]]; then exit 2; fi
  validate_version "$2"
  exit 0
fi
if [[ $# -lt 1 || $# -gt 2 ]]; then
  printf 'Usage: bash scripts/package-release.sh VERSION [OUTPUT_DIR]\n' >&2
  exit 2
fi
release_version=$1
validate_version "$release_version"
release_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
release_out=${2:-"$release_root/dist"}

for dependency in go tar zip; do
  if ! command -v "$dependency" >/dev/null 2>&1; then
    printf 'Required build tool not found: %s\n' "$dependency" >&2
    exit 1
  fi
done
if command -v sha256sum >/dev/null 2>&1; then
  checksum=(sha256sum)
elif command -v shasum >/dev/null 2>&1; then
  checksum=(shasum -a 256)
else
  printf 'Required build tool not found: sha256sum or shasum\n' >&2
  exit 1
fi
mkdir -p "$release_out"
release_out=$(cd "$release_out" && pwd)
release_work=$(mktemp -d)
trap 'rm -rf "$release_work"' EXIT
cd "$release_root"

targets=(linux_amd64 linux_arm64 linux_armv7 darwin_amd64 darwin_arm64 windows_amd64 windows_arm64)
archives=()
for target in "${targets[@]}"; do
  goos=${target%_*}
  goarch=${target#*_}
  goarm=''
  if [[ "$goarch" == 'armv7' ]]; then goarch=arm; goarm=7; fi
  binary=speedprobe
  if [[ "$goos" == 'windows' ]]; then binary=speedprobe.exe; fi
  stage="$release_work/$target"
  mkdir -p "$stage/deploy"
  printf 'Building %s %s\n' "$release_version" "$target"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" GOARM="$goarm" go build \
    -trimpath -buildvcs=false -ldflags "-s -w -X main.version=$release_version" \
    -o "$stage/$binary" ./cmd/speedprobe
  chmod 755 "$stage/$binary"
  cp LICENSE README.md .env.example "$stage/"
  cp deploy/nginx.conf "$stage/deploy/"
  name="speedprobe_${release_version}_${target}"
  if [[ "$goos" == 'windows' ]]; then
    # Build a fresh ZIP: updating an old ZIP would retain obsolete entries.
    (cd "$stage" && zip -q "$release_work/$name.zip" "$binary" LICENSE README.md .env.example deploy/nginx.conf)
    mv "$release_work/$name.zip" "$release_out/$name.zip"
    archives+=("$name.zip")
  else
    # macOS bsdtar otherwise injects AppleDouble/xattr companion files.
    COPYFILE_DISABLE=1 tar -C "$stage" -czf "$release_out/$name.tar.gz" "$binary" LICENSE README.md .env.example deploy/nginx.conf
    archives+=("$name.tar.gz")
  fi
done
(cd "$release_out" && "${checksum[@]}" "${archives[@]}" > checksums.txt)
printf 'Archives and SHA-256 checksums: %s\n' "$release_out"
