# COVA Plan (to Target Platform)

> Baseline Date: 2026-03-02

## 1. 目标

把当前 COVA 从“可联调骨架”推进到“可被 `coco/openclaw` 稳定接入的多租户专家平台”。

## 2. 阶段计划

## Phase A: 边界冻结（1-2 周）

交付：

- 冻结 `VISION.md`、`architecture.md`
- 冻结 `api-assistant.md`、`api-expert.md`
- 冻结专家准入规范（Conformance v1）

完成标准：

- 双 API 字段语义无歧义
- 错误码、状态机、兼容策略定稿

## Phase B: 多租户基线（2-3 周）

交付：

- 在 API、任务模型、存储层引入 `tenant_id/project_id`
- 接入 token claim -> 内部身份上下文映射
- 租户级限流、配额、权限校验

完成标准：

- 跨租户访问被拒绝
- 单租户异常不影响其他租户

## Phase C: 专家准入与运行时（2-3 周）

交付：

- 专家 capability 声明与校验流程
- Conformance 测试套件（契约、幂等、重试、签名、观测）
- 专家上线门禁（未通过不可发布）

完成标准：

- 至少 2 个专家通过 conformance 并上线

## Phase D: 可靠性升级（2-4 周）

交付：

- JSON 文件状态迁移到 DB
- 引入 durable queue + DLQ
- 完整状态机（含 canceled/expired/replay）
- 任务恢复与补偿流程

完成标准：

- 故障恢复后任务可追踪、可补偿
- 重试与取消语义可验证

## Phase E: 观测与治理（2-3 周）

交付：

- OTel tracing + metrics + structured logging
- 成本看板（token/时长/成功率/失败重试成本）
- SLO 与告警策略

完成标准：

- 关键链路可观测
- 具备周度 SLA 报表

## Phase F: 多客户端接入（1-2 周）

交付：

- `coco` 适配层
- `openclaw` 适配层
- 统一接入指南与 SDK 示例

完成标准：

- 两个客户端可通过同一 Assistant API 接入
- 无客户端特有字段泄漏到平台核心协议

## 3. 并行策略

- 文档冻结与实现可以并行，但协议字段冻结必须先于大规模编码。
- 专家准入与多租户治理优先级高于新增专家数量。

## 4. 里程碑定义

- M1: 双 API + 多租户模型冻结
- M2: Conformance 门禁可执行
- M3: 生产级状态与队列可用
- M4: `coco/openclaw` 双接入完成
