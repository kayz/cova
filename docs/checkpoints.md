# COVA Test & Checkpoints

> Baseline Date: 2026-03-03

本文档定义每个阶段完成后的验收方法，避免“功能看起来有了，但不可运营”。

## 执行状态

- A-F 阶段已完成并执行回归测试：`go test ./...`
- 当前检查点文档保留为后续生产化阶段验收模板

## Checkpoint A: Assistant API 与文档一致性

验收项：

1. 文档接口路径与 OpenAPI 一致（`/v1/assistant/*`）。
2. README、Assistant API 文档、OpenAPI 三者字段语义一致。

执行方法：

1. 文档评审会（产品 + 平台 + 客户端 + 专家团队）。
2. 从 `coco` 与 `openclaw` 各挑 2 个任务做字段映射演练。

通过标准：

- 无阻塞性字段歧义
- 评审意见全部关闭或形成明确后续单

## Checkpoint B: 多租户基线验收

验收项：

1. API 请求都能解析并携带 `X-Tenant-Id/X-Project-Id`。
2. 跨租户访问被拒绝并记录审计日志。

执行方法：

1. 集成测试构造 `tenant_a` 与 `tenant_b` 双租户 token。
2. 尝试跨租户读取任务和结果，验证返回 `403`。

通过标准：

- 跨租户访问拦截率 100%
- 审计日志完整记录租户、主体、资源、动作

## Checkpoint C: 专家准入验收

验收项：

1. `registry` conformance 校验可执行（capabilities/runtime/version/schema）。
2. 专家未通过测试不可发布到 active。

执行方法：

1. 构造一个不合规专家（缺 references 或不支持幂等）。
2. 执行 conformance pipeline，验证发布被阻断。

通过标准：

- 不合规专家发布阻断率 100%
- 合规专家可一键发布

## Checkpoint D: 可靠性验收

验收项：

1. 任务状态在异常重启后可恢复（当前 JSON 持久化基线）。
2. `cancel/replay/retry/expired` 语义正确。
3. webhook 重试与去重生效。

执行方法：

1. 压测期间主动重启 orchestrator/worker。
2. 注入下游 5xx、超时、网络中断故障。
3. 检查 DLQ 与补偿流程。

通过标准：

- 恢复后任务可追踪率 100%
- 系统原因任务成功率 >= 99%

## Checkpoint E: 观测与治理验收

验收项：

1. `gateway/orchestrator/worker` 提供 `metrics/healthz`。
2. 可按租户查看任务状态与重试聚合指标。
3. 后续接入 OTel 后补充全链路 trace 验收。

执行方法：

1. 运行标准负载与故障负载。
2. 对比监控、日志、trace 的一致性。

通过标准：

- 链路追踪覆盖率 >= 95%
- 成本报表与账单对账误差 < 2%

## Checkpoint F: 多客户端接入验收

验收项：

1. `coco` 与 `openclaw` 均可接入 Assistant API。
2. 两端使用同一协议，无客户端特有字段污染核心接口。

执行方法：

1. 两端执行同一任务集（同步 + 异步）。
2. 对比状态流转、结果格式、错误处理一致性。

通过标准：

- 双客户端关键任务通过率 >= 99%
- 适配层代码变更不影响平台核心协议

## 最低自动化检查清单（持续执行）

每次阶段结束至少执行：

1. `go test ./...`
2. API 合约测试（Assistant + Expert）
3. webhook 签名与重放防护测试
4. 多租户隔离测试
5. 回归任务集测试（固定输入、固定期望结构）
