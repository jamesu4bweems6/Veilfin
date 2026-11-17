#!/usr/bin/env sh
# 构建前端并交叉编译常用平台的单文件程序到 dist/。需要 Node（仅构建时）和 Go。
set -e
VERSION=${VERSION:-$(date +%Y%m%d)}
cd "$(dirname "$0")"
(cd web && npm ci --no-audit --no-fund && npx vite build)
mkdir -p dist
for target in windows/amd64 linux/amd64 linux/arm64 darwin/arm64 darwin/amd64; do
  os=${target%/*}; arch=${target#*/}
  out="dist/veilfin-$os-$arch"; [ "$os" = windows ] && out="$out.exe"
  echo "构建 $out"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$out" ./cmd/veilfin
done
ls -lh dist
