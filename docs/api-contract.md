# COVA API Contract (v1 Draft)

## 1. 设计目标

- 为调用方 Agent 提供统一专家调用接口。
- 支持异步任务（适合 daily brief 等耗时任务）。
- 支持同步问答（适合快速检索与短回答）。
- 返回结果必须可追溯（references）并包含时效边界（freshness）。

## 2. 通用约定

Base URL（示例）：

`https://api.cova.example.com`

版本：

- 使用 URI 版本：`/v1`

Headers：

- `Authorization: Bearer <token>`
- `Content-Type: application/json`
- `X-Request-Id: <uuid>`（建议）
- `Idempotency-Key: <opaque-key>`（对创建类请求强烈建议）

时间格式：

- RFC3339 / ISO 8601，UTC 优先，例如 `2026-02-27T08:00:00Z`

错误格式（Problem Details）：

```json
{
  "type": "https://api.cova.example.com/errors/invalid-request",
  "title": "Invalid request",
  "status": 400,
  "detail": "field `expert_id` is required",
  "instance": "/v1/jobs/brief",
  "request_id": "req_01J..."
}
```

## 3. 专家标识与枚举

专家唯一标识：

- `expert_id`：唯一专家实例 ID（必填，路由主键）
- `expert_name`：专家展示名（建议传，便于可读性与审计）
- `expert_type`：专家领域分类（可选，不作为唯一标识）

`expert_type` 当前约定：

- `fixed_income`
- `macro`（预留）
- `equity`（预留）

`job_status` 枚举：

- `queued`
- `running`
- `succeeded`
- `failed`
- `canceled`
- `expired`

## 4. 接口定义

## 4.1 提交异步综述任务

`POST /v1/jobs/brief`

请求体：

```json
{
  "expert_id": "fi_cn_primary",
  "expert_name": "CN FI Daily Analyst",
  "expert_type": "fixed_income",
  "date": "2026-02-27",
  "timezone": "Asia/Shanghai",
  "callback": {
    "url": "https://caller.example.com/hooks/cova",
    "signing": {
      "algorithm": "hmac-sha256",
      "secret_ref": "vault://caller/cova-hook-secret"
    }
  },
  "inference": {
    "provider": "openai",
    "credential_mode": "delegated_token",
    "token": "opaque-short-lived-token"
  },
  "options": {
    "max_sources": 50,
    "language": "zh-CN"
  }
}
```

字段约束：

- `expert_id` 必填（唯一指代专家）
- `expert_name` 建议传（解释性字段）
- `expert_type` 可选（领域分类标签）
- `date` 必填，格式 `YYYY-MM-DD`
- `callback.url` 可选；不传时调用方通过轮询获取结果
- `inference.token` 可选；建议短期令牌，不应为长期明文 API Key

响应：

- `202 Accepted`

```json
{
  "job_id": "job_01JABCDEF",
  "expert_id": "fi_cn_primary",
  "expert_name": "CN FI Daily Analyst",
  "expert_type": "fixed_income",
  "status": "queued",
  "submitted_at": "2026-02-27T08:00:00Z",
  "request_id": "req_01JXYZ"
}
```

幂等：

- 相同 `Idempotency-Key` + 相同请求体，返回同一 `job_id`

## 4.2 查询任务状态

`GET /v1/jobs/{job_id}`

响应：

- `200 OK`

```json
{
  "job_id": "job_01JABCDEF",
  "expert_id": "fi_cn_primary",
  "expert_name": "CN FI Daily Analyst",
  "expert_type": "fixed_income",
  "status": "running",
  "progress": 65,
  "submitted_at": "2026-02-27T08:00:00Z",
  "updated_at": "2026-02-27T08:03:20Z",
  "attempt": 1
}
```

错误：

- `404`：任务不存在

## 4.3 获取任务结果

`GET /v1/jobs/{job_id}/result`

响应：

- `200 OK`（任务成功）

```json
{
  "job_id": "job_01JABCDEF",
  "expert_id": "fi_cn_primary",
  "expert_name": "CN FI Daily Analyst",
  "expert_type": "fixed_income",
  "status": "succeeded",
  "result": {
    "title": "2026-02-27 固收市场综述",
    "answer": "......",
    "confidence": 0.81,
    "freshness_cutoff": "2026-02-27T07:59:10Z",
    "references": [
      {
        "kind": "article",
        "ref_id": "src_001",
        "title": "债券市场日报",
        "uri": "https://example.com/report/1",
        "published_at": "2026-02-27T06:20:00Z",
        "metadata": {
          "source_name": "Example Research"
        }
      }
    ],
    "limitations": [
      "部分海外源存在发布延迟"
    ]
  }
}
```

说明：

- `references` 为可扩展证据结构，不对字段做跨领域强约束。
- 固收专家可以返回 article/report 样式引用，其他专家可返回 API 记录、数据库快照、图表对象等。

- `409 Conflict`（任务未完成）

```json
{
  "type": "https://api.cova.example.com/errors/result-not-ready",
  "title": "Result not ready",
  "status": 409,
  "detail": "job is still running"
}
```

## 4.4 同步知识问答

`POST /v1/query`

请求体：

```json
{
  "expert_id": "fi_cn_primary",
  "expert_name": "CN FI Daily Analyst",
  "expert_type": "fixed_income",
  "question": "今天利率债和信用债的主要分化是什么？",
  "time_window": {
    "start": "2026-02-26T00:00:00Z",
    "end": "2026-02-27T08:00:00Z"
  },
  "inference": {
    "provider": "openai",
    "credential_mode": "delegated_token",
    "token": "opaque-short-lived-token"
  }
}
```

响应：

```json
{
  "answer": "......",
  "confidence": 0.77,
  "freshness_cutoff": "2026-02-27T07:58:11Z",
  "references": [
    {
      "kind": "article",
      "ref_id": "src_018",
      "title": "利率债早评",
      "uri": "https://example.com/a",
      "published_at": "2026-02-27T07:30:00Z"
    }
  ],
  "limitations": []
}
```

## 4.5 查询专家能力（建议）

`GET /v1/experts/capabilities`

响应：

```json
{
  "experts": [
    {
      "expert_id": "fi_cn_primary",
      "expert_name": "CN FI Daily Analyst",
      "expert_type": "fixed_income",
      "supports": ["daily_brief", "query"],
      "status": "active",
      "version": "1.0.0"
    }
  ]
}
```

## 4.6 查询 Webhook 投递记录（建议）

`GET /v1/jobs/{job_id}/deliveries`

响应：

- `200 OK`

```json
{
  "job_id": "job_01JABCDEF",
  "deliveries": [
    {
      "delivery_id": "evt_01JABCDE.1",
      "event_id": "evt_01JABCDE",
      "event_type": "com.cova.job.completed",
      "job_id": "job_01JABCDEF",
      "callback_url": "https://caller.example.com/hooks/cova",
      "attempt": 1,
      "max_attempts": 10,
      "success": false,
      "http_status": 500,
      "error": "unexpected status code: 500",
      "requested_at": "2026-02-27T08:05:12Z",
      "duration_ms": 83,
      "next_retry_at": "2026-02-27T08:05:12.400Z",
      "payload_sha256": "4a4c...f98"
    },
    {
      "delivery_id": "evt_01JABCDE.2",
      "event_id": "evt_01JABCDE",
      "event_type": "com.cova.job.completed",
      "job_id": "job_01JABCDEF",
      "callback_url": "https://caller.example.com/hooks/cova",
      "attempt": 2,
      "max_attempts": 10,
      "success": true,
      "http_status": 204,
      "requested_at": "2026-02-27T08:05:12.400Z",
      "duration_ms": 36,
      "payload_sha256": "4a4c...f98"
    }
  ]
}
```

说明：

- `deliveries` 按尝试顺序返回（`attempt` 递增）。
- 用于排障与审计，不影响任务主状态机。

## 5. Webhook Contract

回调事件（建议 CloudEvents 风格字段）：

```json
{
  "specversion": "1.0",
  "id": "evt_01JABCDE",
  "type": "com.cova.job.completed",
  "source": "cova://orchestrator",
  "time": "2026-02-27T08:05:10Z",
  "datacontenttype": "application/json",
  "data": {
    "job_id": "job_01JABCDEF",
    "status": "succeeded",
    "result_url": "/v1/jobs/job_01JABCDEF/result"
  }
}
```

签名头建议：

- `X-COVA-Signature: sha256=<hex>`
- `X-COVA-Timestamp: <unix-seconds>`

回调重试：

- 最大重试次数：10（建议）
- 退避策略：指数退避 + 抖动
- 2xx 视为成功，其余视为失败重试

## 6. 状态机约束

允许转换：

- `queued -> running`
- `running -> succeeded`
- `running -> failed`
- `queued/running -> canceled`
- `queued/running -> expired`
- `failed -> queued`（仅内部重放）

不允许：

- `succeeded -> running`
- `failed -> running`（必须先回到 `queued`）

## 7. 安全约束

- 服务端不持久化调用方长期明文 API Key。
- `inference.token` 应为短期令牌并设 TTL。
- 日志默认脱敏：`token`, `Authorization`, `secret_ref`。
- 回调需验签并校验时间窗口（防重放攻击）。

回调接收端建议最小校验顺序：

1. 读取 `X-COVA-Timestamp`，要求与本机时间偏差在允许窗口内（例如 5 分钟）。
2. 读取 `X-COVA-Signature`，按 `sha256=...` 解析签名值。
3. 用约定 secret 按 `HMAC_SHA256(timestamp + "." + raw_body)` 计算并常量时间比较。
4. 解析事件体中的 `event_id`（或 CloudEvents `id`），在 TTL 窗口内做去重。
5. 去重命中时直接拒绝（例如 `409`），未命中才处理业务。

## 8. 兼容性策略

- 新增字段：向后兼容（调用方应忽略未知字段）。
- 删除字段或改语义：仅在 `v2` 执行。
- 枚举新增：允许，调用方需实现未知值兜底。

## 9. 联调最小清单

1. 提交任务，获得 `job_id`
2. 轮询任务状态，观察 `queued -> running -> succeeded`
3. 拉取结果，校验 `answer/references/freshness`
4. 验证 webhook 到达、签名有效、事件可去重
5. 重放相同 `Idempotency-Key`，确认幂等
