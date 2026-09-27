# 修改过程问题记录

> 记录修改过程中遇到的问题、解决方案及思考过程。

---

## 问题日志

| 日期 | 问题分类 | 问题简述 | 严重程度 | 状态 |
|------|----------|----------|----------|------|
| 2026-09-15 | 环境配置 | 常规部署(8080端口)Swagger 页面返回 401 Unauthorized | 🟡 一般 | 待解决 |
| 2026-09-15 | 环境配置 | 18080 端口被 flygpt-web 容器占用，不是 WeKnora 开发模式后端 | 🟢 轻微 | 已确认 |
| 2026-09-15 | 环境配置 | 开发模式与常规部署 docker compose 共享 project name，导致容器互相覆盖 | 🔴 阻塞 | 已解决 |
| 2026-09-15 | 环境配置 | dev.sh 本地模式端口与 docker-compose.dev.yml 映射端口不一致（5432 vs 65432） | 🔴 阻塞 | 已解决 |
| 2026-09-15 | 环境配置 | 8081 端口被 maxkb 容器占用 | 🟢 轻微 | 已解决（改 8082） |
| 2026-09-16 | LLM 集成 | 火山方舟 Coding Plan 返回内容为空（encrypted_content 加密） | 🟡 一般 | 待解决 |
| 2026-09-17 | Wiki 提示词改造 | L2 实体类提取粒度不一致、分类维度不统一、存在重复类别 | 🟡 一般 | 后续优化 |
| 2026-09-17 | Wiki 去重机制 | 同批次内提取的重复实体类不会去重（去重只做"新条目→已有页面"，不做"新条目→新条目"） | 🟡 一般 | 后续优化 |
| 2026-09-22 | 前端体验 | 大文件上传超时失败（30秒默认超时不够） | 🟡 一般 | 已解决 |
| 2026-09-26 | Wiki前端 | 索引页目录条目截断+分页批次目录名重复 | 🟡 一般 | 已解决 |
| 2026-09-26 | Wiki前端 | 被链接（backlinks）显示slug而非页面标题 | 🟡 一般 | 已解决 |
| 2026-09-26 | 数据质量 | 死链清理：in_links中存在UUID格式无效slug | 🟡 一般 | 已解决 |
| 2026-09-26 | 数据质量 | 关系清理：relations中指向不存在页面的死关系 | 🟢 轻微 | 已解决 |
| 2026-09-26 | 数据质量 | 本体关系缺少description字段 | 🟡 一般 | 已解决 |
| 2026-09-26 | Wiki前端 | 本体关系区反向链接显示slug而非标题 | 🟡 一般 | 已解决 |
| 2026-09-26 | 数据建设 | 跨体系关系（本体实体→分类实体）补充 | 🟢 轻微 | 已完成 |
| 2026-09-27 | Wiki前端 | 左侧目录树：法规/条款/业务对象三目录置顶 | 🟢 轻微 | 已完成 |
> 严重程度：🔴 阻塞 / 🟡 一般 / 🟢 轻微
> 状态：待解决 / 解决中 / 已解决 / 已搁置

---

## 详细记录

### 2026-09-15 - 常规部署 Swagger 页面返回 401 Unauthorized

- **问题描述**：
  访问 `http://192.168.21.10:8080/swagger/index.html` 返回 `{"error":"Unauthorized: missing authentication"}`，HTTP 状态码 401。
  8080 端口为常规部署（`zzh_weknora/WeKnora` 项目，`WeKnora-app` 容器）。

- **问题分类**：
  - [x] 环境配置
  - [ ] 代码逻辑
  - [ ] 依赖冲突
  - [ ] 性能问题
  - [ ] 构建/部署
  - [ ] 其他：____

- **严重程度**：🟡 一般

- **复现步骤**：
  1. 浏览器访问 `http://192.168.21.10:8080/swagger/index.html`
  2. 页面显示 JSON 格式的 401 错误，而非 Swagger UI

- **排查过程**：
  - 确认 8080 端口是 WeKnora 常规部署的后端（`WeKnora-app` 容器），`/health` 正常返回 `{"status":"ok"}`
  - 检查 `zzh_weknora/WeKnora/internal/router/router.go` 代码，Swagger 路由（第138行）注册在全局 Auth 中间件（第186行）之前
  - 按 Gin 的机制，先注册的路由不应被后注册的中间件拦截，但实际返回了 401
  - 可能原因：
    1. 运行的 Docker 镜像版本与本地代码不一致，镜像中 swagger 注册位置可能不同
    2. `GIN_MODE=release` 导致 swagger 路由未注册，但请求落到了其他需要认证的路由上（不过应返回 404 而非 401）
    3. 存在其他中间件或路由分组导致 swagger 被意外拦截

- **根本原因**：
  待进一步排查（需对比运行镜像的实际代码与本地代码是否一致，或检查容器内的环境变量）

- **解决方案**：
  待解决

- **涉及文件**：
  - `zzh_weknora/WeKnora/internal/router/router.go`（第135-144行，Swagger 路由注册）
  - `zzh_weknora/WeKnora/internal/router/router.go`（第186行，全局 Auth 中间件）

- **遗留问题 / 后续优化**：
  - 需登录后获取 token，在 Swagger UI 中配置 Authorize 看是否能正常访问
  - 确认运行镜像的 `GIN_MODE` 环境变量值

---

### 2026-09-15 - 18080 端口被其他容器占用

- **问题描述**：
  开发模式文档记载后端端口为 18080，但实际 18080 端口被 `flygpt-web` 容器占用，不是 WeKnora 开发模式后端。
  开发模式的后端（本地 Go 进程）当前未启动。

- **问题分类**：
  - [x] 环境配置
  - [ ] 代码逻辑
  - [ ] 其他：____

- **严重程度**：🟢 轻微

- **复现步骤**：
  1. `ss -tlnp | grep 18080` 显示监听进程为 `docker-proxy`
  2. `docker ps | grep 18080` 显示为 `flygpt-web` 容器
  3. 浏览器访问 `http://192.168.21.10:18080/` 连接被拒绝（容器内服务不匹配）

- **排查过程**：
  - 确认 8080 端口是常规部署 WeKnora-app（正常运行）
  - 确认 18080 端口是 flygpt-web 容器（非 WeKnora）
  - 开发模式后端需要本地运行 `make dev-app`，当前未启动

- **根本原因**：
  18080 端口被其他项目容器占用，开发模式的 WeKnora 后端未启动。

- **解决方案**：
  - 若使用开发模式，需先修改开发模式后端端口（避开 18080），再执行 `make dev-app` 启动
  - 若只是查看 Swagger，直接使用常规部署 8080 端口（需先解决认证问题）

- **涉及文件**：
  - `my_weknora/WeKnora/config/config.yaml`（server.port 默认 8080）
  - `my_weknora/WeKnora/指南/开发模式部署指南1.md`（记载开发模式端口为 18080）

- **遗留问题 / 后续优化**：
  - 如需启动开发模式，需修改后端端口（如 28080）并同步更新前端 Vite 代理配置

---

### （按日期分节，示例如下）

#### YYYY-MM-DD - 问题标题

- **问题描述**：
  
- **问题分类**：
  - [ ] 环境配置
  - [ ] 代码逻辑
  - [ ] 依赖冲突
  - [ ] 性能问题
  - [ ] 构建/部署
  - [ ] 其他：____

- **严重程度**：🔴 阻塞 / 🟡 一般 / 🟢 轻微

- **复现步骤**：
  1. 
  2. 

- **排查过程**：
  
- **根本原因**：
  
- **解决方案**：
  
- **涉及文件**：
  - 

- **遗留问题 / 后续优化**：
  - 

---

### 2026-09-16 - 火山方舟 Coding Plan LLM 调用返回内容为空

- **问题描述**：
  使用火山方舟 Coding Plan 包月套餐接入 WeKnora（`/api/coding/v3` + `Doubao-Seed-2.1-turbo`），LLM 调用能成功返回 token 计数（completion_tokens=200），但 `Content` 字段为空，`FinishReason` 为 `length`。
  用 curl 直接调原生 API 能正常返回内容，且响应中包含 `encrypted_content` 加密字段。

- **问题分类**：
  - [x] LLM 集成
  - [ ] 环境配置
  - [ ] 代码逻辑
  - [ ] 其他：____

- **严重程度**：🟡 一般

- **复现步骤**：
  1. 配置模型：baseURL=`https://ark.cn-beijing.volces.com/api/coding/v3`，model=`Doubao-Seed-2.1-turbo`，provider=`volcengine`
  2. 调用 `chatClient.Chat()` 发送简单对话
  3. 返回的 `resp.Content` 为空字符串，但 `Usage.CompletionTokens` 有值（200）
  4. 同样的请求用 curl 直接调 API，能拿到正常的 content 内容

- **排查过程**：
  - 确认 provider 从 "doubao" 改为 "volcengine" 无效
  - 确认 MaxTokens 与 MaxCompletionTokens 参数差异不影响（都返回空）
  - curl 原生调用正常，说明 API 本身没问题
  - 原生响应中包含 `encrypted_content` 字段，怀疑是加密传输导致 WeKnora 的标准解析读不到内容
  - WeKnora 使用 `sashabaranov/go-openai` 库，只解析标准的 `content` 字段

- **根本原因**：
  火山方舟 Coding Plan 开启了内容加密传输（`encrypted_content` 字段），WeKnora 的标准 OpenAI 兼容解析只读取 `content` 字段，不支持解密 `encrypted_content`。

- **可能的解决方案**：
  1. 在火山控制台关闭内容加密（如果有开关）
  2. 改用普通推理接入点（ep- 开头），不使用 Coding Plan
  3. 扩展 WeKnora 的 volcengine provider，增加 `encrypted_content` 解密支持
  4. 使用自定义 HTTP 中间件，在响应解析前先解密 `encrypted_content` 并回填到 `content`

- **涉及文件**：
  - `internal/models/chat/remote_api.go`（响应解析）
  - `internal/models/chat/openai_stream.go`（`parseCompletionResponse` 函数）
  - `internal/models/provider/volcengine.go`（火山引擎适配）

- **遗留问题 / 后续优化**：
  - 需要确认火山 Coding Plan 是否有关闭加密的选项
  - 如果必须用 Coding Plan，需要实现 encrypted_content 解密逻辑

---

### 2026-09-17 - Wiki L2 实体类提取质量问题（粒度/维度/重复）

- **问题描述**：
  Wiki 实体提取升级为第二层本体（L2 实体类）后，用 4 篇法律法规文档（保险法、社会保险法、失业保险条例、工伤保险条例）测试，提取出 15 个 L2 实体类。整体方向正确（都是类别级，没有提取具体实例），但存在三类质量问题：

  1. **机构类严重膨胀（6个，占 40%），层级混乱**：
     - 法律主体类（最顶层抽象）
     - 保险机构类（保险领域机构总称）
     - 保险中介机构类（应是保险机构类的子类）
     - 保险行业自律组织类（应是保险机构类的子类）
     - 监管机构类（独立合理）
     - 社会保险服务机构类（偏细，与其他机构类有交叉）
     6 个机构相关类别平级，是"祖孙三代同堂"，L2 应有 2-3 个机构大类即可。

  2. **存在重复/重叠类别**：
     - 保险基金类 ↔ 社会保险基金类（语义高度重叠，应合并）
     - 保险机构类 ↔ 保险中介机构类 ↔ 保险行业自律组织类（层级关系，不应平级）

  3. **粒度差异极大**：
     - 粗的：法律主体类（涵盖所有法律关系主体，接近法理学顶层概念）
     - 细的：保险待遇类（只讲工伤职工待遇类型，更像 L3）
     所有 L2 类别应在同一抽象层级上。

  4. **分类维度不统一**：
     - 法理学五要素视角：法律主体类、法律行为/程序类、法律责任类、法律文书类、法律法规类（5个）
     - 保险行业视角：保险产品类、保险机构类、保险基金类、保险待遇类（4个）
     - 社会保险领域视角：社会保险制度类、社会保险基金类、社会保险服务机构类（3个）
     - 职能视角：监管机构类、保险中介机构类、保险行业自律组织类（3个）
     多种维度交叉混杂，导致重复和层级混乱。

- **问题分类**：
  - [ ] 环境配置
  - [x] 代码逻辑（提示词层面）
  - [ ] 依赖冲突
  - [ ] 性能问题
  - [ ] 构建/部署
  - [x] 其他：Wiki 实体提取质量

- **严重程度**：🟡 一般（方向正确，质量可优化，不阻塞功能验证）

- **复现步骤**：
  1. 新建 Wiki 知识库，上传保险法/社会保险法/失业保险条例/工伤保险条例 4 篇文档
  2. 等待提取完成，查看 Wiki 索引页的实体列表
  3. 共 15 个实体类，观察到上述粒度/维度/重复问题

- **排查过程**：
  - 确认 15 个实体类全部以"类"结尾，均为类别级，无具体实例提取 → 方向正确
  - 统计分类：机构类 6 个（40%）、法律要素类 5 个、保险领域类 4 个、社保领域类 3 个 → 维度混杂
  - 识别重复：保险基金类与社会保险基金类语义重叠 → 去重规则不够强
  - 识别层级：保险中介机构类、保险行业自律组织类明显是保险机构类的子类 → L2/L3 边界模糊

- **根本原因**：
  1. 提示词中对"L2 同一抽象层级"的约束不够明确，LLM 容易把不同层级的类别混在一起
  2. 去重提示词中对"语义重叠的类别应合并"的规则不够强，特别是跨领域表达同一个概念的情况
  3. 机构类是最容易出现层级混乱的类别类型，提示词中没有针对性的约束

- **解决方案**：
  后续优化，拟从提示词入手（不动代码）：
  1. 明确 L2 抽象层级规则：所有实体类必须在同一抽象层级，给出正例（7-9 个核心大类）和反例（过细的子类）
  2. 强化去重/合并规则：增加语义重叠类别的合并判断，特别是机构类、基金类等高风险类别
  3. 限制机构类数量：明确机构相关的 L2 大类不超过 2-3 个，其余作为子类放在页面内容中
  4. 统一分类维度：引导 LLM 用一个统一视角（如"对象性质"）来划分 L2，而不是多维度混杂

- **涉及文件**：
  - `internal/agent/prompts_wiki.go`（WikiKnowledgeExtractPrompt、WikiDeduplicationPrompt、WikiCandidateSlugPrompt）

- **遗留问题 / 后续优化**：
  - 需等更多文档测试后再确认优化方案的效果
  - 优化时只改提示词，不动 Go 代码结构
  - 参考 KnowPla 项目的混合建模第二层本体逻辑

---

### 2026-09-17 - Wiki 同批次内实体类不去重问题

- **问题描述**：
  同一批文档摄入时，提取出的多个重复实体类（如"法律行为程序类"和"法律行为与程序类"，指的是同一个类别但名称略有差异）不会被去重，会同时作为独立页面保留。

  原因：去重机制只做"新条目 vs 已有页面"的比对，不做"新条目 vs 新条目"的同批次内比对。Wiki 去重流水线（`wiki_ingest_dedup.go` + `WikiDeduplicationPrompt`）的设计是：
  1. 用 trigram 相似度从已有 Wiki 页面中筛选候选
  2. LLM 判断新条目是否与某个已有候选重复
  3. 同批次新提取的条目之间不互相比对

  实际现象：
  - 第一批 4 篇法律法规文档提取出 12 个实体类，其中"法律行为程序类"和"法律行为与程序类"明显重复
  - "保险基金类"和"社会保险基金类"也高度重叠
  - 这些重复只有在后续新文档进来时才可能被部分合并，同批次内永远共存

- **问题分类**：
  - [ ] 环境配置
  - [x] 代码逻辑（去重机制设计）
  - [ ] 依赖冲突
  - [ ] 性能问题
  - [ ] 构建/部署
  - [ ] 其他：____

- **严重程度**：🟡 一般（影响第一批提取质量，后续批次越多影响越小）

- **复现步骤**：
  1. 新建 Wiki 知识库
  2. 一次性上传多篇同一领域的文档（如 4 篇法律法规）
  3. 等待提取完成，查看实体类列表
  4. 会发现多个名称不同但语义高度重叠的实体类页面

- **排查过程**：
  - 确认 `WikiDeduplicationPrompt` 提示词已包含语义重复合并规则 → 提示词不是瓶颈
  - 查看 `selectDedupCandidatePages()` 预筛选逻辑 → 候选来源是"已有页面"，不含同批次新条目
  - 查看 `deduplicateExtractedBatch` 调用链路 → 输入是新条目 + 候选已有页面，没有新条目之间的比对
  - 确认结论：同批次去重是机制缺失，不是提示词或 LLM 判断问题

- **根本原因**：
  去重机制的设计目标是"新文档与已有知识库的去重"，没有考虑"同批次内多个新条目之间也可能重复"的场景。在实例级实体提取时这个问题不明显（实例名称差异大），但在 L2 本体类提取时，同一批文档反复提到同一类别的不同表述，同批次重复概率显著提高。

- **解决方案（待实现）**：
  在现有去重流程之前，增加一轮**同批次内去重**：
  1. 把本批次所有新提取的实体类（和概念）两两分组
  2. 用 trigram 相似度预筛出高相似的 pair
  3. 调用 LLM（复用 `WikiDeduplicationPrompt` 或简化版）判断是否应合并
  4. 重复的条目在进入后续流水线前先合并成一个
  5. 再走现有的"新条目 vs 已有页面"去重流程

  改动范围：
  - `internal/application/service/wiki_ingest_dedup.go` — 新增同批次去重函数
  - `internal/application/service/wiki_ingest_batch.go` — 调整流水线顺序，在 batch 去重前加同批次去重

- **涉及文件**：
  - `internal/application/service/wiki_ingest_dedup.go`
  - `internal/application/service/wiki_ingest_batch.go`

- **遗留问题 / 后续优化**：
  - 提示词层面已尽量强化去重规则（语义重叠合并、子类过细则合并到父类）
  - 但提示词无法解决同批次内互不去重的结构性问题，必须改代码
  - 优先级：在 L2 提取质量基本稳定后再实现

---

### 2026-09-22 - 大文件上传超时失败

- **问题描述**：
  上传 20MB+ 的大文件（如"8月23日技术部分.docx"，23.42MB）时，前端报上传失败。
  后端日志显示 `unexpected EOF`，请求 latency 约 30 秒左右断开。

- **问题分类**：
  - [x] 前端体验
  - [ ] 代码逻辑
  - [ ] 环境配置
  - [ ] 其他：____

- **严重程度**：🟡 一般（大文件上传失败，小文件正常）

- **复现步骤**：
  1. 上传 20MB 以上的 docx 文件
  2. 等待约 30 秒后报上传失败
  3. 后端日志显示 `unexpected EOF`，latency ≈ 30s

- **排查过程**：
  - 后端日志显示失败请求 latency 均为 30 秒左右 → 怀疑是超时导致
  - 检查前端 axios 配置：全局默认超时 30000ms（30秒）
  - 知识库上传接口 `uploadKnowledgeFile` 未单独设置超时，继承全局默认值
  - 成功上传的 23.42MB 文件实际总耗时约 50 秒（后端处理仅 0.7 秒，网络传输约 49 秒）
  - 结论：30 秒超时不足以完成大文件传输

- **根本原因**：
  前端 axios 全局超时 30 秒，大文件网络传输时间超过此阈值后被前端主动断开，导致 `unexpected EOF`。

- **解决方案**：
  三处修改：

  1. **前端上传超时改为 5 分钟**（`frontend/src/api/knowledge-base/index.ts`）
     - `uploadKnowledgeFile` 增加 `{ timeout: 300000 }` 配置
     - 与技能包上传的超时配置保持一致

  2. **前端上传失败提示增强**（`frontend/src/views/knowledge/KnowledgeBase.vue`）
     - 新增 `getUploadFailureReason` 函数，区分超时、网络错误、重复文件等不同失败原因
     - 批量上传失败时，显示失败文件列表（文件名、大小、原因），最多显示 5 个

  3. **后端增加中文上传耗时日志**（`internal/application/service/knowledge_create.go`）
     - 开始："开始处理文件上传，文件名: xxx，文件大小: xxx 字节 (xxx MB)"
     - 完成："文件上传完成，文件名: xxx，文件大小: xxx MB，总耗时: xxx (xxx秒)"
     - 重复："文件上传跳过（重复文件），文件名: xxx，文件大小: xxx MB，耗时: xxx (xxx秒)"
     - 方便后续排查不同大小文件的上传性能

- **验证数据**：
  - 23.42MB docx 文件：后端处理 0.7 秒，总请求耗时 50 秒
  - 主要耗时在网络传输（≈49.3 秒），后端处理占比 < 2%
  - 5 分钟超时足够覆盖大文件上传场景

- **涉及文件**：
  - `frontend/src/api/knowledge-base/index.ts` — 上传超时配置
  - `frontend/src/views/knowledge/KnowledgeBase.vue` — 失败提示增强
  - `internal/application/service/knowledge_create.go` — 上传耗时日志

- **遗留问题 / 后续优化**：
  - 如果有更大的文件（100MB+）或更慢的网络，5 分钟也可能不够，届时再考虑分片上传
  - 可结合后端日志数据，统计不同大小文件的上传耗时，用于调整超时阈值

---

### 2026-09-26 - Wiki索引页目录截断与分页重复问题

- **问题描述**：
  法规场景本体v3的索引页有两个显示问题：
  1. 每个分类目录下的wiki页面超过10个时没有截断显示，全部列出来导致索引页过长
  2. 分页加载时，每个批次都会重新输出目录标题，导致同一个目录名重复出现多次

- **问题分类**：
  - [x] 前端体验
  - [ ] 代码逻辑
  - [ ] 其他：____

- **严重程度**：🟡 一般

- **复现步骤**：
  1. 打开法规场景本体v3知识库的Wiki索引页
  2. 看到"法规""条款""业务对象"等目录下列出了全部页面（远超过10个）
  3. 向下滚动加载更多时，同一个目录标题会重复出现

- **排查过程**：
  - 索引页内容不是存储在 wiki_pages.content 中的，而是前端动态生成的
  - 前端通过 `getWikiIndex` API 分页获取页面列表，按 category_path 分组渲染
  - 每批50条数据，每个批次独立调用 `appendIndexDirectoryLines` 输出目录标题和条目
  - 跨批次没有记录已输出的目录，导致重复

- **根本原因**：
  1. 截断逻辑缺失：没有限制每个目录下显示的条目数量
  2. 跨批次状态丢失：每个分页批次独立渲染，不知道上一批已经输出了哪些目录标题

- **解决方案**：
  在 `WikiBrowser.vue` 中：
  1. 增加 `indexEmittedDirs` Set 跟踪已输出的目录标题，跨批次去重
  2. 增加 `indexDirCounters` 记录每个顶级目录已显示的条目数，达到10个就截断
  3. 截断后显示"... 更多请在左侧「xxx」目录查看"
  4. 摘要类型（summary）不截断，全部显示
  5. 非摘要类型的分页大小从50提高到500，确保大部分目录在第一页就能完整显示（减少截断判断的边界情况）

- **涉及文件**：
  - `frontend/src/views/knowledge/wiki/WikiBrowser.vue`（`appendIndexDirectoryLines` 函数、`loadMoreIndexSection` 函数）

- **遗留问题 / 后续优化**：
  - 如果目录条目特别多（如1000+），500条的分页可能还会跨批次，但当前规模下没问题
  - 截断的"更多"提示目前只在顶级目录层级显示，子目录级别的截断提示可以后续优化

---

### 2026-09-26 - 被链接（backlinks）显示slug而非页面标题

- **问题描述**：
  Wiki页面底部的"被链接"（backlinks / in_links）区域，链接显示的是slug（英文字母），而不是页面的中文标题。

- **问题分类**：
  - [x] 前端体验
  - [ ] 代码逻辑
  - [ ] 其他：____

- **严重程度**：🟡 一般

- **复现步骤**：
  1. 打开任意一个有in_links的Wiki页面
  2. 滚动到底部"被链接"区域
  3. 看到链接显示的是英文slug（如`entity/bo-xxx`），不是中文标题

- **排查过程**：
  - `in_links` 字段存储的是 slug 数组，不是标题
  - 前端用 `slugDisplayName(slug)` 函数显示名称，该函数从 `pages.value` 缓存中查找标题
  - `pages.value` 是懒加载的，只包含用户在左侧目录树中展开过的页面
  - 很多反向链接对应的页面用户没展开过，所以查不到标题，fallback到显示slug

- **根本原因**：
  页面缓存（`pages.value`）是按需加载的，in_links 中引用的页面很多不在缓存里，导致显示 slug 而非标题。

- **解决方案**：
  前端方案（纯前端，不改后端）：
  1. 新增 `preloadLinkedPageTitles(page)` 函数
  2. 在页面加载时，收集该页面的 `in_links` 和 `out_links` 中的所有 slug
  3. 过滤出不在 `pages.value` 缓存中的 slug
  4. 并发调用 `getWikiPage` API 预加载这些页面的元数据（只需标题等基本信息）
  5. 将结果 push 到 `pages.value` 缓存中
  6. 这样 `slugDisplayName()` 就能找到标题，显示中文名称

  > 备注：最初考虑过修改后端（在 wiki_page_relations 表中加 source_title/target_title 字段，或用 JOIN 查询返回标题），但被用户否决了——"撤回你的修改"。最终选择纯前端预加载方案。

- **涉及文件**：
  - `frontend/src/views/knowledge/wiki/WikiBrowser.vue`（`preloadLinkedPageTitles` 函数、`watch(selectedPage)`）

- **遗留问题 / 后续优化**：
  - 预加载是串行等待的，页面打开后可能有短暂的slug显示再切换到标题的闪烁
  - 如果 in_links 很多（如几十上百个），一次性并发请求可能较多，可以考虑分批或只预加载前N个

---

### 2026-09-26 - 死链清理：in_links中存在UUID格式无效slug

- **问题描述**：
  v3知识库中有51个页面的 `in_links` 字段包含UUID格式的slug（来自参考库中概念页面的链接），这些slug对应的页面在v3中不存在，是死链。在"被链接"区域显示为无标题的英文UUID字符串。

- **问题分类**：
  - [x] 数据质量
  - [ ] 代码逻辑
  - [ ] 其他：____

- **严重程度**：🟡 一般

- **复现步骤**：
  1. 打开v3知识库中某些主题概念页面
  2. 查看"被链接"区域，会看到UUID格式的无效链接
  3. 这些链接点击后无法跳转到有效页面

- **排查过程**：
  - 用 SQL 查询所有 in_links 中的 slug，检查对应页面是否存在
  - 发现51个页面有UUID格式的死链（27个有混合链接+死链，24个只有死链）
  - 这些UUID是参考库合并前v2页面引用的参考库概念页面的旧ID
  - 合并后参考库页面用了新slug（concept/xxx 格式），旧的UUID slug没有被更新

- **根本原因**：
  v2本体库中的页面内容引用了参考库的概念页面，但使用的是UUID格式的slug（旧的引用方式）。合并到v3时，参考库页面的slug是正常的 concept/xxx 格式，UUID slug对应的页面不存在，导致 in_links 中有死链。

- **解决方案**：
  分两轮清理：
  1. 第一轮：清理有混合链接的页面（27个）—— 只删除UUID格式的死链，保留有效链接
  2. 第二轮：清理全部是死链的页面（24个）—— 直接将 in_links 设为空数组 `[]`

  清理规则：匹配 UUID 格式的 slug（`xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx`），从 in_links 数组中移除。

- **涉及文件**：
  - 数据库操作：wiki_pages.in_links 字段
  - 清理脚本：一次性 Python 脚本

- **遗留问题 / 后续优化**：
  - 死链的根因是内容迁移时没有同步更新旧的wiki链接引用
  - 后续知识库合并时应在合并步骤中增加链接重映射环节
  - 前端也可以增加死链检测和自动隐藏功能

---

### 2026-09-26 - 关系清理：wiki_page_relations中指向不存在页面的死关系

- **问题描述**：
  v3知识库的 `wiki_page_relations` 表中，有部分关系的 source_slug 或 target_slug 指向的页面在v3中不存在（死关系）。这些关系在关系图谱中会产生孤立节点或无效连线。

- **问题分类**：
  - [x] 数据质量
  - [ ] 其他：____

- **严重程度**：🟢 轻微

- **复现步骤**：
  1. 查询 wiki_page_relations 表中 source_slug/target_slug 不在 wiki_pages 中的关系
  2. 发现约100条死关系（来自参考库合并时遗留）

- **根本原因**：
  参考库合并到v3时，relations是批量导入的，部分关系的一端页面没有被导入（如跳过的"公司法"目录下的页面），导致关系残留。

- **解决方案**：
  执行 SQL 删除两端 slug 中至少有一个不存在的关系：
  ```sql
  DELETE FROM wiki_page_relations
  WHERE knowledge_base_id = 'kb-regscene-demo-003'
    AND (source_slug NOT IN (SELECT slug FROM wiki_pages WHERE knowledge_base_id = 'kb-regscene-demo-003')
         OR target_slug NOT IN (SELECT slug FROM wiki_pages WHERE knowledge_base_id = 'kb-regscene-demo-003'));
  ```

  清理后：0条死关系，533条有效关系。

- **涉及文件**：
  - 数据库操作：wiki_page_relations 表

- **遗留问题 / 后续优化**：
  - 合并知识库时，relations 导入应做两端存在性校验，只导入两端都存在的关系
  - v4备份脚本已经加了这个过滤逻辑

---

### 2026-09-26 - 本体关系缺少description字段

- **问题描述**：
  v3知识库中三类核心本体关系（from_document、defined_in、mentioned_in）共405条，只有 relation_label（如"来自文档"），但缺少详细的 `description` 字段。在页面底部的"本体关系"区域只能看到简短标签，看不到具体的关系说明。

- **问题分类**：
  - [x] 数据质量
  - [ ] 代码逻辑
  - [ ] 其他：____

- **严重程度**：🟡 一般

- **复现步骤**：
  1. 打开任意法规/条款/业务对象页面
  2. 查看页面底部的"本体关系"区域
  3. 只能看到关系标签（如"来自文档"），没有详细描述

- **根本原因**：
  v2本体库的关系是通过规则生成的（条款→from_document→法规，BO→defined_in→条款，BO→mentioned_in→法规），只设置了 relation_label 和 reverse_label，没有生成 description。参考库的关系有 description，因为是LLM提取的。

- **解决方案**：
  用LLM批量生成关系描述：
  1. 导出405条关系的基本信息（source_title、target_title、relation_type、relation_label、content摘要）
  2. 分成5个批次，每个批次81条，用5个并行agent同时处理
  3. 每个agent读取批次数据，调用LLM为每条关系生成1-2句话的中文描述
  4. 生成结果写回数据库的 description 字段
  5. 同时确保 reverse_label 和 confidence（设为0.95）字段也有值

  提示词要点：
  - 描述要具体，结合源页面和目标页面的内容，不能只说"A与B有关系"
  - from_document：说明条款是法规的哪一部分、第几章第几条
  - defined_in：说明业务对象在条款中是如何被定义/规定的
  - mentioned_in：说明业务对象在法规中被提及的上下文

- **涉及文件**：
  - 数据库操作：wiki_page_relations.description 字段
  - 处理脚本：export 脚本 + 5个LLM agent batch + update 脚本

- **验证结果**：
  - 405条本体关系全部生成了 description
  - 加上参考库原有的128条有描述的关系，533条关系全部有 description
  - 0条无描述的关系

- **遗留问题 / 后续优化**：
  - 描述的质量依赖LLM，可能需要人工抽查部分结果
  - 后续新增关系时应自动生成 description，而不是批量补全

---

### 2026-09-26 - 本体关系区反向链接显示slug而非标题

- **问题描述**：
  Wiki页面底部"本体关系"区域的"被指向"（incoming relations）部分，源页面名称显示的是英文slug（如 `clause-doc04-c-003`、`bo-产品开发计划`），而不是中文标题。但"指向其他"（outgoing relations）部分目标页面的标题显示正常。

- **问题分类**：
  - [x] 前端体验
  - [ ] 代码逻辑
  - [ ] 其他：____

- **严重程度**：🟡 一般

- **复现步骤**：
  1. 打开一个有 incoming 关系的分类实体页面（如"人身保险公司类"）
  2. 滚动到底部"本体关系"区域
  3. "被指向"列表中显示的是 `clause-doc07-c-005` 等slug，不是中文标题

- **排查过程**：
  - 显示函数用的是 `slugDisplayName(rel.source_slug)`，和"被链接"区域用的是同一个函数
  - 之前已经修复了"被链接"的标题显示问题（preloadLinkedPageTitles），但那个函数只预加载了 in_links 和 out_links 中的 slug
  - 结构化关系（wiki_page_relations）中的 source_slug / target_slug 没有被预加载
  - 当用户打开的是分类实体页面（不在法规/条款/业务对象目录），反向关系的源页面（条款/BO）用户可能从未展开过，不在 pages.value 缓存中

- **根本原因**：
  `preloadLinkedPageTitles` 只处理了 wiki 链接（in_links/out_links）的 slug，没有处理结构化关系（pageRelations）中的 slug。

- **解决方案**：
  在 `WikiBrowser.vue` 中：
  1. 给 `preloadLinkedPageTitles` 函数增加 `extraSlugs: string[] = []` 参数
  2. 在 `watch(selectedPage)` 中，先 `await loadPageRelations()` 加载关系数据
  3. 收集 outgoing 的 `target_slug` 和 incoming 的 `source_slug`
  4. 将这些 slug 作为 extraSlugs 传给 `preloadLinkedPageTitles`
  5. 这样 wiki链接 + 结构化关系的所有 slug 都会被预加载

  关键改动：
  ```js
  watch(selectedPage, async (page) => {
    if (page) {
      await loadPageRelations(page.slug, page.page_type)
      const relationSlugs: string[] = []
      pageRelations.value.outgoing.forEach(r => relationSlugs.push(r.target_slug))
      pageRelations.value.incoming.forEach(r => relationSlugs.push(r.source_slug))
      preloadLinkedPageTitles(page, relationSlugs)
    }
  })
  ```

- **涉及文件**：
  - `frontend/src/views/knowledge/wiki/WikiBrowser.vue`（`preloadLinkedPageTitles` 函数、`watch(selectedPage)`）

- **遗留问题 / 后续优化**：
  - 因为要等关系加载完才开始预加载，标题显示可能比 wiki 链接的标题稍晚一点
  - 如果关系数量很多（几十上百个），一次性预加载请求较多，可以考虑分批或只预加载可见部分

---

### 2026-09-26 - 跨体系关系补充（本体实体 ↔ 分类实体）

- **任务描述**：
  为法规场景本体的260个实体页面（法规11 + 条款148 + 业务对象101）补充它们与21个分类实体（保险产品类、保险机构类、监管机构类等）之间的结构化关系。

- **任务分类**：
  - [x] 数据建设
  - [ ] 代码逻辑
  - [ ] 其他：____

- **背景**：
  v3是双体系融合架构，自底向上的法规场景本体（法规/条款/业务对象）和自顶向下的主题分类（保险/保险监管/金融监管等）共存，但两套体系之间缺少结构化关系连接。用户要求补充这些关系，且关系只对实体页面（两端均为entity）。

- **实施方案**：
  1. 导出260个本体实体页面（带标题+摘要+内容片段）和21个分类实体
  2. 分成10个批次，每批26个页面，10个LLM agent并行处理
  3. 每个agent为每个本体实体从21个分类中选出1-5个相关分类，生成关系描述
  4. 关系类型统一为 `classified_under`（归类于 / 包含），confidence=0.90
  5. 汇总后批量写入 `wiki_page_relations` 表

- **结果**：
  - 新增关系：723 条 `classified_under` 关系
  - 覆盖本体实体：260/260 (100%)
  - 覆盖分类实体：21/21 (100%)
  - 平均每实体：约 2.8 个分类
  - 总关系数从 533 → 1256

- **关系类型**：
  - 方向：本体实体 → 分类实体
  - relation_type: `classified_under`
  - relation_label: "归类于"
  - reverse_label: "包含"
  - confidence: 0.90
  - generated_by: "llm_batch"

- **质量控制**：
  - 每页匹配1-5个分类，不强凑
  - 描述20-50字，结合具体内容
  - 全部723条关系唯一无重复
  - 两端slug均验证存在

- **涉及文件**：
  - 数据库操作：wiki_page_relations 表
  - 处理脚本：`/tmp/export_for_cross_rel.py` + 10个batch JSON + `/tmp/insert_cross_relations.py`

---

### 2026-09-27 - 左侧目录树三目录置顶（法规/条款/业务对象）

- **问题描述**：
  v3知识库左侧"知识"Tab的目录树中，顶层目录按拼音字母序排列（保险/保险指标/保险监管/意外险/意外险监管/业务对象/条款/法规/金融监管），三个核心本体目录（法规、条款、业务对象）散落在中间，不突出。用户希望将这三个目录置顶，其余目录保持原顺序。

- **问题分类**：
  - [x] 前端体验
  - [ ] 代码逻辑
  - [x] 数据配置

- **严重程度**：🟢 轻微

- **复现步骤**：
  1. 打开法规场景本体演示v3知识库
  2. 切换到"知识"Tab
  3. 看到9个顶层目录按字母序排列，法规/条款/业务对象不在最前面

- **排查过程**：
  - 左侧目录树数据来自 `ListChildFolders` API
  - 后端 `ListAllFolders` 查询按 `depth ASC, path ASC` 排序（即按路径字母序）
  - 检查 `wiki_folders` 表，发现法规/条款/业务对象三个目录的 `sort_order` 已经是 1/2/3（其他是0），说明前人有过置顶意图
  - 但查询SQL没有用 `sort_order` 排序，所以这个字段形同虚设

- **根本原因**：
  1. 后端查询 `wiki_folders` 时没有按 `sort_order` 排序，导致该字段配置了也不生效
  2. 其他6个目录的 `sort_order` 是0，比1小，就算加了排序也会排在三个本体目录前面

- **解决方案**：
  改动极小，分两步：

  **1. 后端SQL加排序（1行代码）**
  - 文件：`internal/application/repository/wiki_page.go`
  - 函数：`ListAllFolders`
  - 修改：`Order("depth ASC").Order("path ASC")` → `Order("depth ASC").Order("sort_order ASC").Order("path ASC")`

  **2. 数据库调整sort_order**
  - 保持法规=1、条款=2、业务对象=3（已有）
  - 将其他6个顶层目录的 sort_order 从 0 改为 10
  - 这样三个本体目录（1/2/3）排在最前，其余目录（10）按path字母序跟在后面

  **效果**：
  ```
  顶层目录顺序：
  1. 法规    (sort_order=1)
  2. 条款    (sort_order=2)
  3. 业务对象 (sort_order=3)
  4. 保险    (sort_order=10，内部按path序)
  5. 保险指标
  6. 保险监管
  7. 意外险
  8. 意外险监管
  9. 金融监管
  ```

- **涉及文件**：
  - `internal/application/repository/wiki_page.go`（ListAllFolders，加1行排序）
  - 数据库：`wiki_folders.sort_order` 字段（更新6条记录）

- **验证方式**：
  刷新前端页面，左侧"知识"Tab的顶层目录按上述顺序排列。

- **遗留问题 / 后续优化**：
  - 子目录（depth=2）的sort_order还都是0，全部按path排序，未来如需调整子目录顺序，同样改sort_order即可
  - 前端新建/重命名目录时可以考虑提供调整排序的UI，而不是手动改数据库

---

## 当前待解决问题

- [ ] 常规部署 8080 端口 Swagger 返回 401 Unauthorized，需确认原因并解决
- [ ] 开发模式后端 18080 端口被占用，如需启动需改端口
- [ ] 火山方舟 Coding Plan LLM 调用返回内容为空（encrypted_content 加密字段未解析）
- [ ] Wiki L2 实体类提取粒度不一致、分类维度不统一、存在重复类别（后续优化，主要改提示词）
- [ ] Wiki 同批次内实体类不去重（后续优化，需改代码，加同批次去重步骤）

---

## 经验总结

> 记录修改过程中积累的经验和注意事项，供后续参考。

- 两套部署对应独立的项目目录：常规部署在 `zzh_weknora/WeKnora`，开发模式在 `my_weknora/WeKnora`，代码和数据完全隔离
- 常规部署（8080 端口）运行的是 Docker 镜像中的代码，与当前项目目录的本地代码不一定一致，排查问题时注意区分
- 开发模式后端默认端口文档记载为 18080，但该端口可能被其他服务占用，启动前需确认
- Swagger 仅在非 `GIN_MODE=release` 时启用，生产环境自动禁用
