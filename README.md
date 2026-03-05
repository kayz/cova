# COVA

COVA (Collaborative Orchestrator for Verified Agents) 是面向助理 Agent 的外置专家平台。  
当前代码已实现多租户 Assistant API、异步任务状态机、专家准入校验、基础可靠性与观测能力。

## 文档导航

- [VISION](./VISION.md)
- [Architecture](./docs/architecture.md)
- [Assistant API](./docs/api-assistant.md)
- [Expert Runtime API](./docs/api-expert.md)
- [Gap Analysis](./docs/gap-analysis.md)
- [Execution Plan](./docs/plan.md)
- [Release v0.1.0](./docs/release-v0.1.md)
- [Runbook v0.1](./docs/runbook-v0.1.md)
- [Test & Checkpoints](./docs/checkpoints.md)
- [Delivery Log 2026-03-03](./docs/log/2026-03-03.md)
- [OpenAPI v1](./docs/openapi.v1.yaml)

## 当前实现状态（与代码一致）

- Assistant API 基线路径：`/v1/assistant/*`
- 多租户头：`X-Tenant-Id` + `X-Project-Id`（必填）
- 任务接口：`submit/status/result/deliveries/cancel/replay/query/experts`
- 状态机：`queued/running/succeeded/failed/canceled/expired`
- 幂等：`Idempotency-Key`（按 tenant/project 作用域）
- 回调：HMAC 签名、重试、投递记录
- 观测：`gateway`、`orchestrator`、`worker` 均提供 `metrics/healthz`
- SDK：`coco` 与 `openclaw` 适配层
- Expert Runtime：已提供 `runtime/v1` 服务端（`cmd/expert-runtime`）

说明：专家执行仍可使用 mock adapter；`orchestrator` 已支持通过参数切换 Postgres 状态存储与 Redis Streams durable queue。

## 本地运行

1. 启动 orchestrator

```bash
go run ./cmd/orchestrator
```

2. 启动 gateway

```bash
go run ./cmd/gateway -orchestrator-url http://127.0.0.1:8081
```

3. 启动 expert runtime（建议）

```bash
go run ./cmd/expert-runtime -addr :8082 -event-callback-url http://127.0.0.1:8081/v1/expert/events
```

可选：启动 mock worker（兼容旧入口）

```bash
go run ./cmd/worker
```

默认监听：

- `gateway`: `:8080`
- `orchestrator`: `:8081`
- `worker`: `:8080`（独立运行时）
- `expert-runtime`: `:8082`

默认观测端点：

- `GET /metrics`
- `GET /healthz`

## v0.1 脚本

- 一键启动（开发环境）：`pwsh ./scripts/run-v0.1-stack.ps1`
- 打包发布：`pwsh ./scripts/release-v0.1.ps1`
- 版本回滚：`pwsh ./scripts/rollback-v0.1.ps1`

## 快速验证

运行测试：

```bash
go test ./...
```

使用 API 时，请确保携带：

- `Authorization: Bearer <token>`
- `X-Tenant-Id: <tenant>`
- `X-Project-Id: <project>`
