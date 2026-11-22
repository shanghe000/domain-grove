# 知域 · Domain Grove

**用本体建模和领域驱动设计（DDD）长期维护业务知识。**

把业务术语、规则和关系整理成有出处、可审核、可版本化的领域模型。业务变化时，团队可以查清一条规则为何成立、在哪个上下文生效，以及它何时改变。

![知域工作台的三维索引显示次日回访率及相连的资料出处](docs/media/index-layers.png)

*工作台实际运行截图。演示数据来自虚构的阅读社区。*

## 业务知识如何演进

1. **界定语义。** 用 DDD 的限界上下文明确术语和规则的适用范围。履约团队可以把「订单已完成」定义为签收，财务团队可以定义为结算。需要独立维护和发布时，可以为各自的模型建立知识空间。
2. **把知识建成模型。** 在空间内统一术语，为概念保留稳定标识，用有向关系表达依赖，用业务公理记录适用条件和例外，再关联原文出处。
3. **按提案维护版本。** 术语、规则或来源改变时，提交提案并说明依据，审阅候选图谱。批准后形成独立发布版本，工作台与 MCP 读取者可以使用同一份已审核的模型。

这套流程可用于履约规范、客服政策、产品运营或团队内部流程。领域知识模型也可以从一条规则开始，再逐步补充相关概念和证据。

## 运行示例

需要 Go 1.25+、Node.js 22.12+ 和 Python 3。以下命令在源码目录构建并启动工作台。

```sh
npm ci --prefix web
npm run build --prefix web
sample_data="$(mktemp -d)"
AUTH_ENABLED=false ONTOLOGY_MODE=index INDEX_ALLOW_DEV_REVIEW=true \
  GO_SERVER_ADDR=127.0.0.1:8080 PUBLIC_URL=http://127.0.0.1:8080 \
  DATA_DIR="$sample_data" go run ./cmd/server
```

另开终端，在同一源码目录运行提交脚本。

```sh
python3 examples/community/submit.py
```

打开 <http://127.0.0.1:8080>，在「建模提案」选择「示例社区的阅读留存」。双击「阅读与留存」下钻，单击「次日回访率」查看节点与资料。画布底部可切换「平面」「索引分层」「业务因果」，在分层视图下右键拖动可旋转。核对后选择「返回审核 → 批准并发布 → 确认发布」，图谱从 `r0` 进入 `r1`。

<details>
<summary>查看业务因果视图</summary>

![业务因果视图展示指标、业务机制和资料之间的方向关系](docs/media/causal-layers.png)

</details>

同一份事件字典支持不同指标引用不同段落。[示例提案](examples/community/proposal.json) · [留存规范](examples/community/sources/retention.md) · [事件字典](examples/community/sources/reading-events.md) · [引导说明](examples/community/sources/onboarding.md)

如果社区以后把回访条件从「打开文章」改为「完成一次有效阅读」，就可以沿用同一个指标 ID，提案更新定义和证据，再审阅受影响的口径。发布后，MCP 遍历仍可按修订号回查旧版图谱。

## 核心机制

```mermaid
flowchart LR
    C["限界上下文与领域术语"] --> D["本体建模与变更提案"]
    D --> V["校验候选图谱"]
    V --> R["人工审核和版本检查"]
    R --> S["SQLite 发布快照"]
    S --> W["Canvas 工作台"]
    S --> M["MCP 查询 / YAML 导出"]
    W -->|业务规则或资料变化| D
```

- **分层图谱**区分领域、概念、指标和资料，保留业务方向、资料锚点与活动时间。
- **加权取证**根据关系类型和预算寻找相关指标与资料。工作台与 MCP 对同一版本共用遍历逻辑。
- **审核发布**支持候选图谱、编辑撤销、幂等提交和版本冲突检查。批准后才对 MCP 读取方可见。
- **知识空间**独立保存图谱、审核队列与读写凭据。DeepSeek 可选，用于生成待审草案。

图谱保存仓库别名、相对路径和锚点。引用资料正文由来源侧管理。[YAML 与 MCP 契约](INDEX-SPEC.md)

## 验证与配置

```sh
go test -race ./...
npm test --prefix web
npm run build --prefix web
```

工作台演示只需本机回环环境。共享部署与模型服务需提供相应配置。[身份、模型和运行配置](docs/CONFIGURATION.md)

[贡献](CONTRIBUTING.md) · [安全](SECURITY.md) · [MIT 许可](LICENSE) · [依赖许可](THIRD_PARTY.md)
