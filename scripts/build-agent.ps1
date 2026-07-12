# build-agent.ps1 交叉编译 pfagent 各架构二进制到 backend/internal/agentdist/dist/。
# 主控二进制通过 go:embed 打包它们，运行时零外部依赖即可下发安装脚本 + 二进制。
#
# Docker 部署会自动跑等价步骤（见 Dockerfile）；本地（Windows）想让「节点」页面的
# 下载/安装命令可用时，先跑一次这个脚本：  pwsh scripts/build-agent.ps1

$ErrorActionPreference = 'Stop'

$repoRoot = Resolve-Path (Join-Path $PSScriptRoot '..')
$distDir = Join-Path $repoRoot 'backend/internal/agentdist/dist'
New-Item -ItemType Directory -Force -Path $distDir | Out-Null

# agent 目标平台：覆盖绝大多数 VPS。需要更多架构时在此追加。
$targets = @(
  @{ os = 'linux'; arch = 'amd64' },
  @{ os = 'linux'; arch = 'arm64' }
)

$oldCGO = $env:CGO_ENABLED
$oldGOOS = $env:GOOS
$oldGOARCH = $env:GOARCH
try {
  foreach ($t in $targets) {
    $out = Join-Path $distDir "pfagent-$($t.os)-$($t.arch)"
    Write-Host "[build-agent] $($t.os)/$($t.arch) -> $out"
    $env:CGO_ENABLED = '0'
    $env:GOOS = $t.os
    $env:GOARCH = $t.arch
    go build -trimpath -ldflags="-s -w" -o $out (Join-Path $repoRoot 'backend/cmd/pfagent')
    if ($LASTEXITCODE -ne 0) { throw "build failed for $($t.os)/$($t.arch)" }
  }
} finally {
  $env:CGO_ENABLED = $oldCGO
  $env:GOOS = $oldGOOS
  $env:GOARCH = $oldGOARCH
}

Write-Host "[build-agent] 完成，产物在 $distDir"
Get-ChildItem $distDir
