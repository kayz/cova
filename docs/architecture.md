# COVA Architecture (Platform Boundary v1)

## 1. 目标与范围

本文件定义 COVA 的平台边界，面向两类接入方：

- 助理 Agent（如 `coco`、`openclaw`）
- 专家 Agent（由 COVA 管理、调度、治理）

目标是把复杂任务从助理会话中解耦出来，转移到可治理、可审计、可复用的专家平台中执行。

## 2. 架构分层

```text
Assistant Clients
  (coco / openclaw / others)
          |
          v
Assistant API Layer (Northbound)
          |
          v
Control Plane
- AuthN/AuthZ
- Tenant/Project isolation
- Expert registry/versioning
- Policy/Quota/SLA
- Audit/Cost governance
          |
          v
Execution Plane
- Orchestrator / Queue / Retries
- Expert Runtime Adapter
- Webhook/Event delivery
          |
          v
Expert Agents + Tools + Private Data
```

## 3. 双 API 边界

### 3.1 Assistant API（对助理 Agent）

用于任务提交、状态查询、结果获取、同步问答、能力发现。  
详见 [api-assistant.md](./api-assistant.md)。

### 3.2 Expert Runtime API（对专家 Agent）

用于专家注册、能力声明、任务执行、进度事件、结果回传与健康检查。  
详见 [api-expert.md](./api-expert.md)。

当前状态：

- Assistant API 已实现并可用。
- Expert Runtime API 已提供独立服务实现（`cmd/expert-runtime`），可用 mock adapter 承载首发版本。

## 4. 多租户模型

### 4.1 资源层级

`tenant -> project -> expert/job/dataset/credential`

### 4.2 隔离策略

- 控制面隔离：权限、配额、策略按 `tenant_id/project_id` 生效。
- 数据面隔离：任务、结果、日志、知识库按租户维度隔离。
- 执行隔离：专家运行时可按租户/项目独立池化或命名空间隔离。

### 4.3 身份映射

- 外部调用方使用 OAuth2/OIDC token。
- COVA 从 token claim 解析 `tenant_id/project_id/subject/scopes`。
- 业务请求体中的租户字段仅作校验，不作为授权依据。

## 5. 核心组件职责

### 5.1 Gateway

- 统一入口、鉴权、限流、请求校验、请求 ID 注入。
- 不执行专家业务逻辑。

### 5.2 Orchestrator

- 管理任务状态机：`queued/running/succeeded/failed/canceled/expired`
- 调度专家执行、处理超时/重试/取消、写入审计事件。
- 统一触发 webhook 或事件总线通知。

### 5.3 Expert Registry

- 管理专家定义、版本、能力标签、运行参数。
- 提供发布/回滚/停用流程与兼容性检查。

### 5.4 Expert Runtime Adapter

- 标准化对不同专家运行方式的调用。
- 屏蔽底层差异（本地进程、容器、远程服务、工作流引擎）。

### 5.5 Governance

- 成本统计（token、时长、失败重试成本）
- SLA/SLO 监控与告警
- 审计与合规留痕

## 6. 状态机与执行语义

### 6.1 任务状态

- `queued`：已受理，等待执行
- `running`：执行中
- `succeeded`：成功完成
- `failed`：最终失败
- `canceled`：主动取消
- `expired`：超出 TTL

### 6.2 语义约束

- 投递语义：at-least-once
- 幂等语义：`Idempotency-Key + canonical payload hash`
- 回调语义：签名 + 重放防护 + 指数退避重试

## 7. 专家准入规范（摘要）

被 COVA 管理的专家必须满足：

- 固定输入输出 Schema（版本化）
- 明确超时/并发/预算上限
- 支持幂等与可重试错误分类
- 输出证据字段（references/freshness）
- 输出结构化观测信息（trace_id、metrics、错误码）
- 通过 Conformance 测试后才能上线

## 8. 引用标准与规范

- OpenAPI 3.1（HTTP API 描述）
- JSON Schema 2020-12（输入输出约束）
- RFC 9457 Problem Details（错误响应）
- CloudEvents 1.0（异步事件）
- OAuth2 + OIDC + JWT（身份认证与授权）
- OpenTelemetry + W3C Trace Context（链路观测）
- HMAC-SHA256（回调签名）

## 9. 运行与部署建议

### 9.1 开发阶段

- 单集群：Gateway + Orchestrator + Mock Experts + Postgres + Redis

### 9.2 生产阶段

- Gateway 无状态水平扩展
- Orchestrator 与 Expert Worker 分池扩展
- 可靠队列（Redis Streams/NATS/Kafka）+ 持久化存储
- 租户级限流与配额策略

## 10. 文档索引

- 助理 API： [api-assistant.md](./api-assistant.md)
- 专家 API： [api-expert.md](./api-expert.md)
- 差异分析： [gap-analysis.md](./gap-analysis.md)
- 目标计划： [plan.md](./plan.md)
- 验收检查点： [checkpoints.md](./checkpoints.md)
