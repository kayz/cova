# COVA Plan & Execution Status

> Updated: 2026-03-03

## 1. 执行摘要

本次迭代按既定 Phase A-F 顺序完成了主要改造，并在每阶段后执行 `go test ./...`。

## 2. 阶段状态

| Phase | 目标 | 状态 | 备注 |
| --- | --- | --- | --- |
| A | 边界冻结（双 API 路径对齐） | Completed | Assistant API 统一为 `/v1/assistant/*` |
| B | 多租户基线 | Completed | `tenant/project` 贯穿 header、模型、隔离 |
| C | 专家准入与 conformance | Completed | registry 校验能力/运行参数/version |
| D | 可靠性升级 | Completed | cancel/replay/expired/retry/DLQ 字段 |
| E | 观测治理 | Completed (Baseline) | `metrics/healthz` + 租户维度指标 |
| F | 多客户端接入 | Completed | SDK 增加 `coco/openclaw` adapter |

## 3. 本次交付后剩余计划

### Next P0

1. 持久化生产化  
将 JSON 文件状态迁移到数据库；引入 durable queue 与 DLQ 消费流程。

2. 观测生产化  
接入 OTel tracing、统一日志管道、告警策略与面板。

3. Expert Runtime API 实装  
将 [api-expert.md](./api-expert.md) 从文档变成独立可运行服务。

### Next P1

1. 租户策略中心（配额、预算、权限）  
2. 自动化 conformance 流水线（CI 门禁）  
3. 精细化 token/cost 计量

## 4. 参考

- 差异分析： [gap-analysis.md](./gap-analysis.md)
- 验收检查： [checkpoints.md](./checkpoints.md)
- 交付日志： [log/2026-03-03.md](./log/2026-03-03.md)
