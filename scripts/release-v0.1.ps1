param(
  [string]$Version = "v0.1.0",
  [string]$OutputRoot = "dist"
)

$ErrorActionPreference = "Stop"
$root = Resolve-Path (Join-Path $PSScriptRoot "..")
Set-Location $root

$outDir = Join-Path $OutputRoot $Version
New-Item -ItemType Directory -Force -Path $outDir | Out-Null

Write-Host "[cova] running tests"
go test ./...

Write-Host "[cova] building binaries to $outDir"
go build -o (Join-Path $outDir "gateway.exe") ./cmd/gateway
go build -o (Join-Path $outDir "orchestrator.exe") ./cmd/orchestrator
go build -o (Join-Path $outDir "expert-runtime.exe") ./cmd/expert-runtime

Copy-Item -Path "README.md" -Destination (Join-Path $outDir "README.md") -Force
Copy-Item -Path "docs\release-v0.1.md" -Destination (Join-Path $outDir "release-v0.1.md") -Force
Copy-Item -Path "docs\runbook-v0.1.md" -Destination (Join-Path $outDir "runbook-v0.1.md") -Force

Write-Host "[cova] release artifacts ready: $outDir"
