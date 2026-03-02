# internal

核心架构层代码目录（不对外暴露）：

- `api/`：HTTP handlers、middleware、request validation
- `orchestrator/`：任务状态机、调度、重试
- `registry/`：专家注册表加载（读取 `configs/experts/registry.yaml`）
- `runtime/`：专家运行时抽象与调用
- `storage/`：DB/对象存储访问
- `queue/`：消息队列抽象
- `model/`：领域模型
- `security/`：鉴权、签名、密钥处理
- `delivery/`：webhook 分发与重试

