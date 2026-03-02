# COVA

COVA (Collaborative Orchestrator for Verified Agents) 是一个可被多 Agent 调用的专家系统框架。  
当前阶段目标不是做“最强内容能力”，而是先把专家 Agent 的架构打通：统一接口、异步任务、可追溯知识输出。

## 文档导航

- [VISION](./VISION.md)
- [Architecture](./docs/architecture.md)
- [Assistant API](./docs/api-assistant.md)
- [Expert Runtime API](./docs/api-expert.md)
- [Gap Analysis](./docs/gap-analysis.md)
- [Execution Plan](./docs/plan.md)
- [Test & Checkpoints](./docs/checkpoints.md)
- [Roadmap](./docs/roadmap.md)
- [Legacy Unified API Contract](./docs/api-contract.md)
- [OpenAPI v1](./docs/openapi.v1.yaml)

## 本地运行（当前骨架）

1. 启动 orchestrator（状态与调度）：

```bash
go run ./cmd/orchestrator
```

2. 启动 gateway（统一入口，转发到 orchestrator）：

```bash
go run ./cmd/gateway -orchestrator-url http://127.0.0.1:8081
```

说明：

- `gateway` 默认监听 `:8080`
- `orchestrator` 默认监听 `:8081`
- `gateway` 已内置：
  - Bearer 鉴权（`-auth-enabled` + `-auth-tokens`）
  - 按客户端限流（`-rate-limit-enabled`, `-rate-limit-rps`, `-rate-limit-burst`）
  - JSON POST 请求校验（`-max-body-bytes`）
  - 结构化访问日志（`-access-log-enabled`）
  - 基础指标接口（默认 `GET /metrics`，可通过 `-metrics-path` 修改）
- `worker` 当前提供 mock 运行时与历史兼容入口，后续将逐步收敛为纯执行组件

## 为什么做 COVA

- 让主 Agent（如 `coco`）只依赖一个统一的专家接口。
- 支持多个专家能力按同一契约接入（固收、宏观、行业等）。
- 支持异步综述生成（需要时间的任务不阻塞调用方）。
- 保持专家服务无状态，推理凭证可由调用方透传。

## 项目目标（Phase 1）

- 建立统一 Expert API（提交任务、查询状态、读取结果、知识问答）。
- 跑通“RSS -> 知识库 -> 每日综述 -> 异步回传”的最小闭环。
- 输出结果包含证据引用（citations）和新鲜度时间戳（freshness）。
- 形成可扩展到多专家的路由与调度骨架。

## 非目标（Phase 1）

- 不追求内容质量最优。
- 不追求完整前端产品化。
- 不做复杂多租户计费系统。

## 高层架构

```text
Caller Agent (coco / others)
          |
          v
   [COVA Gateway]
   - Auth / Idempotency
   - Routing / Job API
          |
          +----------------------+
          |                      |
          v                      v
 [FI Expert Service]      [Other Expert ...]
 - RSS ingestion
 - KB retrieval
 - LLM synthesis
          |
          v
 [Storage]
 - Article store
 - Vector index
 - Job store
```

## 统一接口（建议）

### 1) 提交异步综述任务

`POST /v1/jobs/brief`

请求示例：

```json
{
  "expert_type": "fixed_income",
  "date": "2026-02-27",
  "timezone": "Asia/Shanghai",
  "callback_url": "https://caller.example.com/hooks/cova",
  "callback_auth": {
    "type": "hmac",
    "secret_ref": "vault://caller/cova-hook-secret"
  },
  "inference": {
    "provider": "openai",
    "credential_mode": "delegated_token",
    "token": "opaque-short-lived-token"
  },
  "idempotency_key": "brief-2026-02-27-fi"
}
```

响应示例（`202 Accepted`）：

```json
{
  "job_id": "job_01J...",
  "status": "queued",
  "submitted_at": "2026-02-27T08:00:00Z"
}
```

### 2) 查询任务状态

`GET /v1/jobs/{job_id}`

响应示例：

```json
{
  "job_id": "job_01J...",
  "status": "running",
  "progress": 62,
  "updated_at": "2026-02-27T08:02:30Z"
}
```

### 3) 读取任务结果

`GET /v1/jobs/{job_id}/result`

响应示例：

```json
{
  "job_id": "job_01J...",
  "status": "succeeded",
  "result": {
    "title": "2026-02-27 固收市场综述",
    "answer": "......",
    "confidence": 0.81,
    "freshness_cutoff": "2026-02-27T07:59:10Z",
    "citations": [
      {
        "article_id": "art_123",
        "source": "Some Institution",
        "title": "....",
        "url": "https://...",
        "published_at": "2026-02-27T06:20:00Z"
      }
    ],
    "limitations": [
      "海外数据源延迟约 30 分钟"
    ]
  }
}
```

### 4) 同步知识问答（可选）

`POST /v1/query`

用于快速问题查询，返回结构与 `result` 对齐（answer/citations/confidence/freshness）。

## 异步回调（Webhook）

任务完成后，COVA 向 `callback_url` 推送事件：

```json
{
  "event_type": "com.cova.job.completed",
  "event_id": "evt_01J...",
  "occurred_at": "2026-02-27T08:05:10Z",
  "data": {
    "job_id": "job_01J...",
    "status": "succeeded",
    "result_url": "/v1/jobs/job_01J.../result"
  }
}
```

建议：

- 回调签名：`HMAC-SHA256`
- 重试策略：指数退避 + 最大重试次数
- 事件幂等：`event_id` 去重

## 安全与无状态约束

- COVA 服务节点不保存调用方明文 API Key。
- 使用短期委托令牌（delegated token）或调用方代理推理。
- 敏感字段默认脱敏日志，不写入普通应用日志。
- 业务状态持久化到外部存储（DB/Queue/Object Storage），实例可随时重建。

## 仓库规划（建议）

```text
/docs
  architecture.md
  api-contract.md
/cmd
  gateway/
  orchestrator/
  worker/
  ingestion/
/internal
  api/ orchestrator/ registry/ runtime/ storage/ queue/ model/ security/ delivery/
/experts
  _template/
  ficcobservor/
/configs
  experts/registry.yaml
/sdk
  /go
    (主 Agent 调用 SDK)
```

## 里程碑

### M1: 契约冻结

- OpenAPI 初版
- 任务状态机定义
- 统一返回 Schema

### M2: 最小闭环

- 固收 RSS 接入
- 每日综述异步生成
- webhook 回传可用

### M3: 平台化

- 多专家路由
- 观测与告警
- SDK 与示例调用

## 与当前原型的关系

当前仓库已有一个前后端 POC（RSS 抓取 + 文章/综述接口）。  
后续可逐步演进为 COVA 架构，而不是一次性重写：

- 先保留现有采集与文章接口作为 `fixed-income` 专家内部实现。
- 再补统一 `jobs` API 与回调机制。
- 最后将调用入口收敛到统一 Gateway。

## License

Planned: MIT
