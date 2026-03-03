# COVA Gap Analysis (After 2026-03-03 Delivery)

> Baseline Date: 2026-03-03

## 1. 总览

相较 2026-03-02 的初始评估，当前代码已完成文档中 Phase A-F 的核心骨架落地：双边界文档、Assistant API、多租户、专家准入校验、状态机增强、基础观测、客户端适配层。

当前主要差距已从“平台边界未落地”转为“生产级能力深化”。

## 2. 差异矩阵（当前）

| 领域 | 当前实现状态 | 目标状态 | Gap 等级 |
| --- | --- | --- | --- |
| Assistant API | 已实现 `/v1/assistant/*`（含 cancel/replay） | 继续稳定演进 v1 | P1 |
| 多租户 | Header 强校验 + 任务隔离 + token 绑定注入 | 接入 OIDC/JWT claim 自动映射与策略中心 | P1 |
| 状态机 | `queued/running/succeeded/failed/canceled/expired` 已落地 | 引入更细粒度步骤状态与补偿策略 | P1 |
| 重试与 DLQ | 已有 max attempts + DLQ 持久化字段 | 接入 durable queue 与独立 DLQ 消费治理 | P0 |
| 专家准入 | `registry` 已做 conformance 基础校验 | 完整准入流水线（CI 门禁 + 评分 + 审批） | P1 |
| 观测 | `metrics/healthz` + 租户维度聚合 | OTel tracing、统一日志采集、告警平台 | P0 |
| 存储与队列 | 仍以 JSON 文件状态为主 | Postgres + durable queue（Redis/NATS/Kafka） | P0 |
| Expert Runtime API | 文档已定义 | 独立运行时 API 与服务端实现 | P0 |
| 客户端生态 | 已有 coco/openclaw SDK 适配层 | 增加接入指南、示例、契约测试套件 | P1 |
| 成本治理 | 估算 token 指标 | 精确 token/cost 计量与预算控制 | P1 |

## 3. 当前关键风险

1. 生产数据与任务持久化尚未迁移到数据库与 durable queue。  
2. Expert Runtime API 仍是设计文档层，未形成独立服务契约执行。  
3. 观测深度仍不足以支撑复杂生产故障定位（缺 end-to-end trace）。

## 4. 下一步优先级（建议）

### P0（优先）

- 持久化与队列生产化（DB + durable queue + DLQ 消费）
- OTel tracing 与告警接入
- Expert Runtime API 最小可用实现

### P1（随后）

- 租户策略中心（配额、权限、预算）
- Conformance 流水线自动化
- 精细化成本治理与报表
