#!/bin/sh
# Сборка бинарников и публикация релиза на GitHub:  ./release.sh v0.1.0
set -e
v="${1:?usage: ./release.sh vX.Y.Z}"
rm -rf dist && mkdir dist
for t in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  os=${t%/*}; arch=${t#*/}; ext=""; [ "$os" = windows ] && ext=".exe"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=$v" \
    -o "dist/mirea-moodle-mcp-$os-$arch$ext" .
  echo "  built $os/$arch"
done
(cd dist && shasum -a 256 * > SHA256SUMS)
git tag -a "$v" -m "$v" 2>/dev/null || true
git push origin "$v"
gh release create "$v" dist/* --title "$v" --generate-notes
