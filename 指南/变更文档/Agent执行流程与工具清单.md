# Agent 执行流程与工具清单

> 本文档精简记录 WeKnora Agent 的核心执行流程，以及前端展示的所有工具的实现状态（已实现/未实现/伪实现）。

---

## 一、核心执行流程

### 1.1 总体架构

```
用户发消息 → HTTP 入口 → Handler → Service → AgentEngine (ReAct 循环) → SSE 流式返回
```

### 1.2 入口链路

```
POST /api/v1/chat/agent/:session_id
  └─ Handler.AgentQA()                      [handler/session/qa.go]
      └─ sessionService.AgentQA()           [service/session_agent_qa.go]
          ├─ 构建 AgentConfig（CustomAgent + 租户信息）
          ├─ agentService.CreateAgentEngine()
          └─ engine.Execute()               [agent/engine.go]
```

### 1.3 ReAct 核心循环

Agent 采用标准 **ReAct（Reasoning + Acting）** 模式，由 `AgentEngine` 驱动：

```
engine.Execute()
  ├─ buildSystemPrompt()          — 组装系统提示词
  ├─ buildToolsForLLM()           — 组装可用工具列表
  └─ executeLoop()                — 循环（默认最大 100 轮）
       └─ runReActIteration()
           ├─ ① Think:  callLLMWithRetry()    LLM 流式输出（function calling）
           ├─ ② Observe: analyzeResponse()    判断是否停止（无 tool_call 即结束）
           ├─ ③ Act:  executeToolCalls()      执行工具调用（支持并行，最多8并发）
           └─ ④ Observe: appendToolResults()  工具结果追加到消息，继续下一轮
  └─ streamFinalAnswerToEventBus() — 最终答案通过 EventBus → SSE 推给前端
```

### 1.4 工具调用链路

```
LLM 返回 tool_calls
  └─ executeToolCalls()                     [agent/act.go]
      ├─ 参数 JSON 解析 + repair
      ├─ MCP 工具代理解析
      ├─ 触发 UI 进度事件
      └─ toolRegistry.ExecuteTool(name, args)  [agent/tools/registry.go]
          ├─ GetTool(name) — 查表
          ├─ CastParams — 类型修正
          ├─ ValidateParams — JSON Schema 校验
          ├─ tool.Execute(ctx, args)
          └─ 输出截断（默认 16KB）
```

### 1.5 Agent 模式与类型

| 模式 | 说明 |
|---|---|
| Quick Answer | RAG 模式，纯问答，不走 ReAct 循环 |
| Smart Reasoning | ReAct 多步推理 + 工具调用 |

Smart Reasoning 下的 5 种 Agent 类型：

| 类型 | 用途 |
|---|---|
| RAG QA | 向量/关键词检索文档知识库 |
| Wiki QA | Wiki 页面导航（Wiki 知识库） |
| Hybrid RAG+Wiki | 混合 Wiki + RAG |
| Data Analysis | 表格文件 SQL/统计分析 |
| Custom | 用户完全自定义 |

---

## 二、前端工具清单与实现状态

> 对应前端「创建智能体 → 工具配置」页面。✅ = 已实现，⚠️ = 伪实现（名不副实），❌ = 未实现。

### 2.1 基础工具

| 工具名 | 后端标识 | 状态 | 说明 |
|---|---|---|---|
| 思考 | `thinking` | ✅ | 动态/反思性问题解决思考工具 |
| 制定计划 | `todo_write` | ✅ | 创建结构化研究计划 |

### 2.2 知识库检索（RAG）

| 工具名 | 后端标识 | 状态 | 说明 |
|---|---|---|---|
| 关键词搜索 | `knowledge_search` 中的关键词模式 | ✅ | BM25 全文匹配 |
| 语义搜索 | `knowledge_search` 中的向量模式 | ✅ | 向量相似度检索 |
| 查看文档分块 | `list_knowledge_chunks` | ✅ | 获取文档完整分块内容 |
| 查询知识图谱 | `query_knowledge_graph` | ⚠️ **伪实现** | 实际调用的是普通 HybridSearch（向量+关键词），没有真查 Neo4j。代码标注"under development" |
| 获取文档信息 | `get_document_info` | ✅ | 查看文档元数据 |
| 查询数据库 | `database_query` | ✅ | 执行 SQL 查询（数据分析场景） |

### 2.3 Wiki 读取

| 工具名 | 后端标识 | 状态 | 说明 |
|---|---|---|---|
| 搜索 Wiki | `wiki_search` | ✅ | PostgreSQL 正则匹配标题/摘要/正文 |
| 阅读 Wiki 页面 | `wiki_read_page` | ✅ | 读取指定页面完整内容 |
| 精读源文档 | `wiki_read_source_doc` | ✅ | 通过知识点深入阅读 Wiki 页背后的原始文档 |
| 标记 Wiki 问题 | `wiki_flag_issue` | ✅ | 标记页面中存在的事实错误或合并冲突问题 |

### 2.4 Wiki 编辑（可写）

| 工具名 | 后端标识 | 状态 | 说明 |
|---|---|---|---|
| 创建/覆盖 Wiki | `wiki_write_page` | ✅ | 创建新页面或完全覆盖已有页面 |
| 局部替换 Wiki | `wiki_replace_text` | ✅ | 替换页面中的特定文本片段 |
| 重命名 Wiki | `wiki_rename_page` | ✅ | 重命名页面并自动更新关联链接 |
| 删除 Wiki | `wiki_delete_page` | ✅ | 删除页面并自动清理关联死链 |

### 2.5 Wiki 巡检

| 工具名 | 后端标识 | 状态 | 说明 |
|---|---|---|---|
| 查看 Wiki 问题 | `wiki_read_issue` | ✅ | 查看特定 Wiki 页面的巡检问题详情 |
| 更新 Wiki 问题 | `wiki_update_issue` | ✅ | 更新页面问题的处理状态 |

### 2.6 数据分析

| 工具名 | 后端标识 | 状态 | 说明 |
|---|---|---|---|
| 数据分析 | `data_analysis` | ✅ | 理解数据文件并进行数据分析 |
| 查看数据元信息 | `data_schema` | ✅ | 获取表格文件的元信息（表结构等） |

### 2.7 其他工具（前端未展示但后端存在）

| 工具名 | 后端标识 | 状态 | 说明 |
|---|---|---|---|
| 搜索对话 | `search_conversations` | ✅ | 搜索历史对话 |
| 搜索记忆 | `search_memory` | ✅ | 搜索长期记忆 |
| 网页搜索 | `web_search` | ✅ | 需配置网络搜索能力 |
| 网页抓取 | `web_fetch` | ✅ | 抓取指定 URL 内容 |
| 读取文件 | `read_file` | ✅ | 读取沙箱/技能中的文件 |
| 沙箱文件列表 | `list_sandbox_files` | ✅ | 列出沙箱文件 |
| 写沙箱文件 | `write_sandbox_file` | ✅ | 写入沙箱文件 |
| 编辑沙箱文件 | `edit_sandbox_file` | ✅ | 编辑沙箱文件 |
| 写技能文件 | `write_skill_file` | ✅ | 写入技能文件 |
| 编辑技能文件 | `edit_skill_file` | ✅ | 编辑技能文件 |
| Shell 执行 | `shell_exec` | ✅ | 沙箱内执行 Shell 命令（有安全过滤） |
| 发现 MCP 工具 | `discover_mcp_tools` | ✅ | 发现 MCP 服务工具列表 |
| 调用 MCP 工具 | `call_mcp_tool` | ✅ | 调用 MCP 工具 |
| Wiki 链接修改 | `wiki_link_mutation` | ✅ | 修改页面间链接关系 |
| Wiki 路由解析器 | `wiki_route_resolver` | ✅ | 快速解析 slug 所属 KB |
| 全文分块搜索 | `grep_chunks` | ✅ | 正则搜索文档分块内容 |

---

## 三、关键文件速查

| 层级 | 文件 | 核心函数 |
|---|---|---|
| 引擎核心 | `internal/agent/engine.go` | `Execute` / `executeLoop` / `runReActIteration` |
| 思考 | `internal/agent/think.go` | `callLLMWithRetry` / `streamLLMToEventBus` |
| 行动 | `internal/agent/act.go` | `executeToolCalls` / `runToolCall` |
| 观察 | `internal/agent/observe.go` | `analyzeResponse` / `appendToolResults` |
| 终结 | `internal/agent/finalize.go` | `streamFinalAnswerToEventBus` |
| 工具注册 | `internal/agent/tools/registry.go` | `ToolRegistry` / `ExecuteTool` |
| 工具定义 | `internal/agent/tools/definitions.go` | 所有工具名称常量 |
| 会话服务 | `internal/application/service/session_agent_qa.go` | `AgentQA` |
| Agent 服务 | `internal/application/service/agent_service.go` | `CreateAgentEngine` / `registerTools` |
| Handler | `internal/handler/session/qa.go` | `AgentQA` |
| 类型定义 | `internal/types/custom_agent.go` | `CustomAgent` / `AgentMode` / `AgentType` |

---

## 四、重要发现

1. **"查询知识图谱"工具是伪实现**：前端展示了这个工具，但后端实际调的是普通 `HybridSearch`（向量+关键词混合检索），没有真的查询 Neo4j 图谱。代码注释写着 "Full graph query language (Cypher) support is under development"。

2. **Neo4j 只用于可视化**：目前 Neo4j 的作用是存储提取出的实体关系，并通过图谱可视化页面（Neo4jGraphViewer）展示给人看，不参与问答检索链路。

3. **Agent 引擎是标准 ReAct**：多轮推理 + 工具调用 + 流式输出，支持并行工具调用（最多 8 并发），有上下文溢出保护和死循环检测。
