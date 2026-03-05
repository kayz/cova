param(
  [string]$GatewayAddr = ":8080",
  [string]$OrchestratorAddr = ":8081",
  [string]$RuntimeAddr = ":8082",
  [string]$RegistryPath = "configs/experts/registry.yaml",
  [string]$RuntimeAuthToken = "runtime-token-dev",
  [string]$ExpertEventSecret = "runtime-event-secret-dev",
  [string]$PostgresDsn = "",
  [string]$RedisUrl = ""
)

$ErrorActionPreference = "Stop"
$root = Resolve-Path (Join-Path $PSScriptRoot "..")
Set-Location $root

Write-Host "[cova] starting v0.1 stack from $root"

$runtimeArgs = @(
  "run", "./cmd/expert-runtime",
  "-addr", $RuntimeAddr,
  "-registry", $RegistryPath,
  "-auth-enabled=true",
  "-auth-token", $RuntimeAuthToken,
  "-async-default=true",
  "-event-callback-url", "http://127.0.0.1$OrchestratorAddr/v1/expert/events",
  "-event-signing-secret", $ExpertEventSecret
)

$orchestratorArgs = @(
  "run", "./cmd/orchestrator",
  "-addr", $OrchestratorAddr,
  "-registry", $RegistryPath,
  "-runtime-url", "http://127.0.0.1$RuntimeAddr",
  "-runtime-auth-token", $RuntimeAuthToken,
  "-runtime-async=true",
  "-expert-event-signing-secret", $ExpertEventSecret
)

if ($PostgresDsn -ne "") {
  $orchestratorArgs += @("-postgres-dsn", $PostgresDsn)
}
if ($RedisUrl -ne "") {
  $orchestratorArgs += @("-redis-url", $RedisUrl)
}

$gatewayArgs = @(
  "run", "./cmd/gateway",
  "-addr", $GatewayAddr,
  "-orchestrator-url", "http://127.0.0.1$OrchestratorAddr"
)

$runtimeProc = Start-Process -FilePath "go" -ArgumentList $runtimeArgs -PassThru
Start-Sleep -Seconds 1
$orchestratorProc = Start-Process -FilePath "go" -ArgumentList $orchestratorArgs -PassThru
Start-Sleep -Seconds 1
$gatewayProc = Start-Process -FilePath "go" -ArgumentList $gatewayArgs -PassThru

Write-Host "[cova] started"
Write-Host "  runtime pid:      $($runtimeProc.Id)"
Write-Host "  orchestrator pid: $($orchestratorProc.Id)"
Write-Host "  gateway pid:      $($gatewayProc.Id)"
Write-Host ""
Write-Host "health checks:"
Write-Host "  curl http://127.0.0.1$GatewayAddr/metrics"
Write-Host "  curl http://127.0.0.1$OrchestratorAddr/healthz"
Write-Host "  curl http://127.0.0.1$RuntimeAddr/runtime/v1/healthz"
