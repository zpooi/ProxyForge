#!/usr/bin/env bash
set -euo pipefail

# build-agent.sh 交叉编译 pfagent 各架构二进制到 backend/internal/agentdist/dist/。
# 主控二进制通过 go:embed 打包它们，运行时零外部依赖即可下发安装脚本 + 二进制。
#
# Docker 部署会自动跑等价步骤（见 Dockerfile）；本地想让「节点」页面的下载/安装
# 命令可用时，先跑一次这个脚本。

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dist_dir="$repo_root/backend/internal/agentdist/dist"
mkdir -p "$dist_dir"

# agent 目标平台：覆盖绝大多数 VPS。需要更多架构时在此追加。
targets=(
  "linux/amd64"
  "linux/arm64"
)

for target in "${targets[@]}"; do
  goos="${target%%/*}"
  goarch="${target##*/}"
  out="$dist_dir/pfagent-$goos-$goarch"
  echo "[build-agent] $goos/$goarch -> $out"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags="-s -w" -o "$out" "$repo_root/backend/cmd/pfagent"
done

echo "[build-agent] 完成，产物在 $dist_dir"
ls -lh "$dist_dir"
