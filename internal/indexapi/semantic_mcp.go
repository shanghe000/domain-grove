package indexapi

import (
	"context"
	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
	"ontology-engine/internal/index"
	"strings"
)

const consumptionGuide = "用 find_index_nodes 按名称、别名或 ID 找到入口。traverse_index 返回节点、公理、带方向的业务关系及资料绑定。强绑定消耗 0.5，因果和业务协作消耗 1，默认预算 2.5。边界游标的 node_id 与 revision 可作为下一次遍历参数，公共汇点停止扩散。query_index_changes 按带时区的半开区间查有来源的时间记录，排期只表示计划。按叶子的 read_strategy 读取外部知识库，用关系上的逻辑锚点定位，行号是缓存。核对口径后自行决定是否扩展。"
const governanceGuide = "仅在出现新资料、工具或技能、结构或外键变化、业务规则变化时提案。patch.schema_version 使用 2。四类节点 domain/concept/metric/artifact，业务实例写入 upsert_changes，临时玩家身份降为维度。资料在项目内用唯一的 repository/path 标识，在关系上记录各自的锚点。共享汇点只进不出，避免双向边、跨过业务聚合根的捷径。未知值留空；资料、排期与因果判断均需来源。读取当前发布修订后提交，等待人工审核。"

func addGuide(server *protocol.Server, name, description, guide string) {
	server.AddPrompt(&protocol.Prompt{Name: name, Description: description}, func(context.Context, *protocol.GetPromptRequest) (*protocol.GetPromptResult, error) {
		return &protocol.GetPromptResult{Messages: []*protocol.PromptMessage{{Role: "user", Content: &protocol.TextContent{Text: guide}}}}, nil
	})
}
func registerSemanticTools(server *protocol.Server, store *index.Store, canSubmit bool) {
	addGuide(server, "read_business_index", "消费本项目业务索引时的图例、公理、读取契约与续查方式。", consumptionGuide)
	if canSubmit {
		addGuide(server, "propose_model_change", "提交业务索引变更时的触发条件和建模纪律。", governanceGuide)
	}
	protocol.AddTool(server, readTool("traverse_index", "Return the governed semantic subgraph for one or more published node IDs, including axioms, bindings, one upstream hop and revision-pinned boundary IDs. Default budget 2.5."),
		func(ctx context.Context, _ *protocol.CallToolRequest, q index.TraverseArgs) (*protocol.CallToolResult, any, error) {
			g, err := store.PublishedRevision(ctx, q.Revision)
			if err != nil {
				return nil, nil, toolError(err)
			}
			result, err := index.Traverse(g, q)
			return nil, result, toolError(err)
		})
	protocol.AddTool(server, readTool("query_index_changes", "Query published time records overlapping [from,to), RFC3339 with timezone. Optional exact scope includes global records. A schedule is planned evidence, not confirmed runtime."),
		func(ctx context.Context, _ *protocol.CallToolRequest, q index.ChangeQuery) (*protocol.CallToolResult, any, error) {
			g, err := store.PublishedRevision(ctx, q.Revision)
			if err != nil {
				return nil, nil, toolError(err)
			}
			changes, err := index.ChangesInWindow(g, q)
			return nil, map[string]any{"revision": g.Revision, "changes": changes}, toolError(err)
		})
	protocol.AddTool(server, readTool("find_index_nodes", "Find published node IDs by name, ID or alias before traversal."),
		func(ctx context.Context, _ *protocol.CallToolRequest, q struct {
			Query string `json:"query"`
		}) (*protocol.CallToolResult, any, error) {
			term := strings.ToLower(strings.TrimSpace(q.Query))
			if term == "" {
				return nil, nil, toolError(index.ErrInvalid)
			}
			g, err := store.Graph(ctx)
			if err != nil {
				return nil, nil, toolError(err)
			}
			found := []index.Node{}
			for _, n := range g.Nodes {
				match := strings.Contains(strings.ToLower(n.ID+" "+n.Label), term)
				if aliases, ok := n.Properties["aliases"].([]any); ok {
					for _, v := range aliases {
						if alias, ok := v.(string); ok && strings.Contains(strings.ToLower(alias), term) {
							match = true
							break
						}
					}
				}
				if match {
					found = append(found, n)
				}
			}
			return nil, map[string]any{"revision": g.Revision, "nodes": found}, nil
		})
}
