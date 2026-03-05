# COVA v0.1.0 首发版本定义（初始生产可用）

> Version: `v0.1.0`  
> Draft Date: 2026-03-05  
> 目标：在“专家执行仍可用 mock”的前提下，交付可上线、可运维、可扩展到真实专家的第一版。

## 当前实现进展（2026-03-05）

- [x] Runtime API 服务端（`cmd/expert-runtime`）
- [x] orchestrator 异步事件回传入口（`/v1/expert/events`）
- [x] Postgres 状态存储（可选切换）
- [x] Redis Streams durable queue（可选切换）
- [x] 结构化日志与 traceparent 透传基线
- [x] 发布/回滚脚本与运行手册

## 1. 发布定位

`v0.1.0` 是 **初始生产版本（Initial Production Release）**：

- 对助理侧：Assistant API 稳定可用，可承接真实业务流量（受控规模）。
- 对专家侧：Expert Runtime API 先完整落地接口与服务端骨架，专家执行可先由 mock adapter 实现。
- 对平台侧：具备生产级最小可靠性（持久化、队列、可观测、回滚）。

## 2. 首发边界（In Scope / Out of Scope）

## In Scope（`v0.1.0` 必做）

1. Assistant API `v1` 稳定（保持 `/v1/assistant/*` 兼容）。
2. Expert Runtime API `v1` 服务端上线（接口先行，执行可 mock）：
   - `GET /runtime/v1/healthz`
   - `GET /runtime/v1/capabilities`
   - `POST /runtime/v1/tasks`
   - `POST /runtime/v1/tasks/{task_id}/cancel`
   - `POST /v1/expert/events`
3. 存储升级到 Postgres（替代 JSON 文件作为主状态存储）。
4. 任务队列升级到 durable queue（优先 Redis Streams）。
5. 基础观测上线：metrics + 结构化日志 + trace context 贯通（可先采样）。
6. 最小安全基线：
   - gateway 鉴权开启（禁止匿名写接口）
   - webhook / expert 事件验签
   - tenant/project 作用域强校验
7. 发布与回滚机制：可灰度、可回滚、可恢复。

## Out of Scope（`v0.1.0` 不阻塞）

1. 真实专家能力质量优化（可后续替换 mock）。
2. 策略中心（复杂配额/预算/审批流）。
3. 精细化成本核算（provider 级精确账单对账）。

## 3. 初始生产环境（建议拓扑）

单 Region、单集群、可水平扩展的最小拓扑：

- `gateway`：2 实例（无状态）
- `orchestrator`：2 实例（消费 durable queue）
- `expert-runtime`：2 实例（提供 Runtime API，接 mock adapter）
- `postgres`：1 主 + 定时备份（首发阶段）
- `redis`：1 主（开启 AOF，承载 Streams）
- `otel-collector`：1 实例（可选先单点）

运行要求：

1. 所有服务暴露 `GET /healthz` 与 `GET /metrics`。
2. 所有 API 请求链路携带 `X-Request-Id` 与 trace context。
3. 关键配置（token、签名密钥）通过环境变量或 secret 注入。

## 4. v0.1.0 验收门槛（Go/No-Go）

发布前必须同时满足：

1. 功能门槛
   - `submit -> queued/running -> succeeded -> result -> webhook` 全链路通过
   - Runtime API 5 个接口可联调、错误码语义稳定
2. 可靠性门槛
   - orchestrator/runtime 重启后任务可恢复
   - at-least-once 投递可验证，重复投递不破坏幂等
3. 质量门槛
   - `go test ./...` 全绿
   - 新增集成测试覆盖：多租户隔离、幂等冲突、重试/DLQ、事件验签
4. 运维门槛
   - 失败率、延迟、队列积压有面板
   - 至少 3 条告警生效：失败率、积压、回调失败

## 5. 开发顺序与计划（建议）

## Milestone 0: 版本冻结（2026-03-06）

交付：

1. 冻结 `v0.1.0` 范围与 non-goal。
2. 冻结 Runtime API `v1` 字段与错误模型。
3. 明确迁移策略：JSON -> Postgres（一次性迁移 + 回滚方案）。

## Milestone 1: Runtime API 先行（2026-03-07 ~ 2026-03-10）

交付：

1. 新增 `expert-runtime` 服务骨架与路由。
2. 实现 Runtime API 5 个接口（含鉴权、验签、审计日志）。
3. 接入 mock adapter（可执行 mock task 并回传事件）。
4. 补充 Runtime API contract/integration tests。

验收：

- 助理侧可通过 orchestrator 触发 runtime task；
- runtime 返回 accepted/succeeded 两种路径均可跑通。

## Milestone 2: 持久化与队列生产化（2026-03-11 ~ 2026-03-15）

交付：

1. `jobstore.Store` 增加 Postgres 实现。
2. 引入 Redis Streams 队列与消费者组。
3. 崩溃恢复逻辑：未完成任务重入队，避免任务丢失。
4. DLQ 入队与消费补偿流程。

验收：

- 进程异常退出后，重启 5 分钟内恢复并继续处理积压任务。

## Milestone 3: 可观测与发布工程（2026-03-16 ~ 2026-03-18）

交付：

1. 日志结构化（JSON）统一字段。
2. trace context 贯通 gateway -> orchestrator -> runtime。
3. 发布脚本与回滚脚本（至少支持一键回退到上一稳定版本）。
4. 首版运行手册（故障定位、告警处理、回滚流程）。

验收：

- 任一失败任务可在 10 分钟内通过日志+指标定位到失败组件。

## Milestone 4: 预发布与 GA（2026-03-19 ~ 2026-03-20）

交付：

1. 预发布环境灰度（10% 流量或指定租户）。
2. 48 小时稳定性观察。
3. 发布 `v0.1.0` tag 与 release note。

GA 条件：

- P0 阻塞缺陷为 0；
- 连续 48 小时无数据丢失、无跨租户问题、无不可恢复故障。

## 6. 任务优先级规则

冲突时按以下顺序取舍：

1. 接口稳定性 > 新功能数量
2. 数据安全与隔离 > 吞吐优化
3. 可恢复性 > 峰值性能
4. 可观测性 > 开发速度

## 7. v0.1.0 后续版本入口（v0.2.0）

`v0.2.0` 重点：

1. 接入至少 1 个真实专家 agent
2. 租户策略中心（配额/预算）
3. conformance 自动化流水线
4. 成本精细化治理
