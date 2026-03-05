# cmd

可执行入口目录（一个二进制一个子目录）：

- `gateway/`：对外 API 服务
- `orchestrator/`：任务编排与状态管理
- `expert-runtime/`：专家运行时 API（`/runtime/v1/*`）
- `worker/`：兼容旧路径的 mock 专家执行入口
