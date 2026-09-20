# Neo4j 图谱可视化改造 Plan

## 一、背景与目标

### 1.1 现状

WeKnora 知识库页面的"图谱"tab 目前展示的是 **Wiki 页面链接图**（Wiki 页面之间的超引用关系，数据存放在关系型数据库）。而基于 Neo4j 的**实体-关系知识图谱**（从文档中 LLM 提取的语义关系）只在后台用于 Graph RAG 检索增强，前端没有可视化展示入口，用户只能在 Neo4j Browser 中查看。

### 1.2 目标

在知识库页面新增 **Neo4j 图谱** tab，可视化展示 Neo4j 中存储的实体-关系知识图谱，与 Wiki 图谱并列。

改造后顶部 tab 结构：
```
文档 / Wiki / Wiki图谱 / Neo4j图谱
```

（原来的"图谱"改名为"Wiki 图谱"，新增"Neo4j 图谱"）

### 1.3 范围界定

| 项 | 是否做 | 说明 |
|----|--------|------|
| 后端 Neo4j 查询 API | ✅ | 新增概览、ego、搜索、统计、关系类型列表等查询接口 |
| 前端力导向图可视化 | ✅ | 复用 Wiki 图谱的手写 SVG 力导向图实现 |
| 按实体名称筛选 | ✅ | 搜索框模糊匹配 + 选中后 N 跳 ego 子图（当前唯一筛选方式） |
| 按关系类型筛选 | ❌（已移除） | 早期设计包含，Phase 7 因用户反馈"占空间无用"移除，列入后续优化 |
| 复杂启发式贪心搜索 | ❌ | 本期不做，后续可参考 KnowPla 的 `query_subgraph` 贪心策略扩展 |
| 提取逻辑优化 | ❌ | 本期不动，后续单独优化 |
| 本体层级结构（L1/L2/L3） | ❌ | WeKnora 当前 Neo4j 中没有本体类层级，先做纯实体-关系展示 |

---

## 二、Neo4j 数据结构确认

### 2.1 节点结构

节点标签：`ENTITY{kb_id}`（知识库级别标签，连字符替换为下划线）
- 每个节点还有额外的 `ENTITY{knowledge_id}` 标签（文件级）

节点属性：
| 属性 | 类型 | 说明 |
|------|------|------|
| `name` | string | 实体名称（核心标识） |
| `kg` | string | knowledge_id（文件 ID） |
| `attributes` | []string | 实体属性列表 |
| `chunks` | []string | 关联的 chunk ID 列表 |

### 2.2 关系结构

- 关系类型 = 用户配置的关系标签（如 `Author`、`Alias`、`相关` 等）
- 关系属性：当前写入时 attributes 为空 map，即关系没有额外属性
- 关系是有向的，但语义上很多是无向的

### 2.3 查询维度（可视化所需）

1. **概览**：知识库中所有实体节点 + 关系，按度数取 Top N
2. **Ego 图**：以某实体为中心，n 跳邻居子图（后端 `apoc.path.subgraphAll` 实现）
3. **搜索**：按实体名称模糊搜索（前端模糊匹配 + 后端搜索建议）
4. **统计**：节点数、关系数、关系类型列表
5. **筛选（当前唯一方式）**：按实体名称 + N 跳筛选，没有按关系类型筛选（Phase 7 已移除）

---

## 三、后端设计

### 3.1 Repository 层扩展

**文件**：`internal/types/interfaces/retriever_graph.go`

在 `RetrieveGraphRepository` 接口中新增方法：

```go
type RetrieveGraphRepository interface {
    // 已有方法（保持不变）
    AddGraph(ctx context.Context, namespace types.NameSpace, graphs []*types.GraphData) error
    DelGraph(ctx context.Context, namespace []types.NameSpace) error
    SearchNode(ctx context.Context, namespace types.NameSpace, nodes []string) (*types.GraphData, error)
    
    // ===== 新增：可视化查询 =====
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
```

### 3.2 新增数据类型

**文件**：`internal/types/extract_graph.go`（追加）

```go
// Neo4jGraphNode Neo4j 图谱节点（前端可视化用）
type Neo4jGraphNode struct {
    Name       string   `json:"name"`        // 实体名称（唯一标识）
    Attributes []string `json:"attributes"`  // 属性列表
    Degree     int      `json:"degree"`      // 连接度数（in+out）
    ChunkCount int      `json:"chunk_count"` // 关联 chunk 数量
}

// Neo4jGraphRelation Neo4j 图谱关系
type Neo4jGraphRelation struct {
    Source string `json:"source"` // 源实体名
    Target string `json:"target"` // 目标实体名
    Type   string `json:"type"`   // 关系类型
}

// Neo4jGraphData Neo4j 图谱数据
type Neo4jGraphData struct {
    Nodes     []Neo4jGraphNode     `json:"nodes"`
    Relations []Neo4jGraphRelation `json:"relations"`
    Meta      Neo4jGraphMeta       `json:"meta"`
}

type Neo4jGraphMeta struct {
    Mode        string `json:"mode"`          // "overview" | "ego"
    TotalNodes  int    `json:"total_nodes"`   // 知识库总节点数
    TotalRels   int    `json:"total_rels"`    // 知识库总关系数
    Returned    int    `json:"returned"`      // 返回节点数
    Truncated   bool   `json:"truncated"`     // 是否被截断
    CenterNode  string `json:"center_node,omitempty"` // ego 模式中心
    Depth       int    `json:"depth,omitempty"`       // ego 模式深度
}

// Neo4jGraphStats 图谱统计
type Neo4jGraphStats struct {
    NodeCount int      `json:"node_count"`
    RelCount  int      `json:"rel_count"`
    RelTypes  []string `json:"rel_types"`
}

// Neo4jNodeDetail 节点详情
type Neo4jNodeDetail struct {
    Name         string            `json:"name"`
    Attributes   []string          `json:"attributes"`
    Degree       int               `json:"degree"`
    ChunkCount   int               `json:"chunk_count"`
    NeighborCount int              `json:"neighbor_count"`
    RelTypes     map[string]int    `json:"rel_types"` // 各关系类型的数量
    TopNeighbors []Neo4jGraphNode  `json:"top_neighbors"` // 度数前 20 的邻居
}
```

### 3.3 Neo4j Repository 实现

**文件**：`internal/application/repository/retriever/neo4j/repository.go`

实现新增的 6 个方法，核心 Cypher 如下：

#### 3.3.1 GetGraphOverview — 概览模式

```cypher
// Step 1: 按度数取 Top N 节点
MATCH (n:<label>)
OPTIONAL MATCH (n)-[r]-(:<label>)
WITH n, count(DISTINCT r) AS degree
ORDER BY degree DESC
LIMIT $limit
WITH collect(n) AS topNodes

// Step 2: 取这些节点之间的关系（可选按类型过滤）
UNWIND topNodes AS n
MATCH (n)-[r]->(m:<label>)
WHERE m IN topNodes
  AND (size($rel_types) = 0 OR type(r) IN $rel_types)
RETURN
  collect(DISTINCT {name: n.name, attributes: n.attributes, degree: ...}) AS nodes,
  collect(DISTINCT {source: n.name, target: m.name, type: type(r)}) AS relations
```

- 按度数（连接数）降序取 Top N（默认 800，最大 1000）
- 只返回这些节点之间存在的关系
- ~~支持 `rel_types` 参数过滤关系类型~~（后端保留参数，前端未使用，Phase 7 已移除筛选 UI）

#### 3.3.2 GetEgoGraph — 自我中心模式

```cypher
MATCH (center:<label> {name: $center})
CALL apoc.path.subgraphAll(center, {
    maxLevel: $depth,
    labelFilter: '+<label>',
    relationshipFilter: ... // 可选关系类型过滤
}) YIELD nodes, relationships
WITH nodes, relationships
LIMIT $limit  // 截断保护
// ... 构造返回格式
```

- 以 `centerNode` 为中心向外扩展 `depth` 跳
- 默认 depth=1，最大 3
- 支持关系类型过滤

> **注意**：如果 APOC 的 `subgraphAll` 不可用或有兼容性问题，改用可变长度路径查询：
> ```cypher
> MATCH path = (center:<label> {name: $center})-[*1..<depth>]-(:<label>)
> RETURN nodes(path) AS nodes, relationships(path) AS rels
> LIMIT $limit
> ```

#### 3.3.3 SearchNodes — 节点搜索

```cypher
MATCH (n:<label>)
WHERE n.name CONTAINS $query
OPTIONAL MATCH (n)-[r]-(:<label>)
WITH n, count(DISTINCT r) AS degree
ORDER BY degree DESC
LIMIT $limit
RETURN n.name AS name, n.attributes AS attributes, degree
```

- 按名称模糊匹配
- 按度数降序排列
- 用于搜索框自动补全

#### 3.3.4 GetGraphStats — 统计信息

```cypher
// 节点数
MATCH (n:<label>) RETURN count(n) AS nodeCount
// 关系数
MATCH (n:<label>)-[r]->(:<label>) RETURN count(r) AS relCount
// 关系类型列表
MATCH (n:<label>)-[r]->(:<label>)
RETURN DISTINCT type(r) AS rel_type ORDER BY rel_type
```

#### 3.3.5 GetRelationTypes — 关系类型列表

同上第三条查询。

#### 3.3.6 GetNodeDetail — 节点详情

```cypher
// 基本信息 + 度数
MATCH (n:<label> {name: $name})
OPTIONAL MATCH (n)-[r]-(:<label>)
WITH n, count(DISTINCT r) AS degree,
     count(DISTINCT startNode(r)) + count(DISTINCT endNode(r)) - 1 AS neighbor_count
RETURN n, degree, neighbor_count

// 各关系类型数量
MATCH (n:<label> {name: $name})-[r]-(:<label>)
RETURN type(r) AS rel_type, count(*) AS cnt

// Top 邻居（按度数）
MATCH (n:<label> {name: $name})--(m:<label>)
OPTIONAL MATCH (m)-[r2]-(:<label>)
WITH m, count(DISTINCT r2) AS m_degree
ORDER BY m_degree DESC
LIMIT 20
RETURN m.name AS name, m_degree AS degree
```

### 3.4 Service 层

**新建文件**：`internal/application/service/graph_neo4j.go`

封装 `Neo4jGraphService`，负责：
- 参数验证（limit 范围、depth 范围等）
- 结果格式转换（Neo4j record → DTO）
- 错误处理

```go
type Neo4jGraphService struct {
    graphRepo interfaces.RetrieveGraphRepository
}

func NewNeo4jGraphService(graphRepo interfaces.RetrieveGraphRepository) *Neo4jGraphService { ... }

func (s *Neo4jGraphService) GetOverview(ctx context.Context, kbID string, limit int, relTypes []string) (*types.Neo4jGraphData, error)
func (s *Neo4jGraphService) GetEgoGraph(ctx context.Context, kbID string, centerNode string, depth int, limit int, relTypes []string) (*types.Neo4jGraphData, error)
func (s *Neo4jGraphService) SearchNodes(ctx context.Context, kbID string, query string, limit int) ([]types.Neo4jGraphNode, error)
func (s *Neo4jGraphService) GetStats(ctx context.Context, kbID string) (*types.Neo4jGraphStats, error)
func (s *Neo4jGraphService) GetRelationTypes(ctx context.Context, kbID string) ([]string, error)
func (s *Neo4jGraphService) GetNodeDetail(ctx context.Context, kbID string, nodeName string) (*types.Neo4jNodeDetail, error)
```

### 3.5 Handler 层

**新建文件**：`internal/handler/graph_neo4j.go`

新增 6 个 API 端点：

| 方法 | 路径 | 说明 | 权限 |
|------|------|------|------|
| GET | `/knowledgebase/{kb_id}/graph/neo4j` | 获取图谱（overview/ego） | 读权限 |
| GET | `/knowledgebase/{kb_id}/graph/neo4j/search` | 搜索实体节点 | 读权限 |
| GET | `/knowledgebase/{kb_id}/graph/neo4j/stats` | 获取统计信息 | 读权限 |
| GET | `/knowledgebase/{kb_id}/graph/neo4j/relation-types` | 获取关系类型列表 | 读权限 |
| GET | `/knowledgebase/{kb_id}/graph/neo4j/node/{name}` | 获取节点详情 | 读权限 |

**查询参数**（`GET /graph/neo4j`）：
- `mode`: `overview`（默认）/ `ego`
- `center`: 中心节点名称（ego 模式必填）
- `depth`: 1-3，默认 1
- `limit`: 1-1000，默认 200
- `rel_types`: 逗号分隔的关系类型（可选，后端保留参数但前端未使用，Phase 7 已移除筛选 UI）

**前置检查**：
1. 检查 Neo4j 是否已启用（通过 graphRepo/driver 是否可用）
2. 检查知识库的 `indexing_strategy.graph_enabled`
3. 不满足返回 400 + 错误信息

### 3.6 路由注册

**文件**：`internal/router/routes_knowledge.go`

在 Wiki graph 路由附近添加：

```go
// Neo4j 知识图谱可视化
neo4jGraph := wikiRead.Group("/graph/neo4j")
{
    neo4jGraph.GET("", g.Viewer(), g.KBAccessRead("kb_id"), graphHandler.GetNeo4jGraph)
    neo4jGraph.GET("/search", g.Viewer(), g.KBAccessRead("kb_id"), graphHandler.SearchNeo4jNodes)
    neo4jGraph.GET("/stats", g.Viewer(), g.KBAccessRead("kb_id"), graphHandler.GetNeo4jGraphStats)
    neo4jGraph.GET("/relation-types", g.Viewer(), g.KBAccessRead("kb_id"), graphHandler.GetNeo4jRelationTypes)
    neo4jGraph.GET("/node/:name", g.Viewer(), g.KBAccessRead("kb_id"), graphHandler.GetNeo4jNodeDetail)
}
```

### 3.7 依赖注入

**文件**：`internal/container/container.go`

注册 `Neo4jGraphService`。

---

## 四、前端设计

### 4.1 Tab 结构调整

**文件**：`frontend/src/views/knowledge/KnowledgeBase.vue`

#### 4.1.1 Tab 定义

```typescript
// 原：['documents', 'wiki', 'graph']
// 改为：
const validTabs = ['documents', 'wiki', 'wiki-graph', 'neo4j-graph'] as const
type KbTab = typeof validTabs[number]
```

#### 4.1.2 显示条件

- `wiki` / `wiki-graph`：`isWiki` 为 true 时显示
- `neo4j-graph`：图数据库启用（`graph_database_engine !== 'Not Enabled'`）且知识库启用了图谱提取时显示
- 两者都有的情况下，四个 tab 全显示

#### 4.1.3 面包屑展示

```
文档 / Wiki / Wiki图谱 / Neo4j图谱
```

- 原来的"图谱"改名为"Wiki 图谱"
- 新增"Neo4j 图谱"

#### 4.1.4 内容区切换

```html
<!-- Wiki 图谱 -->
<div v-if="activeKbTab === 'wiki-graph'" class="wiki-main-area">
  <WikiBrowser v-if="kbId" :knowledge-base-id="kbId" view="graph" ... />
</div>

<!-- Neo4j 图谱 -->
<div v-if="activeKbTab === 'neo4j-graph'" class="neo4j-graph-area">
  <Neo4jGraphViewer v-if="kbId" :knowledge-base-id="kbId" />
</div>
```

WikiBrowser 的调用：
- `wiki` tab → view="browser"
- `wiki-graph` tab → view="graph"

### 4.2 Neo4jGraphViewer 组件

**新建文件**：`frontend/src/views/knowledge/wiki/Neo4jGraphViewer.vue`

复用 WikiBrowser 中力导向图的核心实现（SVG + requestAnimationFrame），适配 Neo4j 数据结构。

#### 4.2.1 布局结构

```
┌─────────────────────────────────────────────────────────┐
│  ┌──────────┐                                           │
│  │[搜索实体▼]│  ← 左上角浮动搜索框（唯一筛选入口）       │
│  └──────────┘                                           │
│                                                         │
│                    力导向图 SVG 画布                     │
│                                                         │
│                                                         │
│  ┌──────────────┐                    ┌──────┐           │
│  │ 图例          │                    │ ⟳ 适 │ ← 右上    │
│  │ ● 实体节点    │                    │ 应屏幕│   角按钮  │
│  │ → 关系        │                    └──────┘           │
│  │              │                                       │
│  │ 统计：        │                                       │
│  │ N 节点 / M 关 │                                       │
│  └──────────────┘                                       │
└─────────────────────────────────────────────────────────┘
```

> **注**：早期设计包含顶部 toolbar + 关系类型筛选下拉框，Phase 7 已全部移除，改为浮动搜索框 + 画布占满全屏。当前没有关系类型筛选功能。

#### 4.2.2 核心功能

| 功能 | 说明 | 优先级 | 状态 |
|------|------|--------|------|
| 概览模式 | 按度数 Top-N 展示 | P0 | ✅ |
| Ego 模式 | 选中节点后加载 n 跳 ego 子图（后端 API） | P0 | ✅ |
| Bloom 扩展 | 悬停 + 按钮增量加载邻居 | P1 | ❌ 未实现 |
| 搜索定位 | 搜索框模糊匹配，选中后进入 ego 模式 | P0 | ✅ |
| ~~关系类型筛选~~ | ~~多选下拉框，过滤显示的关系类型~~ | ~~P0~~ | ❌ 已移除（Phase 7） |
| 模糊匹配筛选 | 输入关键词实时高亮匹配节点（前端过滤） | P0 | ✅ Phase 9 |
| 节点详情抽屉 | 点击节点，右侧显示属性、邻居、关联文档数 | P0 | ✅ |
| 节点拖拽 | 可拖拽固定位置 | P1 | ✅ |
| 画布平移缩放 | 滚轮缩放、拖拽平移 | P0 | ✅ |
| 适应屏幕 | 一键适配 | P1 | ✅ |
| 关系标签 | 边上显示关系类型（hover 时显示相连边的标签） | P1 | ✅ Phase 9 |

> **当前实际筛选方式**：只有"按实体名称搜索 + N 跳 ego 子图"这一种筛选路径。没有按关系类型筛选。

#### 4.2.3 力导向图实现（从 WikiBrowser 迁移）

**核心逻辑复用**：tick 函数（斥力、引力、重力、冷却）、节点渲染、边渲染、pan/zoom、拖拽等。

**适配点**：

| 维度 | Wiki 图谱 | Neo4j 图谱 |
|------|-----------|------------|
| 节点标识 | `slug` | `name`（实体名） |
| 节点类型/颜色 | page_type（6 种颜色） | 统一用一种颜色（如紫色 #722ed1），或者按度数深浅 |
| 节点大小 | 基于 link_count（对数缩放） | 基于 degree（线性+对数混合，5px ~ 45px，度数越大越突出） |
| 节点颜色 | page_type（6 种颜色） | 按度数分 4 级：核心(红) / 重要(橙) / 一般(紫) / 边缘(蓝) |
| 节点视觉层次 | 多层环（扩展环/激活环/主圆/文字背景） | 多层环（光晕/扩展环/激活环/主圆/高光/文字背景条） |
| 边的权重 | 统一粗细 | 按两端节点重要度分级（核心节点间最粗最亮） |
| 边标识 | source slug → target slug | source name → target name |
| 边类型 | 统一灰色 | 可按关系类型着色（一期统一灰色，hover 高亮） |
| 搜索 API | `searchWikiPages` | `/graph/neo4j/search` |
| 概览 API | `getWikiGraph(mode=overview)` | `getNeo4jGraph({mode: 'overview'})` |
| Ego API | `getWikiGraph(mode=ego, center=slug)` | `getNeo4jGraph({mode: 'ego', center: name})` |
| 点击节点 | 打开 Wiki 页面 | 打开节点详情抽屉 |

#### 4.2.4 节点详情抽屉

右侧弹出（400px 宽），展示：

- **实体名称**（标题）
- **属性列表**：以 chip/标签形式展示 attributes
- **连接度数**：总度数
- **关系类型分布**：该节点涉及的各关系类型及数量
- **Top 邻居**：按度数排序的前 20 个邻居节点，点击可跳转到（切换 ego 中心）
- **关联文档数**：chunk 数量（可点击跳转到文档列表，后续做）

#### 4.2.5 节点视觉设计（度数驱动的视觉分层）

**核心原则：被引用越多（度数越高）的节点，越大、越亮、越突出。**

节点大小：线性 + 对数混合缩放，范围 5px ~ 45px
- 线性部分 `6 + ratio * 30`：保证高度数节点视觉上明显更大
- 对数补充 `log(degree+1) * 1.2`：平滑极端值

颜色分层（4 级，按度数百分比）：

| 层级 | 阈值 | 颜色 | 说明 |
|------|------|------|------|
| 核心节点 | Top 5% | 🔴 `#cf1322` 红 | 最重要的实体，最大最亮 |
| 重要节点 | Top 20% | 🟠 `#d46b08` 橙 | 次重要实体 |
| 一般节点 | Top 50% | 🟣 `#722ed1` 紫 | 中等连接度 |
| 边缘节点 | Bottom 50% | 🔵 `#1677ff` 蓝 | 连接较少的实体 |

视觉层次（从内到外多层结构，参考 Wiki 图谱风格）：
1. **光晕 (glow)**：核心/重要节点的辉光效果，营造"突出感"
2. **扩展环 (expansion ring)**：虚线外环，hover 时显示，表示可探索更多邻居
3. **激活环 (active ring)**：选中/高亮时的实心圆环
4. **主圆 (main circle)**：节点主体，白色描边 + drop-shadow
5. **内部高光 (inner highlight)**：顶部半透明白色圆，增加立体感
6. **文字背景条 (label bg)**：label 下方的白色圆角背景，提升可读性
7. **文字标签 (label)**：核心/重要节点始终显示，其余悬停/选中时显示

边的视觉权重（按两端节点的最低层级）：
- 核心 ↔ 核心：2.5px / 55% 透明度
- 重要 ↔ 重要：1.8px / 45% 透明度
- 一般 ↔ 一般：1.2px / 28% 透明度
- 边缘 ↔ 边缘：1px / 12% 透明度

布局初始位置：高度数节点放在更靠近中心的位置（按 tier 调整初始半径），加速布局收敛。

#### 4.2.6 筛选逻辑（当前实际实现）

> **重要更新**：早期设计包含"关系类型筛选"，Phase 7 根据用户反馈已移除。当前唯一的筛选方式是**按实体名称 + N 跳**。

**筛选路径有两种模式**：

| 模式 | 触发方式 | 实现方式 | 效果 |
|------|----------|----------|------|
| **模糊匹配模式** | 搜索框输入关键词（未选中） | 前端过滤：节点名包含关键词即匹配 | 匹配节点高亮，直接邻居半透明，其余淡出；自动适配视图 |
| **N 跳 ego 模式** | 选中搜索结果 / 点击节点"设为中心" | 后端 API：`mode=ego&center=xxx&depth=N` | 从 Neo4j 加载完整 N 跳子图，替换当前视图 |

**N 跳 ego 模式的关键变化（Phase 13）**：
- 最初是前端 BFS（在已加载的 top-800 节点中搜索邻居）→ 跳数越大节点越少
- Phase 13 改为后端 `apoc.path.subgraphAll` → 跳数越大节点越多，结果完整

#### 4.2.7 ~~关系类型筛选~~（已废弃设计）

以下为早期设计，**未实际使用**，Phase 7 已移除：
- ~~调用 `/relation-types` 获取所有关系类型~~
- ~~多选下拉框（t-select multiple）~~
- ~~切换后重新请求当前模式的图谱数据~~
- ~~支持"全选/取消全选"快捷操作~~

> 该功能列入"后续优化方向"，待需要时再加回来。

### 4.3 前端 API 封装

**文件**：`frontend/src/api/wiki/index.ts`（追加）

```typescript
// ===== Neo4j 知识图谱 =====

export interface Neo4jGraphNode {
  name: string;
  attributes: string[];
  degree: number;
  chunk_count: number;
}

export interface Neo4jGraphRelation {
  source: string;
  target: string;
  type: string;
}

export interface Neo4jGraphMeta {
  mode: 'overview' | 'ego';
  total_nodes: number;
  total_rels: number;
  returned: number;
  truncated: boolean;
  center_node?: string;
  depth?: number;
}

export interface Neo4jGraphData {
  nodes: Neo4jGraphNode[];
  relations: Neo4jGraphRelation[];
  meta: Neo4jGraphMeta;
}

export interface Neo4jGraphStats {
  node_count: number;
  rel_count: number;
  rel_types: string[];
}

export interface Neo4jNodeDetail {
  name: string;
  attributes: string[];
  degree: number;
  chunk_count: number;
  neighbor_count: number;
  rel_types: Record<string, number>;
  top_neighbors: Neo4jGraphNode[];
}

// 获取图谱数据（overview 或 ego）
export function getNeo4jGraph(kbId: string, params: {
  mode?: 'overview' | 'ego';
  center?: string;
  depth?: number;
  limit?: number;
  rel_types?: string[];
}) { ... }

// 搜索实体节点
export function searchNeo4jNodes(kbId: string, q: string, limit = 20) { ... }

// 获取图谱统计
export function getNeo4jGraphStats(kbId: string) { ... }

// 获取关系类型列表
export function getNeo4jRelationTypes(kbId: string) { ... }

// 获取节点详情
export function getNeo4jNodeDetail(kbId: string, name: string) { ... }
```

### 4.4 系统信息复用

前端已有 `systemInfo.graph_database_engine` 反映图数据库状态，直接复用控制 Neo4j 图谱 tab 的显示。

---

## 五、实现步骤

### Phase 1：后端 API（先做，好让前端有数据调）

1. 新增数据类型（`Neo4jGraphNode/Relation/Data/Meta/Stats/NodeDetail`）
2. 扩展 `RetrieveGraphRepository` 接口（6 个新方法）
3. 在 `Neo4jRepository` 中实现 6 个查询方法
4. 新建 `Neo4jGraphService`（参数验证 + 结果转换）
5. 新建 `GraphNeo4jHandler`（6 个端点）
6. 注册路由 + DI 注册
7. 后端自测（curl 验证各接口）

### Phase 2：前端骨架 + API

1. 前端 API 封装（6 个函数 + 类型定义）
2. KnowledgeBase.vue 中拆分 tab（wiki-graph + neo4j-graph）
3. 新建 `Neo4jGraphViewer.vue` 空骨架（空状态 + loading）
4. 接入组件，tab 切换正常显示

### Phase 3：力导向图核心

1. 从 WikiBrowser 提取力导向图代码，迁移到 Neo4jGraphViewer
2. 适配 Neo4j 数据结构（name 代替 slug 等）
3. 实现概览模式加载与渲染
4. 实现画布平移缩放
5. 实现节点悬停高亮
6. 实现节点拖拽固定

### Phase 4：交互功能

1. 搜索框（模糊搜索 + 定位）
2. Ego 模式（选中节点切换）
3. Bloom 增量扩展（未实现）
4. 节点详情抽屉
5. ~~关系类型筛选~~（Phase 7 已移除）
6. 适应屏幕、返回概览等操作按钮

### Phase 5：完善与联调

1. 空状态/错误状态/加载状态 ✅
2. 图例 + 统计信息 ✅
3. 关系标签显示切换
4. 前后端联调 ✅
5. 大数据量测试（500 节点性能）
6. 代码清理 + 注释

### Phase 6：视觉优化（用户反馈第一轮迭代）

**用户反馈**："neo4j 显示太粗糙，参考 wiki 图谱中图标的形式，好区分不同节点；即使节点都是本体，也可以多添加一些节点的颜色；不明白当前的筛选逻辑是啥"

优化内容：
1. **节点大小按度数放大** ✅ — 线性+对数混合缩放（5px ~ 40px），度数越高越大越突出
2. **5 级颜色分层** ✅ — 核心(红)/重要(橙)/一般(紫)/常见(蓝)/边缘(绿)
3. **Wiki 风格多层环视觉** ✅ — 光晕、扩展虚线环、激活环、主圆、内部高光、文字背景条
4. **边按权重分级** ✅ — 连接高重要度节点的边更粗更亮
5. **初始布局优化** ✅ — 高度数节点初始放在中心附近，加速收敛
6. **图例说明** ✅ — 底部浮层显示颜色图例 + 统计信息
7. **有向边箭头** ✅ — 单向边目标端箭头，双向边两端箭头，参考 Wiki 图谱逻辑
8. **核心节点标签常驻显示** ✅ — tier 0/1/2 节点始终显示标签

### Phase 7：交互简化（用户反馈第二轮迭代）

**用户反馈**："占这么多空间的筛选条件不合理 / 放图谱的页面太小了 / 暂时不考虑其它无用筛选，直接去除，显示完整的图谱 / 除了通过实体名称筛选外，实体名筛选只显示和其相连接的节点，可以控制层级，1、2、3表示几跳 / 边是关系，要有方向的箭头"

核心改动：
1. **去除关系类型筛选** ✅ — 移除外层 toolbar，画布占满整个页面
2. **默认显示完整图谱** ✅ — 加载 800 节点（limit 上限），完整展示
3. **搜索框改为浮层** ✅ — 左上角浮动卡片，不占画布空间（Wiki 图谱风格）
4. **搜索 = 实体名 + N 跳过滤** ✅ — 搜索实体后，只显示该实体及其 1/2/3 跳邻居
5. **跳数控制** ✅ — 搜索后出现 1跳/2跳/3跳 切换按钮，BFS 前端过滤
6. **"显示全部"按钮** ✅ — 一键清除搜索过滤，回到完整图谱视图
7. **适应屏幕按钮** ✅ — 右上角浮动操作按钮
8. **底部图例浮层** ✅ — 毛玻璃效果，不占固定高度
9. **边去重 + 方向箭头** ✅ — 双向边合并为单条线两端箭头，单向边单箭头（同 Wiki 图谱逻辑）
10. **修复高度为 0 的 bug** ✅ — `.neo4j-graph-area` 加上 `flex:1; min-height:0`

### Phase 8：视觉风格对齐 Wiki 图谱（用户反馈第三轮迭代）

**用户反馈**："我建议你符合wiki的显示逻辑和图像处理，你现在的不好看 / 参考wiki图谱的显示"

核心改动（完全对齐 WikiBrowser.vue 的渲染风格）：

| 维度 | 调整前 | 调整后（Wiki 风格） |
|------|--------|---------------------|
| 节点样式 | 光晕+内高光+阴影+多层环 | 扁平纯色圆 + 白色 2px 描边 |
| 节点大小 | 线性+对数混合 5~40px | log 缩放 8~24px（同 Wiki） |
| 标签样式 | 白色圆角背景框 | text-shadow 4 方向描边（无背景） |
| 边的粗细 | 按节点 tier 分级（1~2.2px） | 统一 1.2px / 0.4 透明度 |
| 箭头形状 | 实心三角形 | 飞箭形 `M0,0 L10,3 L0,6 L2,3 Z`（同 Wiki） |
| 高亮淡出 | 0.15 不透明度 | 0.2 节点 / 0.08 边（同 Wiki） |
| 选中指示 | 圆变粗 + 激活环 | 仅激活环（同 Wiki active ring） |
| 背景色 | 径向渐变浅灰 | 纯白 |
| 配色 | 红/橙/紫/蓝/绿 | 红/橙/紫/天蓝/绿（参考 Wiki 的 comparison/concept/synthesis/entity 色系） |
| 扩展虚线环 | 1px / 2.5-2 dash | 1.5px / 3-3 dash（同 Wiki） |
| 边端点偏移 | 半径处 | 半径 + 4px（给箭头留余量） |

### Phase 9：关系标签显示 + 模糊搜索筛选（用户反馈第四轮迭代）

**用户反馈**："怎么不见关系名称？" / "节点名称筛选没效果，跟没筛选一样" / "筛选实体节点我希望支持模糊匹配"

**核心改动**：

1. **边关系标签显示** ✅
   - 每条边添加关系类型文字标签（`type` 字段，如 `part_of`、`Alias`）
   - 白底半透明背景，位于边中点旁（线法向偏移 6px）
   - 默认隐藏，hover 节点或点击选中时，相连边的标签显示
   - 高亮时标签文字变为紫色（`#722ed1`），与高亮边颜色一致
   - 边标签随力导向布局实时更新位置

2. **搜索筛选修复（改用 t-select + :on-search）** ✅
   - 之前用 `t-input` + 自定义下拉，事件绑定有问题导致筛选不生效
   - 改用 `t-select` 的 `:on-search` 属性（函数式搜索回调），与 Wiki 图谱完全一致
   - 事件模型：`:on-search` → 输入时触发（模糊过滤 + 后端搜索建议），`@change` → 选中时触发（进入 N 跳模式）

3. **模糊匹配筛选** ✅
   - 输入关键词时**实时前端模糊过滤**：节点名称包含关键词即匹配
   - 匹配节点完整显示，直接邻居 50% 透明度显示（提供上下文）
   - 边：至少一端匹配关键词才显示
   - 自动 fitToView 适配到匹配结果
   - 显示"模糊匹配：N 个节点"提示
   - 清空输入 → 恢复全图

4. **搜索建议防抖 + seq 防过期** ✅
   - 防抖 200ms（同 Wiki 图谱）
   - 用单调递增 `searchSeq` 丢弃过期的后端响应，避免竞态

5. **N 跳精确模式** ✅（保留已有）
   - 选中搜索结果后进入 N 跳模式（BFS 前端过滤）
   - 1跳/2跳/3跳 可切换
   - 打开详情抽屉

### Phase 10：布局优化 + 标签显示（用户反馈第五轮迭代）

**用户反馈**："聚成一团有没有名称的节点是怎么生成的，是否毫无意义"

**分析**：堆在一起的绿色节点是**度数最低的边缘节点**（tier 4，Bottom 30%），默认不显示标签导致看不清。这些节点是有意义的（LLM 从文本中提取的实体），只是连接少。

**优化内容**：

1. **默认显示标签扩大到 tier 0/1/2** ✅
   - 之前只显示 tier 0（核心）和 tier 1（重要）的标签
   - 扩大到 tier 2（一般节点）也默认显示，更多节点能直接看到名字
   - tier 3/4 仍需 hover 或开启"显示全部标签"

2. **显示全部标签按钮** ✅
   - 右上角操作栏新增 "A" 字图标按钮
   - 点击切换所有节点标签的显示/隐藏
   - 激活态有高亮背景色

3. **力导向布局更舒展** ✅
   - 最大斥力距离：320px → **400px**（斥力作用范围更大）
   - 边目标距离：95px → **120px**（节点间距更大）
   - 弹簧强度：0.008 → **0.006**（弹簧更弱，斥力主导布局）
   - 最大速度：18 → **20**
   - 效果：低度数节点不再挤成一团，整体布局更分散

### Phase 11：空壳节点过滤 + 叶子节点标签（用户反馈第六轮迭代）

**用户反馈**：
- "图中这一坨节点是什么情况？又没有信息还存在？"
- "有的节点指向空节点"
- "不是过滤孤儿节点，而是过滤连节点信息都没有的节点"
- "边只有2个节点才显示，只有一个节点就不显示边"

**根因分析**：

图谱中存在大量**空壳节点（phantom nodes）**——这些节点只有一个 `name` 属性，`chunk_count = 0`，`attributes` 为空。它们**不是 LLM 真正从文本中提取的实体**，而是导入关系时 `apoc.merge.node` 自动创建的占位节点。

完整生成链路：
```
LLM 提取 → Node 列表（真正的实体） + Relation 列表
              ↓                           ↓
         节点导入（有 props）          关系导入
                                      apoc.merge.node(source)
                                      apoc.merge.node(target)
                                      ↑
                              如果 target 不在 Node 列表中，
                              会自动创建一个空壳节点
```

**为什么 target 不在 Node 列表中？**
- LLM 在提取关系时可能提到了一个它没有识别为实体的事物
- 关系提取的 prompt 约束较松，实体提取的 prompt 约束较紧，两者不一致
- 多个 chunk 之间的实体名有细微差异

**这些空壳节点的特征**：
- `chunks` 数组为空 → `chunk_count = 0`
- `attributes` 数组为空
- 只有一个 `name` 属性
- 通常 degree 也很低（很多是 degree = 1 的叶子节点）
- 因为没有标签（默认 tier 4 不显示），视觉上就是一个绿色小圆点，像"空节点"

**修复内容**：

#### 1. 空壳节点过滤 ✅
- **判定条件**：`chunk_count = 0` 且 `attributes` 为空 且 `name` 非空
- 在 `initGraphFromData()` 入口处过滤掉这些节点，不加入渲染
- 过滤掉的节点数在底部统计栏显示（"已滤除 N 个空壳节点"）
- **调试时如果想看到全部节点**，把 `validNodes` 的 filter 条件注释掉即可

#### 2. 边的双向校验 ✅
- 只有当 `source` 和 `target` **两端都在有效节点集中**时，边才渲染
- 之前的逻辑：边直接从 `data.relations` 全量导入，不检查端点是否存在
- 后果：一条边指向一个不存在的节点 → 视觉上就是"边指向空"
- 同时跳过自环（`source === target`）

#### 3. 叶子节点默认显示标签 ✅
- degree ≤ 1 的叶子节点也默认显示标签
- 原因：即使节点是有效的，如果只有一条边连过来又没标签，视觉上也像"空节点"
- 结合空壳节点过滤后，剩下的叶子节点都是有真实信息的，显示标签更合理

#### 关键代码位置
- 文件：`frontend/src/views/knowledge/wiki/Neo4jGraphViewer.vue`
- 函数：`initGraphFromData()`（过滤逻辑）
- 函数：`updateLabelsVisibility()`（叶子节点标签逻辑）
- 计算属性：`statsText`（统计显示）

---

### Phase 12：后端同名节点合并修复（根因修复）

**用户反馈**：图谱中存在大量"节点团"——一堆绿色节点挤在一起没有信息。经查是**同名重复节点**导致的。

#### 问题根因

Neo4j 中同一个实体被创建了多个节点（重名节点）。从 API 返回数据验证：800 个节点中有 **28 个名字重复，共 63 个重复节点**。例如：
- "保险公司" 出现 4 次（degree 分别为 16/10/8/4）
- "费率合理性说明材料" 出现 2 次
- "总精算师" 出现 3 次（degree 都是 1）

这些重复节点在力导向图中因为有连接（弹簧力）而互相靠近，又因为斥力不能完全重叠，最终挤成一团，标签重叠后看起来就像"没有信息的节点团"。

#### 为什么会有重名节点？

`apoc.merge.node(labels, identProps)` 的匹配逻辑是：节点必须**拥有所有给定 labels** 且 identProps 全部匹配，才算命中。

**原逻辑（有问题）**：
```go
apoc.merge.node(n.Labels(namespace), {name: name, kg: knowledge_id}, ...)
// Labels(namespace) = [ENTITY{kb_id}, ENTITY{knowledge_id}]  ← 两个标签
// kg = knowledge_id  ← 知识集合 ID
```

每个 chunk 属于不同的 `KnowledgeID`（知识集合），导致：
1. 同一个实体 "保险公司" 在知识集合 A 和知识集合 B 中都被提取
2. 两次导入的 labels 不同（`ENTITYkb1:ENTITYknowA` vs `ENTITYkb1:ENTITYknowB`）
3. `apoc.merge.node` 认为是不同节点 → 各自创建 → 产生重名

#### 修复方案（参考 KnowPla 实现模式）

**改后逻辑**：
```go
// 只用知识库级单标签 + name 作为匹配键
apoc.merge.node([ENTITY{kb_id}], {name: name}, onCreateProps, onMatchProps)
// 同知识库内同名就合并，chunks 和 attributes 合并去重
```

改动点（`internal/application/repository/retriever/neo4j/repository.go` 的 `addGraph`）：

| 项 | 原逻辑 | 改后 | 影响 |
|----|--------|------|------|
| **merge 标签** | 所有 namespace labels（kb + knowledge 两个） | 仅知识库级标签 `ENTITY{kb_id}` | 同 KB 同名即合并 |
| **match key** | `{name, kg}` | `{name}` | 简化，只用 name 做去重 |
| **chunks 合并** | `apoc.coll.union` | `apoc.coll.union`（保留） | 不变 |
| **attributes 合并** | onCreateProps 中设置 | 用 `apoc.coll.union` 显式合并 | 合并时属性也会累加去重 |
| **kg 属性** | 作为 match key | 只设置（覆盖），不参与匹配 | 含义不变（存 knowledge_id） |
| **knowledge 级标签** | 作为 merge key 的一部分 | **不再添加**（只用单标签） | 简化标签体系 |

#### 与原始提取逻辑的区别说明

原始设计：
- 每个知识集合（knowledge）有自己独立的命名空间
- 不同知识集合的同名实体被视为不同节点（按知识集合隔离）
- 标签 = 知识库标签 + 知识集合标签（双重标签）

改动后：
- 同一知识库内的实体全局去重（按 name 合并）
- 不同知识集合提取到的同名实体会合并为一个节点，chunks 和 attributes 累加
- 标签简化为单标签（知识库级）

**为什么这样改是合理的**：
1. 知识图谱的核心价值就是实体之间的关联，如果同 KB 内同名实体还隔离，图谱连接度会大打折扣
2. KnowPla 也是按 name 做节点合并的，实践证明合理
3. 如果后续需要按知识集合过滤，可以通过 chunks 反查知识集合，不需要在节点层隔离

#### 兼容性说明

- **已有数据**：旧数据节点有两个标签（`ENTITY{kb_id}:ENTITY{knowledge_id}`），新数据只有一个标签（`ENTITY{kb_id}`）。查询用 `MATCH (n:ENTITY{kb_id})` 两种都能查到，**查询兼容**。
- **合并不会自动发生**：已有的重名节点不会自动合并。需要**重新加工文档**后，新导入的节点才会按新逻辑合并。
- **验证方式**：重新加工文档后，调用概览接口，检查是否还有重名节点。

#### 验证结果

修改已验证生效。重新加工文档后：
- 重名节点消失，图谱中"挤成一团的节点团"问题解决
- 同一实体的 chunks 和 attributes 被正确合并到同一个节点上
- 节点度数统计更准确（合并后 degree 是累加的，反映真实连接数）

#### 调试命令

```sql
-- 查当前有多少重名节点（按 name 分组，count > 1 的）
-- 把 {kb_id} 换成实际的知识库 ID（如 ENTITYkb_xxx）
MATCH (n:ENTITY{kb_id})
WITH n.name AS name, count(*) AS cnt
WHERE cnt > 1
RETURN name, cnt
ORDER BY cnt DESC
LIMIT 20

-- 查某个具体节点的详情（验证合并后 chunks 和 attributes 是否正确）
MATCH (n:ENTITY{kb_id} {name: '保险公司'})
RETURN n.name, labels(n) as labels, size(n.chunks) as chunk_count, size(n.attributes) as attr_count, n.kg as kg
LIMIT 5
```

### Phase 13：跳数逻辑修复（前端 BFS → 后端 ego 图）

**用户反馈**："跳数逻辑不对"——1 跳模式下只能看到很少的节点，很多邻居没显示出来。

**问题根因**：
原跳数实现是**前端 BFS 过滤**——在已加载的 top-800 概览节点中做广度优先搜索，找出 N 跳邻居。但概览模式只加载了按 degree 排序的 top-N 节点，大量低度数的叶子节点不在概览中，导致：
- 1 跳可能只有几个节点（叶子节点没加载进来）
- 2 跳/3 跳和 1 跳差别不大（因为数据源就是不全的）

**修复方案**：
选中节点后**调用后端 ego 图 API**（`mode=ego&center=xxx&depth=N`），从 Neo4j 直接加载完整的 N 跳邻域子图，而不是在前端已有数据里过滤。

#### 改动细节

| 项 | 原逻辑 | 改后 |
|----|--------|------|
| **数据来源** | 前端已有 overview 数据（top-N 节点） | 后端 ego 图 API（完整 N 跳子图） |
| **触发方式** | `setActiveCenter` → 前端 BFS → 控制 `visible` | `setActiveCenter` → `loadEgoGraph()` → 重新 initGraph + render |
| **"显示全部"按钮** | `clearAllFilters()` → 显示所有节点 | 检测当前是否 ego 模式 → 是则 `loadFullGraph()` 回到概览 |
| **搜索选中限制** | 节点不在当前视图中 → 报错 | 移除限制，直接加载 ego 图 |
| **切换跳数** | 前端重新 BFS | 重新请求后端 ego 图（不同 depth） |

#### 关键函数

- `loadEgoGraph(centerName, depth)` — 新增：调用后端 ego 接口，加载并渲染子图
- `setActiveCenter(name)` — 修改：改为调用 `loadEgoGraph`
- `onHopChange(val)` — 修改：切换跳数时重新请求后端
- `clearFilter()` — 修改：ego 模式下回到全量 overview

#### API 依赖

后端 `GetEgoGraph` 接口（`internal/application/repository/retriever/neo4j/repository.go`），使用 `apoc.path.subgraphAll` 或可变长度路径查询。前端 limit 设为 500，防止节点过多。

### Phase 14：ego 模式视觉样式修复（所有节点/边清晰可见）

**用户反馈**："目前多跳还是没显示出非直连节点和边"、"只有我将鼠标放在节点上，隐藏边和节点才显示完全"

**问题根因**：
ego 模式（N 跳子图）加载完数据后，沿用了概览模式的 `applyHighlight` 高亮逻辑——中心节点的直接邻居清晰显示，2 跳/3 跳的非邻居节点被淡化到 opacity=0.2，边也被淡化到 0.08。

在概览模式下，淡化非邻居节点是合理的（突出重点）。但在 ego 模式下，**所有节点都是 N 跳内的有效节点**，不应该被淡化——用户选择 N 跳就是想看完整的 N 跳路径。

**修复方案**：
引入 `isEgoMode` 状态变量，区分两种模式的视觉样式：

| 样式 | 概览模式 (overview) | ego 模式 (N跳子图) |
|------|---------------------|-------------------|
| 节点默认 opacity | 1 | 1 |
| 非邻居节点 opacity（hover 时） | 0.2（淡化） | 1（保持清晰） |
| 边默认 stroke-opacity | 0.4（淡色） | 0.85（清晰） |
| 非邻居边 opacity（hover 时） | 0.08（极淡） | 0.85（保持清晰） |
| hover 效果 | 邻居高亮 + 其他淡化 | 邻居高亮 + 其他不变（只是对比不强） |

#### 改动点

- **`isEgoMode`**：新增 ref 状态变量，`loadFullGraph` 置 false，`loadEgoGraph` 置 true
- **`clearHighlight()`**：根据 `isEgoMode` 决定边的默认透明度（0.4 vs 0.85）
- **`applyHighlight()`**：根据 `isEgoMode` 决定非邻居节点/边是否淡化
- **`loadEgoGraph()`**：简化逻辑，不再手动遍历设置样式，统一由 `clearHighlight`/`applyHighlight` 管控

#### 交互行为说明

ego 模式下：
- 默认：所有节点和边清晰可见，中心节点用激活环标记
- hover 某节点：该节点的直连边高亮（变色加粗），直连邻居标签显示
- hover 离开：恢复全部清晰状态
- click 某节点：高亮直连关系 + 打开详情抽屉
- 点击"显示全部"：回到概览模式（边变回淡色）

---

### 关于"全是 Alias 关系"的根因分析

**现象**：用户反馈图谱中所有关系都是 `Alias` 类型。

**根因**：系统默认的图谱提取配置中，**关系类型（tags）只有 2 种**：`["Author", "Alias"]`（见 `config/config.yaml` 的 `extract_graph.tags`）。

完整提取链路：
1. LLM 被 prompt 限制**只能从给定的关系类型列表中选择**（不能自行发明）
2. 默认配置只有 `Author`（创作关系）和 `Alias`（别名关系）两种
3. 法规/保险领域内容中几乎没有"作者"关系 → Author 用不上
4. LLM 只能把所有识别到的关系都塞进 `Alias` → 结果全是 Alias

**解决方式**：
- 在知识库设置 → 知识图谱 → 关系类型中，添加适合领域的关系类型（如 `subclass_of`、`part_of`、`regulates`、`contains`、`references`、`related_to` 等）
- 重新加工文档
- 系统默认配置也可以考虑优化（后续）

**提取 prompt 位置**：
- 系统默认模板：`config/prompt_templates/graph_extraction.yaml`（`default_extract_relationships`）
- 系统默认 tags + 示例：`config/config.yaml` → `conversation.extract_graph`
- 知识库级配置：前端设置页 → 知识图谱 → 关系类型/示例文本/实体列表
- 实际组装：`internal/application/service/chat_pipeline/extract_entity.go` → `QAPromptGenerator.System()`

---

## 六、关键文件清单

### 后端

| 文件 | 改动类型 | 说明 |
|------|----------|------|
| `internal/types/extract_graph.go` | 修改 | 新增 Neo4j 可视化数据类型 |
| `internal/types/interfaces/retriever_graph.go` | 修改 | 扩展 Repository 接口 |
| `internal/application/repository/retriever/neo4j/repository.go` | 修改 | 实现 6 个查询方法 |
| `internal/application/service/graph_neo4j.go` | 新建 | Neo4j 图谱服务层 |
| `internal/handler/graph_neo4j.go` | 新建 | Neo4j 图谱 API Handler |
| `internal/router/routes_knowledge.go` | 修改 | 注册路由 |
| `internal/container/container.go` | 修改 | DI 注册 |

### 前端

| 文件 | 改动类型 | 说明 |
|------|----------|------|
| `frontend/src/api/wiki/index.ts` | 修改 | 新增 Neo4j 图谱 API |
| `frontend/src/views/knowledge/wiki/Neo4jGraphViewer.vue` | 新建 | 主组件（约 1500 行） |
| `frontend/src/views/knowledge/KnowledgeBase.vue` | 修改 | 拆分 tab，接入新组件 |

---

## 七、风险与注意事项

1. **性能**：Neo4j 节点可能很多，必须通过 limit 严格控制返回量。默认 200，最大 1000。前端 SVG 渲染 1000+ 节点会卡顿。

2. **命名空间隔离**：所有查询必须用知识库标签（`ENTITY{kb_id}`）做过滤，防止跨 KB 数据泄露。

3. **特殊字符**：实体名可能含中文、空格、特殊符号。URL 中用 `encodeURIComponent`；Cypher 中作为参数传递（不做字符串拼接）。

4. **代码复用**：WikiBrowser 力导向图约 600 行，直接复制到新组件。短期快速交付，长期可考虑抽 composable。

5. **关系类型数量**：如果后续加回关系类型筛选，关系类型过多（几十种）时筛选下拉需要做搜索或分组。（当前无关系类型筛选）

6. **Neo4j 未启用兼容**：
   - 后端：所有接口检查 driver 是否可用，不可用返回明确错误
   - 前端：tab 直接隐藏

7. **APOC 依赖**：`apoc.path.subgraphAll` 需要 APOC 插件。WeKnora 的 Neo4j 部署已启用 APOC，可放心使用。但也要准备 fallback 方案（可变长度路径查询）。

---

## 八、验证方式

### 后端验证

```bash
# 概览
curl "http://localhost:8080/api/v1/knowledgebase/{kb_id}/graph/neo4j?mode=overview&limit=50"

# Ego 图
curl "http://localhost:8080/api/v1/knowledgebase/{kb_id}/graph/neo4j?mode=ego&center=保险公司&depth=2"

# 搜索
curl "http://localhost:8080/api/v1/knowledgebase/{kb_id}/graph/neo4j/search?q=保险"

# 统计
curl "http://localhost:8080/api/v1/knowledgebase/{kb_id}/graph/neo4j/stats"

# 关系类型
curl "http://localhost:8080/api/v1/knowledgebase/{kb_id}/graph/neo4j/relation-types"

# 节点详情
curl "http://localhost:8080/api/v1/knowledgebase/{kb_id}/graph/neo4j/node/保险公司"
```

### 前端验证

前置条件：知识库已启用知识图谱提取，且已有文档完成提取。

1. 进入知识库 → 确认看到"Neo4j 图谱"tab
2. 点击 tab → 概览模式正常显示节点和关系
3. 搜索框输入关键词 → 有匹配结果
4. 点击搜索结果 → 视图定位到该节点 + 打开详情抽屉
5. 选中搜索结果 / 点击节点 → 进入 ego 模式（N 跳子图）
6. ~~关系类型筛选 → 选择/取消后图重新加载~~（Phase 7 已移除，当前无此功能）
7. 画布可缩放、可平移
8. 节点可拖拽
9. 节点详情抽屉显示属性、关系类型分布、Top 邻居
10. 空知识库 → 显示友好空状态
11. 搜索框输入关键词 → 实时模糊匹配高亮
12. ego 模式下可切换 1/2/3 跳 → 重新加载子图
13. 点击"显示全部" → 回到概览模式

### 测试

- 后端：Repository 层对新增方法做单元测试（需要 Neo4j 实例，可考虑用 testcontainers 或跳过）
- 后端：Handler 层参数验证测试
- 前端：手动验证为主

---

## 九、后续优化方向（已验证可行，待实现）

### 边方向可视化优化

**现状**：
- 有向边用箭头标记方向，但所有边统一灰色/同色系，方向感知不够直观
- 双向边（A→B 和 B→A 同时存在）两端都有箭头，方向容易混淆
- 大量边交织时，很难快速判断每条边的流向

**优化方向**（参考 Wiki 图谱 / KnowPla 的做法）：
1. **按关系类型着色**：不同 `type` 的边用不同颜色（如 `subclass_of` 紫色、`part_of` 蓝色、`regulates` 红色等），颜色同时也能暗示方向语义
2. **从中心向外发散的视觉强化**：ego 模式下，以中心节点为基准，"向外"的边（中心→邻居）和"向内"的边（邻居→中心）用不同粗细/颜色区分
3. **双向边的区分**：双向关系的两条边可以用平行偏移（path 偏移）或虚线/实线区分，避免两条边重叠成一条
4. **边上的方向标签**：关系类型文字放在边的起点侧（靠近 source 节点），从位置上就能判断方向

**优先级**：中。当前边方向是准确的，只是感知效率低。等核心交互稳定后再优化。

### 其他待优化项

- **布局算法升级**：当前是手写力导向，节点很多时性能一般，可以考虑引入 d3-force 或 ngraph
- **子图聚类**：按关系类型或社区发现算法对节点聚类，不同簇用不同背景色区分
- **渐进式展开**：点击节点展开它的 1 跳邻居（当前是全量加载），类似 Wiki 图谱的交互模式
- **关系类型筛选**：顶部加关系类型多选筛选，只显示指定类型的边
- **时间轴 / 版本对比**：不同时间点的图谱结构对比
