# Wiki 部署完整架构流程

> 本文档详细描述 WeKnora 项目中 Wiki 知识库的部署（生成）流程，从文档上传到 Wiki 页面自动生成的完整调用链路，具体到每个函数的调用关系。

---

## 一、整体架构概览

Wiki 部署采用 **两阶段 Map-Reduce 异步管道** 架构：

```
文档上传 → 解析分块 → 后处理触发 → Wiki Ingest 任务
                                    │
                     ┌──────────────┴──────────────┐
                     │         Map 阶段 (并发)      │
                     │  逐文档提取实体/概念/摘要     │
                     └──────────────┬──────────────┘
                                    │
                     ┌──────────────┴──────────────┐
                     │    Taxonomy 规划（目录分类）  │
                     └──────────────┬──────────────┘
                                    │
                     ┌──────────────┴──────────────┐
                     │       Reduce 阶段 (并发)     │
                     │  按 slug 合并 → 写入 Wiki    │
                     └──────────────┬──────────────┘
                                    │
                     ┌──────────────┴──────────────┐
                     │   后处理：交叉链接 / 死链清理  │
                     └─────────────────────────────┘
```

---

## 二、触发入口：文档后处理

### 2.1 触发位置

**文件**：`internal/application/service/knowledge_post_process.go`

Wiki 生成由知识文档后处理阶段触发，触发条件：

```go
willSpawnWiki := kb.IndexingStrategy.WikiEnabled && len(textChunks) > 0
```

- KB 的索引策略中 `WikiEnabled` 为 true
- 文档包含文本 chunk（非纯图片/表格）

### 2.2 触发函数

**函数**：`enqueueWikiIngestTrigger()`

| 项 | 内容 |
|---|---|
| 定义文件 | `internal/application/service/wiki_ingest.go`（第561行） |
| 签名 | `func enqueueWikiIngestTrigger(ctx context.Context, task interfaces.TaskEnqueuer, tenantID uint64, kbID string) error` |
| 触发时机 | 文档解析完成后（`ParseStatusFinalizing` 状态） |
| 重试触发 | 服务恢复时，若文档处于 Finalizing 状态且 Wiki 已启用，重新入队 |

### 2.3 触发调用链

```
知识文档处理流程（knowledge_post_process）
  │
  ├─ default 分支（文档解析完成）
  │   └─ willSpawnWiki == true
  │       └─ enqueueWikiIngestTrigger(ctx, s.task, tenantID, kbID)
  │
  └─ 重试分支（ParseStatusFinalizing && WikiEnabled）
      └─ enqueueWikiIngestTrigger(...)
```

---

## 三、Wiki Ingest 入队

### 3.1 入队函数

**函数**：`EnqueueWikiIngest()`

| 项 | 内容 |
|---|---|
| 定义文件 | `internal/application/service/wiki_ingest.go`（第502行） |
| 签名 | `func EnqueueWikiIngest(ctx context.Context, task interfaces.TaskEnqueuer, pendingRepo interfaces.TaskPendingOpsRepository, tenantID uint64, kbID, knowledgeID string) (bool, error)` |
| 任务类型 | `types.TypeWikiIngest = "wiki:ingest"` |
| 队列 | `QueueWiki` / `WorkerPoolWiki` |
| 持久化 | 写入 `task_pending_ops` 表（Pending 状态） |

### 3.2 任务类型定义

**文件**：`internal/types/task.go`

| 常量 | 值 | 说明 |
|---|---|---|
| `TypeWikiIngest` | `"wiki:ingest"` | Wiki 页面同步（主任务） |
| `TypeWikiFinalize` | `"wiki:finalize"` | KB 级收尾任务 |

### 3.3 分布式锁机制

**前缀定义**（wiki_ingest.go）：

| 锁前缀 | 作用 |
|---|---|
| `wikiSlugLockPrefix = "wiki:slug:"` | 单页面读写锁，防止并发写入同一 slug |
| `wikiIdentityClaimPrefix = "wiki:identity:"` | slug 身份声明锁，避免两批次对同一实体产生不同 slug |

### 3.4 并发控制参数

来源：`WikiConfig`（`internal/types/knowledgebase.go`）

| 参数 | 作用 |
|---|---|
| `IngestBatchSize` | 每批次处理的文档数 |
| `IngestMapParallel` | Map 阶段并发数 |
| `IngestReduceParallel` | Reduce 阶段并发数 |
| `IngestMaxInflight` | KB 级最大并发批次数 |

---

## 四、Wiki Ingest 主处理器

### 4.1 入口 Handle 函数

**函数**：`wikiIngestService.Handle()`

| 项 | 内容 |
|---|---|
| 定义文件 | `internal/application/service/wiki_ingest.go`（第671行） |
| 签名 | `func (s *wikiIngestService) Handle(ctx context.Context, t *asynq.Task) error` |
| 分派逻辑 | 按任务类型分派：<br>- `TypeWikiFinalize` → `s.ProcessWikiFinalize()`<br>- 默认 → `s.ProcessWikiIngest()` |

### 4.2 ProcessWikiIngest 主流程

**函数**：`ProcessWikiIngest()`（wiki_ingest_batch.go 第192行）

执行顺序如下：

```
ProcessWikiIngest(ctx, t)
  │
  ├─ 1. 解析 task payload → 注入 context
  ├─ 2. Lite 模式加锁（liteLocks.LoadOrStore）
  ├─ 3. s.kbService.GetKnowledgeBaseByIDOnly()  — 加载 KB 配置
  ├─ 4. s.modelService.GetChatModel()          — 获取合成模型
  ├─ 5. 解析 batchSize / mapParallel / reduceParallel
  ├─ 6. s.reserveInflightSlot()                — 抢占 in-flight 限额
  ├─ 7. s.claimPendingList() / s.peekPendingList()  — 领取待处理 ops
  ├─ 8. s.newWikiBatchContext()                — 构建批量上下文
  │
  ├─ ═══════════ Map 阶段 ═══════════
  │   eg.SetLimit(mapParallel) 并发处理每个文档
  │   → s.mapOneDocument()
  │
  ├─ 9. s.remapSlugUpdatesByIdentity()   — 按 identity 重新映射 slug
  ├─ 10. s.planBatchTaxonomy()           — 规划目录分类
  ├─ 11. s.resolvePlannedFolders()       — 解析/创建目录节点
  │
  ├─ ═══════════ Reduce 阶段 ═══════════
  │   egReduce.SetLimit(reduceParallel) 并发处理每个 slug
  │   → s.reduceSlugUpdates()
  │
  ├─ 12. s.sanitizeDeadSummaryLinks()    — 清理摘要页死链
  ├─ 13. s.publishDraftPages()           — 发布草稿页
  ├─ 14. s.enqueueFinalize()             — 入队 finalize 任务
  ├─ 15. s.finalizeWikiSubtask()         — 子任务收尾 + span 结束
  ├─ 16. s.trimPendingList()             — 清理成功的 pending 行
  ├─ 17. s.requeueFailedOps()            — 失败重试 / 死信
  └─ 18. s.scheduleFollowUp()            — 安排后续批次
```

---

## 五、Map 阶段（逐文档处理）

### 5.1 mapOneDocument 函数

**文件**：`internal/application/service/wiki_ingest_batch.go`（第1156行）

对单个知识文档进行 Wiki 提取和摘要生成。

```
mapOneDocument(ctx, kb, doc, ...)
  │
  ├─ 1. s.isKnowledgeGone()              — 检查文档是否已删除（已删除则跳过）
  ├─ 2. s.chunkRepo.ListChunksByKnowledgeID()  — 获取文档所有 chunk
  ├─ 3. reconstructEnrichedContent()     — 重组 chunk 为完整内容
  ├─ 4. hasSufficientTextContent()       — 文本充足性检查（不足则降级）
  ├─ 5. s.getExistingPageSlugsForKnowledge() — 获取该文档已有的 Wiki 页面 slug
  │
  ├─ 6. s.extractCandidateSlugs()        — Pass 0: 候选 slug 提取（轻量骨架）
  │     ↓ 失败回退
  ├─ 7. s.extractEntitiesAndConceptsNoUpsert()  — 传统实体/概念提取
  │
  ├─ 8. 并行执行（两个 goroutine）：
  │   ├─ s.generateWithTemplate(WikiSummaryPrompt)  — 生成文档摘要页面
  │   └─ s.classifyChunkCitations()                   — chunk 引用分类
  │
  ├─ 9. mergeCitationsIntoItems()        — 合并 chunk 引用到页面条目
  ├─ 10. s.reclaimExtractedIdentities()  — 回收提取的 identity 声明
  │
  ├─ 11. 构建 SlugUpdate 列表：
  │     ├─ summary 类型（文档摘要页）
  │     ├─ entity 类型（实体页）
  │     └─ concept 类型（概念页）
  │
  └─ 12. 与旧页面对比，生成 retract / retractStale 更新
```

### 5.2 相关辅助模块

| 模块文件 | 功能 |
|---|---|
| `internal/application/service/wiki_ingest_dedup.go` | 页面去重检测逻辑 |
| `internal/application/service/wiki_ingest_cite.go` | Chunk 引用（citation）生成与分类 |
| `internal/agent/prompts_wiki.go` | Wiki 生成 Prompt 模板（WikiSummaryPrompt 等） |

---

## 六、Taxonomy 规划阶段

### 6.1 planBatchTaxonomy

**文件**：`internal/application/service/wiki_ingest_taxonomy.go`

```
planBatchTaxonomy(batchCtx)
  │
  ├─ 收集本批次所有新页面的候选分类
  ├─ 调用 LLM（WikiTaxonomyPlanPrompt）生成目录分类规划
  └─ 输出：每个 slug 对应的目标 folder_id
```

### 6.2 resolvePlannedFolders

```
resolvePlannedFolders(batchCtx)
  │
  ├─ 检查规划的目录是否已存在
  ├─ 不存在则创建 WikiFolder 节点
  └─ 解析所有 slug → folder_id 的映射
```

---

## 七、Reduce 阶段（按 slug 合并）

### 7.1 reduceSlugUpdates 函数

**文件**：`internal/application/service/wiki_ingest_batch.go`（第1702行）

按 slug 合并来自多个文档的更新，写入/更新 Wiki 页面。

```
reduceSlugUpdates(ctx, batchCtx, slug, updates)
  │
  ├─ 1. s.filterLiveUpdates()            — 过滤已删除文档的更新
  ├─ 2. s.wikiService.GetPageBySlug()    — 获取现有页面（如存在）
  │
  ├─ 3. 分类 updates：
  │     ├─ summaryUpdate（摘要页更新）
  │     ├─ retracts（需撤回的内容）
  │     └─ additions（需新增的内容）
  │
  ├─ 4. Summary 页面处理分支：
  │     └─ 直接覆盖 → UpdatePage() / CreatePage()
  │
  ├─ 5. Retracts 处理：
  │     ├─ 构建 deletedContent（被删除的来源内容）
  │     ├─ 构建 remainingSourcesContent（剩余来源内容）
  │     └─ 清理 source_refs
  │
  ├─ 6. Additions 处理：
  │     ├─ s.resolveCitedChunks()       — 解析引用 chunk 的完整内容
  │     ├─ 构建 newContentBuilder
  │     └─ 准备 sharedSourceContexts
  │
  ├─ 7. s.generateWithTemplate(WikiPageModifyUserPrompt)
  │     — 调用 LLM 编辑/合成页面内容
  │
  ├─ 8. slugHandles.decodeContent()     — 解码 slug 句柄为链接
  ├─ 9. 应用 taxonomy 规划的 folder_id
  ├─ 10. mergeChunkRefs()               — 合并 chunk 引用列表
  │
  └─ 11. 持久化：
        ├─ 已有页面 → s.wikiService.UpdatePage()
        └─ 新页面   → s.wikiService.CreatePage()
```

---

## 八、后处理阶段

### 8.1 自动交叉链接注入

**函数**：`InjectCrossLinks()`

| 项 | 内容 |
|---|---|
| 文件 | `internal/application/service/wiki_linkify.go` |
| 功能 | 自动在 Wiki 页面内容中检测实体名，注入 `[[slug]]` 格式的内部链接 |
| 调用时机 | Reduce 阶段完成后，批量级别执行 |

### 8.2 死链清理

**函数**：`sanitizeDeadSummaryLinks()`

```
sanitizeDeadSummaryLinks(batchCtx)
  │
  ├─ 扫描所有摘要页的 [[链接]]
  ├─ 检查目标 slug 是否存在
  └─ 移除指向不存在页面的链接
```

### 8.3 草稿发布

**函数**：`publishDraftPages()`

将状态为 `draft` 的新生成页面，在验证通过后转为 `published` 状态。

### 8.4 Finalize 任务

**函数**：`ProcessWikiFinalize()`

KB 级别的收尾任务，处理：
- 全量索引页（Index Page）重建
- 全局链接图谱更新
- 统计信息刷新

---

## 九、Wiki 页面持久化（Service → Repository）

### 9.1 CreatePage

```
wikiPageService.CreatePage(ctx, page)        # service/wiki_page.go:70
  └─ wikiPageRepository.CreatePage(ctx, page)  # repository/wiki_page.go
       ├─ 生成 ID / Slug 规范化
       ├─ GORM Create → 写入 wiki_pages 表
       ├─ 创建初始版本 → wiki_page_revisions 表
       └─ 记录审计日志
```

### 9.2 UpdatePage

```
wikiPageService.UpdatePage(ctx, page)
  ├─ 获取现有页面
  ├─ 解析 outlinks（parseOutLinks）
  ├─ 更新 wiki_pages 表
  ├─ 创建新版本 → wiki_page_revisions 表
  └─ 维护 InLinks / OutLinks 双向链接
```

### 9.3 parseOutLinks 函数

| 项 | 内容 |
|---|---|
| 文件 | `internal/application/service/wiki_page.go` |
| 功能 | 从 Markdown 内容中解析 `[[wiki-link]]` 语法，提取出站链接 |
| 支持语法 | `[[slug]]`、`[[slug\|显示文本]]`、`[[slug#锚点]]` |

### 9.4 数据模型

**文件**：`internal/types/wiki_page.go`

| 结构体 | 对应表 | 说明 |
|---|---|---|
| `WikiPage` | `wiki_pages` | Wiki 页面主体 |
| `WikiPageRevision` | `wiki_page_revisions` | 页面历史版本 |
| `WikiFolder` | `wiki_folders` | Wiki 目录树节点 |
| `WikiPageIssue` | `wiki_page_issues` | 页面质量问题 |
| `WikiGraphData` | — | 链接图谱（内存结构） |

---

## 十、HTTP API 层（手动部署操作）

### 10.1 路由注册

**函数**：`RegisterWikiPageRoutes()`

| 项 | 内容 |
|---|---|
| 文件 | `internal/router/routes_knowledge.go` |
| 路径前缀 | `/knowledgebase/:kb_id/wiki` |

### 10.2 Handler 入口

**文件**：`internal/handler/wiki_page.go`

**结构体**：`WikiPageHandler`

**依赖**：`wikiService` (WikiPageService), `kbService` (KnowledgeBaseService), `lintService`, `auditService`, `memoryService`

**核心方法**：

| 方法 | 路径 | 功能 |
|---|---|---|
| `CreatePage` | `POST /pages` | 手动创建 Wiki 页面 |
| `UpdatePage` | `PUT /pages/*slug` | 更新页面内容 |
| `DeletePage` | `DELETE /pages/*slug` | 删除页面 |
| `MovePage` | `PUT /move-page` | 移动页面到目录 |
| `RebuildLinks` | `POST /rebuild-links` | 重建全站链接 |
| `RevertPage` | `POST /revert` | 回滚到历史版本 |
| `CreateFolder` | `POST /folders` | 创建目录 |
| `UpdateFolder` | `PUT /folders/:folder_id` | 更新目录 |
| `DeleteFolder` | `DELETE /folders/:folder_id` | 删除目录 |
| `AutoFix` | `POST /auto-fix` | 自动修复问题 |

### 10.3 校验函数

**函数**：`validateWikiKB()`

```
validateWikiKB(c, kbID) → (*types.KnowledgeBase, error)
  ├─ 检查 KB 是否存在
  ├─ 校验租户权限
  └─ 确认 KB 类型为 wiki 或已启用 wiki 索引
```

---

## 十一、启动恢复机制

**文件**：`internal/container/recover_pending_wiki_tasks.go`

服务启动时，扫描挂起的 wiki 任务并恢复执行：

```
recoverPendingWikiTasks()
  ├─ 查询 task_pending_ops 表中所有 wiki 类型的 pending 记录
  ├─ 按 kb 分组
  └─ 对每个 kb 调用 enqueueWikiIngestTrigger() 重新触发
```

---

## 十二、完整调用链路总览

```
用户上传文档
  │
  ▼
POST /knowledge-bases/:id/knowledge/file
  → handler.KnowledgeHandler.CreateKnowledgeFromFile()
  → service.KnowledgeService.CreateKnowledge()
  → asynq 入队 knowledge_process 任务
  │
  ▼
knowledge_process 任务
  → 解析文档（PDF/Markdown/Word 等）
  → 分块（chunking）
  → 向量化（embedding）
  → 写入 chunks 表 + 向量索引
  → 状态更新为 Processed
  │
  ▼
knowledge_post_process 任务
  → 各种后处理（去重、摘要等）
  → willSpawnWiki 判定
  → enqueueWikiIngestTrigger()  [wiki_ingest.go]
  → 写入 task_pending_ops 表
  → asynq 入队 "wiki:ingest" 任务
  │
  ▼
wikiIngestService.Handle()  [wiki_ingest.go:671]
  → ProcessWikiIngest()  [wiki_ingest_batch.go:192]
  │
  ├─ 加载 KB + 模型
  ├─ 领取 pending ops
  │
  ├─ ─── Map 阶段（并发）───
  │   mapOneDocument(doc)
  │     ├─ 提取候选 slugs（实体/概念）
  │     ├─ 生成摘要页（LLM）
  │     ├─ 分类 chunk 引用
  │     └─ 输出 SlugUpdate 列表
  │
  ├─ Taxonomy 规划
  │   ├─ planBatchTaxonomy() → LLM 规划目录
  │   └─ resolvePlannedFolders() → 创建目录节点
  │
  ├─ ─── Reduce 阶段（并发）───
  │   reduceSlugUpdates(slug, updates)
  │     ├─ 获取现有页面
  │     ├─ 合并 additions / retracts
  │     ├─ LLM 合成/编辑页面内容
  │     ├─ 解析双向链接
  │     └─ CreatePage() / UpdatePage() → 落库
  │
  ├─ 后处理
  │   ├─ InjectCrossLinks() — 自动交叉链接
  │   ├─ sanitizeDeadSummaryLinks() — 死链清理
  │   └─ publishDraftPages() — 草稿发布
  │
  └─ 收尾
      ├─ enqueueFinalize() — 入队 KB 级收尾
      ├─ trimPendingList() — 清理成功记录
      ├─ requeueFailedOps() — 失败重试
      └─ scheduleFollowUp() — 安排后续批次
  │
  ▼
wikiIngestService.ProcessWikiFinalize()
  ├─ 重建索引页（Index Page）
  ├─ 更新链接图谱
  └─ 刷新统计信息
```

---

## 十三、系统提示词（Prompt）总览

### 13.1 全局 Prompt 模板目录

项目中所有对话/问答/Agent 的系统提示词模板集中在：

**目录**：`config/prompt_templates/`

| 文件名 | 用途 |
|--------|------|
| `system_prompt.yaml` | 知识库对话主系统提示词（`default_kb` 等） |
| `agent_system_prompt.yaml` | Agent 模式系统提示词（含 Wiki Researcher / Wiki Fixer / Hybrid RAG Wiki 等多种 Agent 预设） |
| `context_template.yaml` | 上下文拼接模板 |
| `fallback.yaml` | 无匹配时的兜底回复 |
| `rewrite.yaml` | 查询改写/重写提示词 |
| `intent_prompts.yaml` | 意图识别 |
| `keywords_extraction.yaml` | 关键词提取 |
| `generate_summary.yaml` | 对话摘要生成 |
| `generate_session_title.yaml` | 会话标题生成 |
| `generate_questions.yaml` | 问题生成 |
| `graph_extraction.yaml` | 知识图谱实体/关系抽取 |

在 `config/config.yaml` 中通过 `xxx_prompt_id` 字段引用模板 ID（如 `default_kb`、`default_rewrite`）。

### 13.2 Wiki Ingest 的 Prompt 在哪里？

**Wiki 部署（ingest 生成）流程不使用 `config/prompt_templates/` 目录。** 所有 Wiki 生成相关的 prompt 均以 Go 常量形式硬编码在：

**文件**：`internal/agent/prompts_wiki.go`

按 pipeline 阶段分布：

| Prompt 常量 | Purpose 标识 | 用途 | 调用位置 |
|-------------|-------------|------|---------|
| `WikiSummaryPrompt` | `wiki_summary` | 为新文档生成 Wiki 摘要页 | `wiki_ingest_batch.go:1329` |
| `WikiKnowledgeExtractPrompt` | `wiki_knowledge_extract` | 从文档提取实体+概念（旧版单 pass） | `wiki_ingest_batch.go:1630` |
| `WikiCandidateSlugPrompt` | `wiki_candidate_slug` | chunk-cited Pass 0：扫描输出候选 slug 骨架 | `wiki_ingest_cite.go:96` |
| `WikiChunkCitationPrompt` | `wiki_chunk_citation` | chunk-cited Pass 1..N：批量选 chunk + 引用 | `wiki_ingest_cite.go:283` |
| `WikiPageModifySystemPrompt` | `wiki_page_modify` | Wiki 页面更新 system 侧（共享规则） | `wiki_ingest.go:2538` |
| `WikiPageModifyUserPrompt` | `wiki_page_modify` | Wiki 页面更新 user 侧（reduce 阶段核心） | `wiki_ingest_batch.go:2047` |
| `WikiDeduplicationPrompt` | `wiki_deduplication` | 新提取 items 与现有页面去重 | `wiki_ingest.go:2456` |
| `WikiTaxonomyPlanPrompt` | `wiki_taxonomy_plan` | 批量分配目录路径（分类规划） | `wiki_ingest_taxonomy.go:83` |
| `WikiIndexIntroPrompt` | `wiki_index_intro` | 首次创建 Wiki 索引页简介 | `wiki_ingest.go:2163` |
| `WikiIndexIntroUpdatePrompt` | `wiki_index_intro` | 增量更新 Wiki 索引页简介 | `wiki_ingest.go:2181` |

**调用入口**：`wikiIngestService.generateWithTemplate()`（`wiki_ingest.go:2521`），使用 Go `text/template` 渲染 + 指数退避重试 + singleflight 合并并发请求。

### 13.3 generateWithTemplate 函数详解

`generateWithTemplate` 是 **Wiki 生成流程中所有 LLM 调用的统一入口**。标题生成、摘要、要点提取、去重、引用、大纲... 都走这个函数，区别只是传入的 prompt 模板和数据不同。

> ⚠️ 注意：它只管 Wiki 模块的 LLM 调用，**不是全项目通用的**。对话系统（聊天、Agent、RAG）走的是 `chat.Chat` 接口直接调用，不经过这个函数。
>
> 全项目真正的底层统一入口是 `chat.Chat` 接口（`Chat()` / `ChatStream()` 方法），`generateWithTemplate` 内部最终也是调用它。

#### 13.3.0 函数签名与核心参数

```go
func (s *wikiIngestService) generateWithTemplate(
    ctx        context.Context,   // 上下文：超时/取消/元数据传递
    chatModel  chat.Chat,         // LLM 客户端：已初始化好的 chat 接口实例
    promptTpl  string,           // prompt 模板：含 {{.变量名}} 的预设模板字符串
    data       map[string]string, // 模板变量：key=变量名，value=实际值
) (string, error)
```

**核心理解：**

| 概念 | 说明 |
|------|------|
| `promptTpl` | **官方预设的 prompt 模板**（写死在代码里，不是用户填的 |
| `data` | 模板变量数据，其中 `CustomInstructions` 是**用户自定义提示词**（前端 Wiki 配置里填的） |
| 调用方式 | **只用非流式**（`Chat()`），Wiki 后台生成不需要流式 |
| 做了什么 | 模板渲染 → 追加自定义提示词 → 调 LLM（重试+去重+缓存）→ 返回文本 |

#### 13.3.0.1 data 参数结构与自定义提示词机制

**`data` 的类型：** `map[string]string`（字符串字典），key = 模板变量名，value = 实际值。

**每个调用点传的字段不一样**，取决于对应 prompt 模板需要哪些 `{{.变量名}}`。但有几个通用字段几乎每次都有：

| 字段 | 说明 |
|------|------|
| `Content` | 主要内容（文档文本、chunk 内容等） |
| `Language` | 语言（中文/英文等） |
| `CustomInstructions` | **用户自定义提示词**（前端 Wiki 配置里填的「内容生成要求」「知识提取要求」） |
| `InstructionScope` | 作用域标签（`wiki_content` 或 `extraction`），写在 XML 标签属性里 |

**典型调用示例（摘要生成）：**

```go
s.generateWithTemplate(ctx, chatModel, agent.WikiSummaryPrompt, map[string]string{
    "Content":            content,
    "Language":           lang,
    "ExtractedSlugs":     slugListing,
    "CustomInstructions": batchCtx.ContentInstructions,
    "InstructionScope":   "wiki_content",
})
```

**maskedData vs data：**

| | `data` | `maskedData` |
|---|------|-----------|
| 结构 | `map[string]string` | `map[string]string`（key 完全相同） |
| 图片 URL | 原始 URL | 占位符（如 `img_001`） |
| 用途 | 原始数据 | 传给 LLM（省 token + 防误处理） |

`maskTemplateDataImageURLs(data)` 生成 maskedData + urlMap（占位符→原始URL 的映射表），LLM 返回后再用 `unmaskImageURLs(content, urlMap)` 还原。

**用户自定义提示词（CustomInstructions）的 3 个要点：**

1. **纯文本，没有占位符** — 用户填什么就是什么，不支持 `{{.变量}}` 模板语法
2. **追加方式** — 由 `AppendCustomPromptInstructions` 函数用 XML 标签包起来，追加到主 prompt 末尾
3. **追加位置分两种** — Wiki 页面修改追加到 system prompt，其他场景追加到 user prompt

#### 13.3.1 执行流程（7 步）

```
输入：promptTpl(模板字符串) + data(变量数据)
  ↓
① 解析模板（template.Parse）
  ↓
② 屏蔽图片 URL（maskTemplateDataImageURLs）
  ↓
③ 渲染模板（template.Execute → 变量填进模板）
  ↓
④ 构造 messages + 追加用户自定义提示词
  ↓
⑤ 生成缓存键 + 预热键（Prompt Cache）
  ↓
⑥ 调用 LLM（指数退避重试 + singleflight 去重合并）
  ↓
⑦ 还原图片 URL（unmaskImageURLs） + 返回结果
输出：生成的文本
```

#### 13.3.2 每一步详解

**① 解析模板（第 2542 行）**

```go
tmpl, err := template.New("wiki").Parse(promptTpl)
```

把含 `{{.变量名}}` 的 prompt 模板字符串编译成 Go template 对象。

**② 屏蔽图片 URL（第 2547 行）**

```go
maskedData, urlMap := maskTemplateDataImageURLs(data)
```

把 data 里的图片 URL 替换成占位符（减少 token 消耗，防止 LLM 误处理），同时记录原始 URL。LLM 返回后再替换回来。

**③ 渲染模板（第 2549-2555 行）**

```go
var buf strings.Builder
tmpl.Execute(&buf, maskedData)
prompt := buf.String()
```

把变量数据填进模板，生成最终的 prompt 文本。`strings.Builder` 是 Go 高效拼接字符串的工具。

**④ 构造 messages + 追加自定义提示词（第 2556-2571 行）**

分两种情况：
- **Wiki 页面修改**（`WikiPageModifyUserPrompt`）：有独立的 system prompt，自定义指令加在 system 里
- **其他所有 Wiki 生成**：只有 user prompt，自定义指令直接追加在 user 内容末尾

`AppendCustomPromptInstructions` 把用户自定义的要求（Wiki 配置里的「内容生成要求」「知识提取要求」）用 XML 标签包起来加在 prompt 末尾。

**⑤ 生成缓存键（第 2572-2586 行）**

```go
prefixFingerprint := chat.PromptPrefixFingerprint(messages, opts)
warmupKey = chat.BuildPromptCacheKey(tenantID, modelID, purpose, prefixFingerprint)
```

计算 prompt 前缀的指纹（哈希），用于 Prompt Cache（提示词缓存）。多个文档共享相同的系统提示词前缀时，缓存起来可以省 token。

`purpose`（由 `wikiPromptPurpose` 函数生成）是缓存分类标签，把长 prompt 模板映射成简短标识（如 `wiki_summary`、`wiki_knowledge_extract`），用于日志、缓存键、元数据。

**⑥ 调用 LLM（核心，第 2598-2657 行）**

这一步最复杂，又分 3 层：

| 层级 | 机制 | 说明 |
|------|------|------|
| 内层 | `chatModel.Chat()` | 真正发 HTTP 请求给 LLM |
| 中层 | 指数退避重试 | 临时性错误（408/429/5xx/超时）自动重试，间隔 2s→4s→8s... |
| 外层 | singleflight 去重 | 相同请求并发时只发 1 次，其余等结果，省 token |

重试策略：
- 最多 `wikiLLMMaxAttempts` 次
- 只重试临时性错误，4xx（除 408/429）直接失败
- 退避时间：`wikiLLMBackoffBase * 2^(attempt-1)`

**⑦ 还原图片 URL + 返回**

```go
content, _ := result.Val.(string)
return unmaskImageURLs(content, urlMap), nil
```

把第 ② 步替换掉的图片 URL 占位符还原成原始 URL。

#### 13.3.3 设计目的

为什么要统一封装成一个函数？

| 特性 | 说明 |
|------|------|
| 统一模板渲染 | 所有 Wiki prompt 用同一套模板机制，避免重复代码 |
| 统一重试策略 | 临时性错误自动重试，避免上游网关抖动导致 Wiki 页面永久缺失 |
| 统一去重合并 | singleflight 合并相同请求，省钱省 token |
| 统一缓存机制 | Prompt Cache 预热，提高重复场景的性能 |
| 统一自定义提示词 | 用户的「内容生成要求」「知识提取要求」统一追加 |
| 统一日志埋点 | purpose + 指纹，方便排查和统计 |

---

## 十四、核心文件索引

| 层级 | 文件路径 | 核心功能 |
|---|---|---|
| 类型定义 | `internal/types/wiki_page.go` | WikiPage / Revision / Folder / Issue / Graph 数据模型 |
| 类型定义 | `internal/types/knowledgebase.go` | KnowledgeBase / WikiConfig / IndexingStrategy |
| 类型定义 | `internal/types/task.go` | 任务类型常量（TypeWikiIngest 等） |
| 接口定义 | `internal/types/interfaces/wiki_page.go` | WikiPageService / WikiPageRepository 接口 |
| 路由层 | `internal/router/routes_knowledge.go` | RegisterWikiPageRoutes |
| Handler | `internal/handler/wiki_page.go` | WikiPageHandler（HTTP 入口） |
| Service | `internal/application/service/wiki_page.go` | wikiPageService（页面 CRUD + 搜索） |
| Service | `internal/application/service/wiki_ingest.go` | wikiIngestService（Handle + 入队） |
| Service | `internal/application/service/wiki_ingest_batch.go` | ProcessWikiIngest + Map + Reduce |
| Service | `internal/application/service/wiki_ingest_dedup.go` | 去重逻辑 |
| Service | `internal/application/service/wiki_ingest_taxonomy.go` | 目录分类规划 |
| Service | `internal/application/service/wiki_ingest_cite.go` | Chunk 引用生成 |
| Service | `internal/application/service/wiki_linkify.go` | 自动交叉链接注入 |
| Service | `internal/application/service/wiki_lint.go` | Wiki 质量检查 |
| Service | `internal/application/service/knowledge_post_process.go` | 后处理触发 wiki ingest |
| Repository | `internal/application/repository/wiki_page.go` | wikiPageRepository（GORM 实现） |
| Agent Prompt | `internal/agent/prompts_wiki.go` | Wiki 生成 prompt 模板 |
| Agent 工具 | `internal/agent/tools/wiki_tools.go` | wiki_read_page + wiki_search |
| Agent 工具 | `internal/agent/tools/wiki_write_page.go` | wiki_write_page 工具 |
| 容器 | `internal/container/recover_pending_wiki_tasks.go` | 启动恢复挂起任务 |
| 前端 API | `frontend/src/api/wiki/index.ts` | Wiki API 客户端封装 |
| 前端视图 | `frontend/src/views/knowledge/wiki/WikiBrowser.vue` | Wiki 浏览器主组件 |
