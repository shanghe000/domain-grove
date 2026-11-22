# 知域 · 领域知识模型契约

知域保存各知识空间的业务概念、关系、资料定位和审核记录。团队在来源侧维护 Markdown、规则文档和工具说明，工作台与 MCP 客户端通过索引定位原文。

## 新版建模契约

新项目提案在 patch 明确填写 schema_version 为 2。首次审核发布后，该空间进入新版。历史修订保留原契约，未采纳的旧草案保留审核原因。完整字段由项目 MCP 的 get_index_schema 返回。

- 节点使用 domain、concept、metric、artifact。资料载体的 repository 与 path 在空间内唯一。共用字典在不同关系上分别挂 anchor、line_start、line_end。具体活动、版本和实验写入 upsert_changes，以 RFC3339 表达起止时间，保留来源和适用范围。
- 公理类型为 co_check、pitfall、cross_check、prune。关系使用 depends_on、defined_in、configured_by、foreign_key_to、impacted_by、collaborates_with。外键注明 source_field、target_field；多值列可以标明 separator。
- 必要取证范围通过 traverse_index 读取。边界提示支持后续展开，query_index_changes 负责区间查询。查询只读所在项目发布版。审核工作台可查看草案自己的同源遍历。
- 人工编辑及 AI 调图只变更指定待审草案，带编辑版本校验，支持逐步撤销。审核发布以服务端校验后的图谱为准。

## 知识空间

在工作台顶部选择空间。每个命名空间都有独立的本体数据库、发布版本、提案队列和审核记录。不同空间可以使用相同的节点 ID、仓库别名与版本号。关系端点与 AI 建模上下文均以当前空间为边界。YAML 导出也来自当前空间。

默认空间使用 `/mcp`。启用本机审核后可创建知识空间，新空间从 `r0` 空图开始。空间设置用于查看现有成员和管理 MCP 凭据。

命名空间的 MCP 地址是 `/mcp/<空间标识>`。负责人在「空间设置」分别签发读取和提交凭据。读取凭据只能查询该空间的发布图谱。提交凭据可在该空间读图并提交提案。凭据明文在签发时展示一次，服务端保存校验摘要。重新签发替换同类旧凭据。默认空间也支持 `/mcp/sse`。

## 工作台 AI 建模

在启用本机审核的工作台进入「AI 建模」，输入要新增或调整的业务节点及关系，可选填仓库别名、相对路径、行号和标题锚点。模型依据已发布图谱和这些输入生成结构化草稿，工作台显示候选图及变更清单。生成过程中的指令和原始模型响应仅用于当前请求。点击「提交审核」才将结构化提案加入同一个审核队列，之后仍需人工批准才能发布。模型提案引用已有或明确提供的定位。

## YAML 文件结构

[完整示例](docs/ontology-index.example.yaml) 中的 `sample-community` 仓库别名对应 [examples/community](examples/community)。在实际使用时，由客户端把别名映射到相应的来源仓库。空索引的版本为 `0`，`nodes` 和 `edges` 为 `[]`。

| 字段 | 约定 |
| --- | --- |
| `schema_version` | 新模型为 `2`。既有 v1 快照和草案可继续读取，同一项目通过审核发布完成升级。 |
| `revision` | 服务生成的已发布版本。每次批准一份提案自增 1。 |
| `nodes[].id` | 稳定节点 ID，后续调整用同一个 ID，显示名称可以修改。 |
| `nodes[].kind` | `domain` 领域、`concept` 概念、`metric` 指标、`artifact` 资料。资料子类通过 `properties.artifact_type` 区分 `document / config_table / event_spec / tool / skill`。 |
| `nodes[].label` | 显示名称，必填。 |
| `nodes[].parent_id` | 顶层域省略，可嵌套子域。指标与概念归于域。资料可归于域、概念或指标，多个业务通过关系投影同一资料。 |
| `nodes[].description` / `properties` | 均可省略。已知属性键允许值为 `null`。 |
| `nodes[].ref` | 资料载体必填。记录 `repository` 仓库别名和 `path` 仓库内相对路径。Markdown 文档直接记录 `.md` 路径。 |
| `ref.line_start` / `ref.line_end` | 可选。从第 1 行计算，含首尾。只有起始行时指向单行。 |
| `ref.anchor` | 可选。记录标题锚点。行号或锚点缺省时指向整份文件。 |
| `nodes[].axioms[]` | 每条含 `kind` 和 `text`。`co_check / pitfall / cross_check / prune` 分别表示连带排查、口径防坑、跨源核对与剪枝。 |
| `ref.read_strategy` / `url` | 资料的读取方式与可选原文 HTTP(S) 地址。 |
| `changes[]` | 经过审核的业务时间记录。含 `id / kind / label / starts_at / node_ids`，可附 `ends_at / scope / ref`。类型为 `activity / version / experiment / source`，时间含时区，区间含起点、不含终点，省略终点表示瞬时记录。 |
| `edges[]` | `id`、`from`、`to`、`relation` 必填，可选 `description`。方向由 `from` 指向 `to`，语义关系与父级层次分别表达。 |

资料以 `repository + path` 唯一标识。关系可用 `anchor` 指定目标资料中所需的段落、事件或表头，`line_start / line_end` 是定位缓存。示例中多个指标可以指向同一个事件字典，每个指标各自定位其事件。路径相对仓库根目录，支持中文文件名。

| 业务关系 | 起点 → 终点 | 遍历代价 |
| --- | --- | --- |
| `depends_on` | 指标 → 资料或基础指标 | 0.5 |
| `defined_in / configured_by` | 概念 → 定义资料 / 配置表 | 0.5 |
| `foreign_key_to` | 配置表 → 被引用配置表，需 `source_field / target_field`，多编号可指定 `separator: ";"` | 0.5 |
| `impacted_by` | 指标 → 可能影响其表现的业务概念 | 1 |
| `collaborates_with` | 发起方概念 → 被调用概念 | 1 |

`properties.cognitive_layer` 为 S0 载体、S1 稳态机制、S2 干预或 S3 指标。`properties.hub_role: sink` 表示公共汇点，`source` 表示核心源点。`properties.lifecycle: deprecated` 可保留历史节点，查询默认排除。每份资料可落在一个归属域，从多个关联概念或指标下查看。审核时核对实例、临时维度、捷径与证据。后台阻止错误端点、反向循环、汇点外泄和重复资料绑定，提示超过 15 个概念或指标的域，以及超过 10 条关系的普通概念与指标。

更新飞书文档时，解析器按唯一的 Markdown 标题定位并更新资料、关系和时态引用的行号。本地资料库可以在文件更新钩子中运行 `workers/extractor/index_anchors.py`。用 `--graph` 指定已发布的图 JSON、`--node` 指定要检查的资料、`--root` 指定该资料的仓库根目录。脚本只读该文件，校验 SHA-256，以 Markdown 标题、CSV/TSV 字段或 YAML 键重新定位。匹配歧义或标题消失时会清除过期行号并返回待核实锚点。将输出的 `base_revision` 和 `patch` 通过提案工具提交审核。

## MCP

Streamable HTTP 连接 `/mcp`，使用服务签发的 Bearer 凭据。

| 工具 | 用途 | 凭据 |
| --- | --- | --- |
| `get_index_schema` | 查看 YAML 结构和字段约定。 | 读取或提交 |
| `get_index_graph` | 读取已发布图谱。省略 `format` 默认 JSON，也可传 `yaml`。 | 读取或提交 |
| `find_index_nodes` | 按名称、ID 或别名定位入口。 | 读取或提交 |
| `traverse_index` | 传 `node_ids`，默认预算 2.5，返回带公理和资料定位的子图；取 `boundary[].node_id` 和 `revision` 可继续展开。与工作台悬停共用实现。 | 读取或提交 |
| `query_index_changes` | 以 `from / to`（带时区的半开区间）查询已发布记录，可按 `scope / node_id` 筛选。 | 读取或提交 |
| `get_index_node` | 按 `node_id` 读取已发布节点、父节点、子节点及有向关系。 | 读取或提交 |
| `submit_index_proposal` | 提交一组待审核的节点和关系修改。 | 提交 |

提案需要 `request_id`、`title`、`base_revision` 和 `patch`，建议说明 `rationale`。新模型在 `patch.schema_version` 指定 `2`。时态记录使用 `upsert_changes` 和 `remove_change_ids`。例如，更新某个节点时在 `patch.upsert_nodes` 带上该节点的完整新定义，包括保留的引用和属性。其他操作为 `upsert_edges`、`remove_node_ids` 和 `remove_edge_ids`，删除节点时需要处理仍引用它的父子关系和边。网络重试应复用相同 `request_id` 和请求内容。

提案状态从 `pending` 变成 `approved` 或 `rejected`。在本机工作台检查提案图谱、关系方向和知识定位，确认后一次发布。提案仅在审核界面可见。基线版本过期时，提交方用最新版本重新整理并提交新提案。发布版本和审核记录留痕。

工作台支持导出当前发布版本的 `ontology.yaml`。MCP 也可按需获取同一 YAML。两个出口都从同一已发布版本生成。
