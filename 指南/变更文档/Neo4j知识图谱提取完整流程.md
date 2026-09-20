# Neo4j 知识图谱提取完整流程

## 一、概述

WeKnora 的知识图谱功能基于 **Neo4j** 图数据库，用于从文档中自动提取实体与关系，构建知识图谱，并用于检索增强（Graph RAG）。

> **注意**：知识图谱（实体-关系图）与 Wiki 页面链接图是两回事：
> - **知识图谱**：基于 LLM 从文本中提取的实体-关系结构，存储在 Neo4j 中，用于 Graph RAG 问答增强
> - **Wiki 页面链接图**：Wiki 页面之间的相互引用关系，存储在关系型数据库中，用于 Wiki 侧边栏图谱展示

---

## 二、启用条件

知识图谱功能是**可选**的，需要同时满足以下条件才能启用：

### 2.1 环境变量（全局开关）

在 `.env` 或 `docker-compose.yml` 中设置：

```bash
NEO4J_ENABLE=true
NEO4J_URI=bolt://neo4j:7687
NEO4J_USERNAME=neo4j
NEO4J_PASSWORD=password
```

- `NEO4J_ENABLE` 为空或 false 时，整个图谱功能完全关闭（不初始化 Neo4j 客户端、不入队提取任务）
- Neo4j 容器使用 APOC 插件（`apoc.merge.node` / `apoc.merge.relationship` 用于幂等写入）

### 2.2 知识库级开关

每个知识库可以独立配置是否启用实体关系提取：
- 知识库设置 → 知识图谱 → 「启用实体关系提取」
- 对应字段：`ExtractConfig.Enabled`（同步到 `IndexingStrategy.GraphEnabled`）

### 2.3 前端可见性判断

前端通过系统信息 API 获取 `graph_database_engine`：
- 值为 `"Neo4j"` 时显示知识图谱配置界面
- 值为 `"Not Enabled"` 时隐藏相关配置

---

## 三、配置项说明

知识库设置 → 知识图谱配置页面（`GraphSettings.vue`）包含以下配置项：

| 配置项 | 字段 | 说明 |
|--------|------|------|
| 启用实体关系提取 | `enabled` | 总开关，控制是否对该知识库的文档进行图谱提取 |
| 额外提取要求 | `custom_instructions` | 领域特定的提取指导语，会注入到提取 prompt 中 |
| 关系类型 | `tags` | 允许提取的关系类型标签列表（如 Author、Alias、工作于 等） |
| 示例文本 | `text` | 用于演示/测试提取效果的输入文本 |
| 管理实体 | `nodes` | 手动添加/删除实体节点（含属性） |
| 管理关系 | `relations` | 手动添加/删除实体间关系（源-关系类型-目标） |
| 提取操作 | - | 「开始提取」对示例文本执行提取演示（仅 admin 可见） |

辅助功能按钮：
- **生成随机标签**：从预定义列表随机生成一组关系标签
- **生成随机文本**：根据当前标签生成一段相关的示例文本
- **默认示例** / **清除示例**：快速载入或清空示例数据

---

## 四、完整提取流程

### 4.1 流程图

```
文档上传
   │
   ▼
文档分块 (Chunking)
   │
   ▼
asynq 任务入队 (graph 队列)
   │  └─ 仅当 NEO4J_ENABLE=true 且 KB 启用图谱提取
   ▼
ChunkExtractService (异步消费)
   │
   ├─ 获取 chunk 内容 + 知识库配置
   │
   ├─ 调用 chatpipeline.Extractor → LLM
   │     │
   │     ├─ Step 1: 实体提取（节点 + 属性）
   │     └─ Step 2: 关系提取（源-关系类型-目标）
   │
   └─ graphEngine.AddGraph() → 写入 Neo4j
         │
         ├─ 节点: apoc.merge.node（幂等创建）
         └─ 关系: apoc.merge.relationship（幂等创建）
```

### 4.2 详细步骤说明

#### 步骤 1：文档上传与分块

- 用户上传文档到知识库
- 文档被切分为多个 chunk（受 `chunk_size`、`chunk_overlap` 配置影响）
- 每个 chunk 生成后，检查是否需要入队图谱提取任务

#### 步骤 2：任务入队

**代码位置**：`internal/application/service/extract.go` → `NewChunkExtractTask`

```go
// 只有 NEO4J_ENABLE=true 时才入队
// 队列名: graph
// 最大重试: 3 次
// 超时: 30 分钟
```

入队前检查：
1. 全局 `NEO4J_ENABLE` 是否为 true
2. 知识库 `ExtractConfig.Enabled` 是否为 true
3. 知识库配置了关系类型标签

#### 步骤 3：LLM 提取

**代码位置**：`internal/application/service/chat_pipeline/extract_entity.go` → `Extractor`

提取分为两步：

**Step 1 — 实体提取**：
- 调用 LLM 从 chunk 文本中提取实体
- 每个实体包含：`name`（实体名）、`attributes`（属性列表）
- 支持的实体类型（graph_extraction.yaml 中定义）：
  Person, Organization, Location, Product, Event, Date, Work, Concept, Resource, Category, Operation

**Step 2 — 关系提取**：
- 基于已提取的实体，识别实体之间的关系
- 每个关系包含：`node1`（源实体）、`node2`（目标实体）、`type`（关系类型，必须在 tags 列表内）
- 关系强度（strength）分级：
  - 10：直接创建/从属关系（作者-作品、母公司-子公司）
  - 9：同一实体的不同表现（别名、曾用名）
  - 8：紧密关联、相互影响（亲密伙伴、家庭成员）
  - 7：明确但间接关系（作品中角色、组织成员）
  - 6：间接关联（同事关系、同类产品）
  - 5：相关但松散（同领域不同概念）

**容错处理**：
- 解析 LLM 输出时支持 JSON / YAML 格式，带 fence 解析 + 多种 fallback
- 自动去重节点、补充缺失节点
- `RemoveUnknownRelation` 过滤不在 tags 列表中的关系类型

#### 步骤 4：写入 Neo4j

**代码位置**：`internal/application/repository/retriever/neo4j/repository.go` → `AddGraph`

**节点写入**：
- 使用 `apoc.merge.node` 幂等创建（已存在则不重复创建）
- 节点标签格式：`ENTITY{KnowledgeBaseID}:ENTITY{KnowledgeID}`
  - 连字符 `kb-xxx` 替换为下划线 `kb_xxx`（Neo4j 标签限制）
- 节点属性：
  - `name`：实体名称
  - `kg`：knowledge_id（用于按文档删除）
  - `attributes`：属性列表
  - `chunks`：关联的 chunk ID 列表

**关系写入**：
- 使用 `apoc.merge.relationship` 幂等创建
- 关系类型即用户配置的关系标签（如 `Author`、`Alias`）
- 使用 `UNWIND` 批量写入提升性能

---

## 五、检索使用流程

### 5.1 Graph RAG 问答增强

知识图谱的主要用途是增强问答检索效果（Graph RAG）：

```
用户提问
   │
   ▼
PluginExtractEntity (QUERY_UNDERSTAND 阶段)
   │  从问题中提取关键实体
   ▼
PluginSearchEntity (ENTITY_SEARCH 阶段)
   │  在 Neo4j 中搜索相关节点和关系
   ▼
返回关联的 chunk ID → 用于检索增强
```

### 5.2 Agent 工具查询

**代码位置**：`internal/agent/tools/query_knowledge_graph.go`

Agent 可以使用 `QueryKnowledgeGraphTool` 工具：
- 输入：`knowledge_base_ids`（知识库ID数组） + `query`（查询内容）
- 功能：并发查询多个知识库的图谱，进行混合搜索（HybridSearch）
- 返回：结构化图谱数据 + 搜索结果

### 5.3 内存图构建器（Chunk 级关联）

**代码位置**：`internal/application/service/graph.go` → `graphBuilder`

除了 Neo4j 持久化图谱，还有一个内存中的图构建器，用于 chunk 间的关联计算：
- `BuildGraph`：从 chunk 列表构建内存图
- `GetRelationChunks`：获取直接关联的 chunk（TopK）
- `GetIndirectRelationChunks`：获取二度关联的 chunk（带权重衰减）

权重算法：
- PMI（点互信息）占 60%，强度占 40%
- 间接关系权重 = 直接关系1权重 × 直接关系2权重 × 0.5（衰减系数）

---

## 六、提示词位置与内容

### 6.1 主提示词配置（config.yaml）

**文件**：`config/config.yaml` → `extract.extract_graph`

这是知识图谱提取的主提示词，采用「描述 + 标签 + 示例」的结构化格式：

```yaml
extract:
  extract_graph:
    description: |
      分两步提取：
      Step 1: 实体提取与属性丰富
      Step 2: 关系提取与验证
    tags: ["Author", "Alias"]    # 默认关系类型
    examples:                     # 示例（罗密欧与朱丽叶）
      - text: "..."
        node: [...]
        relation: [...]
  extract_entity:                 # 问答时的实体提取
    description: |
      从用户问题中提取关键实体...
  fabri_text:                     # 生成示例文本
    with_tag: "请生成一段与 %s 相关的文本..."
    with_no_tag: "请随机生成一段文本..."
```

### 6.2 图提取提示词模板库

**文件**：`config/prompt_templates/graph_extraction.yaml`

包含两个默认模板：

| 模板 ID | 名称 | 作用 |
|---------|------|------|
| `default_extract_entities` | Entity Extraction | 从文本中提取实体（11 种实体类型） |
| `default_extract_relationships` | Relationship Extraction | 从实体对中提取关系（含强度分级） |

模板使用 `{{language}}` 占位符控制输出语言。

### 6.3 提示词渲染流程

**代码位置**：`internal/application/service/chat_pipeline/extract_entity.go` → `QAPromptGenerator`

```
PromptTemplateStructured { description, tags, examples }
        │
        ▼ 渲染为
System Prompt（LLM 输入）
        │
        ▼
LLM 输出 JSON
        │
        ▼
解析为 GraphData { nodes, relations }
```

---

## 七、关键数据结构

### 7.1 图提取数据结构

**文件**：`internal/types/extract_graph.go`

```go
type GraphNode struct {
    Name       string   // 实体名称
    Chunks     []string // 关联的 chunk ID
    Attributes []string // 属性列表
}

type GraphRelation struct {
    Node1 string // 源实体名
    Node2 string // 目标实体名
    Type  string // 关系类型
}

type GraphData struct {
    Text     string           // 源文本
    Node     []*GraphNode     // 节点列表
    Relation []*GraphRelation // 关系列表
}
```

### 7.2 知识库配置结构

**文件**：`internal/types/knowledgebase.go` → `ExtractConfig`

```go
type ExtractConfig struct {
    Enabled            bool             // 是否启用
    Text               string           // 示例文本
    Tags               []string         // 关系类型标签
    Nodes              []*GraphNode     // 管理的实体节点
    Relations          []*GraphRelation // 管理的关系
    CustomInstructions string           // 自定义提取指令
}
```

### 7.3 内存图数据结构

**文件**：`internal/types/graph.go`

```go
type Entity struct {
    ID, Title, Type, Description string
    ChunkIDs  []string
    Frequency int   // 出现频次
    Degree    int   // 连接度数
}

type Relationship struct {
    ID, Source, Target, Description string
    ChunkIDs       []string
    CombinedDegree int
    Weight         float64
    Strength       int // 1-10 强度
}
```

---

## 八、删除与清理

**代码位置**：`internal/application/repository/retriever/neo4j/repository.go` → `DelGraph`

删除文档时，同步删除 Neo4j 中的关联图谱数据：
- 使用 `apoc.periodic.iterate` 分批删除（每批 1000 条，并行）
- 先删关系，再删节点
- 按 `kg` 属性（knowledge_id）过滤，确保只删该文档的数据

---

## 九、关键文件索引

| 类别 | 文件路径 |
|------|---------|
| 前端配置组件 | `frontend/src/views/knowledge/settings/GraphSettings.vue` |
| 前端 API | `frontend/src/api/initialization/index.ts` |
| 后端 Handler | `internal/handler/initialization.go` |
| 系统信息(图数据库状态) | `internal/handler/system.go` |
| Chunk 提取服务(Neo4j 写入) | `internal/application/service/extract.go` |
| 内存图构建服务 | `internal/application/service/graph.go` |
| 实体提取 pipeline | `internal/application/service/chat_pipeline/extract_entity.go` |
| 实体搜索 pipeline | `internal/application/service/chat_pipeline/search_entity.go` |
| Agent 图谱查询工具 | `internal/agent/tools/query_knowledge_graph.go` |
| Neo4j Repository | `internal/application/repository/retriever/neo4j/repository.go` |
| Neo4j 初始化 | `internal/container/container.go` |
| 图 Repository 接口 | `internal/types/interfaces/retriever_graph.go` |
| 图提取数据类型 | `internal/types/extract_graph.go` |
| 内存图数据类型 | `internal/types/graph.go` |
| ExtractConfig 定义 | `internal/types/knowledgebase.go` |
| 主提示词配置 | `config/config.yaml` |
| 图提取提示词模板 | `config/prompt_templates/graph_extraction.yaml` |
| Docker Compose 配置 | `docker-compose.yml` / `docker-compose.dev.yml` |
