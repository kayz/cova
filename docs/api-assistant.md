# COVA Assistant API (v1 Draft)

## 1. 适用对象

本 API 提供给助理 Agent（`coco`、`openclaw` 等）调用，用于：

- 提交复杂任务给专家执行
- 查询异步任务状态与结果
- 发起同步问答
- 查询可用专家能力

## 2. 通用约定

- Base Path: `/v1/assistant`
- Content-Type: `application/json`
- 时间格式: RFC3339（UTC）

### 2.1 Headers

- `Authorization: Bearer <token>` 必填
- `X-Request-Id: <id>` 建议
- `Idempotency-Key: <key>` 创建任务强烈建议

### 2.2 多租户身份

以下字段由 token claim 提供并在服务端强校验：

- `tenant_id`
- `project_id`
- `sub`（调用主体）
- `scopes`

## 3. 核心接口

### 3.1 提交异步任务

`POST /v1/assistant/jobs`

请求示例：

```json
{
  "task_type": "daily_brief",
  "expert_selector": {
    "expert_id": "fi_cn_primary",
    "expert_type": "fixed_income",
    "tags": ["cn"]
  },
  "input": {
    "date": "2026-03-02",
    "timezone": "Asia/Shanghai",
    "question": "请总结今日债市变化"
  },
  "delivery": {
    "mode": "webhook",
    "callback_url": "https://client.example.com/hooks/cova",
    "signing": {
      "algorithm": "hmac-sha256",
      "secret_ref": "vault://client/cova-webhook"
    }
  },
  "constraints": {
    "timeout_seconds": 180,
    "max_cost_usd": 2.5
  }
}
```

响应：`202 Accepted`

```json
{
  "job_id": "job_01J...",
  "status": "queued",
  "submitted_at": "2026-03-02T09:00:00Z"
}
```

### 3.2 查询任务状态

`GET /v1/assistant/jobs/{job_id}`

响应：`200 OK`

```json
{
  "job_id": "job_01J...",
  "status": "running",
  "progress": 64,
  "attempt": 1,
  "submitted_at": "2026-03-02T09:00:00Z",
  "updated_at": "2026-03-02T09:01:13Z"
}
```

### 3.3 获取任务结果

`GET /v1/assistant/jobs/{job_id}/result`

响应：`200 OK`

```json
{
  "job_id": "job_01J...",
  "status": "succeeded",
  "result": {
    "answer": "...",
    "confidence": 0.84,
    "freshness_cutoff": "2026-03-02T08:58:10Z",
    "references": [
      {
        "kind": "article",
        "ref_id": "src_001",
        "title": "市场早报",
        "uri": "https://example.com/report",
        "published_at": "2026-03-02T08:30:00Z"
      }
    ],
    "limitations": []
  }
}
```

未完成返回：`409 Conflict`（`result-not-ready`）。

### 3.4 同步问答

`POST /v1/assistant/query`

请求示例：

```json
{
  "expert_selector": {
    "expert_id": "fi_cn_primary"
  },
  "question": "今天利率债和信用债的分化是什么？",
  "context": {
    "time_window": {
      "start": "2026-03-01T00:00:00Z",
      "end": "2026-03-02T09:00:00Z"
    }
  }
}
```

响应：`200 OK`，结构同 `result`。

### 3.5 查询专家能力

`GET /v1/assistant/experts`

响应：`200 OK`

```json
{
  "experts": [
    {
      "expert_id": "fi_cn_primary",
      "expert_name": "FICC Observor CN Primary",
      "expert_type": "fixed_income",
      "status": "active",
      "supports": ["daily_brief", "query"],
      "version": "1.0.0"
    }
  ]
}
```

## 4. 错误模型

统一使用 RFC 9457 Problem Details：

```json
{
  "type": "https://api.cova.example.com/errors/invalid-request",
  "title": "Invalid request",
  "status": 400,
  "detail": "field `question` is required",
  "instance": "/v1/assistant/query",
  "request_id": "req_01J..."
}
```

## 5. 幂等与重试

- 创建任务使用 `Idempotency-Key`，相同 key + 相同 payload 必须返回同一 `job_id`。
- 相同 key + 不同 payload 必须返回 `409 idempotency-mismatch`。

## 6. 兼容策略

- 新增字段必须向后兼容。
- 破坏性变更通过 `/v2` 发布。
