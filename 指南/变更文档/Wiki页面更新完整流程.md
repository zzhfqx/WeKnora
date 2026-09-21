# Wiki 页面更新完整流程技术文档

> 本文档详细记录 WeKnora 项目中 Wiki 页面从创建、更新、删除到 AI 自动生成、关系提取、Neo4j 图谱同步的完整技术流程。
> 适用版本：当前 `pg-zzh` 分支（含 Neo4j 图谱可视化改造）

---

## 目录

1. [整体架构概览](#1-整体架构概览)
2. [数据模型与核心表结构](#2-数据模型与核心表结构)
3. [前端 Wiki 浏览器与 API 层](#3-前端-wiki-浏览器与-api-层)
4. [路由层与权限控制](#4-路由层与权限控制)
5. [Handler 层处理逻辑](#5-handler-层处理逻辑)
6. [Service 层业务逻辑](#6-service-层业务逻辑)
   - 6.1 [页面创建流程](#61-页面创建流程)
   - 6.2 [页面更新流程](#62-页面更新流程)
   - 6.3 [页面删除流程](#63-页面删除流程)
   - 6.4 [版本历史与回滚](#64-版本历史与回滚)
   - 6.5 [文件夹管理](#65-文件夹管理)
7. [Repository 层数据持久化](#7-repository-层数据持久化)
8. [AI 自动生成流程（Wiki Ingest Pipeline）](#8-ai-自动生成流程wiki-ingest-pipeline)
   - 8.1 [触发入口](#81-触发入口)
   - 8.2 [任务入队机制](#82-任务入队机制)
   - 8.3 [批处理核心流程](#83-批处理核心流程)
   - 8.4 [Map 阶段：文档提取](#84-map-阶段文档提取)
   - 8.5 [Reduce 阶段：页面写入](#85-reduce-阶段页面写入)
   - 8.6 [Finalize 阶段：全局收敛](#86-finalize-阶段全局收敛)
9. [页面关系提取与 Neo4j 图谱同步](#9-页面关系提取与-neo4j-图谱同步)
   - 9.1 [结构化语义关系（wiki_page_relations）](#91-结构化语义关系wiki_page_relations)
   - 9.2 [Neo4j 图谱写入](#92-neo4j-图谱写入)
   - 9.3 [Neo4j 图谱查询与可视化](#93-neo4j-图谱查询与可视化)
10. [Agent 工具集（Wiki Tools）](#10-agent-工具集wiki-tools)
11. [并发控制与一致性保障](#11-并发控制与一致性保障)
12. [关键配置项与调优参数](#12-关键配置项与调优参数)

---

## 1. 整体架构概览

Wiki 系统采用经典的分层架构，结合异步任务队列与 LLM 驱动的内容生成。

### 1.1 分层架构图

```
┌──────────────────────────────────────────────────────────────┐
│                        前端 (Vue 3)                           │
│  WikiBrowser.vue ── api/wiki/index.ts ── HTTP 请求            │
└──────────────────────────────┬───────────────────────────────┘
                               │ HTTP / REST API
┌──────────────────────────────▼───────────────────────────────┐
│                        路由层 (Router)                         │
│  routes_knowledge.go  ──  RBAC 鉴权 ──  Wiki 路由注册         │
└──────────────────────────────┬───────────────────────────────┘
                               │
┌──────────────────────────────▼───────────────────────────────┐
│                      Handler 层 (Handler)                      │
│  wiki_page.go ── 参数校验/上下文绑定 ── graph_neo4j.go         │
└──────────────────────────────┬───────────────────────────────┘
                               │
┌──────────────────────────────▼───────────────────────────────┐
│                      Service 层 (Service)                      │
│  wiki_page.go    wiki_ingest.go    wiki_ingest_batch.go       │
│  wiki_linkify.go wiki_lint.go     graph_neo4j.go              │
│  wiki_ingest_taxonomy.go  wiki_ingest_dedup.go                │
└──────────────────────────────┬───────────────────────────────┘
                               │
┌──────────────────────────────▼───────────────────────────────┐
│                   Repository 层 (Repository)                   │
│  wiki_page.go  ──  neo4j/repository.go  ──  chunk 相关仓库     │
└──────────────────────────────┬───────────────────────────────┘
                               │
┌───────────────┬──────────────┼──────────────┬────────────────┐
│   PostgreSQL  │   Redis      │   Neo4j      │   LLM (API)    │
│  wiki_pages   │  任务队列     │  知识图谱     │  内容生成/提取  │
│  wiki_folders │  分布式锁     │  实体节点     │  关系提取       │
│  wiki_page_   │  缓存         │  关系边       │                │
│  revisions    │              │              │                │
│  task_pending_│              │              │                │
│     ops       │              │              │                │
└───────────────┴──────────────┴──────────────┴────────────────┘
```

### 1.2 关键技术组件

| 组件 | 技术选型 | 作用 |
|------|---------|------|
| Web 框架 | Gin | HTTP 请求处理 |
| ORM | GORM | PostgreSQL 数据访问 |
| 任务队列 | asynq (Redis) | 异步 Wiki 生成任务调度 |
| 图数据库 | Neo4j | 知识图谱存储与查询 |
| 前端框架 | Vue 3 + TypeScript | Wiki 浏览器 UI |
| LLM | 可配置（支持多模型） | 内容生成、实体提取、关系抽取 |

---

## 2. 数据模型与核心表结构

### 2.1 核心数据表

#### 2.1.1 `wiki_pages` — Wiki 页面主表

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | varchar(36) | UUID 主键 |
| `tenant_id` | uint64 | 工作空间 ID（多租户隔离） |
| `knowledge_base_id` | varchar(36) | 所属知识库 ID |
| `slug` | varchar(255) | URL 友好的唯一标识，如 `entity/acme-corp` |
| `title` | varchar(512) | 页面标题 |
| `page_type` | varchar(32) | 页面类型：summary / entity / concept / index / synthesis / comparison |
| `status` | varchar(32) | 状态：draft / published / archived |
| `content` | text | Markdown 全文内容 |
| `summary` | text | 一句话摘要（用于目录列表） |
| `aliases` | json | 别名、缩写、译名等 |
| `parent_slug` | varchar(255) | 语义父页面 slug |
| `folder_id` | varchar(36) | 所在文件夹 ID（目录树的唯一真相来源） |
| `category_path` | json | 目录面包屑（从 folder_id 派生的缓存） |
| `wiki_path` | varchar(1024) | 可排序的路径字符串（派生缓存） |
| `depth` | int | 目录深度（派生缓存） |
| `sort_order` | int | 同级排序权重 |
| `source_refs` | json | 来源文档引用，格式：`<knowledge_id>\|<doc_title>` |
| `chunk_refs` | json | 来源 chunk UUID 列表（细粒度引用） |
| `in_links` | json | 入链（哪些页面链接到本页） |
| `out_links` | json | 出链（本页链接到哪些页面） |
| `page_metadata` | json | 任意元数据 |
| `version` | int | 版本号（仅内容变更时递增） |
| `last_edit_source` | varchar(16) | 当前版本作者类型：pipeline / agent / user / revert |
| `last_editor_id` | varchar(64) | 编辑者用户 ID |
| `created_at` / `updated_at` | timestamp | 创建/更新时间 |
| `deleted_at` | timestamp | 软删除时间 |

**页面类型（page_type）**：
- `summary`：文档摘要页（每个文档自动生成一个）
- `entity`：实体页（人物、组织、产品等）
- `concept`：概念/主题页
- `index`：Wiki 索引页（index.md）
- `synthesis`：综合分析页（Agent 对话中生成）
- `comparison`：对比页（Agent 对话中生成）

**编辑来源（last_edit_source）**：
- `pipeline`：Wiki 摄入流水线自动生成
- `agent`：Agent 通过 wiki 工具写入
- `user`：用户通过编辑器手动编辑
- `revert`：版本回滚产生

#### 2.1.2 `wiki_page_revisions` — 页面版本历史

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | varchar(36) | UUID 主键 |
| `page_id` | varchar(36) | 所属页面 ID |
| `version` | int | 版本号（与 page_id 联合唯一） |
| `slug` | varchar(255) | 快照时的 slug |
| `title` / `page_type` / `status` | - | 快照内容 |
| `content` / `summary` / `aliases` | - | 快照内容 |
| `edit_source` / `editor_id` | - | 该版本作者 |
| `edited_at` | timestamp | 该版本创建时间 |

**版本保留策略（两级）**：
- 软上限（`WikiMaxRevisionsPerPage` = 50）：仅对机器生成版本（pipeline）生效
- 硬上限（`WikiMaxRevisionsHardCap` = 200）：所有版本的绝对上限
- 用户/Agent/回滚版本始终保留到硬上限

#### 2.1.3 `wiki_folders` — Wiki 文件夹

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | varchar(36) | UUID 主键 |
| `tenant_id` / `knowledge_base_id` | - | 租户/知识库 |
| `parent_id` | varchar(36) | 父文件夹 ID（空字符串 = 根目录） |
| `name` | varchar(255) | 文件夹名称 |
| `path` | varchar(1024) | 物化路径（"/" 连接，如 "AI/RAG"） |
| `depth` | int | 深度 |
| `sort_order` | int | 排序 |

> **设计要点**：文件夹是独立于页面的一级目录节点。页面的 `folder_id` 是其在目录树中位置的唯一真相。`category_path`、`wiki_path`、`depth` 均为从 folder 链派生的缓存字段，每次写入时重新计算。

#### 2.1.4 `wiki_page_relations` — 页面结构化语义关系

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | varchar(36) | UUID 主键 |
| `tenant_id` / `knowledge_base_id` | - | 租户/知识库 |
| `source_slug` | varchar(255) | 源页面 slug |
| `target_slug` | varchar(255) | 目标页面 slug |
| `relation_type` | varchar(64) | 关系类型（如 `belongs_to`, `created_by`） |
| `relation_label` | varchar(128) | 关系显示标签 |
| `reverse_label` | varchar(128) | 反向关系标签 |
| `description` | text | 关系描述 |
| `confidence` | float | 置信度 |
| `source_page_type` / `target_page_type` | varchar(32) | 源/目标页面类型 |
| `generated_by` | varchar(16) | 生成来源：pipeline / agent / user |
| `version` | int | 版本 |

> 这是 L2 本体关系表，存储实体类与实体类之间的结构化语义关系，区别于 `in_links/out_links` 这种纯超链接关系。

#### 2.1.5 `wiki_page_issues` — 页面问题记录

用于 Wiki Lint 检测出的问题（断链、孤立页面等），供人工审核。

### 2.2 核心类型定义位置

| 类型 | 文件路径 |
|------|---------|
| WikiPage / WikiPageRevision / 常量 | [internal/types/wiki_page.go](internal/types/wiki_page.go) |
| WikiPageService 接口 | [internal/types/interfaces/wiki_page.go](internal/types/interfaces/wiki_page.go) |
| WikiPageRepository 接口 | [internal/types/interfaces/wiki_page.go](internal/types/interfaces/wiki_page.go) |
| Neo4jGraphData 等类型 | 见 types 包 Neo4j 相关定义 |

---

## 3. 前端 Wiki 浏览器与 API 层

### 3.1 前端文件结构

```
frontend/src/
├── api/wiki/
│   ├── index.ts           # Wiki API 封装（当前版本）
│   └── index_v1.ts        # 旧版 API（兼容保留）
└── views/knowledge/wiki/
    ├── WikiBrowser.vue         # Wiki 浏览器主组件（新版）
    ├── WikiBrowser_v1.vue      # 旧版浏览器（兼容保留）
    ├── Neo4jGraphViewer.vue    # Neo4j 图谱可视化组件
    ├── WikiRevisionDrawer.vue  # 版本历史抽屉
    ├── WikiFolderActions.vue   # 文件夹操作菜单
    ├── wikiDirectoryState.ts   # 目录树状态管理
    └── wikiDirectoryState.test.ts
```

### 3.2 前端 API 封装（`api/wiki/index.ts`）

核心 API 函数列表：

| 函数 | 方法与路径 | 说明 |
|------|-----------|------|
| `listWikiPages(kbId, params)` | GET `/wiki/pages` | 分页列出页面 |
| `listWikiFolders(kbId, parentId, pageTypes)` | GET `/wiki/folders` | 列出子文件夹 |
| `getWikiPage(kbId, slug)` | GET `/wiki/pages/*slug` | 获取单页面 |
| `createWikiPage(kbId, page)` | POST `/wiki/pages` | 创建页面 |
| `updateWikiPage(kbId, slug, req)` | PUT `/wiki/pages/*slug` | 更新页面 |
| `deleteWikiPage(kbId, slug)` | DELETE `/wiki/pages/*slug` | 删除页面 |
| `moveWikiPage(kbId, slug, folderId)` | PUT `/wiki/move-page` | 移动页面到文件夹 |
| `getWikiIndex(kbId, types, limit, cursor)` | GET `/wiki/index` | 获取索引视图 |
| `getWikiGraph(kbId, params)` | GET `/wiki/graph` | 获取链接图谱 |
| `getWikiStats(kbId)` | GET `/wiki/stats` | 获取统计信息 |
| `getWikiRelations(kbId, slug)` | GET `/wiki/relations/*slug` | 获取页面结构化关系 |
| `searchWikiPages(kbId, q, limit)` | GET `/wiki/search` | 全文搜索页面 |
| `listWikiRevisions(kbId, slug, limit, offset)` | GET `/wiki/revisions/*slug` | 版本历史列表 |
| `getWikiRevision(kbId, slug, version)` | GET `/wiki/revisions/*slug?version=` | 获取单版本详情 |
| `revertWikiPage(kbId, slug, version)` | POST `/wiki/revert` | 回滚到指定版本 |
| `rebuildWikiLinks(kbId)` | POST `/wiki/rebuild-links` | 重建链接 |
| `getWikiLint(kbId)` | GET `/wiki/lint` | Wiki 健康检查 |
| `autoFixWiki(kbId)` | POST `/wiki/auto-fix` | 自动修复问题 |
| `getNeo4jGraph(kbId, params)` | GET `/graph/neo4j` | 获取 Neo4j 图谱 |
| `searchNeo4jNodes(kbId, q, limit)` | GET `/graph/neo4j/search` | 搜索 Neo4j 节点 |
| `getNeo4jGraphStats(kbId)` | GET `/graph/neo4j/stats` | Neo4j 图谱统计 |
| `getNeo4jRelationTypes(kbId)` | GET `/graph/neo4j/relation-types` | 获取关系类型列表 |
| `getNeo4jNodeDetail(kbId, name)` | GET `/graph/neo4j/node/:name` | 获取节点详情 |

### 3.3 Slug 编码

Wiki slug 支持层级结构（如 `entity/acme-corp`），API 层使用 `encodeSlugPath()` 函数对每段单独编码以保留 "/" 分隔符：

```typescript
function encodeSlugPath(slug: string): string {
  return slug.split("/").map(encodeURIComponent).join("/");
}
```

---

## 4. 路由层与权限控制

### 4.1 路由注册

Wiki 页面路由在 [internal/router/routes_knowledge.go](internal/router/routes_knowledge.go) 的 `RegisterWikiPageRoutes()` 函数中注册。

**基础路径**：`/api/v1/knowledgebase/:kb_id/wiki`

Neo4j 图谱路由在同一文件的 `RegisterNeo4jGraphRoutes()` 中注册。

**基础路径**：`/api/v1/knowledgebase/:kb_id/graph/neo4j`

### 4.2 完整路由列表

#### Wiki 页面 CRUD

| 方法 | 路径 | Handler | 权限 | 说明 |
|------|------|---------|------|------|
| GET | `/pages` | `ListPages` | Viewer+ | 分页列表 |
| POST | `/pages` | `CreatePage` | KB Owner/Admin+ | 创建页面 |
| GET | `/pages/*slug` | `GetPage` | Viewer+ | 获取单页面 |
| PUT | `/pages/*slug` | `UpdatePage` | KB Owner/Admin+ | 更新页面 |
| DELETE | `/pages/*slug` | `DeletePage` | KB Owner/Admin+ | 删除页面 |
| PUT | `/move-page` | `MovePage` | KB Owner/Admin+ | 移动页面 |

#### 版本管理

| 方法 | 路径 | Handler | 权限 | 说明 |
|------|------|---------|------|------|
| GET | `/revisions/*slug` | `ListRevisions` | Viewer+ | 版本列表 |
| POST | `/revert` | `RevertPage` | KB Owner/Admin+ | 回滚版本 |

#### 文件夹管理

| 方法 | 路径 | Handler | 权限 | 说明 |
|------|------|---------|------|------|
| GET | `/folders` | `ListFolders` | Viewer+ | 子文件夹列表 |
| POST | `/folders` | `CreateFolder` | KB Owner/Admin+ | 创建文件夹 |
| PUT | `/folders/:folder_id` | `UpdateFolder` | KB Owner/Admin+ | 重命名/移动文件夹 |
| DELETE | `/folders/:folder_id` | `DeleteFolder` | KB Owner/Admin+ | 删除空文件夹 |

#### 索引与图谱

| 方法 | 路径 | Handler | 权限 | 说明 |
|------|------|---------|------|------|
| GET | `/index` | `GetIndex` | Viewer+ | 结构化索引视图 |
| GET | `/graph` | `GetGraph` | Viewer+ | 链接图谱数据 |
| GET | `/stats` | `GetStats` | Viewer+ | Wiki 统计 |
| GET | `/relations/*slug` | `GetPageRelations` | Viewer+ | 页面结构化关系 |

#### Neo4j 图谱

| 方法 | 路径 | Handler | 权限 | 说明 |
|------|------|---------|------|------|
| GET | `/graph/neo4j` | `GetNeo4jGraph` | Viewer+ | Neo4j 图谱（overview/ego） |
| GET | `/graph/neo4j/search` | `SearchNeo4jNodes` | Viewer+ | 节点搜索 |
| GET | `/graph/neo4j/stats` | `GetNeo4jGraphStats` | Viewer+ | 图谱统计 |
| GET | `/graph/neo4j/relation-types` | `GetNeo4jRelationTypes` | Viewer+ | 关系类型列表 |
| GET | `/graph/neo4j/node/:name` | `GetNeo4jNodeDetail` | Viewer+ | 节点详情 |

#### 维护工具

| 方法 | 路径 | Handler | 权限 | 说明 |
|------|------|---------|------|------|
| GET | `/search` | `SearchPages` | Viewer+ | 全文搜索 |
| POST | `/rebuild-links` | `RebuildLinks` | KB Owner/Admin+ | 重建所有链接 |
| GET | `/lint` | `Lint` | Viewer+ | 健康检查 |
| POST | `/auto-fix` | `AutoFix` | KB Owner/Admin+ | 自动修复 |

#### 问题管理

| 方法 | 路径 | Handler | 权限 | 说明 |
|------|------|---------|------|------|
| GET | `/issues` | `ListIssues` | Viewer+ | 问题列表 |
| PUT | `/issues/:issue_id/status` | `UpdateIssueStatus` | KB Owner/Admin+ | 更新问题状态 |

### 4.3 权限控制机制

Wiki 路由采用**两层正交鉴权**：

1. **角色层**（RBAC）：
   - 读取操作：`g.Viewer()` — 工作空间 Viewer+ 角色
   - 写入操作：`g.OwnedWikiKBOrAdmin()` — KB 创建者本人或 Admin+

2. **KB 访问层**（跨空间共享）：
   - 读取：`g.KBAccessRead("kb_id")` — 自有/组织共享(viewer)/通过共享 Agent 可见
   - 写入：`g.KBAccessWrite("kb_id")` — 自有/组织共享(editor)

3. **API Key 层**：
   - 读取：`apiKeyRetrieve(apiKeyFullAccess())` — retrieve 或 full-access 能力
   - 写入：`apiKeyIngest(apiKeyFullAccess())` — ingest 或 full-access 能力
   - 仍受 KB 白名单约束

---

## 5. Handler 层处理逻辑

### 5.1 WikiPageHandler 结构

定义位置：[internal/handler/wiki_page.go](internal/handler/wiki_page.go)

```go
type WikiPageHandler struct {
    wikiService   interfaces.WikiPageService
    kbService     interfaces.KnowledgeBaseService
    lintService   *service.WikiLintService
    auditService  interfaces.AuditLogService
    memoryService interfaces.MemoryService
}
```

### 5.2 通用校验流程

每个 Handler 方法开头都调用 `validateWikiKB(c)`：
1. 从 URL 提取 `kb_id`
2. 从上下文获取 `tenant_id`
3. 校验 KB 存在性
4. 校验 KB 是否启用了 Wiki 功能（`kb.IsWikiEnabled()`）

Neo4j Handler 对应调用 `validateKB(c)`，校验 `kb.IsGraphEnabled()`。

### 5.3 页面创建 Handler 流程

```
CreatePage(c *gin.Context)
  │
  ├─ validateWikiKB() → 获取 kbID, tenantID
  ├─ 绑定 JSON → types.WikiPage
  ├─ 设置 KB ID / Tenant ID
  ├─ 校验 page_type / status 合法性
  ├─ ctx = WithWikiEditSource(User)  // 标记编辑来源为用户
  ├─ wikiService.CreatePage(ctx, &page)
  │    └─ （见 Service 层流程）
  ├─ recordManualWikiActivity() → 记录审计日志
  └─ 返回 201 Created
```

### 5.4 页面更新 Handler 流程（乐观锁）

```
UpdatePage(c *gin.Context)
  │
  ├─ validateWikiKB()
  ├─ 从路径提取 slug（去掉前缀 "/"）
  ├─ 绑定 JSON → WikiPageUpdateRequest（所有字段为可选指针）
  ├─ ctx = WithWikiEditSource(User)
  ├─ wikiService.GetPageBySlug() → 读取现有版本
  │
  ├─ 乐观锁校验：req.Version > 0 且 ≠ existing.Version → 返回 409 Conflict
  │
  ├─ 字段合并（仅更新提供的字段）：
  │   page = *existing
  │   if req.Title != nil { page.Title = *req.Title }
  │   ... 对 content/summary/page_type/status/aliases 做同样处理
  │
  ├─ wikiService.UpdatePage(ctx, &page)
  │    └─ （见 Service 层）
  │
  ├─ 版本变化时记录 audit 活动
  └─ 返回 200 OK + 更新后的页面
```

### 5.5 Slug 参数提取

Wiki slug 支持层级（如 `entity/foo/bar`），使用 Gin 的 wildcard 路由 `*slug`。
Handler 层通过 `getSlugParam(c)` 去掉前缀 "/" 后使用。

---

## 6. Service 层业务逻辑

Service 层实现位置：[internal/application/service/wiki_page.go](internal/application/service/wiki_page.go)

### 6.1 页面创建流程

```
CreatePage(ctx, page)
  │
  ├─ 生成 UUID（如果为空）
  ├─ 校验 slug / kb_id 必填
  ├─ 设置默认 status = published
  ├─ 设置默认 version = 1
  ├─ 从 ctx 提取 last_edit_source / last_editor_id
  ├─ stripWikiPageInlineChunkCitations()  // 清除内部 chunk 引用标记
  │
  ├─ parseOutLinks(content) → 从 [[wiki-link]] 提取出链
  │    （正则：\[\[([^\]]+)\]\]）
  │
  ├─ applyFolderToPage(ctx, page)
  │    └─ 从 folder_id 解析完整 folder 链，填充 category_path / wiki_path / depth
  │
  ├─ normalizeWikiHierarchy(page)
  │    └─ 规范化 category_path（清洗、去重、深度限制）
  │
  ├─ 设置 created_at / updated_at
  │
  ├─ repo.Create(ctx, page)  // 写入 DB
  │
  └─ updateInLinks(ctx, kbID, slug, outLinks)
       └─ 对每个出链目标页面，将当前 slug 加入其 in_links
```

**关键细节**：
- `WikiCategoryMaxDepth = 3`：目录路径最大深度
- 出链从 Markdown 内容中自动解析，不需要手动维护
- 入链通过反向更新维护，保证双向链接一致性

### 6.2 页面更新流程

```
UpdatePage(ctx, page)
  │
  ├─ repo.GetBySlug() → 读取现有页面 existing
  ├─ stripWikiPageInlineChunkCitations(page)
  │
  ├─ 保存 oldOutLinks = existing.OutLinks
  │
  ├─ contentChanged 判定：
  │    title / content / summary / page_type / status / aliases
  │    任一变化 → true
  │
  ├─ prev = *existing  // 保存变更前快照
  │
  ├─ 合并更新字段到 existing（title/content/summary/...）
  ├─ applyFolderToPage() → 重新计算 category_path 等缓存
  ├─ existing.OutLinks = parseOutLinks(existing.Content)
  ├─ normalizeWikiHierarchy(existing)
  │
  ├─ contentChanged == true ?
  │    ├─ Yes:
  │    │   ├─ 设置 last_edit_source / last_editor_id
  │    │   ├─ repo.UpdateWithRevision(ctx, existing, revisionFromPage(&prev))
  │    │   │    └─ 事务内：插入旧版本快照 + 更新当前页（version+1）
  │    │   └─ pruneRevisions() → 清理超出保留上限的历史版本
  │    │
  │    └─ No:
  │        └─ repo.UpdateMeta(ctx, existing)  // 只更新元数据，不递增 version
  │
  ├─ removeInLinks(ctx, kbID, slug, oldOutLinks)  // 从旧目标移除入链
  └─ updateInLinks(ctx, kbID, slug, existing.OutLinks)  // 加入新目标的入链
```

**版本递增策略**：
- 只有用户可见字段（title/content/summary/page_type/status/aliases）变化才递增 version
- 纯簿记更新（source_refs、chunk_refs、链接维护、状态同步等）走 `UpdateMeta`，不递增 version
- 这使得 version 可以作为"页面是否被真正编辑过"的信号

### 6.3 页面删除流程

```
DeletePage(ctx, kbID, slug)
  │
  ├─ repo.GetBySlug() → 获取页面
  ├─ repo.Delete(ctx, kbID, slug)  // 软删除（设置 deleted_at）
  │
  └─ 清理反向链接：
       对每个出链目标页面，从其 in_links 中移除当前 slug
```

> 删除是软删除（GORM DeletedAt）。版本历史表 `wiki_page_revisions` 的处理方式由具体业务路径决定。

### 6.4 版本历史与回滚

#### 版本列表
- 列表模式：省略 `content` 字段，仅返回元数据（节省传输）
- 单版本查询：带 `?version=N` 参数，返回完整 content

#### 回滚流程
```
RevertPageToVersion(ctx, kbID, slug, targetVersion)
  │
  ├─ repo.GetBySlug() → 当前页面
  ├─ repo.GetRevision() → 目标历史版本
  ├─ 校验目标版本不是当前版本 → ErrWikiRevertToCurrentVersion
  │
  ├─ 用目标版本内容构建更新请求（title/content/summary/page_type/aliases）
  ├─ UpdatePage(ctx, updatedPage)  // 走正常更新流程（自动创建新版本快照）
  │
  └─ 新版本的 last_edit_source = "revert"
```

> 回滚本身作为一次新编辑记录，版本号继续递增（不递减），保证历史完整可追溯。

### 6.5 文件夹管理

#### 创建文件夹
```
CreateFolder(ctx, kbID, tenantID, parentID, name)
  │
  ├─ 校验名称合法性（不含 "/" 等分隔符）
  ├─ 检查同级是否有重名 → ErrWikiFolderConflict
  ├─ 生成 UUID / 设置 path / depth（从父链推导）
  └─ repo.CreateFolder(ctx, folder)
```

#### 重命名/移动文件夹
```
RenameOrMoveFolder(ctx, kbID, id, newName, newParentID, moveParent)
  │
  ├─ 读取现有文件夹
  ├─ 重命名或重定父（仅当 moveParent=true 时改 parent_id）
  ├─ 重新计算 path / depth
  ├─ 递归更新所有子文件夹的 path / depth
  ├─ 递归更新所有子页面的 category_path / wiki_path / depth
  └─ 返回更新后的文件夹
```

#### 删除文件夹
- 仅允许删除**空文件夹**（无页面且无子文件夹）
- 非空时返回 `ErrWikiFolderNotEmpty`

#### 页面移动
```
MovePage(ctx, kbID, slug, folderID)
  │
  ├─ 读取页面
  ├─ 更新 folder_id
  ├─ applyFolderToPage() → 重新计算 category_path / wiki_path / depth
  └─ repo.UpdateMeta()（目录移动不递增版本号）
```

---

## 7. Repository 层数据持久化

实现位置：[internal/application/repository/wiki_page.go](internal/application/repository/wiki_page.go)

### 7.1 核心写入方法

| 方法 | 说明 | version 变化 |
|------|------|-------------|
| `Create(page)` | 插入新页面 | 初始化为 1 |
| `Update(page)` | 更新页面（乐观锁） | +1 |
| `UpdateWithRevision(page, rev)` | 更新 + 插入历史快照（事务） | +1 |
| `UpdateMeta(page)` | 仅更新元数据 | 不变 |
| `UpdateAutoLinkedContent(page)` | 仅自动链接装饰 | 不变 |
| `Delete(kbID, slug)` | 软删除 | - |

### 7.2 乐观锁实现

`updateWikiPageRow()` 函数实现乐观锁：

```go
result := db.Model(page).
    Where("id = ? AND version = ?", page.ID, expectedVersion).
    Updates(map[string]interface{}{...})

if result.RowsAffected == 0 {
    // 判断是不存在还是版本冲突
    if count == 0 { return ErrWikiPageNotFound }
    return ErrWikiPageConflict
}
```

写入失败时恢复 page.Version 为期望值，避免调用方观察到"写失败但版本已递增"。

### 7.3 更新列映射

Repository 使用显式列映射（`map[string]interface{}`）而非 GORM 的 struct Updates。原因：
- struct Updates 会跳过零值，导致"清空 summary"等操作不生效
- 显式映射覆盖所有 UpdatePage 修改的列，语义明确

### 7.4 原子性：更新 + 历史快照

`UpdateWithRevision` 在一个事务内完成：
1. 插入旧版本快照（`page_id + version` 唯一冲突时 DoNothing）
2. 更新当前页面（version + 1）

保证历史记录不会出现"快照了不存在的版本"或"当前版本没有对应历史"的不一致。

### 7.5 列表查询与排序

- `wikiPageListSortColumn()` 将用户输入的 `sort_by` 映射为安全的列名（防注入）
- 支持按 title / created_at / updated_at / page_type / wiki_path / sort_order / depth 排序
- `wiki_path` 排序时特殊处理：有分类路径的排前，再按 wiki_path、sort_order、title

---

## 8. AI 自动生成流程（Wiki Ingest Pipeline）

这是 Wiki 系统最核心的部分——当文档上传到知识库后，系统自动通过 LLM 提取实体、概念，生成 Wiki 页面并建立关系。

实现位置：
- [internal/application/service/wiki_ingest.go](internal/application/service/wiki_ingest.go)
- [internal/application/service/wiki_ingest_batch.go](internal/application/service/wiki_ingest_batch.go)
- [internal/application/service/wiki_ingest_taxonomy.go](internal/application/service/wiki_ingest_taxonomy.go)
- [internal/application/service/wiki_ingest_dedup.go](internal/application/service/wiki_ingest_dedup.go)
- [internal/application/service/wiki_ingest_cite.go](internal/application/service/wiki_ingest_cite.go)

### 8.1 触发入口

Wiki 生成的触发点是文档处理完成后的后处理阶段：

```
文档上传 / 重新解析
  │
  ▼
Chunk 分块完成
  │
  ▼
PostProcess（后处理）
  ├─ 摘要生成
  └─ Wiki Ingest 入队（EnqueueWikiIngest）
       └─ task_pending_ops 插入一条待处理记录
       └─ 调度 asynq 触发任务（延迟 30s，用于防抖）
```

**关键函数**：`EnqueueWikiIngest()` → `enqueueWikiIngestTrigger()`

### 8.2 任务入队机制

Wiki ingest 使用双层队列：

```
┌───────────────────────────────────────────────────┐
│  task_pending_ops（持久化待操作表）                │
│  ┌─────────────────────────────────────────────┐  │
│  │ 每个文档一条记录，task_type="wiki:ingest"    │  │
│  │ scope=knowledge_base, scope_id=kbID         │  │
│  │ op=ingest/retract                            │  │
│  │ payload: { knowledge_id, language, ... }    │  │
│  └─────────────────────────────────────────────┘  │
└──────────────────────┬────────────────────────────┘
                       │
                       ▼
┌───────────────────────────────────────────────────┐
│  asynq（Redis 任务队列）                           │
│  ┌─────────────────────────────────────────────┐  │
│  │ 触发任务：TypeWikiIngest                    │  │
│  │ Payload: { tenant_id, kb_id, language }    │  │
│  │ Queue: wiki                                │  │
│  │ 延迟 30s（防抖）                            │  │
│  └─────────────────────────────────────────────┘  │
└───────────────────────────────────────────────────┘
```

**设计原因**：
- `task_pending_ops` 是持久化的真实工作项，支持：
  - 去重（dedup_key）
  - 失败重试计数
  - 死信队列（超过最大重试次数后归档）
- asynq 任务只是"触发器"，通知 worker "该 KB 有待处理的文档了"
- 快速上传 N 个文档只会产生 N 条 pending op，但只调度 1-2 个 asynq 触发任务（防抖合并）

### 8.3 批处理核心流程

`ProcessWikiIngest()` 是批处理的入口函数，完整流程如下：

```
asynq 触发 → ProcessWikiIngest(ctx, task)
  │
  ├─ 解析 payload → {tenant_id, kb_id, language}
  ├─ 注入 tenant / language 到 ctx
  │
  ├─ Lite 模式（无 Redis）：per-KB 串行锁（liteLocks）
  │  Standard 模式（有 Redis）：支持并发，用行级 claiming 保证不重复
  │
  ├─ 校验 KB 存在且 wiki 已启用
  ├─ 获取合成模型（synthesis_model_id，回退到 summary_model_id）
  │
  ├─ Standard 模式：per-KB 并发上限（inflight cap）
  │    超过上限 → 调度延迟重试，直接返回
  │
  ├─ 加载待处理 ops（claimPendingList / peekPendingList）
  │    数量 = batchSize（默认 5，可配置）
  │    无待处理 → 调度 stale-claim 重检查，返回
  │
  ├─ 创建 batchCtx（懒加载缓存：slug→title、kid→summary）
  │
  ├─ ═══ 1. MAP 阶段 ═══（并行处理每个文档）
  │    ├─ 对 ingest 类型 op：mapOneDocument()
  │    │    ├─ 加载文档 chunks
  │    │    ├─ 提取候选实体/概念（提取 LLM 调用）
  │    │    ├─ 生成摘要页
  │    │    ├─ 去重检测
  │    │    └─ 产出 SlugUpdate 列表（待写入的页面更新）
  │    │
  │    └─ 对 retract 类型 op：mapOneRetract()
  │         └─ 产出需要移除引用的 slug 列表
  │
  ├─ ═══ 2. REDUCE 阶段 ═══（按 slug 合并后写入）
  │    ├─ 按 slug 聚合所有 map 结果
  │    ├─ 对每个 slug，加分布式锁（withSlugLock）
  │    ├─ 读取现有页面 + 合并新内容
  │    ├─ 写入页面（CreatePage / UpdatePage）
  │    └─ 记录 affectedSlugs
  │
  ├─ 清理已处理的 pending ops
  │    ├─ 成功的 → 从 task_pending_ops 删除
  │    └─ 失败的 → fail_count + 1，超过上限 → 死信归档
  │
  ├─ 写入 finalize lane（用于全局收敛）
  │    ├─ 每个 affected slug 一条
  │    ├─ 每个文档变更一条（added/removed）
  │    └─ 空文件夹候选
  │
  ├─ 调度 finalize 任务（debounced，20s 延迟）
  │
  └─ 还有未处理 ops → 调度 follow-up 任务（5s 延迟）
       否则 → 结束
```

### 8.4 Map 阶段：文档提取

每个文档独立处理，核心函数 `mapOneDocument()`（位于 wiki_ingest_batch.go）。

主要步骤：

1. **加载文档内容**：读取 chunk 文本，截断到 `maxContentForWiki = 32768` 字符

2. **候选提取（Pass 0）**：
   - 调用 LLM 提取候选实体和概念
   - 输出：entities[] + concepts[]，每项含 slug、title、aliases
   - 受 `ExtractionGranularity` 控制：
     - `focused`：仅文档主要主题
     - `standard`：主要主题 + 有实质讨论的实体/概念（默认）
     - `exhaustive`：所有命名实体和可识别概念

3. **去重预过滤**：
   - 标题相似度搜索（pg_trgm）
   - 规范化标题精确匹配
   - 标记可合并的候选

4. **Taxonomy 规划**：
   - 为每个候选分配分类路径（category_path）
   - 优先复用现有文件夹
   - 通过 `WikiTaxonomyPlanPrompt` 生成

5. **摘要页生成**：
   - 为文档生成 summary 类型页面
   - slug 格式：`summary/<knowledge_id>`

6. **Chunk 引用（Citation pass）**：
   - 为每个生成的页面标注具体引用了哪些 chunk
   - 填充 `chunk_refs` 字段

### 8.5 Reduce 阶段：页面写入

Map 阶段产出的 `SlugUpdate` 按 slug 聚合后，逐个写入。

**关键机制**：

- **Slug 级分布式锁**（`withSlugLock`）：
  - Redis key: `wiki:slug:{kbID}:{slug}`
  - TTL: 5 分钟
  - 防止并发批次同时更新同一页面导致丢失更新
  - 获取失败（等待超过 2 分钟）则跳过该 slug（下次重试）

- **Identity Claim**（`wikiIdentityClaimScript`）：
  - 解决"不同批次用不同 slug 表示同一标题"的问题
  - Redis key: `wiki:identity:{kbID}:{normalizedTitle}`
  - 原子 GET-or-SET，保证同一（类型, 标题）始终使用同一 slug

- **合并策略**：
  - 新文档内容追加/合并到现有页面
  - source_refs 和 chunk_refs 合并去重
  - 内容合并通过 LLM 完成（`merge_wiki_page` 相关提示词）

### 8.6 Finalize 阶段：全局收敛

每个 ingest 批次结束后，将受影响的 slug 写入 `wiki:finalize` 任务通道，触发延迟的全局收敛。

Finalize 任务处理内容：

1. **死链清理**（`cleanDeadLinks`）：
   - 扫描受影响页面的出链
   - 移除指向不存在/已归档页面的链接
   - 不递增 version（机器装饰，不算内容变更）

2. **交叉链接注入**（`injectCrossLinks`）：
   - 扫描页面内容中提到的其他 Wiki 页面标题/别名
   - 自动加上 `[[slug]]` 链接标记
   - 不递增 version

3. **索引页简介重建**（`rebuildIndexPage`）：
   - 重新生成 index 页的简介 intro
   - 基于近期新增/删除的文档变化描述

4. **空文件夹清理**（`PruneEmptyFolderChains`）：
   - 清理文档撤回后遗留下的空文件夹
   - 向上递归清理父级（如果也变空）

**Debounce 机制**：
- finalize 任务使用固定 TaskID：`wiki-finalize-<kbID>`
- 多个批次在 20s 窗口内调度会自动合并为一次运行
- 由 asynq 的 `ErrTaskIDConflict` 静默实现

---

## 9. 页面关系提取与 Neo4j 图谱同步

### 9.1 结构化语义关系（wiki_page_relations）

L2 本体层面的结构化关系，存储在 `wiki_page_relations` 表中。

**与 in_links/out_links 的区别**：
- `in_links/out_links`：Markdown 超链接关系（有 `[[slug]]` 就算）
- `wiki_page_relations`：结构化语义关系（有明确的 relation_type、label、方向）

**关系提取位置**：`generatePageRelations()` 函数（wiki_ingest_batch.go:1890）

提取流程：
1. 在 ingest 的合适阶段调用 LLM 关系提取提示词
2. 解析输出为 `WikiPageRelation` 结构
3. 写入 `wiki_page_relations` 表
4. 支持置信度（confidence）和版本（version）跟踪

### 9.2 Neo4j 图谱写入

Neo4j 图谱数据来自文档处理阶段的实体-关系提取（不是直接从 Wiki 页面同步）。

实现位置：[internal/application/repository/retriever/neo4j/repository.go](internal/application/repository/retriever/neo4j/repository.go)

#### 写入流程
```
AddGraph(ctx, namespace, graphs)
  │
  ├─ kbLabel = "ENTITY_<kb_id>"  // 知识库级标签
  │
  ├─ 节点写入（apoc.merge.node）
  │    └─ 匹配键：labels + name
  │    └─ 合并属性：attributes / chunks（union 去重）
  │    └─ 同一 KB 内同名实体会合并为一个节点
  │
  └─ 关系写入（apoc.merge.relationship）
       └─ 源/目标节点同样按 name 合并
       └─ 按 type + 方向合并关系
```

**关键设计决策**：
- 节点标签使用知识库级标签（`ENTITY_<kb_id>`），支持 KB 级隔离
- 同一 KB 内同名实体合并（原逻辑用所有标签+name+kg 匹配会导致重复节点）
- chunks 和 attributes 采用 union 合并去重
- 删除文档时按 knowledge_id 删除对应节点和关系（`DelGraph`）

#### 命名空间
```go
type NameSpace struct {
    KnowledgeBase string  // 知识库 ID
    Knowledge     string  // 文档 ID
    Labels        []string // 实体类型标签
}
```

### 9.3 Neo4j 图谱查询与可视化

#### 查询服务层
实现位置：[internal/application/service/graph_neo4j.go](internal/application/service/graph_neo4j.go)

| 方法 | 说明 | 默认 limit | Max limit |
|------|------|-----------|-----------|
| `GetOverview()` | 总览模式：按 degree 取 Top-N 节点 | 200 | 1000 |
| `GetEgoGraph()` | Ego 模式：以某节点为中心的 BFS 子图 | 不截断 | 1000 |
| `SearchNodes()` | 按名称模糊搜索节点 | 20 | 100 |
| `GetStats()` | 图谱统计（节点数、关系数等） | - | - |
| `GetRelationTypes()` | 所有关系类型列表 | - | - |
| `GetNodeDetail()` | 单节点详情（属性、关系分布、Top 邻居） | - | - |

#### Handler 层
实现位置：[internal/handler/graph_neo4j.go](internal/handler/graph_neo4j.go)

路由注册在 `RegisterNeo4jGraphRoutes()`（routes_knowledge.go:344）。

#### 前端可视化
实现位置：[frontend/src/views/knowledge/wiki/Neo4jGraphViewer.vue](frontend/src/views/knowledge/wiki/Neo4jGraphViewer.vue)

主要功能：
- 力导向图可视化（基于 D3.js / 自研 SVG）
- 两种模式切换：Overview（全局）和 Ego（中心节点扩展）
- 节点搜索与定位
- 关系类型筛选
- 节点详情面板
- 点击节点跳转 Wiki 页面

---

## 10. Agent 工具集（Wiki Tools）

Agent 在对话中可以通过工具直接操作 Wiki 页面。

实现位置：[internal/agent/tools/](internal/agent/tools/) 目录

### 10.1 工具清单

| 工具名 | 文件 | 说明 |
|--------|------|------|
| `wiki_write_page` | wiki_write_page.go | 创建或完全覆盖页面 |
| `wiki_replace_text` | wiki_replace_text.go | 替换页面中的部分文本 |
| `wiki_delete_page` | wiki_delete_page.go | 删除页面 |
| `wiki_rename_page` | wiki_rename_page.go | 重命名页面（改 slug） |
| `wiki_flag_issue` | wiki_flag_issue.go | 标记页面问题 |
| `wiki_read_issue` | wiki_read_issue.go | 读取页面问题列表 |
| `wiki_update_issue` | wiki_update_issue.go | 更新问题状态 |
| `wiki_link_mutation` | wiki_link_mutation.go | 链接增删维护 |
| `wiki_read_source_doc` | wiki_read_source_doc.go | 读取来源文档 |
| `wiki_index_overview` | - | 索引浏览 |

### 10.2 工具调用流程

```
Agent 对话
  │
  ├─ LLM 决定调用 wiki_write_page
  │
  ├─ 工具执行（Execute）
  │    ├─ ctx = WithWikiEditSource(Agent)  // 标记来源
  │    ├─ 参数解析与校验
  │    ├─ KB 范围校验（scopeEnforced）
  │    ├─ 调用 wikiPageService.CreatePage/UpdatePage
  │    └─ 返回 ToolResult
  │
  └─ LLM 基于结果继续对话
```

### 10.3 工具写入的特殊性

- `last_edit_source = "agent"`：版本历史中区分 Agent 编辑
- 受搜索范围（SearchTargets）约束：Agent 只能引用它能检索到的源文档
- 自动链接修复：`RepairContentLinks()` 自动修正 LLM 可能写错的 slug

---

## 11. 并发控制与一致性保障

Wiki 系统在高并发下的一致性通过多层机制保障：

### 11.1 数据库层面

- **乐观锁**：页面更新使用 `version` 字段做乐观并发控制
- **事务**：页面更新 + 历史快照写入在同一事务中
- **唯一索引**：`(knowledge_base_id, slug)` 联合唯一，防止重名

### 11.2 Redis 分布式锁

| 锁名称 | Key 格式 | 保护对象 | TTL |
|--------|---------|---------|-----|
| Slug Lock | `wiki:slug:{kbID}:{slug}` | 单页面读写 | 5 min |
| Identity Claim | `wiki:identity:{kbID}:{normalizedTitle}` | 标题→slug 映射一致性 | 2 hr |
| Inflight Slot | `wiki:inflight:{kbID}`（sorted set） | per-KB 并发批次数量 | 90 s |
| Finalize Lock | -（TaskID 去重） | 全局收敛运行次数 | - |

### 11.3 行级 Claiming（Standard 模式）

Redis 模式下，多个批次可以并发处理同一 KB：
- `claimPendingList()` 声明式领取待处理行
- 使用 `FOR UPDATE SKIP LOCKED`（或 claimed_at 时间戳）保证不重复领取
- 异常退出时自动释放 claim（defer 清理）
- 超过 `wikiClaimStaleAfter = 90 min` 的 stale claim 可被重新领取

### 11.4 Per-KB 并发上限

- 通过 Redis sorted set 实现：每个运行批次占一个 slot（score = 过期时间）
- 上限可配置（`IngestMaxInflight`，默认 4）
- 防止单个 KB 的大量导入占满整个 worker 池

### 11.5 Lite 模式（无 Redis）

- 单进程内存锁 `liteLocks`（sync.Map）
- 每 KB 串行执行
- 适合小规模/本地部署

---

## 12. 关键配置项与调优参数

所有配置位于 `WikiConfig` 结构体，通过知识库的 wiki 设置存储。

### 12.1 模型与内容配置

| 配置项 | 默认值 | 说明 |
|--------|-------|------|
| `SynthesisModelID` | 回退到 SummaryModelID | Wiki 页面生成/更新使用的 LLM 模型 |
| `MaxPagesPerIngest` | 0（不限制） | 每次摄入最多创建/更新页面数 |
| `ExtractionGranularity` | `standard` | 实体提取粒度：focused / standard / exhaustive |
| `ContentInstructions` | "" | 生成内容的语气/结构/侧重说明 |
| `ExtractionInstructions` | "" | 候选提取的领域概念侧重说明 |

### 12.2 并发与吞吐配置

| 配置项 | 默认值 | 说明 |
|--------|-------|------|
| `IngestBatchSize` | 5 | 单批次处理文档数 |
| `IngestMapParallel` | 10 | Map 阶段并行度（errgroup limit） |
| `IngestReduceParallel` | 10 | Reduce 阶段并行度 |
| `IngestMaxInflight` | 4 | 同一 KB 最大并发批次数 |

**峰值 LLM 并发估算**：
```
单 KB 峰值 ≈ IngestMaxInflight × max(IngestMapParallel, IngestReduceParallel)
默认 ≈ 4 × 10 = 40 并发 LLM 调用
```

### 12.3 时序参数

| 参数 | 值 | 说明 |
|------|-----|------|
| `wikiIngestDelay` | 30s | 首触发延迟（防抖） |
| `wikiFollowUpDelay` | 5s | 后续批次间隔 |
| `wikiFinalizeDelay` | 20s | Finalize 延迟（合并窗口） |
| `wikiRateLimitBackoff` | 60s | 上游限流时的重试间隔 |
| `wikiClaimStaleAfter` | 90min | Stale claim 可回收时间 |
| `wikiSlugLockTTL` | 5min | Slug 锁 TTL |
| `wikiMaxFailRetries` | 5 次 | 单文档最大失败重试次数 |

### 12.4 图谱查询限制

| 参数 | 默认值 | 上限 | 说明 |
|------|-------|------|------|
| wikiGraphDefaultLimit | 500 | 2000 | Wiki 内链图谱默认节点数 |
| wikiGraphMaxDepth | 3 | - | Wiki 图谱 Ego 最大深度 |
| neo4jGraphDefaultLimit | 200 | 1000 | Neo4j 图谱默认节点数 |
| neo4jGraphMaxDepth | 3 | - | Neo4j Ego 最大深度 |

---

## 附录：关键文件索引

| 层级 | 文件路径 | 主要内容 |
|------|---------|---------|
| 类型 | [internal/types/wiki_page.go](internal/types/wiki_page.go) | WikiPage / Revision / Folder / Relation / Config 等核心类型 |
| 接口 | [internal/types/interfaces/wiki_page.go](internal/types/interfaces/wiki_page.go) | WikiPageService / WikiPageRepository 接口定义 |
| 路由 | [internal/router/routes_knowledge.go](internal/router/routes_knowledge.go) | Wiki + Neo4j 路由注册 |
| Handler | [internal/handler/wiki_page.go](internal/handler/wiki_page.go) | Wiki HTTP 请求处理 |
| Handler | [internal/handler/graph_neo4j.go](internal/handler/graph_neo4j.go) | Neo4j 图谱 HTTP 请求处理 |
| Service | [internal/application/service/wiki_page.go](internal/application/service/wiki_page.go) | 页面 CRUD / 链接 / 文件夹 / 版本 |
| Service | [internal/application/service/wiki_ingest.go](internal/application/service/wiki_ingest.go) | Wiki ingest 服务定义 / 常量 / 工具函数 |
| Service | [internal/application/service/wiki_ingest_batch.go](internal/application/service/wiki_ingest_batch.go) | 批处理主流程 / Map / Reduce |
| Service | [internal/application/service/wiki_ingest_taxonomy.go](internal/application/service/wiki_ingest_taxonomy.go) | 分类规划 |
| Service | [internal/application/service/wiki_ingest_dedup.go](internal/application/service/wiki_ingest_dedup.go) | 去重逻辑 |
| Service | [internal/application/service/wiki_ingest_cite.go](internal/application/service/wiki_ingest_cite.go) | Chunk 引用标注 |
| Service | [internal/application/service/wiki_linkify.go](internal/application/service/wiki_linkify.go) | 交叉链接注入 / 死链清理 |
| Service | [internal/application/service/wiki_lint.go](internal/application/service/wiki_lint.go) | Wiki 健康检查 |
| Service | [internal/application/service/graph_neo4j.go](internal/application/service/graph_neo4j.go) | Neo4j 图谱查询服务 |
| Repository | [internal/application/repository/wiki_page.go](internal/application/repository/wiki_page.go) | Wiki 页面数据访问 |
| Repository | [internal/application/repository/retriever/neo4j/repository.go](internal/application/repository/retriever/neo4j/repository.go) | Neo4j 读写 |
| Agent 提示词 | [internal/agent/prompts_wiki.go](internal/agent/prompts_wiki.go) | Wiki 相关 LLM 提示词 |
| Agent 工具 | [internal/agent/tools/wiki_*.go](internal/agent/tools/) | Agent Wiki 工具集 |
| 前端 API | [frontend/src/api/wiki/index.ts](frontend/src/api/wiki/index.ts) | Wiki 前端 API 封装 |
| 前端组件 | [frontend/src/views/knowledge/wiki/WikiBrowser.vue](frontend/src/views/knowledge/wiki/WikiBrowser.vue) | Wiki 浏览器主组件 |
| 前端组件 | [frontend/src/views/knowledge/wiki/Neo4jGraphViewer.vue](frontend/src/views/knowledge/wiki/Neo4jGraphViewer.vue) | Neo4j 图谱可视化 |

---

## 附录 A：Wiki Ingest Map 阶段详解

### A.1 单文档处理流程（mapOneDocument）

输入：一个文档的 chunks  
输出：`docIngestResult` + `[]SlugUpdate`

```
mapOneDocument(ctx, op, batchCtx, chatModel)
  │
  ├─ 1. 竞态检查：文档是否已被删除（Redis tombstone）
  │    已删除 → 直接返回，标记为 retract 处理
  │
  ├─ 2. 加载文档 chunks
  │    └─ 截断到 maxContentForWiki = 32768 runes
  │
  ├─ 3. Pass 0：候选 slug 提取
  │    └─ LLM 调用：WikiCandidateSlugPrompt
  │       ├─ 输入：文档标题 + 截断内容
  │       ├─ 输出：entities[] + concepts[]
  │       │    每项含：name, slug, aliases, description, details
  │       └─ 失败回退：WikiKnowledgeExtractPrompt（旧版单步提取器）
  │
  ├─ 4. 并行子任务（errgroup）：
  │    ├─ 4a. 摘要生成 (WikiSummaryPrompt)
  │    │    ├─ 输入：文档全文
  │    │    └─ 输出：SUMMARY 行 + Markdown 摘要正文
  │    │
  │    └─ 4b. Chunk 引用分类 (WikiChunkCitationPrompt)
  │         ├─ 输入：候选 slugs + chunk 内容列表
  │         └─ 输出：每个 slug 对应哪些 chunk 作为支撑证据
  │
  ├─ 5. 合并 chunk 引用到候选条目
  │
  └─ 6. 组装 SlugUpdate 列表
       ├─ summary 类型：每个文档 1 条（slug = summary/<knowledge_id>）
       ├─ entity 类型：每个提取的实体 1 条
       └─ concept 类型：每个提取的概念 1 条
```

### A.2 去重预过滤（Dedup Pre-filter）

在 Map 阶段的候选提取之后，对每个候选进行去重检查：

- **pg_trgm 相似度搜索**：基于标题相似度查找可能重复的现有页面
- **规范化标题精确匹配**：大小写折叠、空白去除后的精确匹配
- 只做"标记候选合并目标"，真正的合并在 Reduce 阶段通过 LLM 完成

### A.3 SlugUpdate 数据结构

```go
type SlugUpdate struct {
    Slug          string   // 目标页面 slug
    PageType      string   // summary / entity / concept
    Title         string   // 页面标题
    Aliases       []string // 新增别名
    Summary       string   // 摘要（仅 summary 类型）
    Content       string   // 正文内容（实体/概念的详情）
    SourceRef     string   // 来源文档引用
    ChunkRefs     []string // 支撑 chunk IDs
    CategoryPath  []string // 建议分类路径（taxonomy 阶段填充）
    Op            string   // "add" 或 "remove"
    KnowledgeID   string   // 来源文档 ID
}
```

---

## 附录 B：Wiki Ingest Reduce 阶段详解

### B.1 按 Slug 聚合

Map 阶段产出的所有 `SlugUpdate` 按 slug 分组：
- 同一 slug 可能有来自多个文档的 add 更新
- 同一 slug 可能同时有 add 和 remove（不同文档）

### B.2 单 Slug 处理流程（reduceSlugUpdates）

```
reduceSlugUpdates(ctx, slug, updates, batchCtx, chatModel)
  │
  ├─ 1. 过滤已删除文档的更新
  │
  ├─ 2. 分布式锁：withSlugLock(slug)
  │    获取失败 → 将关联文档重新入队，跳过本次
  │
  ├─ 3. 读取或创建页面
  │    ├─ 存在 → 加载现有页面
  │    └─ 不存在 → 创建 draft 状态的新页面
  │
  ├─ 4. 按类型分支处理：
  │    │
  │    ├─ [Summary 类型]
  │    │   ├─ 直接设置 title / content / source_refs
  │    │   └─ 写入数据库（Create/Update）
  │    │
  │    ├─ [Entity/Concept Add 类型]
  │    │   ├─ 收集所有源文档上下文 + 引用 chunk 内容
  │    │   ├─ 收集已有页面内容 + 已有 out_links
  │    │   ├─ 构造 WikiEditorPrompt
  │    │   │   ├─ 已有页面内容
  │    │   │   ├─ 新文档及其摘要
  │    │   │   ├─ 被删除文档（如有）
  │    │   │   └─ 现有 wiki 链接列表（辅助 LLM 正确引用）
  │    │   ├─ LLM 调用：重新生成整合后的页面内容
  │    │   ├─ 合并 source_refs、chunk_refs、aliases（去重）
  │    │   ├─ 应用 taxonomy 规划的分类（新页面且未分类时）
  │    │   └─ 写入数据库
  │    │
  │    └─ [Retract 类型]
  │        ├─ 从 source_refs 中移除该文档
  │        ├─ 从 chunk_refs 中移除该文档的 chunks
  │        ├─ LLM 调用：重写不包含该文档的页面内容
  │        └─ 写入数据库
  │
  └─ 5. 返回变更结果（用于 finalize 阶段追踪）
```

### B.3 Wiki 编辑器 LLM 提示词（WikiEditorPrompt）

这是 Reduce 阶段的核心 LLM 调用，负责将新文档信息整合到现有 Wiki 页面中：

- **输入**：
  - 现有页面完整内容
  - 新文档列表（标题 + 摘要 + 引用 chunk）
  - 删除文档列表（标题 + 摘要）
  - 现有 Wiki 页面链接列表（slug → title 映射）
- **输出**：
  - 整合后的完整 Markdown 页面内容
  - 更新后的 aliases
  - 更新后的一句话摘要

### B.4 并发度控制

- Reduce 阶段也有独立的并发度配置（`IngestReduceParallel`，默认 10）
- 与 Map 阶段并发度分开配置，因为两者的 LLM 调用特征不同
- 加上 per-slug 锁，实际并发度可能低于配置值（热点 slug 会串行）

---

## 附录 C：Finalize 阶段详解

### C.1 Finalize Lane 结构

Finalize 操作也存储在 `task_pending_ops` 表中，但使用独立的 `task_type="wiki:finalize"`。

有三种 finalize 操作类型（`op` 字段）：

| op | 用途 | payload 内容 | dedup_key |
|----|------|-------------|-----------|
| `slug` | 受影响页面（死链清理+交叉链接） | `{slug, title}` | slug |
| `change` | 文档增减变化（索引页介绍） | `{action, doc_title, doc_summary}` | 空（每条独立） |
| `folder_prune` | 可能变空的目录 | `{folder_ids: []}` | 空 |

### C.2 Finalize 处理流程

```
ProcessWikiFinalize(ctx, task)
  │
  ├─ 1. 加载 finalize lane 中最多 wikiFinalizeMaxRows 条
  │
  ├─ 2. 按 op 类型分组聚合
  │    ├─ slugs：所有受影响的 slug + title
  │    ├─ changes：所有文档增减记录
  │    └─ folder_ids：所有待检查空目录
  │
  ├─ 3. 死链清理 (cleanDeadLinks)
  │    ├─ 扫描每个受影响页面的 out_links
  │    ├─ 检查每个目标 slug 是否存在（ExistsSlugs 批量查询）
  │    ├─ 不存在的 → 从 content 中移除 [[slug]] 标记
  │    └─ UpdateAutoLinkedContent（不递增 version）
  │
  ├─ 4. 交叉链接注入 (injectCrossLinks)
  │    ├─ 收集所有其他 Wiki 页面的标题+别名
  │    ├─ 扫描每个受影响页面的正文
  │    ├─ 匹配到的标题/别名 → 用 [[slug|name]] 包裹
  │    └─ UpdateAutoLinkedContent（不递增 version）
  │
  ├─ 5. 索引页介绍重建 (rebuildIndexPage)
  │    ├─ 收集所有增减变更描述
  │    ├─ 调用 LLM 重写 index 页的 intro 段落
  │    └─ UpdateAutoLinkedContent（不递增 version）
  │
  ├─ 6. 实体关系提取 (generatePageRelations)
  │    ├─ 收集新增实体页面的两两组合
  │    ├─ 查询已有关系（去重）
  │    ├─ 并行 LLM 调用提取结构化关系（并发度 5）
  │    └─ 批量写入 wiki_page_relations 表
  │
  ├─ 7. 空目录清理 (PruneEmptyFolderChains)
  │    └─ 从 leaf 向 root 递归删除空目录
  │
  └─ 8. 如果还有剩余 finalize 行 → 调度下一次 finalize
```

### C.3 Debounce 与 Coalescing

- Finalize 触发任务使用固定 TaskID：`wiki-finalize-<kbID>`
- asynq 的 `ErrTaskIDConflict` 静默合并：多个批次在 20s 窗口内调度只产生一个 finalize 任务
- 这使得批量导入 N 个文档，最终只重建一次索引页、做一次死链清理

---

## 附录 D：Wiki 两种图谱对比

Wiki 系统中存在**两套独立的图谱**，容易混淆，在此明确区分：

| 维度 | Wiki 链接图谱 | Neo4j 知识图谱 |
|------|-------------|---------------|
| **数据来源** | Wiki 页面 Markdown 内容中的 `[[slug]]` 超链接 | Chunk 级别的 NLP 实体-关系抽取 |
| **节点** | Wiki 页面（以 slug 标识） | 实体节点（以 name 标识，带 attributes/chunks） |
| **边** | 页面引用关系（有向，无类型） | 语义关系（有向，有类型+标签+描述+置信度） |
| **存储** | MySQL `wiki_pages` 表的 in_links/out_links JSON 字段 | Neo4j 图数据库 |
| **触发时机** | 每次页面创建/更新时自动解析 | Chunk 后处理的 extract_graph 任务 |
| **API 路径** | `/wiki/graph` | `/graph/neo4j` |
| **前端组件** | WikiBrowser.vue 内的图视图 | Neo4jGraphViewer.vue（独立组件） |
| **查询模式** | overview（Top-N 连接度）/ ego（BFS 邻居） | overview（Top-N degree）/ ego（BFS 邻居） |
| **默认节点上限** | 500 | 200 |
| **最大节点上限** | 2000 | 1000 |
| **最大 Ego 深度** | 3 | 3 |

### D.1 两套图谱的关系

- **wiki_page_relations 表**是中间层：结构化的页面间语义关系（比纯超链接更丰富，比 Neo4j 更轻量）
- Wiki 页面 ↔ Neo4j 节点之间没有直接的外键关联，但可以通过**实体名称**间接对应
- 未来可能的演进方向：将 Neo4j 图谱与 Wiki 页面建立更紧密的映射

---

## 附录 E：启动恢复机制

### E.1 恢复流程

实现位置：[internal/container/recover_pending_wiki_tasks.go](internal/container/recover_pending_wiki_tasks.go)

服务启动时，从 `task_pending_ops` 表恢复未完成的 Wiki 任务：

```
服务启动
  │
  ▼
RecoverPendingWikiTasks()
  │
  ├─ 1. 清理已被删除的 KB 的所有 pending 行
  │
  ├─ 2. 查询所有有 pending 行的 (tenant_id, task_type, scope_id) 组合
  │
  ├─ 3. 对每个组合重建 asynq trigger 任务
  │    ├─ wiki:ingest → 入队 TypeWikiIngest
  │    └─ wiki:finalize → 入队 TypeWikiFinalize（带固定 TaskID）
  │
  └─ 4. 重复 trigger 是安全的：
       ├─ ingest：通过行级 claim 机制天然去重（谁 claim 到谁处理）
       └─ finalize：通过 asynq TaskID 合并（同 TaskID 只保留一个）
```

### E.2 崩溃安全

- **Standard 模式**：已 claim 但未处理的行，超过 `wikiClaimStaleAfter = 90 min` 后可被重新 claim
- **Lite 模式**：服务重启后锁自动释放（内存锁），所有 pending 行重新可被 peek
- **Finalize**：固定 TaskID + debounce 机制，重启后自然重新调度

---

## 附录 F：失败与重试机制

### F.1 多层重试架构

```
┌─────────────────────────────────────────────┐
│  LLM 调用内重试（wikiLLMMaxAttempts = 3）    │
│  指数退避：2s → 4s → 8s                     │
│  处理：瞬时 504 / 超时 / 网络抖动            │
└──────────────────┬──────────────────────────┘
                   ▼
┌─────────────────────────────────────────────┐
│  文档级重试（wikiMaxFailRetries = 5）        │
│  失败的 op 重新入队 task_pending_ops        │
│  处理：LLM 输出解析失败、内容有问题等        │
└──────────────────┬──────────────────────────┘
                   ▼
┌─────────────────────────────────────────────┐
│  死信队列（task_dead_letters）               │
│  超过 5 次失败 → 永久归档，供人工排查        │
└─────────────────────────────────────────────┘

┌─────────────────────────────────────────────┐
│  asynq trigger 级重试（wikiIngestMaxRetry = 10） │
│  处理：DB 临时故障、Redis 临时故障等         │
└─────────────────────────────────────────────┘
```

### F.2 限流退避

- 当任何 LLM 失败看起来是上游 429/配额限制时，设置 `rateLimited = true`
- follow-up 任务从正常的 5s 间隔改为 `wikiRateLimitBackoff = 60s`
- 避免在限流窗口内反复重试，持续打满限流预算

---

## 附录 G：提示词清单

所有 Wiki 相关 LLM 提示词定义在 [internal/agent/prompts_wiki.go](internal/agent/prompts_wiki.go)

| 提示词 | 阶段 | 用途 |
|--------|------|------|
| `WikiKnowledgeExtractPrompt` | Map（回退） | 旧版单步实体+概念提取 |
| `WikiCandidateSlugPrompt` | Map Pass 0 | 轻量候选 slug 提取 |
| `WikiSummaryPrompt` | Map | 文档摘要页生成 |
| `WikiChunkCitationPrompt` | Map | Chunk 引用分类 |
| `WikiEditorPrompt` | Reduce | 多文档内容整合重写 |
| `WikiMergePrompt` | Reduce | 去重合并（两页合并） |
| `WikiTaxonomyPlanPrompt` | Taxonomy | 目录分类规划 |
| `WikiRelationExtractPrompt` | Finalize | 实体关系提取 |
| `WikiIndexIntroPrompt` | Finalize | 索引页介绍生成 |
| `WikiLintPrompt` | Lint | Wiki 健康检查 |

---

## 附录 H：文件索引补充

### 后处理触发入口
- [internal/application/service/knowledge_post_process_wiki_enqueue.go](internal/application/service/knowledge_post_process_wiki_enqueue.go) — 文档后处理中 wiki 入队
- [internal/application/service/extract.go](internal/application/service/extract.go) — Chunk 级图谱抽取（Neo4j 触发）

### 工具与辅助
- [internal/application/service/wiki_slug_handles.go](internal/application/service/wiki_slug_handles.go) — Slug 句柄转义（防 LLM 篡改 UUID slug）
- [internal/agent/tools/wiki_route_resolver.go](internal/agent/tools/wiki_route_resolver.go) — 多 KB 路由解析
- [internal/handler/session/wiki_fixer_scope.go](internal/handler/session/wiki_fixer_scope.go) — Session 中 wiki fixer 的作用域

### 启动/容器
- [internal/container/recover_pending_wiki_tasks.go](internal/container/recover_pending_wiki_tasks.go) — 启动恢复 pending wiki 任务
- [internal/container/container.go](internal/container/container.go) — 依赖注入容器

### 测试
- [internal/application/service/wiki_ingest_test.go](internal/application/service/wiki_ingest_test.go)
- [internal/application/service/wiki_page_test.go](internal/application/service/wiki_page_test.go)
- [internal/application/service/wiki_ingest_batch.go](internal/application/service/wiki_ingest_batch.go) 内的自测用例
- [internal/router/router_wiki_test.go](internal/router/router_wiki_test.go)

---

> **文档版本**：v1.1
> **最后更新**：2026-09-20
> **基于分支**：pg-zzh
