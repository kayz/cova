param(
  [string]$LiveDir = "dist/live",
  [string]$RollbackFrom = "dist/v0.1.0"
)

$ErrorActionPreference = "Stop"
$root = Resolve-Path (Join-Path $PSScriptRoot "..")
Set-Location $root

if (-not (Test-Path $RollbackFrom)) {
  throw "rollback source does not exist: $RollbackFrom"
}

New-Item -ItemType Directory -Force -Path $LiveDir | Out-Null

Get-ChildItem -Path $LiveDir -File -ErrorAction SilentlyContinue | Remove-Item -Force
Copy-Item -Path (Join-Path $RollbackFrom "*") -Destination $LiveDir -Recurse -Force

Write-Host "[cova] rollback completed: $RollbackFrom -> $LiveDir"
