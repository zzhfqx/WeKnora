package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// RetrieveGraphRepository is a repository for retrieving graphs
type RetrieveGraphRepository interface {
	// AddGraph adds a graph to the repository
	AddGraph(ctx context.Context, namespace types.NameSpace, graphs []*types.GraphData) error
	// DelGraph deletes a graph from the repository
	DelGraph(ctx context.Context, namespace []types.NameSpace) error
	// SearchNode searches for nodes in the repository
	SearchNode(ctx context.Context, namespace types.NameSpace, nodes []string) (*types.GraphData, error)

	// ===== 可视化查询 =====

	// GetGraphOverview 获取图谱概览（按度数取 top-N 节点及其之间的关系）
	GetGraphOverview(ctx context.Context, kbID string, limit int, relTypes []string) (*types.Neo4jGraphData, error)
	// GetEgoGraph 获取以实体为中心的 ego 图
	GetEgoGraph(ctx context.Context, kbID string, centerNode string, depth int, limit int, relTypes []string) (*types.Neo4jGraphData, error)
	// SearchNodes 按名称模糊搜索实体节点
	SearchNodes(ctx context.Context, kbID string, query string, limit int) ([]types.Neo4jGraphNode, error)
	// GetGraphStats 获取图谱统计信息
	GetGraphStats(ctx context.Context, kbID string) (*types.Neo4jGraphStats, error)
	// GetRelationTypes 获取所有关系类型
	GetRelationTypes(ctx context.Context, kbID string) ([]string, error)
	// GetNodeDetail 获取单个节点详情（含邻居概览）
	GetNodeDetail(ctx context.Context, kbID string, nodeName string) (*types.Neo4jNodeDetail, error)
}
