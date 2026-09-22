# Agent 执行流程与工具清单

> 本文档精简记录 WeKnora Agent 的核心执行流程，以及前端展示的所有工具的实现状态（已实现/未实现/伪实现）。

---

## 一、核心执行流程

### 1.1 总体架构

```
用户发消息 → HTTP 入口 → Handler → Service → AgentEngine (ReAct 循环) → SSE 流式返回
```

### 1.2 入口路由（Router 层）

路由注册：[`internal/router/routes_chat.go`](internal/router/routes_chat.go) → `RegisterChatRoutes()`

| 接口 | 路由 | Handler | 说明 | LLM | 流式 | Session |
|---|---|---|---|---|---|---|
| 知识检索 | `POST /api/v1/knowledge-search` | `handler.SearchKnowledge` | 纯检索，返回文档片段 | ❌ | ❌ | ❌ |
| 知识库问答 | `POST /api/v1/knowledge-chat/:session_id` | `handler.KnowledgeQA` | Quick Answer / RAG 问答 | ✅ | ✅ SSE | ✅ |
| Agent 问答 | `POST /api/v1/agent-chat/:session_id` | `handler.AgentQA` | Smart Reasoning / ReAct 推理 | ✅ | ✅ SSE | ✅ |

> **Agent 主入口**是 `POST /api/v1/agent-chat/:session_id`。

### 1.3 Handler 层

文件：[`internal/handler/session/qa.go`](internal/handler/session/qa.go)

Handler 层职责：接 HTTP 请求 → 解析参数 → 存消息 → 设 SSE 流 → 调 Service → 事件转 SSE 推前端。

#### 1.3.1 `AgentQA` 入口函数

`AgentQA` 是 `/agent-chat` 接口的 Handler，做了 3 件事：
1. `parseQARequest()` — 解析请求参数
2. **判断实际模式**：优先级 `customAgent.IsAgentMode() > request.AgentEnabled`
   - 开了 Agent 模式但找不到智能体配置 → 直接返回 400
3. 分发到 `executeQA()`：
   - Agent 模式 → `executeQA(reqCtx, qaModeAgent, true)`
   - 普通模式 → `executeQA(reqCtx, qaModeNormal, ...)`

> 💡 注意：`/agent-chat` 接口不一定走 Agent 模式。如果智能体配置的是 Quick Answer，实际走普通 RAG 流程。

`KnowledgeQA` 和 `AgentQA` 最终都走同一个内部函数 `executeQA()`，核心差异在 mode 参数：
- `qaModeNormal` → 调 `sessionService.KnowledgeQA()`
- `qaModeAgent` → 调 `sessionService.AgentQA()`

#### 1.3.2 `executeQA` 统一执行流程

`executeQA` 是 Handler 层的核心编排函数，共 7 步：

```
executeQA(reqCtx, mode, generateTitle)
  │
  ├─① 异步保存 UI 状态（智能体/知识库/模型选择）
  ├─② Agent 模式发 EventAgentQuery 事件
  ├─③ 存用户消息 + 创建助手消息占位
  ├─④ setupSSEStream() — 初始化 SSE + EventBus
  │     └─ Normal 模式注册 EventAgentFinalAnswer 回调（收尾用）
  ├─⑤ 开 goroutine 异步执行：
  │     ├─ resolveTemporaryAttachments()   处理附件
  │     ├─ runVLMAnalysisIfNeeded()        VLM 图片分析
  │     ├─ buildQARequest()                组装请求
  │     └─ 调 Service 层：
  │          Normal → sessionService.KnowledgeQA()
  │          Agent  → sessionService.AgentQA()
  ├─⑥ defer 收尾（panic 恢复 + Agent 模式存最终消息）
  └─⑦ 阻塞式 handleAgentEventsForSSE() — EventBus 事件 → SSE 推前端
```

关键设计：
- **异步执行**：Service 层在 goroutine 里跑，主线程负责 SSE 推送
- **EventBus 解耦**：Service 不直接碰 HTTP，通过事件总线通信
- **两种模式收尾方式不同**：Normal 模式监听 `EventAgentFinalAnswer` 事件收尾；Agent 模式在 goroutine 的 defer 里收尾

### 1.4 入口链路（总览）

```
POST /api/v1/agent-chat/:session_id
  └─ Handler.AgentQA()                      [handler/session/qa.go]
      └─ executeQA(qaModeAgent)
          └─ sessionService.AgentQA()       [service/session_agent_qa.go]
              ├─ 构建 AgentConfig（CustomAgent + 租户信息）
              ├─ agentService.CreateAgentEngine()
              └─ engine.Execute()           [agent/engine.go]
```

### 1.5 Service 层（AgentQA 准备层）

文件：[`internal/application/service/session_agent_qa.go`](internal/application/service/session_agent_qa.go) → `sessionService.AgentQA()`

Service 层是**准备层**：把智能体运行需要的所有资源准备好，交给 Agent Engine 执行。

```
sessionService.AgentQA()
  │
  ├─① 校验 + 确定租户归属（处理共享智能体跨空间场景）
  ├─② buildAgentConfig() — 组装运行时配置
  │     ├─ 智能体参数（迭代次数/温度/思考模式/引用开关）
  │     ├─ 工具列表 + MCP 服务 + 技能
  │     ├─ 知识库范围 + 搜索目标 + 标签 @Mention
  │     ├─ 网页搜索 + 长期记忆 + 多轮对话
  │     └─ 系统提示词（自定义覆盖默认）
  ├─③ 解析模型：聊天模型 + 重排模型（KB 检索用）
  ├─④ 加载历史对话（多轮模式，默认 5 轮）
  ├─⑤ 准备沙箱：占用一轮 + 同步会话附件到沙箱
  ├─⑥ agentService.CreateAgentEngine() — 创建 Agent 引擎
  ├─⑦ 召回长期记忆（如果开启）
  ├─⑧ 组装最终 query（图片描述/附件/引用上下文）
  └─⑨ engine.Execute() — 开始 ReAct 推理（核心）
```

> 💡 Handler 层通过 `interfaces.SessionService` 接口调用 Service 层，具体实现是 `sessionService` 结构体。

以下 1.5.1 ~ 1.5.3 是 `AgentQA()` 内部第 ⑥ ~ ⑨ 步的展开。

#### 1.5.1 引擎工厂（CreateAgentEngine）

文件：[`internal/application/service/agent_service.go`](internal/application/service/agent_service.go) → `agentService.CreateAgentEngine()`

**定位**：AgentQA 第 ⑥ 步，Service 层内部的引擎工厂。把配置、模型、工具等"零件"组装成一个可执行的 `AgentEngine` 实例。

```
CreateAgentEngine(config, chatModel, rerankModel, eventBus, ...)
  │
  ├─① ValidateConfig() — 校验配置合法性
  ├─② 注册所有工具到 ToolRegistry
  │     ├─ registerTools()          内置工具（知识库搜索/Wiki/记忆/网页搜索等）
  │     ├─ registerMCPTools()       MCP 外部服务工具
  │     ├─ registerSandboxShellIfAllowed()  沙箱 Shell
  │     ├─ registerSandboxFileTools()       沙箱文件读写
  │     └─ PrepareMCPTools()         准备 MCP 工具（按需发现）
  ├─③ resolveKBAndDocInfos() — 解析知识库+选中文档元信息（用于系统提示词）
  ├─④ ResolveSystemPrompt() — 解析系统提示词模板
  ├─⑤ agent.NewAgentEngine() — 创建引擎实例（核心构造）
  │     ├─ 设置 pinned MCP / 技能（@Mention 优先）
  │     ├─ 设置 VLM 图片描述器（MCP 返回图片时转文字）
  │     └─ 初始化 Skills Manager（技能管理器）
  └─⑥ 返回 engine（AgentEngine 实例）
```

之后 `sessionService.AgentQA()` 拿到 engine，调用 `engine.Execute()` 正式启动推理。

#### 1.5.2 ReAct 核心循环（engine.Execute）

Agent 采用标准 **ReAct（Reasoning + Acting）** 模式，由 `AgentEngine` 驱动，对应 `AgentQA()` 的第 ⑨ 步。

**核心机制**：LLM 根据系统提示词自主决定是否调用工具。没有 `final_answer` 工具，结束标志是 **LLM 返回纯文本且不带任何 tool_calls**。

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

**6 种退出条件**：

| 条件 | 说明 |
|---|---|
| ✅ 自然结束 | LLM 不返回 tool_calls，输出纯文本答案 |
| 🛡️ 内容过滤 | `finish_reason == "content_filter"` |
| 🔢 最大轮次 | 达到 `MaxIterations`（默认 100），强制综合答案 |
| 🛑 用户取消 | `ctx.Done()` 触发，用已有结果综合 |
| 🔄 空响应重试耗尽 | 连续空响应超 3 次 |
| 🌀 死循环检测 | 连续相同内容 > 3 次 |

> 每轮开始时还会做**上下文窗口管理**：token 超阈值则压缩历史消息，防止溢出。

#### 1.5.3 工具调用链路

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

### 1.6 Agent 模式与类型

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

#### 1.6.1 各 Agent 类型对应的默认系统提示词

每种 Agent 类型预设（preset）都绑定了一个默认系统提示词模板，通过 `system_prompt_id` 关联到 `config/prompt_templates/agent_system_prompt.yaml` 中的模板。

**对应关系（配置文件：`config/agent_type_presets.yaml`）：**

| Agent 类型 | 模板 ID | 模板名称 | 模式（mode）| 长度 |
|---|---|---|---|---|
| RAG QA (`rag-qa`) | `progressive_rag_agent` | Progressive RAG Agent | `rag` | ~2700 字 |
| Wiki QA (`wiki-qa`) | `wiki_researcher` | Wiki Researcher | `wiki_researcher` | ~9000 字 |
| Hybrid RAG+Wiki (`hybrid-rag-wiki`) | `hybrid_rag_wiki_agent` | Hybrid RAG + Wiki Agent | `smart-reasoning` | ~10000 字 |
| Data Analysis (`data-analysis`) | `data_analyst` | Data Analyst | `data_analyst` | ~2100 字 |
| Custom (`custom`) | 无（用户自定义） | — | — | — |

**模板总数**：YAML 中定义了 7 套系统提示词模板：

| 模板 ID | mode | 说明 |
|---|---|---|
| `pure_agent` | `pure` | 纯智能体（无知识库） |
| `progressive_rag_agent` ⭐默认 | `rag` | 渐进式 RAG（有知识库时默认） |
| `data_analyst` | `data_analyst` | 数据分析 |
| `wiki_researcher` | `wiki_researcher` | Wiki 研究员 |
| `wiki_fixer` | `smart-reasoning` | Wiki 修复（巡检用） |
| `hybrid_rag_wiki_agent` | `smart-reasoning` | 混合 RAG + Wiki |
| `skill_installer` | `smart-reasoning` | 技能安装 |

> 💡 **注意**：mode 相同的模板有多个（如 `smart-reasoning` 下有 3 套），Agent 类型预设通过 `system_prompt_id` 精确指定用哪一套，而不是靠 mode 匹配。

#### 1.6.2 系统提示词优先级与来源

```
用户自定义提示词（数据库 custom_agent.system_prompt）
    ↓ 为空时
Agent 类型预设绑定的模板（system_prompt_id → YAML 模板）
    ↓ 都没有时
按 mode 选默认模板（有 KB → progressive_rag_agent，无 KB → pure_agent）
```

**前后端同源**：前端编辑器展示的默认提示词通过 `GET /api/v1/tenants/kv/prompt-templates` 从后端拉取，和后端实际运行的是同一份 YAML（`config/prompt_templates/agent_system_prompt.yaml`），不存在两套内容。

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

## 三、沙箱（Sandbox）

沙箱是 Agent 执行 Shell 命令、读写文件、运行脚本的**隔离环境**。所有有副作用的操作（写文件、跑代码、装依赖）都在沙箱里完成，不影响宿主机。

### 3.1 四种沙箱后端

| 类型 | 隔离技术 | 配置方式 | 适用场景 |
|---|---|---|---|
| **Docker** | 容器 | `.env` 部署级开关 + 系统设置 | 私有化单机部署 / 本地开发 |
| **CubeSandbox** | MicroVM（腾讯） | 前端配置（API 端点 + Proxy + 域名） | 生产多租户 / 私有化集群 |
| **E2B** | MicroVM（第三方） | 前端配置（API Key） | 不想自己运维 / 云托管 |
| **Disabled** | 无 | 默认 | 不需要脚本执行能力 |

代码位置：`internal/sandbox/sandbox.go` → `SandboxType`

### 3.2 核心机制

**一个会话 = 一个沙箱 = 一个容器/MicroVM**

| 特性 | 说明 |
|---|---|
| **会话级隔离** | 每个会话独立沙箱，文件/进程/网络互相隔离 |
| **懒加载创建** | 第一次调用 shell/file 工具时才创建，节省资源 |
| **会话内复用** | 同一会话多次工具调用共享同一个沙箱，状态保留 |
| **空闲回收** | 超过 TTL 未使用自动销毁（idle sweeper） |
| **资源限制** | CPU / 内存 / 超时 / 网络策略均可配置 |

### 3.3 沙箱工具（依赖沙箱才能用）

这些工具只有在**沙箱可用 + 技能开关开启**时才会注册到 Agent：

| 工具 | 说明 |
|---|---|
| `shell_exec` | 在沙箱内执行 Shell 命令（有安全过滤，危险命令会被拒） |
| `read_file` | 读取沙箱/技能中的文件 |
| `write_sandbox_file` | 写入沙箱文件 |
| `edit_sandbox_file` | 编辑沙箱文件（部分替换） |
| `list_sandbox_files` | 列出沙箱目录文件 |

注册逻辑：`agent_service.go` → `registerSandboxShellIfAllowed()` / `registerSandboxFileTools()`

### 3.4 启用条件

沙箱工具要生效，需要同时满足：

```
① 部署层面有可用的沙箱后端（Docker/Cube/E2B 至少一种）
       ↓
② 空间级"允许在沙箱中执行技能脚本"开关打开
       ↓
③ 智能体配置中选择了沙箱配置（SandboxConfigID）
       ↓
④ 智能体开启了"技能"开关（SkillsEnabled = true）
       ↓
⑤ AgentQA 时自动注册 shell_exec / 文件读写工具
```

### 3.5 配置入口

| 沙箱类型 | 配置入口 | 说明 |
|---|---|---|
| Docker | ① 「系统设置 → 网络安全」打开 Docker 沙箱开关<br/>（或 `.env` → `WEKNORA_SANDBOX_DOCKER_ENABLED=true`）<br/>② 「沙箱配置 → Docker」标签页出现默认配置 | 不用"添加"向导，开了开关就有默认配置<br/>⚠️ 挂 docker.sock 等同宿主机 root，仅适合私有化单机 |
| CubeSandbox | 「设置 → 沙箱配置 → 添加沙箱 → CubeSandbox」 | 三步向导：连接 → 模板 → 运行配置 |
| E2B | 「设置 → 沙箱配置 → 添加沙箱 → E2B」 | 填 API Key 即可 |

---

## 四、关键文件速查

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

## 五、重要发现

1. **"查询知识图谱"工具是伪实现**：前端展示了这个工具，但后端实际调的是普通 `HybridSearch`（向量+关键词混合检索），没有真的查询 Neo4j 图谱。代码注释写着 "Full graph query language (Cypher) support is under development"。

2. **Neo4j 只用于可视化**：目前 Neo4j 的作用是存储提取出的实体关系，并通过图谱可视化页面（Neo4jGraphViewer）展示给人看，不参与问答检索链路。

3. **Agent 引擎是标准 ReAct**：多轮推理 + 工具调用 + 流式输出，支持并行工具调用（最多 8 并发），有上下文溢出保护和死循环检测。
