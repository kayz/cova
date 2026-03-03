# COVA Expert Runtime API (v1 Draft, Not Implemented Yet)

## 1. 适用对象

本 API 面向“被 COVA 管理的专家 Agent”。  
专家可以是纯 Agent，也可以是 Agent + 工作流代码 + 私有数据源的组合服务。

状态说明：

- 本文档定义平台目标接口与准入规范。
- 当前仓库尚未提供独立 `runtime/v1/*` HTTP 服务端实现。
- 当前代码中的专家执行路径为 orchestrator 内置执行器 + mock worker。

## 2. 准入与合规要求（Conformance）

专家必须满足以下要求才能接入生产：

1. 必须声明 `expert_id/version/capabilities/input_schema/output_schema`。
2. 必须支持租户上下文（`tenant_id/project_id`）并强隔离数据。
3. 必须返回标准结果字段：`answer/confidence/freshness_cutoff/references/limitations`。
4. 必须区分可重试错误与不可重试错误。
5. 必须支持超时和并发上限配置。
6. 必须输出结构化日志和 trace 关联 ID。
7. 必须通过 COVA conformance tests。

## 3. 交互模式

- COVA 作为调度方，向专家下发任务。
- 专家可同步返回结果，或先 `accepted` 后异步回传事件。

## 4. COVA -> Expert 接口

### 4.1 健康检查

`GET /runtime/v1/healthz`

响应：`200 OK`

```json
{
  "status": "ok",
  "version": "1.0.0"
}
```

### 4.2 能力声明

`GET /runtime/v1/capabilities`

响应示例：

```json
{
  "expert_id": "fi_cn_primary",
  "version": "1.0.0",
  "supports": ["daily_brief", "query"],
  "limits": {
    "timeout_seconds": 300,
    "max_concurrency": 4
  }
}
```

### 4.3 执行任务

`POST /runtime/v1/tasks`

请求示例：

```json
{
  "task_id": "tsk_01J...",
  "job_id": "job_01J...",
  "tenant_id": "tenant_a",
  "project_id": "proj_research",
  "task_type": "daily_brief",
  "input": {
    "date": "2026-03-02",
    "timezone": "Asia/Shanghai"
  },
  "constraints": {
    "timeout_seconds": 180,
    "max_cost_usd": 2.5
  },
  "trace_id": "trc_01J..."
}
```

同步完成响应：`200 OK`

```json
{
  "status": "succeeded",
  "result": {
    "answer": "...",
    "confidence": 0.82,
    "freshness_cutoff": "2026-03-02T08:59:00Z",
    "references": [],
    "limitations": []
  }
}
```

异步处理响应：`202 Accepted`

```json
{
  "status": "accepted",
  "expert_task_id": "exp_t_01J..."
}
```

### 4.4 取消任务

`POST /runtime/v1/tasks/{task_id}/cancel`

响应：`202 Accepted`

```json
{
  "status": "canceling"
}
```

## 5. Expert -> COVA 事件回传

### 5.1 任务进度/完成事件

`POST /v1/expert/events`

建议使用 CloudEvents 1.0：

```json
{
  "specversion": "1.0",
  "id": "evt_01J...",
  "type": "com.cova.expert.task.completed",
  "source": "expert://fi_cn_primary",
  "time": "2026-03-02T09:01:00Z",
  "datacontenttype": "application/json",
  "data": {
    "task_id": "tsk_01J...",
    "job_id": "job_01J...",
    "status": "succeeded",
    "result": {
      "answer": "...",
      "confidence": 0.82,
      "freshness_cutoff": "2026-03-02T08:59:00Z",
      "references": []
    }
  }
}
```

签名头：

- `X-COVA-Timestamp`
- `X-COVA-Signature: sha256=<hex>`

## 6. 错误码约定

- `invalid-input`
- `unauthorized`
- `forbidden`
- `timeout`
- `dependency-unavailable`
- `non-retryable-failure`
- `retryable-failure`

错误格式统一使用 RFC 9457。

## 7. 安全要求

1. 禁止持久化明文长期 API key。
2. 所有敏感配置通过 secret manager 注入。
3. 严格校验租户边界，禁止跨租户读写。
4. 所有回调与事件接口必须验签并防重放。

## 8. 版本策略

- 专家 runtime 合约采用语义化版本。
- 新增字段向后兼容。
- 破坏性变更通过 `/runtime/v2` 引入。
