# Wiki 检索完整架构流程

> 本文档详细描述 WeKnora 项目中 Wiki 知识库的检索流程，涵盖 Wiki 页面搜索、知识库混合检索、Agent 工具检索、聊天管道检索等多条路径，具体到每个函数的调用关系。

---

## 一句话理解 Wiki 混合检索

**像 Google 搜索一样，既看语义又看关键词，两路结果融合后给你最相关的内容。**

三步搞定：

1. **向量检索（语义匹配）**：把你输入的问题通过 AI 模型变成一串数字（向量），去知识库中找"意思最接近"的段落。比如你搜"苹果手机"，它能找到写着"iPhone"的段落。
2. **关键词检索（字面匹配）**：传统搜索引擎做法，看你的关键词在不在文本里，用 BM25 算法打分，关键词出现在标题里分数更高。
3. **RRF 融合**：两路结果各自排好名次，用倒数排名公式加权合并（向量权重 0.7，关键词权重 0.3——语义更重要）。

**类比**：就像你找书，先问图书管理员"有没有讲金融监管的书"（语义），再自己看书架上书名有没有"银保监会"这几个字（关键词），两边结果合起来就是你要找的。

> **Wiki 页面搜索 vs 知识库混合检索的区别**：前者是 PostgreSQL 正则匹配 Wiki 页面的标题/摘要/正文，用来快速定位页面；后者是向量+关键词混合检索文档 chunk，用于问答/RAG 场景的语义检索。

---

## 一、检索体系总览

WeKnora 的检索体系是一个 **多层级、多引擎融合** 的系统，包含以下检索路径：

```
                        ┌─────────────────────┐
                        │     用户查询请求      │
                        └──────────┬──────────┘
                                   │
            ┌──────────────────────┼──────────────────────┐
            │                      │                      │
            ▼                      ▼                      ▼
   ┌────────────────┐    ┌──────────────────┐   ┌─────────────────┐
   │  Wiki 页面搜索  │    │  知识库混合搜索    │   │  Agent 对话检索  │
   │  (正则全文)     │    │  (向量+关键词)     │   │  (工具调用)      │
   └────────┬───────┘    └─────────┬────────┘   └────────┬────────┘
            │                      │                     │
            │              ┌───────┴───────┐             │
            │              ▼               ▼             │
            │       ┌───────────┐  ┌────────────┐       │
            │       │ 向量检索   │  │ 关键词检索  │       │
            │       └─────┬─────┘  └──────┬─────┘       │
            │             │               │             │
            │             └───────┬───────┘             │
            │                     ▼                     │
            │               RRF 融合 + 重排             │
            │                     │                     │
            ▼                     ▼                     ▼
            └─────────────────────┼─────────────────────┘
                                  ▼
                        ┌─────────────────────┐
                        │    检索结果返回       │
                        └─────────────────────┘
```

---

## 二、路径一：Wiki 页面搜索（正则全文）

### 2.1 HTTP 入口

**文件**：`internal/handler/wiki_page.go`

**函数**：`SearchPages()`

| 项 | 内容 |
|---|---|
| 签名 | `func (h *WikiPageHandler) SearchPages(c *gin.Context)` |
| 路由 | `GET /knowledgebase/:kb_id/wiki/search` |
| 参数 | `q`（查询词）、`limit`（返回数量） |

调用流程：

```
WikiPageHandler.SearchPages(c)
  ├─ 从 URL 解析 kb_id
  ├─ h.validateWikiKB(c, kbID)        — 校验 KB 存在且 wiki 已启用
  ├─ 从 query 中获取 q 和 limit 参数
  ├─ h.wikiService.SearchPages(ctx, kbID, q, limit)
  └─ 返回 JSON 格式的 WikiPage 列表
```

### 2.2 Service 层

**文件**：`internal/application/service/wiki_page.go`

**函数**：`SearchPages()`

| 项 | 内容 |
|---|---|
| 签名 | `func (s *wikiPageService) SearchPages(ctx context.Context, kbID string, query string, limit int) ([]*types.WikiPage, error)` |
| 功能 | 调用 Repository 层执行搜索 |

```
wikiPageService.SearchPages(ctx, kbID, query, limit)
  └─ s.repo.Search(ctx, kbID, query, limit)
```

### 2.3 Repository 层

**文件**：`internal/application/repository/wiki_page.go`

**函数**：`Search()`（第1282行）

**实现方式**：PostgreSQL POSIX 正则匹配（`~*` 操作符，大小写不敏感）

**评分机制**：按匹配位置加权排序

| 匹配位置 | 权重 |
|---|---|
| title 匹配 | 4 分 |
| slug 匹配 | 3 分 |
| summary 匹配 | 2 分 |
| content 匹配 | 1 分 |

```
wikiPageRepository.Search(ctx, kbID, query, limit)
  ├─ 构建 SQL：SELECT *, match_rank FROM (
  │     SELECT *,
  │       CASE WHEN title ~* query THEN 4 ELSE 0 END
  │       + CASE WHEN slug ~* query THEN 3 ELSE 0 END
  │       + CASE WHEN summary ~* query THEN 2 ELSE 0 END
  │       + CASE WHEN content ~* query THEN 1 ELSE 0 END AS match_rank
  │     FROM wiki_pages
  │     WHERE knowledge_base_id = kbID
  │       AND status = 'published'
  │       AND (title ~* query OR slug ~* query OR summary ~* query OR content ~* query)
  │   ) sub
  │   ORDER BY match_rank DESC
  │   LIMIT limit
  └─ GORM Raw → Scan → 返回 []*WikiPage
```

### 2.4 页面读取（单页详情）

**Handler**：`WikiPageHandler.GetPage(c)`

```
WikiPageHandler.GetPage(c)
  └─ h.wikiService.GetPageBySlug(ctx, kbID, slug)
       └─ s.repo.GetBySlug(ctx, kbID, slug)
```

**函数签名**：

| 层级 | 签名 |
|---|---|
| Service | `func (s *wikiPageService) GetPageBySlug(ctx context.Context, kbID string, slug string) (*types.WikiPage, error)` |
| Repository | `func (r *wikiPageRepository) GetBySlug(ctx context.Context, kbID string, slug string) (*types.WikiPage, error)` |

---

## 三、路径二：知识库混合搜索（向量 + 关键词）

这是最核心的检索路径，用于从知识库中检索 chunk 级别的内容。Wiki 页面本身也会被分块并加入向量索引，因此会出现在混合搜索结果中。

### 3.1 HTTP 入口

**文件**：`internal/handler/knowledgebase.go`

**函数**：`HybridSearch()`

| 项 | 内容 |
|---|---|
| 路由 | `POST/GET /knowledge-bases/:id/hybrid-search` |
| 方法 | `func (h *KnowledgeBaseHandler) HybridSearch(c *gin.Context)` |

### 3.2 Service 主流程

**文件**：`internal/application/service/knowledgebase_search.go`

**函数**：`HybridSearch()`（第125行）

| 项 | 内容 |
|---|---|
| 签名 | `func (s *knowledgeBaseService) HybridSearch(ctx context.Context, id string, params types.SearchParams) ([]*types.SearchResult, error)` |

完整调用链：

```
knowledgeBaseService.HybridSearch(ctx, id, params)
  │
  ├─ 1. normalizedMatchCount(params.MatchCount)
  │     — 标准化返回数量（默认 10，上限 50）
  │
  ├─ 2. 确定 searchKBIDs
  │     — 单个 KB 或跨 KB 搜索
  │
  ├─ 3. s.repo.GetKnowledgeBaseByIDs(ctx, searchKBIDs)
  │     — 加载所有 KB 信息
  │
  ├─ 4. s.authorizeKBAccess(ctx, kbs)
  │     — 校验租户权限
  │
  ├─ 5. s.validateSameEmbeddingModel(ctx, kbs)
  │     — 确保所有 KB 使用相同 embedding 模型
  │
  ├─ 6. pickPrimary(kbs, id)
  │     — 选取主 KB（用于模型解析等）
  │
  ├─ 7. s.GetQueryEmbedding(ctx, kb.ID, params.QueryText)
  │     — 如需向量检索，计算查询向量
  │     └─ s.modelService.GetEmbeddingModel(kb)
  │          └─ embeddingModel.Embed(queryText)
  │
  ├─ 8. s.resolveStoreGroups(ctx, kb, kbs, params, matchCount)
  │     — 按向量存储分组 KB
  │     — 为每组解析对应的 retriever engine
  │
  ├─ 9. langfuse.GetManager().StartSpan(...)
  │     — 启动观测 span
  │
  ├─ 10. s.retrieveFromStores(retrieveCtx, groups, EngineAwareNormalizer{})
  │      — 并发检索（多存储组）
  │
  ├─ 11. classifyRetrievalResults(ctx, retrieveResults)
  │      — 结果分类：vectorResults + keywordResults
  │
  ├─ 12. fuseOrDeduplicate(ctx, vectorResults, keywordResults, retrievalCfg)
  │      — RRF 融合或去重
  │
  ├─ 13. s.applyFAQPostProcessing(ctx, kb, deduplicatedChunks, ...)
  │      — FAQ 类型 KB 的后处理（精确匹配优先等）
  │
  ├─ 14. 截断到 params.MatchCount
  │
  └─ 15. s.processSearchResults(ctx, deduplicatedChunks, params.SkipContextEnrichment)
         — 上下文增强（父 chunk / 邻居 chunk 扩展）
```

### 3.3 检索参数数据结构

**文件**：`internal/types/search.go` — `SearchParams`

| 字段 | 类型 | 说明 |
|---|---|---|
| `QueryText` | `string` | 查询文本 |
| `QueryEmbedding` | `[]float32` | 查询向量（可选，可预计算传入） |
| `VectorThreshold` | `float64` | 向量相似度阈值 |
| `KeywordThreshold` | `float64` | 关键词匹配阈值 |
| `MatchCount` | `int` | 返回结果数量 |
| `DisableKeywordsMatch` | `bool` | 禁用关键词检索 |
| `DisableVectorMatch` | `bool` | 禁用向量检索 |
| `KnowledgeIDs` | `[]string` | 限定文档 ID |
| `TagIDs` | `[]uint64` | 限定标签 |
| `Rerank` | `bool` | 是否重排 |
| `SkipContextEnrichment` | `bool` | 跳过上下文增强 |

**文件**：`internal/types/retrieval_config.go` — `RetrievalConfig`

| 字段 | 默认值 | 说明 |
|---|---|---|
| `EmbeddingTopK` | 50 | 向量搜索返回 chunk 数 |
| `VectorThreshold` | 0.15 | 向量相似度阈值 |
| `KeywordThreshold` | 0.3 | 关键词匹配阈值 |
| `RerankTopK` | 10 | 重排后结果数 |
| `RerankThreshold` | 0.2 | 重排分数阈值 |
| `RerankModelID` | — | 重排模型 ID |
| `RRFK` | 60 | RRF 算法 k 值 |
| `RRFVectorWeight` | 0.7 | RRF 向量权重 |
| `RRFKeywordWeight` | 0.3 | RRF 关键词权重 |

### 3.4 多存储扇出检索

**文件**：`internal/application/service/knowledgebase_search_fanout.go`

**函数**：`retrieveFromStores()`

```
retrieveFromStores(ctx, groups, normalizer)
  │
  ├─ 单组快速路径：len(groups) == 1 → 直接调用，无额外开销
  │
  └─ 多组并发：errgroup 并发（默认 4 组并发上限）
      ├─ 每组调用 group.Engine.Retrieve(ctx, params)
      ├─ 多引擎时做分数归一化（normalizer）
      └─ 合并所有组的结果
```

**存储分组逻辑**（`resolveStoreGroups`）：
- 按 `(vectorStoreID, ownerTenantID)` 分组
- 每组对应一个 retriever engine 实例
- 支持的引擎：OpenSearch / Elasticsearch / Milvus / Qdrant / PostgreSQL / SQLite / Weaviate / Doris / Tencent VectorDB

### 3.5 RRF 融合

**文件**：`internal/application/service/knowledgebase_search_fusion.go`

**函数**：`fuseOrDeduplicate()`

```
fuseOrDeduplicate(vectorResults, keywordResults, retrievalCfg)
  │
  ├─ 纯向量：deduplicateByScore() — 按分数去重
  ├─ 纯关键词：deduplicateByScore() — 按分数去重
  └─ 混合：fuseWithRRF() — RRF 算法融合
```

**fuseWithRRF 算法**：

```
RRF_score = vectorWeight / (k + vectorRank)
          + keywordWeight / (k + keywordRank)

默认: k=60, vectorWeight=0.7, keywordWeight=0.3
```

RRF（Reciprocal Rank Fusion）是一种无参数融合算法，通过倒数排名加权融合多个检索结果列表。

### 3.6 结果处理与上下文增强

**文件**：`internal/application/service/knowledgebase_search_results.go`

**函数**：`processSearchResults()`

```
processSearchResults(ctx, results, skipEnrichment)
  │
  ├─ 父 chunk 扩展：
  │   若 chunk 有 parent_chunk_id，则附加父 chunk 内容
  │
  └─ 邻居 chunk 扩展：
      若配置了上下文窗口，附加前后相邻的 chunk
```

---

## 四、检索引擎实现（OpenSearch 为例）

OpenSearch 是默认的主检索引擎，支持向量检索和关键词检索。

**文件**：`internal/application/repository/retriever/opensearch/retrieve.go`

### 4.1 Retrieve 主函数

| 项 | 内容 |
|---|---|
| 签名 | `func (r *Repository) Retrieve(ctx context.Context, params types.RetrieveParams) ([]*types.RetrieveResult, error)` |
| 行号 | 第30行 |

```
Repository.Retrieve(ctx, params)
  │
  ├─ 1. resolveDim(params)
  │     — 解析向量维度
  │
  ├─ 2. r.ensureReady(ctx, dim)
  │     — 确保索引就绪（不存在则创建）
  │
  ├─ 3. 向量检索分支（params.Type 包含 VectorRetrieverType）：
  │     r.vectorRetrieve(ctx, params, dim)
  │       ├─ buildKNNQuery(embedding, topK, threshold, filters)
  │       ├─ r.search(ctx, r.indexAlias(dim), body)
  │       └─ wrapResults(hits, VectorRetrieverType, MatchTypeEmbedding)
  │
  └─ 4. 关键词检索分支（params.Type 包含 KeywordsRetrieverType）：
        r.keywordsRetrieve(ctx, params, indexPattern)
          ├─ buildKeywordQuery(query, topK, threshold, filters)
          ├─ r.search(ctx, indexPattern, body)
          └─ wrapResults(hits, KeywordsRetrieverType, MatchTypeKeywords)
```

### 4.2 向量检索

**原理**：基于 OpenSearch k-NN 插件，使用 cosine similarity

**索引命名**：`<baseIndex>_<dim>`（按维度分区，如 `knowledge_chunks_1536`）

```
buildKNNQuery(embedding, topK, threshold, filters)
  → OpenSearch k-NN 查询 DSL
  → size: topK
  → min_score: threshold
  → knn: { field: "embedding", vector: [...], k: topK }
  → filter: 租户/KB/文档/标签等过滤条件
```

### 4.3 关键词检索

**原理**：BM25 全文搜索

```
buildKeywordQuery(query, topK, threshold, filters)
  → multi_match 查询
  → fields: ["content^1", "title^3", "summary^2"]
  → fuzziness: "AUTO"
  → minimum_should_match: "70%"
```

### 4.4 其他引擎

目录：`internal/application/repository/retriever/`

| 引擎 | 目录 | 向量 | 关键词 |
|---|---|---|---|
| OpenSearch | `opensearch/` | ✅ k-NN | ✅ BM25 |
| Elasticsearch v7 | `elasticsearch/v7/` | ✅ | ✅ |
| Elasticsearch v8 | `elasticsearch/v8/` | ✅ | ✅ |
| PostgreSQL | `postgres/` | ✅ pgvector | ✅ tsquery |
| SQLite | `sqlite/` | ✅ sqlite-vec | ✅ FTS5 |
| Milvus | `milvus/` | ✅ | ❌ |
| Qdrant | `qdrant/` | ✅ | ✅ sparse |
| Weaviate | `weaviate/` | ✅ | ✅ |
| Doris | `doris/` | ✅ | ✅ |
| Tencent VectorDB | `tencentvectordb/` | ✅ | ❌ |
| Neo4j (图谱) | `neo4j/` | — | 图谱遍历 |

---

## 五、路径三：Agent 工具检索

Agent 对话时通过工具调用进行检索，Wiki KB 有专用的 Wiki 工具。

### 5.1 KnowledgeSearchTool（通用知识搜索）

**文件**：`internal/agent/tools/knowledge_search.go`

**函数**：`Execute()`

| 项 | 内容 |
|---|---|
| 签名 | `func (t *KnowledgeSearchTool) Execute(ctx context.Context, args json.RawMessage) (*types.ToolResult, error)` |
| 行号 | 第157行 |

```
KnowledgeSearchTool.Execute(ctx, args)
  │
  ├─ 1. json.Unmarshal(args, &input)
  │     — 解析工具参数（query, knowledge_base_ids, top_k 等）
  │
  ├─ 2. validateKnowledgeBaseIDsInSearchTargets(...)
  │     — 验证 KB ID 有效性和权限
  │
  ├─ 3. t.getKnowledgeBaseTypes(ctx, kbIDs)
  │     — 获取每个 KB 的类型（document/faq/wiki）
  │
  ├─ 4. t.concurrentSearchByTargets(ctx, queries, searchTargets, topK, ...)
  │     — 并发执行混合搜索
  │     └─ 对每个 target → knowledgeBaseService.HybridSearch()
  │
  ├─ 5. t.deduplicateResults(allResults)
  │     — rerank 前去重
  │
  ├─ 6. t.rerankResults(ctx, rerankQuery, deduplicatedBeforeRerank)
  │     — 重排（配置了 rerank 模型时）
  │
  ├─ 7. t.applyMMR(ctx, filteredResults, mmrK, 0.7)
  │     — 最大边际相关性（多样性优化）
  │
  ├─ 8. t.deduplicateResults(filteredResults)
  │     — 最终去重
  │
  ├─ 9. sort.Slice(...)  — 按分数降序排序
  │
  ├─ 10. searchutil.EnrichSearchResultsImageInfo(...)
  │      — 丰富图片信息
  │
  └─ 11. t.formatOutput(ctx, deduplicatedResults, kbIDs, queries)
         — 格式化为 XML 输出供 LLM 阅读
```

### 5.2 Wiki 专用工具

**文件**：`internal/agent/tools/wiki_tools.go`

#### wiki_search 工具

| 项 | 内容 |
|---|---|
| 结构体 | `wikiSearchTool` |
| 构造函数 | `NewWikiSearchTool()` |
| Execute 签名 | `func (t *wikiSearchTool) Execute(ctx context.Context, args json.RawMessage) (*types.ToolResult, error)` |
| 行号 | 第812行 |

```
wikiSearchTool.Execute(ctx, args)
  ├─ 解析参数（query, kb_id, limit）
  ├─ 校验 KB 权限
  ├─ t.wikiService.SearchPages(ctx, kbID, query, limit)
  └─ 格式化为匹配页面列表输出
```

#### wiki_read_page 工具

| 项 | 内容 |
|---|---|
| 结构体 | `wikiReadPageTool` |
| 构造函数 | `NewWikiReadPageTool()` |
| Execute 签名 | `func (t *wikiReadPageTool) Execute(ctx context.Context, args json.RawMessage) (*types.ToolResult, error)` |
| 行号 | 第497行 |

```
wikiReadPageTool.Execute(ctx, args)
  ├─ 解析参数（kb_id, slug, include_content, include_metadata）
  ├─ 校验 KB 权限
  ├─ t.wikiService.GetPageBySlug(ctx, kbID, slug)
  └─ 渲染为 <wiki_page> XML 格式输出
```

### 5.3 其他 Wiki 工具

| 工具名 | 文件 | 功能 |
|---|---|---|
| `wiki_write_page` | `wiki_write_page.go` | 创建或写入 Wiki 页面 |
| `wiki_replace_text` | `wiki_replace_text.go` | 替换页面中的文本片段 |
| `wiki_delete_page` | `wiki_delete_page.go` | 删除 Wiki 页面 |
| `wiki_rename_page` | `wiki_rename_page.go` | 重命名页面（slug 变更） |
| `wiki_link_mutation` | `wiki_link_mutation.go` | 修改页面链接 |
| `wiki_flag_issue` | `wiki_flag_issue.go` | 标记页面质量问题 |
| `wiki_read_issue` | `wiki_read_issue.go` | 读取页面问题详情 |
| `wiki_update_issue` | `wiki_update_issue.go` | 更新问题状态 |
| `wiki_read_source_doc` | `wiki_read_source_doc.go` | 读取源文档内容 |

### 5.4 Wiki 路由解析器

**文件**：`internal/agent/tools/wiki_route_resolver.go`

**结构体**：`WikiRouteResolver`

功能：根据 slug 历史路由记录，快速解析 slug 所属的 KB，避免在多 KB 场景下逐一遍历。

---

## 六、路径四：聊天管道检索

### 6.1 PluginSearch（搜索插件）

**文件**：`internal/application/service/chat_pipeline/search.go`

**结构体**：`PluginSearch`（实现 `EventPlugin` 接口）

**触发阶段**：`CHUNK_SEARCH` 事件

```
PluginSearch.OnEvent(eventType, chatManage)
  │
  ├─ 判断是否需要检索（基于对话上下文判断）
  │
  ├─ 并发执行：
  │   ├─ KB 搜索 → knowledgeBaseService.HybridSearch()
  │   └─ Web 搜索（如启用）
  │
  └─ 结果写入 chatManage.SearchResults
```

### 6.2 PluginWikiBoost（Wiki 分数增强）

**文件**：`internal/application/service/chat_pipeline/wiki_boost.go`

**结构体**：`PluginWikiBoost`

**触发阶段**：`CHUNK_RERANK` 事件

```
PluginWikiBoost.OnEvent(CHUNK_RERANK, chatManage)
  └─ 对每个搜索结果：
       若 chunk 来自 Wiki 页面
         → 分数 *= wikiBoostFactor (1.3)
```

**设计理念**：Wiki 页面是 LLM 合成、交叉引用的高质量知识，应优先于原始文档 chunk 被引用。

---

## 七、路径五：知识图谱检索

### 7.1 QueryKnowledgeGraph

**文件**：`internal/application/service/knowledge_graph.go`

支持基于 Neo4j 图数据库的图谱遍历检索，用于：
- 实体关系查询
- 路径推理
- 子图扩展

---

## 八、前端检索交互

### 8.1 Wiki 搜索

**文件**：`frontend/src/views/knowledge/wiki/WikiBrowser.vue`

Wiki 浏览器中的搜索框，调用：

```
searchWikiPages(kbID, query, limit)
  → GET /knowledgebase/{kb_id}/wiki/search?q={query}
  → 渲染匹配页面列表
```

### 8.2 知识库搜索

**文件**：`frontend/src/api/knowledge-base/index.ts`

```
hybridSearchKnowledgeBase(kbID, params)
  → POST /knowledge-bases/{id}/hybrid-search
  → 返回 SearchResult 列表
```

### 8.3 检索设置页面

**文件**：`frontend/src/views/settings/RetrievalSettings.vue`

可配置项：
- 重排模型选择
- Embedding Top K 滑块
- Vector Threshold 滑块
- Keyword Threshold 滑块
- Rerank Top K / Rerank Threshold
- RRF 参数（k 值、向量权重、关键词权重）

### 8.4 部署能力检测

**前端**：`frontend/src/config/deploymentCapabilities.ts`

**函数**：`isDeploymentCapabilitySupported()`

```
isDeploymentCapabilitySupported(capability)
  → 从 window.__DEPLOYMENT_CAPABILITIES__ 读取
  → 判断当前部署是否支持该能力
```

**后端**：`internal/handler/deployment_capabilities.go`

```
GET /system/capabilities
  → BuildDeploymentCapabilities()
  → 返回版本号 + 功能能力列表
```

能力项包括：`organizations`, `agents`, `integrations.im/embed/api`, `settings.mcp/websearch/vectorstore/storage/sandbox/sandbox.docker`

---

## 九、完整调用链路对比

| 检索路径 | 入口 | 核心函数 | 搜索范围 | 结果形式 | 适用场景 |
|---|---|---|---|---|---|
| Wiki 页面搜索 | `GET /wiki/search` | `wikiPageService.SearchPages()` | Wiki 页面（title/slug/summary/content 正则） | WikiPage 列表 | 快速定位 Wiki 页面 |
| 知识库混合搜索 | `POST /hybrid-search` | `knowledgeBaseService.HybridSearch()` | 所有 chunk（向量+关键词） | SearchResult (chunk 级) | 语义检索、精确引用 |
| Agent 知识搜索 | Agent 工具调用 | `KnowledgeSearchTool.Execute()` | KB 混合搜索 + 重排 + MMR | 格式化 XML | Agent 对话引用知识 |
| Agent Wiki 搜索 | Agent 工具调用 | `wikiSearchTool.Execute()` | Wiki 页面正则搜索 | 匹配页面列表 | Agent 读取 Wiki 内容 |
| 聊天管道搜索 | Chat 事件 | `PluginSearch.OnEvent()` | 混合搜索 + Web 搜索 | SearchResult 列表 | 对话式问答 |

---

## 十、核心文件索引

### 10.1 检索核心文件

| 层级 | 文件路径 | 核心功能 |
|---|---|---|
| 类型定义 | `internal/types/retriever.go` | RetrieverEngineType / RetrieveParams / RetrieveResult |
| 类型定义 | `internal/types/search.go` | SearchParams / SearchResult / SearchTarget |
| 类型定义 | `internal/types/retrieval_config.go` | RetrievalConfig（全局检索参数） |
| 类型定义 | `internal/types/chunk.go` | Chunk 数据模型 |
| 接口定义 | `internal/types/interfaces/retriever.go` | Retriever 接口 |
| 路由层 | `internal/router/routes_knowledge.go` | 混合搜索 / Wiki 搜索路由 |
| Handler | `internal/handler/knowledgebase.go` | HybridSearch handler |
| Handler | `internal/handler/wiki_page.go` | SearchPages / GetPage handler |
| Handler | `internal/handler/deployment_capabilities.go` | 部署能力查询 |
| Service | `internal/application/service/knowledgebase_search.go` | HybridSearch 主流程 |
| Service | `internal/application/service/knowledgebase_search_fusion.go` | RRF 融合 + 去重 |
| Service | `internal/application/service/knowledgebase_search_fanout.go` | 多存储扇出检索 |
| Service | `internal/application/service/knowledgebase_search_results.go` | 结果处理 + 上下文增强 |
| Service | `internal/application/service/knowledgebase_search_storegroup.go` | 存储分组逻辑 |
| Service | `internal/application/service/wiki_page.go` | Wiki 页面 CRUD + 搜索 |
| Service | `internal/application/service/chat_pipeline/search.go` | 聊天管道搜索插件 |
| Service | `internal/application/service/chat_pipeline/wiki_boost.go` | Wiki 页面分数增强 |
| Service | `internal/application/service/knowledge_graph.go` | 知识图谱查询 |
| Repository | `internal/application/repository/wiki_page.go` | Wiki 页面 Repository（含 Search） |
| 检索引擎 | `internal/application/repository/retriever/opensearch/` | OpenSearch 引擎 |
| 检索引擎 | `internal/application/repository/retriever/elasticsearch/` | Elasticsearch 引擎 |
| 检索引擎 | `internal/application/repository/retriever/postgres/` | PostgreSQL 引擎 |
| 检索引擎 | `internal/application/repository/retriever/sqlite/` | SQLite 引擎 |
| 检索引擎 | `internal/application/repository/retriever/milvus/` | Milvus 引擎 |
| 检索引擎 | `internal/application/repository/retriever/qdrant/` | Qdrant 引擎 |
| Agent 工具 | `internal/agent/tools/knowledge_search.go` | KnowledgeSearchTool |
| Agent 工具 | `internal/agent/tools/wiki_tools.go` | wiki_read_page + wiki_search |
| Agent 工具 | `internal/agent/tools/wiki_route_resolver.go` | Wiki 路由解析器 |
| 前端 API | `frontend/src/api/wiki/index.ts` | Wiki API 客户端 |
| 前端 API | `frontend/src/api/knowledge-base/index.ts` | 知识库 API 客户端 |
| 前端视图 | `frontend/src/views/knowledge/wiki/WikiBrowser.vue` | Wiki 浏览器 + 搜索 |
| 前端设置 | `frontend/src/views/settings/RetrievalSettings.vue` | 检索参数设置页 |
| 前端能力 | `frontend/src/config/deploymentCapabilities.ts` | 部署能力配置检测 |
