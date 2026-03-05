# COVA v0.1 Runbook

> Updated: 2026-03-05  
> Scope: 初始生产环境（Expert Runtime 接口已上线，专家执行可 mock）

## 1. 组件清单

- `gateway`（北向入口，`/v1/assistant/*`）
- `orchestrator`（状态机、持久化、队列、回调、`/v1/expert/events`）
- `expert-runtime`（`/runtime/v1/*`，可 sync/async）
- `postgres`（任务状态持久化）
- `redis`（durable queue）

## 2. 启动顺序

1. `postgres`
2. `redis`
3. `expert-runtime`
4. `orchestrator`
5. `gateway`

## 3. 启动示例

### 3.1 expert-runtime

```powershell
go run ./cmd/expert-runtime `
  -addr :8082 `
  -registry configs/experts/registry.yaml `
  -auth-enabled=true `
  -auth-token runtime-token-prod `
  -async-default=true `
  -event-callback-url http://127.0.0.1:8081/v1/expert/events `
  -event-signing-secret runtime-event-secret-prod
```

### 3.2 orchestrator

```powershell
go run ./cmd/orchestrator `
  -addr :8081 `
  -registry configs/experts/registry.yaml `
  -runtime-url http://127.0.0.1:8082 `
  -runtime-auth-token runtime-token-prod `
  -runtime-async=true `
  -expert-event-signing-secret runtime-event-secret-prod `
  -postgres-dsn "postgres://user:pass@127.0.0.1:5432/cova?sslmode=disable" `
  -redis-url "redis://127.0.0.1:6379/0"
```

### 3.3 gateway

```powershell
go run ./cmd/gateway `
  -addr :8080 `
  -orchestrator-url http://127.0.0.1:8081 `
  -auth-enabled=true `
  -auth-tokens assistant-token-prod
```

## 4. 健康检查

- `GET http://127.0.0.1:8080/metrics`
- `GET http://127.0.0.1:8081/healthz`
- `GET http://127.0.0.1:8082/runtime/v1/healthz`

## 5. 烟测

1. `POST /v1/assistant/jobs` 提交任务。
2. `GET /v1/assistant/jobs/{job_id}` 观察 `queued/running/succeeded`。
3. `GET /v1/assistant/jobs/{job_id}/result` 获取结果。
4. 检查 orchestrator 日志是否收到 `/v1/expert/events` 回传。

## 6. 故障处理

### 6.1 队列积压上升

1. 检查 `orchestrator` 进程和 `redis` 连通性。
2. 查看 `queue_depth` 与 `runtime` 错误日志。
3. 若 runtime 故障，优先恢复 runtime，再观察 pending 消费恢复。

### 6.2 任务长时间 running

1. 检查 `expert-runtime` 是否持续回传 `/v1/expert/events`。
2. 检查 `expert-event-signing-secret` 是否一致（签名不一致会被拒绝）。
3. 检查 redis pending 是否异常堆积。

### 6.3 回调失败

1. 查询 `/v1/assistant/jobs/{job_id}/deliveries`。
2. 修复下游回调服务后，必要时 `replay` 任务。

## 7. 回滚

1. 执行：

```powershell
pwsh ./scripts/rollback-v0.1.ps1 -LiveDir dist/live -RollbackFrom dist/v0.1.0
```

2. 使用上一个稳定版本二进制重启 `expert-runtime/orchestrator/gateway`。
3. 验证健康检查与烟测。
