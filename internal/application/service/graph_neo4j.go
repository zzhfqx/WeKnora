package service

import (
	"context"
	"fmt"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	neo4jGraphDefaultLimit = 200
	neo4jGraphMaxLimit     = 1000
	neo4jGraphDefaultDepth = 1
	neo4jGraphMaxDepth     = 3
)

// Neo4jGraphService provides Neo4j knowledge graph visualization queries.
type Neo4jGraphService struct {
	graphRepo interfaces.RetrieveGraphRepository
}

// NewNeo4jGraphService creates a new Neo4jGraphService.
func NewNeo4jGraphService(graphRepo interfaces.RetrieveGraphRepository) *Neo4jGraphService {
	return &Neo4jGraphService{graphRepo: graphRepo}
}

// GetOverview returns the graph overview (top-N nodes by degree and their relationships).
func (s *Neo4jGraphService) GetOverview(
	ctx context.Context,
	kbID string,
	limit int,
	relTypes []string,
) (*types.Neo4jGraphData, error) {
	if kbID == "" {
		return nil, fmt.Errorf("knowledge base id is required")
	}
	if limit <= 0 {
		limit = neo4jGraphDefaultLimit
	}
	if limit > neo4jGraphMaxLimit {
		limit = neo4jGraphMaxLimit
	}
	return s.graphRepo.GetGraphOverview(ctx, kbID, limit, relTypes)
}

// GetEgoGraph returns an ego (center-node) graph.
func (s *Neo4jGraphService) GetEgoGraph(
	ctx context.Context,
	kbID string,
	centerNode string,
	depth int,
	limit int,
	relTypes []string,
) (*types.Neo4jGraphData, error) {
	if kbID == "" {
		return nil, fmt.Errorf("knowledge base id is required")
	}
	if centerNode == "" {
		return nil, fmt.Errorf("center node is required for ego mode")
	}
	if depth <= 0 {
		depth = neo4jGraphDefaultDepth
	}
	if depth > neo4jGraphMaxDepth {
		depth = neo4jGraphMaxDepth
	}
	if limit <= 0 {
		limit = neo4jGraphDefaultLimit
	}
	if limit > neo4jGraphMaxLimit {
		limit = neo4jGraphMaxLimit
	}
	return s.graphRepo.GetEgoGraph(ctx, kbID, centerNode, depth, limit, relTypes)
}

// SearchNodes searches entity nodes by name.
func (s *Neo4jGraphService) SearchNodes(
	ctx context.Context,
	kbID string,
	query string,
	limit int,
) ([]types.Neo4jGraphNode, error) {
	if kbID == "" {
		return nil, fmt.Errorf("knowledge base id is required")
	}
	if query == "" {
		return []types.Neo4jGraphNode{}, nil
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	return s.graphRepo.SearchNodes(ctx, kbID, query, limit)
}

// GetStats returns graph statistics.
func (s *Neo4jGraphService) GetStats(
	ctx context.Context,
	kbID string,
) (*types.Neo4jGraphStats, error) {
	if kbID == "" {
		return nil, fmt.Errorf("knowledge base id is required")
	}
	return s.graphRepo.GetGraphStats(ctx, kbID)
}

// GetRelationTypes returns all relation types in the knowledge base graph.
func (s *Neo4jGraphService) GetRelationTypes(
	ctx context.Context,
	kbID string,
) ([]string, error) {
	if kbID == "" {
		return nil, fmt.Errorf("knowledge base id is required")
	}
	return s.graphRepo.GetRelationTypes(ctx, kbID)
}

// GetNodeDetail returns detailed information about a single node.
func (s *Neo4jGraphService) GetNodeDetail(
	ctx context.Context,
	kbID string,
	nodeName string,
) (*types.Neo4jNodeDetail, error) {
	if kbID == "" {
		return nil, fmt.Errorf("knowledge base id is required")
	}
	if nodeName == "" {
		return nil, fmt.Errorf("node name is required")
	}
	return s.graphRepo.GetNodeDetail(ctx, kbID, nodeName)
}
