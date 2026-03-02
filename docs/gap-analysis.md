# COVA Gap Analysis (As-Is vs Target)

> Baseline Date: 2026-03-02

## 1. 总览

当前仓库已经具备 Phase 1 骨架（统一入口、异步任务、基础 webhook、专家注册），但距离“企业级专家生产力平台”仍有关键差距，特别在多租户治理、双 API 契约、生产可靠性与专家准入体系。

## 2. 差异矩阵

| 领域 | 当前状态（As-Is） | 目标状态（Target） | Gap 等级 |
| --- | --- | --- | --- |
| 平台边界 | 以统一 API 草案为主，助理/专家边界未完全拆分 | 明确 Assistant API + Expert Runtime API 双边界 | P0 |
| 多租户 | 文档提及预留，代码未形成完整租户模型 | token claim 驱动的 tenant/project 强隔离 | P0 |
| 状态机完整性 | 已有 queued/running/succeeded/failed 主路径 | 完整支持 canceled/expired + 恢复与补偿 | P0 |
| 任务持久化 | JSON 文件存储，适合开发验证 | 生产级 DB + durable queue + DLQ | P0 |
| 专家准入 | 有 `expert.yaml` 模板与 registry | 可执行 Conformance 规范与准入门禁 | P0 |
| 安全治理 | 网关鉴权、回调签名基础可用 | 细粒度 scope、租户策略、密钥治理闭环 | P1 |
| 可观测性 | 基础 metrics + 日志 | OTel tracing + 成本指标 + SLO 告警 | P1 |
| 版本治理 | OpenAPI 与生成代码已建立 | API 版本策略 + 专家版本发布回滚流程 | P1 |
| 客户端生态 | 当前偏向单调用模式 | `coco/openclaw/第三方` 多客户端适配层 | P1 |
| 质量保障 | 单元测试较完整 | 合约测试、回归评测、混沌测试 | P1 |

## 3. 关键风险

1. 没有多租户强隔离时，越早接外部客户端，越容易出现权限边界事故。  
2. 专家无准入门禁时，平台可用性会被“个别不稳定专家”拉低。  
3. 队列与状态存储未升级前，异常恢复能力有限。  
4. Assistant/Expert API 不分离会导致后续协议演进成本增大。

## 4. 优先级建议

### P0（先做）

- 落地双 API 契约并冻结 v1 字段语义
- 落地多租户身份与授权模型
- 落地专家 conformance 门禁
- 落地生产级状态与队列

### P1（随后）

- 完成成本与质量治理看板
- 完成专家版本发布/回滚机制
- 完成多客户端适配标准

### P2（后续）

- 专家市场化目录与评分
- 自动化策略调度与容量优化
