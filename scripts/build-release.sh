#!/usr/bin/env bash
set -euo pipefail

# Build portable CLI archives from the checked-out release source.
tag=${1:?Usage: scripts/build-release.sh vX.Y.Z [output-directory]}
output=${2:-dist}
if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo 'Expected a stable version tag such as v1.2.3' >&2
  exit 1
fi
mkdir -p "$output"
output=$(cd "$output" && pwd)
staging=$(mktemp -d)
trap 'rm -rf "$staging"' EXIT
for platform in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  target_os=${platform%/*}
  target_arch=${platform#*/}
  name="invisi-protocol-rpc_${tag}_${target_os}_${target_arch}"
  package="$staging/$name"
  mkdir -p "$package"
  binary=invisi-protocol-rpc
  if [[ "$target_os" == windows ]]; then binary+=.exe; fi
  CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" \
    go build -trimpath -o "$package/$binary" ./cmd
  cp LICENSE README.md "$package/"
  if [[ -f cammand.md ]]; then cp cammand.md "$package/"; fi
  if [[ -f api.md ]]; then cp api.md "$package/"; fi
  if [[ "$target_os" == windows ]]; then
    (cd "$staging" && zip -qr "$output/$name.zip" "$name")
  else
    tar -czf "$output/$name.tar.gz" -C "$staging" "$name"
  fi
  echo "Built $name"
done
# Scope checksums to this release, including when reusing an output directory.
(cd "$output" && sha256sum invisi-protocol-rpc_"$tag"_*.tar.gz invisi-protocol-rpc_"$tag"_*.zip > "SHA256SUMS-$tag.txt")
