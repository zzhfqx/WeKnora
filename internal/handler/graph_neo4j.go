package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/gin-gonic/gin"
)

// GraphNeo4jHandler handles Neo4j knowledge graph visualization endpoints.
type GraphNeo4jHandler struct {
	neo4jGraphService *service.Neo4jGraphService
	kbService         interfaces.KnowledgeBaseService
}

// NewGraphNeo4jHandler creates a new GraphNeo4jHandler.
func NewGraphNeo4jHandler(
	neo4jGraphService *service.Neo4jGraphService,
	kbService interfaces.KnowledgeBaseService,
) *GraphNeo4jHandler {
	return &GraphNeo4jHandler{
		neo4jGraphService: neo4jGraphService,
		kbService:         kbService,
	}
}

// validateKB validates that the KB exists.
func (h *GraphNeo4jHandler) validateKB(c *gin.Context) (string, bool) {
	ctx := c.Request.Context()
	kbID := secutils.SanitizeForLog(c.Param("kb_id"))
	if kbID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "knowledge base id is required"})
		return "", false
	}

	kb, err := h.kbService.GetKnowledgeBaseByID(ctx, kbID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "knowledge base not found"})
		return "", false
	}

	if !kb.IsGraphEnabled() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "knowledge graph is not enabled for this knowledge base"})
		return "", false
	}

	return kbID, true
}

// parseLimit parses and validates the limit query parameter.
func parseLimit(c *gin.Context, def, max int) int {
	limit := def
	if v := c.Query("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > max {
		limit = max
	}
	return limit
}

// parseRelTypes parses the rel_types query parameter (comma-separated).
func parseRelTypes(c *gin.Context) []string {
	v := strings.TrimSpace(c.Query("rel_types"))
	if v == "" {
		return nil
	}
	var types []string
	for _, t := range strings.Split(v, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			types = append(types, t)
		}
	}
	return types
}

// GetNeo4jGraph godoc
// @Summary      Get Neo4j knowledge graph (overview or ego mode)
// @Description  Returns graph data for visualization. Supports overview mode (top-N by degree) and ego mode (center-node subgraph).
// @Tags         KnowledgeGraph
// @Param        kb_id    path      string  true  "Knowledge base ID"
// @Param        mode     query     string  false "Mode: overview or ego (default: overview)"
// @Param        center   query     string  false "Center node name (required for ego mode)"
// @Param        depth    query     int     false "Ego depth 1-3 (default: 1)"
// @Param        limit    query     int     false "Max nodes to return 1-1000 (default: 200)"
// @Param        rel_types query    string  false "Comma-separated relation types filter"
// @Success      200 {object} types.Neo4jGraphData
// @Router       /knowledgebase/{kb_id}/graph/neo4j [get]
func (h *GraphNeo4jHandler) GetNeo4jGraph(c *gin.Context) {
	kbID, ok := h.validateKB(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	mode := strings.TrimSpace(c.Query("mode"))
	if mode == "" {
		mode = "overview"
	}

	relTypes := parseRelTypes(c)
	limit := parseLimit(c, 200, 1000)

	if mode == "overview" {
		graph, err := h.neo4jGraphService.GetOverview(ctx, kbID, limit, relTypes)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, graph)
		return
	}

	if mode == "ego" {
		center := strings.TrimSpace(c.Query("center"))
		if center == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "center is required when mode=ego"})
			return
		}
		depth := 1
		if v := c.Query("depth"); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
				depth = parsed
			}
		}
		if depth > 3 {
			depth = 3
		}
		graph, err := h.neo4jGraphService.GetEgoGraph(ctx, kbID, center, depth, limit, relTypes)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, graph)
		return
	}

	c.JSON(http.StatusBadRequest, gin.H{"error": "mode must be 'overview' or 'ego'"})
}

// SearchNeo4jNodes godoc
// @Summary      Search Neo4j entity nodes by name
// @Description  Fuzzy search entity nodes by name, ordered by degree descending.
// @Tags         KnowledgeGraph
// @Param        kb_id    path      string  true  "Knowledge base ID"
// @Param        q        query     string  true  "Search query"
// @Param        limit    query     int     false "Max results 1-100 (default: 20)"
// @Success      200 {array}  types.Neo4jGraphNode
// @Router       /knowledgebase/{kb_id}/graph/neo4j/search [get]
func (h *GraphNeo4jHandler) SearchNeo4jNodes(c *gin.Context) {
	kbID, ok := h.validateKB(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	q := strings.TrimSpace(c.Query("q"))
	if q == "" {
		c.JSON(http.StatusOK, []interface{}{})
		return
	}

	limit := parseLimit(c, 20, 100)

	nodes, err := h.neo4jGraphService.SearchNodes(ctx, kbID, q, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, nodes)
}

// GetNeo4jGraphStats godoc
// @Summary      Get Neo4j graph statistics
// @Description  Returns node count, relationship count, and relation type list.
// @Tags         KnowledgeGraph
// @Param        kb_id    path      string  true  "Knowledge base ID"
// @Success      200 {object} types.Neo4jGraphStats
// @Router       /knowledgebase/{kb_id}/graph/neo4j/stats [get]
func (h *GraphNeo4jHandler) GetNeo4jGraphStats(c *gin.Context) {
	kbID, ok := h.validateKB(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	stats, err := h.neo4jGraphService.GetStats(ctx, kbID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, stats)
}

// GetNeo4jRelationTypes godoc
// @Summary      Get all relation types in the Neo4j graph
// @Description  Returns a list of all distinct relationship types.
// @Tags         KnowledgeGraph
// @Param        kb_id    path      string  true  "Knowledge base ID"
// @Success      200 {array}  string
// @Router       /knowledgebase/{kb_id}/graph/neo4j/relation-types [get]
func (h *GraphNeo4jHandler) GetNeo4jRelationTypes(c *gin.Context) {
	kbID, ok := h.validateKB(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	relTypes, err := h.neo4jGraphService.GetRelationTypes(ctx, kbID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, relTypes)
}

// GetNeo4jNodeDetail godoc
// @Summary      Get detail of a single Neo4j node
// @Description  Returns node attributes, degree, relation type distribution, and top neighbors.
// @Tags         KnowledgeGraph
// @Param        kb_id    path      string  true  "Knowledge base ID"
// @Param        name     path      string  true  "Node name (URL-encoded)"
// @Success      200 {object} types.Neo4jNodeDetail
// @Router       /knowledgebase/{kb_id}/graph/neo4j/node/{name} [get]
func (h *GraphNeo4jHandler) GetNeo4jNodeDetail(c *gin.Context) {
	kbID, ok := h.validateKB(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	name := c.Param("name")
	name = strings.TrimSpace(name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "node name is required"})
		return
	}

	detail, err := h.neo4jGraphService.GetNodeDetail(ctx, kbID, name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, detail)
}
