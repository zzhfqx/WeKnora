# 前端 API 关键查询操作记录

> 记录 WeKnora 前端各模块 API 的分布位置，以及与 Wiki 创建、检索相关的所有接口清单，方便快速定位。

---

## 一、前端 API 文件分布

前端 API 按模块分散定义，**并非全部集中在一个文件**。核心文件如下：

| API 文件路径 | 涵盖模块 |
|---|---|
| `frontend/src/api/knowledge-base/index.ts` | 知识库 CRUD、知识文档上传/解析/分块、标签、FAQ、全局知识搜索 |
| `frontend/src/api/wiki/index.ts` | Wiki 页面、目录、版本、图谱、搜索（独立文件） |
| `frontend/src/api/agent/index.ts` | Agent 相关接口 |
| `frontend/src/api/chat-history.ts` | 对话历史 + 消息搜索 |
| `frontend/src/api/retrieval.ts` | 全局检索配置 |
| `frontend/src/api/tenant/index.ts` | 租户管理 |
| `frontend/src/api/organization/index.ts` | 组织管理 |

---

## 二、Wiki 相关接口清单

所有 Wiki API 定义在 `frontend/src/api/wiki/index.ts`，共 **21 个**。

### 2.1 创建 / 编辑类（10 个）

| 函数名 | 方法 + 路径 | 说明 |
|---|---|---|
| `createWikiPage(kbId, data)` | `POST /api/v1/knowledgebase/{kbId}/wiki/pages` | 手动创建 Wiki 页面 |
| `updateWikiPage(kbId, slug, data)` | `PUT /api/v1/knowledgebase/{kbId}/wiki/pages/{slug}` | 更新页面（含乐观锁 version） |
| `deleteWikiPage(kbId, slug)` | `DELETE /api/v1/knowledgebase/{kbId}/wiki/pages/{slug}` | 删除页面 |
| `moveWikiPage(kbId, slug, folderId)` | `PUT /api/v1/knowledgebase/{kbId}/wiki/move-page` | 移动页面到目录 |
| `revertWikiPage(kbId, slug, version)` | `POST /api/v1/knowledgebase/{kbId}/wiki/revert` | 回滚到历史版本 |
| `rebuildWikiLinks(kbId)` | `POST /api/v1/knowledgebase/{kbId}/wiki/rebuild-links` | 重建全站链接 |
| `createWikiFolder(kbId, parentId, name)` | `POST /api/v1/knowledgebase/{kbId}/wiki/folders` | 创建目录 |
| `updateWikiFolder(kbId, folderId, data)` | `PUT /api/v1/knowledgebase/{kbId}/wiki/folders/{folderId}` | 更新/重命名目录 |
| `deleteWikiFolder(kbId, folderId)` | `DELETE /api/v1/knowledgebase/{kbId}/wiki/folders/{folderId}` | 删除空目录 |
| `updateWikiIssueStatus(kbId, issueId, status)` | `PUT /api/v1/knowledgebase/{kbId}/wiki/issues/{issueId}/status` | 更新质量问题状态 |

### 2.2 检索 / 查询类（11 个）

| 函数名 | 方法 + 路径 | 说明 |
|---|---|---|
| `searchWikiPages(kbId, q, limit?)` | `GET /api/v1/knowledgebase/{kbId}/wiki/search?q=xxx` | **Wiki 页面搜索**（正则全文） |
| `getWikiPage(kbId, slug)` | `GET /api/v1/knowledgebase/{kbId}/wiki/pages/{slug}` | 获取单页详情 |
| `listWikiPages(kbId, params?)` | `GET /api/v1/knowledgebase/{kbId}/wiki/pages` | 分页列表 + 筛选 + 搜索 |
| `getWikiIndex(kbId, params?)` | `GET /api/v1/knowledgebase/{kbId}/wiki/index` | Wiki 索引页（结构化目录） |
| `getWikiGraph(kbId, params?)` | `GET /api/v1/knowledgebase/{kbId}/wiki/graph` | 链接图谱（overview/ego 模式） |
| `getWikiStats(kbId)` | `GET /api/v1/knowledgebase/{kbId}/wiki/stats` | Wiki 统计信息 |
| `listWikiFolders(kbId, parentId?, pageTypes?)` | `GET /api/v1/knowledgebase/{kbId}/wiki/folders` | 目录列表（树状） |
| `listWikiRevisions(kbId, slug, params?)` | `GET /api/v1/knowledgebase/{kbId}/wiki/revisions/{slug}` | 版本历史列表 |
| `getWikiRevision(kbId, slug, version)` | `GET /api/v1/knowledgebase/{kbId}/wiki/revisions/{slug}?version=` | 单版本详情 |
| `listWikiIssues(kbId, slug?, status?)` | `GET /api/v1/knowledgebase/{kbId}/wiki/issues` | 质量问题列表 |

### 2.3 相关数据结构

| 类型名 | 说明 |
|---|---|
| `WikiPage` | Wiki 页面完整结构 |
| `WikiPageListResponse` | 分页列表响应 |
| `WikiFolder` / `WikiFolderNode` | 目录节点 |
| `WikiFolderListResponse` | 目录列表响应 |
| `WikiGraphData` / `WikiGraphMeta` | 链接图谱数据 |
| `WikiStats` | 统计信息 |
| `WikiPageIssue` | 页面质量问题 |
| `WikiPageRevision` | 页面历史版本 |
| `WikiRevisionListResponse` | 版本列表响应 |
| `WikiIndexResponse` / `WikiIndexGroup` | 结构化索引页 |
| `WikiPageUpdatePayload` | 页面更新 payload（含乐观锁） |

---

## 三、知识库层面与 Wiki 相关的接口

定义在 `frontend/src/api/knowledge-base/index.ts`，在 KB 创建/配置时决定 Wiki 能力。

### 3.1 KB 创建与配置

| 函数名 | 方法 + 路径 | Wiki 相关字段 |
|---|---|---|
| `createKnowledgeBase(data)` | `POST /api/v1/knowledge-bases` | `wiki_config`（合成模型、粒度、指令等）<br>`indexing_strategy.wiki_enabled`（是否启用 Wiki 索引） |
| `updateKnowledgeBase(id, data)` | `PUT /api/v1/knowledge-bases/{id}` | `config.wiki_config`<br>`config.indexing_strategy.wiki_enabled` |
| `getKnowledgeBaseById(id)` | `GET /api/v1/knowledge-bases/{id}` | 返回 KB 的 wiki_config 和索引策略 |
| `rebuildKBIndex(kbId)` | `POST /api/v1/knowledge-bases/{kbId}/rebuild-index` | 重建索引（含 Wiki 索引） |

### 3.2 知识检索接口

| 函数名 | 方法 + 路径 | 说明 |
|---|---|---|
| `knowledgeSemanticSearch(data)` | `POST /api/v1/knowledge-search` | 跨 KB 语义搜索（全局命令面板使用） |
| `searchKnowledge(keyword, ...)` | `GET /api/v1/knowledge/search` | 知识文件关键字搜索 |
| `searchFAQEntries(kbId, data)` | `POST /api/v1/knowledge-bases/{kbId}/faq/search` | FAQ 搜索 |

> 💡 **注意**：前端没有直接封装 `/api/v1/knowledge-bases/{id}/hybrid-search` 的函数。
> 混合搜索（向量+关键词 RRF 融合）主要在后端 Agent 管道内部调用，前端通过以下方式间接使用：
> - 聊天对话（Agent 自主调用工具检索）
> - 全局命令面板 `knowledgeSemanticSearch`（跨库语义搜索）
