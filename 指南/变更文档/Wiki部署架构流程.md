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

## 十三、核心文件索引

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
