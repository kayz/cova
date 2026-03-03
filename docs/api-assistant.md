# COVA Assistant API (v1, code-aligned)

## 1. 适用对象

面向助理 Agent（`coco`、`openclaw` 等），用于提交与消费专家任务结果。

规范基线以 [openapi.v1.yaml](./openapi.v1.yaml) 为准；本文为实现摘要。

## 2. 基础约定

- Base Path: `/v1/assistant`
- Content-Type: `application/json`
- 时间格式: RFC3339（UTC）
- 错误格式: RFC 9457 Problem Details

### 2.1 必要 Header

- `Authorization: Bearer <token>`
- `X-Tenant-Id: <tenant_id>`
- `X-Project-Id: <project_id>`

### 2.2 建议 Header

- `X-Request-Id: <request_id>`
- `Idempotency-Key: <idempotency_key>`（创建任务时建议）

### 2.3 多租户校验

- 服务端以 Header 为租户边界主依据。
- 请求体中的 `tenant_id/project_id`（如传入）必须与 Header 一致。

## 3. 已实现接口

### 3.1 创建任务

`POST /v1/assistant/jobs`

请求体最小字段：

```json
{
  "expert_id": "fi_cn_primary",
  "date": "2026-03-03"
}
```

成功返回：`202 Accepted`，含 `job_id/tenant_id/project_id/status/submitted_at`。

### 3.2 查询任务状态

`GET /v1/assistant/jobs/{job_id}`

成功返回：`200 OK`，含 `status/progress/attempt`。

### 3.3 查询任务结果

`GET /v1/assistant/jobs/{job_id}/result`

- `200`: 结果可读
- `409`: `result-not-ready`

### 3.4 查询回调投递记录

`GET /v1/assistant/jobs/{job_id}/deliveries`

返回 webhook 每次投递记录（含 attempt、http_status、payload_sha256）。

### 3.5 取消任务

`POST /v1/assistant/jobs/{job_id}/cancel`

- `202`: 取消受理（状态变为 `canceled`）
- `409`: 非法状态转换（终态任务不可取消）

### 3.6 重放任务

`POST /v1/assistant/jobs/{job_id}/replay`

- 仅允许对 `failed/canceled/expired` 任务重放
- 成功返回 `202` 并将任务重置为 `queued`

### 3.7 同步问答

`POST /v1/assistant/query`

请求体最小字段：

```json
{
  "expert_id": "fi_cn_primary",
  "question": "今天固收市场的关键变化是什么？"
}
```

### 3.8 查询专家能力

`GET /v1/assistant/experts`

返回已启用专家的 `expert_id/expert_type/supports/version/status`。

## 4. 任务状态机（实现）

- `queued`
- `running`
- `succeeded`
- `failed`
- `canceled`
- `expired`

已实现语义：

- 幂等提交（按 tenant/project 作用域）
- 自动重试（max attempts）
- 超时过期（TTL -> `expired`）
- 手动取消与重放

## 5. 与代码一致的注意事项

1. OpenAPI `JobResultResponse.status` 目前仍限制为 `succeeded`；而运行时内部状态机包含 `failed/canceled/expired`。  
2. 当前执行器为 mock，结果文本与 token 成本指标为估算值。  
3. 权威字段定义与参数约束请以 [openapi.v1.yaml](./openapi.v1.yaml) 为最终准绳。
