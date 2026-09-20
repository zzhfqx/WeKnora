package types

// ChunkContext represents chunk content with surrounding context
type ChunkContext struct {
	ChunkID     string `json:"chunk_id"`
	Content     string `json:"content"`
	PrevContent string `json:"prev_content,omitempty"` // Previous chunk content for context
	NextContent string `json:"next_content,omitempty"` // Next chunk content for context
}

// PromptTemplateStructured represents the prompt template structured
type PromptTemplateStructured struct {
	Description string      `json:"description"`
	Tags        []string    `json:"tags"`
	Examples    []GraphData `json:"examples"`
}

type GraphNode struct {
	Name       string   `json:"name,omitempty"`
	Chunks     []string `json:"chunks,omitempty"`
	Attributes []string `json:"attributes,omitempty"`
}

// GraphRelation represents the relation of the graph
type GraphRelation struct {
	Node1 string `json:"node1,omitempty"`
	Node2 string `json:"node2,omitempty"`
	Type  string `json:"type,omitempty"`
}

type GraphData struct {
	Text     string           `json:"text,omitempty"`
	Node     []*GraphNode     `json:"node,omitempty"`
	Relation []*GraphRelation `json:"relation,omitempty"`
}

// NameSpace represents the name space of the knowledge base and knowledge
type NameSpace struct {
	KnowledgeBase string `json:"knowledge_base"`
	Knowledge     string `json:"knowledge"`
}

// Labels returns the labels of the name space
func (n NameSpace) Labels() []string {
	res := make([]string, 0)
	if n.KnowledgeBase != "" {
		res = append(res, n.KnowledgeBase)
	}
	if n.Knowledge != "" {
		res = append(res, n.Knowledge)
	}
	return res
}

// ===== Neo4j 图谱可视化数据类型 =====

// Neo4jGraphNode Neo4j 图谱节点（前端可视化用）
type Neo4jGraphNode struct {
	Name       string   `json:"name"`
	Attributes []string `json:"attributes"`
	Degree     int      `json:"degree"`
	ChunkCount int      `json:"chunk_count"`
}

// Neo4jGraphRelation Neo4j 图谱关系
type Neo4jGraphRelation struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Type   string `json:"type"`
}

// Neo4jGraphData Neo4j 图谱数据
type Neo4jGraphData struct {
	Nodes     []Neo4jGraphNode     `json:"nodes"`
	Relations []Neo4jGraphRelation `json:"relations"`
	Meta      Neo4jGraphMeta       `json:"meta"`
}

// Neo4jGraphMeta 图谱元信息
type Neo4jGraphMeta struct {
	Mode       string `json:"mode"`
	TotalNodes int    `json:"total_nodes"`
	TotalRels  int    `json:"total_rels"`
	Returned   int    `json:"returned"`
	Truncated  bool   `json:"truncated"`
	CenterNode string `json:"center_node,omitempty"`
	Depth      int    `json:"depth,omitempty"`
}

// Neo4jGraphStats 图谱统计信息
type Neo4jGraphStats struct {
	NodeCount int      `json:"node_count"`
	RelCount  int      `json:"rel_count"`
	RelTypes  []string `json:"rel_types"`
}

// Neo4jNodeDetail 节点详情
type Neo4jNodeDetail struct {
	Name          string           `json:"name"`
	Attributes    []string         `json:"attributes"`
	Degree        int              `json:"degree"`
	ChunkCount    int              `json:"chunk_count"`
	NeighborCount int              `json:"neighbor_count"`
	RelTypes      map[string]int   `json:"rel_types"`
	TopNeighbors  []Neo4jGraphNode `json:"top_neighbors"`
}
