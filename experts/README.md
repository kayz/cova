# Experts Directory

`experts/` 存放可插拔专家实现，每个专家一个目录。

建议结构：

```text
experts/
  _template/
    expert.yaml
    prompts/
    sources/
    schemas/
  <expert-id>/
    expert.yaml
    prompts/
    sources/
    schemas/
```

核心约定：

- `expert.yaml`：专家元信息、能力、运行入口、路由标签。
- `prompts/`：该专家独有提示词与模板。
- `sources/`：数据源配置（网站/API/RSS/DB/文件）。
- `schemas/`：该专家输出结构定义（可选）。

新增专家推荐流程：

1. 复制 `experts/_template` 为新目录。
2. 修改 `expert.yaml` 中的 `expert_id`、`expert_name`、`capabilities`。
3. 在 `configs/experts/registry.yaml` 中启用新专家。
