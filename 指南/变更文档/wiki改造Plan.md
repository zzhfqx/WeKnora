# Wiki 实体提取升级为第二层本体（L2）提示词改造计划

## Context

### 层级关系（本体建模视角）

```
L1 — 本体第一层（Wiki taxonomy 第一层目录）
  │   = 顶层领域分类，比 OWL 的 Thing 更具体
  │   例："法律"、"社会保险"、"保险产品"
  │   （由 WikiTaxonomyPlanPrompt 生成，只保留一级目录）
  │
L2 — 本体第二层 / 实体类（Wiki 实体页面）← 本次改动核心
  │   = 业务本体的父类 / 分类
  │   例："法律主体类"、"保险机构类"、"法律法规类"
  │   （KnowPla 中是 L3 提取完后自动聚类得到的 L2 父类本体）
  │
L3 — 本体第三层 / 业务本体（内部 KnowPla 项目提取）
  │   = 具体的业务本体类
  │   例："两全保险"、"年金保险"、"保险公司"
  │   （Wiki 暂不处理，由 KnowPla 项目后续补全）
  │
实例 — 实际数据（本体建模完成后再提取，和本体不是一回事）
      例："小康安心享两全保险（分红型）"、"平安人寿"
```

### Wiki 中的对应关系

| 本体层级 | Wiki 中的呈现 | 生成方式 |
|----------|---------------|----------|
| L1 顶层领域 | 左侧目录树的第一层文件夹 | `WikiTaxonomyPlanPrompt`（taxonomy 规划） |
| L2 实体类 | Wiki 实体页面（entity/xxx） | `WikiKnowledgeExtractPrompt` 等 7 个提示词 |
| L3 业务本体 | （暂无，KnowPla 后续补全） | - |
| 实例 | （不在 Wiki 本体建模范围内） | - |

**Taxonomy 的新定位**：taxonomy 不再是纯导航目录，而是**本体 L1 层的可视化载体**。因此 taxonomy 必须只有一级目录（不能有二级子目录），避免视觉上造成"三层本体"的误解。

### 当前状态与改造目标

当前 WeKnora Wiki 的实体提取是**扁平的具体事物级**（提取的是具体单个事物，如某个具体产品、某个人名）。

用户希望将 Wiki 实体提取的定位**改为第二层本体（L2）**，即提取的是**业务本体的父类/分类**（如"保险产品类"、"机构主体"），替代原有实体的定位。

- Wiki 只做第二层本体提取（L2）
- 第三层业务本体（L3）由内部 KnowPla 项目继续处理，后续补全到 Wiki
- 实例是更下层的数据提取，和本体建模不是一回事

**约束：**
- slug 命名保持 `entity/xxx` 不变
- 概念（concept）提取保留，后续再清除
- taxonomy 目录调整为**只有一级**，作为本体 L1 层的可视化载体
- 本次**只改提示词**，不动 Go 代码结构（数据结构、解析逻辑等先不改）

## 改动范围

全部集中在一个文件：`internal/agent/prompts_wiki.go`

共 8 处提示词需要调整，按影响程度排列：

---

### 1. WikiKnowledgeExtractPrompt（核心改动）

**当前**：实体定义为"人物、组织、产品、地点、技术、事件等"具体命名事物（实例级）。

**改造**：将"实体"重新定义为**第二层本体类（L2，业务本体的父类/分类）**：
- 实体 = 对业务本体类别的抽象概括（产品大类、机构类别、监管类别、业务类型等）
- 明确告诉 LLM：提取的是**类别/分类**，不是具体单个事物，也不是细粒度的业务本体
- 参考 KnowPla 的 L2 父类本体命名风格：多用"xx类"、"xx主体"、"xx对象"、"xx文件"、"xx事件"等后缀
- 示例从 "Acme Corp"（具体公司）改为 "企业机构类"、"金融产品类"（类别级）
- description/details 的内容侧重也从"介绍这个具体实体"改为"定义这个类别、说明其涵盖范围、特征、下属分类方向"
- 去重规则部分保持 entity/concept 分离不变

**输出结构不变**：仍然是 `{"entities": [...], "concepts": [...]}`，Go 代码不需要改。

---

### 2. WikiCandidateSlugPrompt（核心改动）

**当前**：候选实体提取是具体实例级的轻量骨架（Pass 0，chunk 引用流水线第一遍）。

**改造**：同 #1，将实体定义改为第二层本体类（L2，业务本体的父类/分类）。粒度描述中的示例也要同步改为类别级。

**输出结构不变**：`{"entities": [...], "concepts": [...]}`。

---

### 3. 三个粒度指引文本（WikiGranularityGuidance*）

**当前**：示例都是实例级的（"技术栈/库/框架"、"命名技术"、"顺便提到的公司"等）。

**改造**：三个级别（focused / standard / exhaustive）的描述和示例全部改为 L2 本体类语境：
- **focused（聚焦）**：只提取文档最核心的**领域大类**（如"保险产品类"、"机构主体类"、"监管规则类"），总共 3-7 个
- **standard（标准）**：核心大类 + 被实质性讨论的**二级类别**（如"人身保险产品"、"财产保险产品"、"保险机构"、"监管文件"）
- **exhaustive（穷尽）**：所有可识别的**细分 L2 类别**（尽可能细的分类，但仍然是类别级，不到具体单个事物）

原则：始终是类别/分类级别的提取，不到具体单个事物，也不到 L3 细粒度业务本体。

**注意**：这三个常量通过 `WikiGranularityGuidance()` 函数注入到 `WikiCandidateSlugPrompt` 中，不是独立 prompt。

---

### 4. WikiChunkCitationPrompt（下游适配）

**当前**：候选是具体实体/概念，`new_slugs` 中的 `type: "entity"` 指具体实例级实体。

**改造**：
- "候选实体/概念"的语义变更——实体指的是第二层本体类（L2）
- `new_slugs` 的示例从具体实例改为 L2 本体类
- "实质性讨论"的判断标准也相应调整：讨论一个类别的定义、特征、分类、适用范围等才算实质性
- 输出结构完全不变（`{"citations": {...}, "new_slugs": [...]}`），Go 代码不用改

---

### 5. WikiDeduplicationPrompt（下游适配）

**当前**：去重标准是针对具体实例的（名称变体、缩写、翻译、拼写差异）。

**改造**：
- 去重标准调整为 L2 本体类别的判断逻辑
- 核心判断：两个类别名称是否指的是**同一个语义分类**
- 正确合并例子："金融机构类" → "机构主体类"（同一个分类的不同说法）
- 错误合并例子："保险产品类" → "银行产品类"（都是金融产品但是不同类别）
- 类型兼容规则保持：实体（L2 本体类）只能合并到实体，概念只能合并到概念
- 保持"存疑则不合并"的严格原则不变
- 输出结构不变（`{"merges": {...}}`）

---

### 6. WikiPageModifySystemPrompt + WikiPageModifyUserPrompt（下游适配）

**当前**：实体页面内容是围绕"介绍一个具体实体"组织的（具体事实、数据、历史、人物等）。

**改造**：
- 页面内容侧重调整：L2 本体类页面的内容应该围绕**类别定义、涵盖范围、主要特征、下属分类方向、与其他类别的关系**等
- 不需要具体到单个实例的数据（如某家公司的具体营收数据、某个人的履历）
- 接地规则、合并规则、编辑规则等通用规则保持不变
- 输出结构不变（SUMMARY 行 + Markdown）

具体改动点：
- `WikiPageModifySystemPrompt`：通用编辑规则（接地、合并、输出格式）保留，不需要大改；主要是内容引导上的措辞调整
- `WikiPageModifyUserPrompt`：`<page_metadata>` 中 `type: entity` 的语义从"具体实体"变为"第二层本体类"，但数据结构不变

**注**：这两个 prompt 的改动相对轻量，因为页面内容生成主要受输入的 new_information（chunk 引用内容）驱动，输入是类别级的，输出自然会是类别级的。主要改动在指令措辞上。

---

### 7. WikiTaxonomyPlanPrompt（taxonomy 升级为本体 L1）

**当前**：taxonomy 是纯导航目录，最多两级，按"条目本质是什么"自由分类，分类维度随意。

**改造**：将 taxonomy 第一层升级为**本体 L1 层**（顶层领域分类）：
- 明确 taxonomy 第一层 = 本体第一层，是 L2 实体类的父分类
- **严格限制只有一级目录**，不允许二级子目录，避免视觉上变成三层本体
- 引导 LLM 按统一的顶层领域维度分类（如按领域/行业分：法律、社会保险、保险产品、金融监管等），而不是按"主体类别/机构类别/文书类别"这种 L2 维度分
- 分类命名风格：简洁的领域名（"法律"、"社会保险"、"保险监管"），不要"XX相关"、"XX类别"这种模糊后缀
- 概念（concept）页面的 taxonomy 分类保持不变，仍按其自身逻辑归类

**输出结构不变**：仍然是 `{"assignments": [{"slug": "...", "path": [...]}, ...]}`，Go 代码不用改。
- 只是 path 数组长度从"最多 2 个"变为"恰好 1 个"（实体类页面）
- 代码层面不做强制限制，靠提示词约束

---

### 8. WikiSummaryPrompt（间接影响）

**当前**：摘要中链接到的是具体实体页面。

**改造**：
- available_wiki_pages 中的实体从具体实例变为 L2 本体类，LLM 选择链接对象时会自然调整
- 不需要结构性改动，只需要把示例中的 entity 示例从具体实例改为类别级
- 输出结构不变（SUMMARY 行 + Markdown）

---

## 不改动的提示词

| 提示词 | 原因 |
|--------|------|
| `WikiIndexIntroPrompt` | 索引页介绍，影响极小，随内容自然变化 |
| `WikiIndexIntroUpdatePrompt` | 同上 |

## 改动模式

所有改动遵循以下原则：

1. **输出 JSON 结构完全不变** — 确保 Go 侧的 `json.Unmarshal` 不需要改
2. **字段名完全不变** — `name`, `slug`, `aliases`, `description`, `details` 都保留
3. **slug 格式不变** — 仍然是 `entity/xxx` 和 `concept/xxx`
4. **只改定义、示例、描述措辞** — 把"具体实体"的语境全部换成"类别/本体类"的语境
5. **concept 部分保持原样** — 概念提取不动，后续再清

## 验证方式

1. **静态检查**：`go build` 通过，确保没有语法错误
2. **模板测试**：运行 `go test ./internal/agent/ -run Wiki` 检查 prompt 模板可正常解析
3. **语言测试**：运行 `go test ./internal/application/service/ -run Wiki.*Language` 检查多语言渲染
4. **手动验证 - 实体提取**：上传测试文档，观察生成的实体页面是否为 L2 类别级（而非具体实例）
5. **手动验证 - taxonomy 层级**：确认左侧目录树只有一级文件夹（L1 领域分类），没有二级子目录
6. **手动验证 - 本体一致性**：L1（目录）+ L2（实体页面）的两层结构清晰，不造成多层本体的误解

---

## 阶段二：实体类关系提取（新增功能）

### 背景与目标

Wiki 实体类页面之间存在丰富的语义关系（如"监管机构类 监管 保险机构类"、"法律法规类 规定 法律责任类"）。当前这些关系只体现在页面内容的交叉链接（out_links / in_links）中，没有结构化的关系记录。

**目标**：新增实体类之间的结构化关系提取，为每个实体类对生成明确的关系类型和关系描述，存储在独立的新表中，不影响原有表结构和逻辑。

**范围说明**：
- 关系的两端都是 Wiki 页面（实体类 entity、概念 concept），以实体类为主
- 不是 neo4j 图谱，也不是知识图谱的三元组提取
- 是 Wiki 内部实体类页面之间的语义关系结构化

### 设计原则

1. **只做新增**：新增表、新增函数、新增提示词，不改原表结构、不改原有核心逻辑
2. **复用现有流水线机制**：利用 finalize 阶段的去抖合并、避免重复处理的基础设施
3. **增量生成，不重复计算**：已有关系不重新生成，只处理新增/变更的页面关联
4. **LLM 驱动**：关系类型和描述由 LLM 根据两个页面的内容生成，不是规则匹配

### 新增数据表：wiki_page_relations

#### 表结构设计

```sql
CREATE TABLE IF NOT EXISTS wiki_page_relations (
    id                    VARCHAR(36) PRIMARY KEY,          -- UUID
    tenant_id             BIGINT NOT NULL,                  -- 多租户
    knowledge_base_id     VARCHAR(36) NOT NULL,             -- 所属知识库
    source_slug           VARCHAR(255) NOT NULL,            -- 源实体 slug
    target_slug           VARCHAR(255) NOT NULL,            -- 目标实体 slug
    relation_type         VARCHAR(64) NOT NULL,             -- 关系类型（短标签，如"监管"、"包含"、"适用"）
    relation_label        VARCHAR(128) NOT NULL,            -- 关系正向标签（人类可读，如"监管"）
    reverse_label         VARCHAR(128) NOT NULL DEFAULT '', -- 关系反向标签（如"被监管"）
    description           TEXT NOT NULL DEFAULT '',         -- 关系描述（一段话，说明两者关系的具体内涵）
    confidence            FLOAT NOT NULL DEFAULT 1.0,       -- 置信度
    source_page_type      VARCHAR(32) NOT NULL,             -- 源页面类型（entity/concept）
    target_page_type      VARCHAR(32) NOT NULL,             -- 目标页面类型
    generated_by          VARCHAR(16) NOT NULL DEFAULT 'pipeline', -- 生成方式 pipeline/user/agent
    version               INT NOT NULL DEFAULT 1,           -- 关系版本
    created_at            TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);
```

#### 索引设计

```sql
-- 知识库内按源 slug 查询所有关系
CREATE INDEX IF NOT EXISTS idx_wiki_relations_kb_source 
    ON wiki_page_relations (knowledge_base_id, source_slug);

-- 知识库内按目标 slug 查询（反向查询）
CREATE INDEX IF NOT EXISTS idx_wiki_relations_kb_target 
    ON wiki_page_relations (knowledge_base_id, target_slug);

-- 唯一约束：同一知识库内同一对 slug 的关系只有一条（方向敏感）
CREATE UNIQUE INDEX IF NOT EXISTS idx_wiki_relations_kb_source_target 
    ON wiki_page_relations (knowledge_base_id, source_slug, target_slug);

-- 按关系类型筛选
CREATE INDEX IF NOT EXISTS idx_wiki_relations_kb_type 
    ON wiki_page_relations (knowledge_base_id, relation_type);
```

#### 迁移文件

- `migrations/versioned/000093_wiki_page_relations.up.sql`
- `migrations/versioned/000093_wiki_page_relations.down.sql`

### 新增提示词：WikiRelationExtractPrompt

**位置**：`internal/agent/prompts_wiki.go` 新增

**功能**：给定两个实体类页面的**完整正文内容**，判断这两个实体类之间是否存在明确的语义关系，输出关系类型、正向/反向标签、关系描述。

**输入**：
- 源实体类：title + page_type + summary + **完整 content**
- 目标实体类：title + page_type + summary + **完整 content**
- 页面的完整正文内容是 LLM 判断关系的依据，关系的两端是实体类本身（不是内容里的某个具体事物）

**输出格式**：
```json
{
  "relation_type": "subclass_of",
  "relation_label": "属于",
  "reverse_label": "包含",
  "description": "人身保险产品是以人的生命和身体为保险标的的保险产品，是保险产品大类下的一个主要子类。",
  "confidence": 0.95
}
```

如果两个实体类之间没有明确的语义关系，返回 `relation_type: "none"`。

**关系类型**：开放类型，由 LLM 根据内容自由判断（snake_case 英文标识），不受预定义枚举限制。
常见关系类型举例（仅作参考，不限于此）：
- subclass_of — 子类/父类
- part_of — 组成/部分
- regulates / governs — 监管/治理
- contains / includes — 包含/包括
- related_to — 一般关联
- disjoint_from — 互斥
- 等等...

### 生成时机与触发逻辑

#### 放在 finalize 阶段

与 `injectCrossLinks` 一样，放在 `ProcessWikiFinalize` 中执行。理由：

1. **去抖合并**：finalize 已经有 20 秒去抖窗口，多个 batch 的变更合并处理，减少 LLM 调用次数
2. **避免重复**：finalize lane 有 per-slug 去重机制（`DedupKey = slug`），同一页面不会重复入队
3. **内容已稳定**：finalize 阶段页面内容已经写完、cross-link 已经注入，out_links 是最终状态，关系生成的依据最完整

#### 处理流程

```
ProcessWikiFinalize
  │
  ├─ rebuildIndexPage          （现有）
  ├─ cleanDeadLinks            （现有）
  ├─ injectCrossLinks          （现有）
  │
  ├─ generatePageRelations     （新增） ← 我们加在这里
  │   │
  │   ├─ 1. 收集受影响页面的 out_links（仅限 entity/concept 类型）
  │   ├─ 2. 过滤出"还没有关系记录"的实体对
  │   │   （查询 wiki_page_relations 表，跳过已有关系的 pair）
  │   ├─ 3. 从 DB 加载这些页面的**完整内容**（Content 字段，不仅是摘要）
  │   ├─ 4. 逐对调用 LLM（WikiRelationExtractPrompt）生成关系
  │   │   （并发度 5，带置信度过滤，relation_type=none 的跳过）
  │   └─ 5. 批量 upsert 写入 wiki_page_relations 表（ON CONFLICT 幂等）
  │
  └─ PruneEmptyFolderChains    （现有）
```

#### 增量判断逻辑（避免重复生成）

对每个 (source_slug, target_slug) 对：
1. 先查询 `wiki_page_relations` 表是否已有记录
2. 有记录 → 跳过（除非源页面或目标页面的 version 有变化，且关系也需要刷新）
3. 无记录 → 生成并写入

**初始版本策略**：只做新增，不做更新。即一旦关系生成就不修改。这样实现最简单，也避免了"什么时候该刷新关系"的复杂判断。后续如果需要，可以再加版本比对逻辑。

#### 并发与限流

- 复用 finalize 的 per-KB 锁，同一 KB 同时只有一个 finalize 在跑，不会并发写关系表
- LLM 调用按 pair 串行或小批量并行，控制并发量
- 受影响页面多时，只处理 top-N 最高优先级的 pair（按 in/out link 数量排序），避免一次调用 LLM 太多

### 改动文件清单

| 文件 | 改动类型 | 状态 | 说明 |
|------|----------|------|------|
| `migrations/versioned/000093_wiki_page_relations.up.sql` | 新增 | ✅ 已完成 | 正向迁移 |
| `migrations/versioned/000093_wiki_page_relations.down.sql` | 新增 | ✅ 已完成 | 回滚迁移 |
| `internal/types/wiki_page.go` | 新增 struct | ✅ 已完成 | WikiPageRelation 类型定义 |
| `internal/agent/prompts_wiki.go` | 新增提示词 | ✅ 已完成 | WikiRelationExtractPrompt（传入完整页面内容） |
| `internal/application/service/wiki_ingest.go` | 新增函数 | ✅ 已完成 | `generatePageRelations()` 关系生成逻辑（用 gorm 直接操作关系表） |
| `internal/application/service/wiki_ingest_batch.go` | 少量修改 | ✅ 已完成 | 在 `ProcessWikiFinalize` 中调用关系生成 |
| `wikiIngestService` struct | 新增字段 | ✅ 已完成 | 添加 `db *gorm.DB` 字段用于关系表操作 |
| `internal/application/service/wiki_page.go` | 新增方法 | ⏸️ 暂不做 | 关系表的 CRUD 封装（当前直接用 gorm 操作，后续可封装） |
| `internal/router/routes_wiki.go` | 新增路由 | ⏸️ 暂不做 | 关系查询 API（后续前端展示需要时再加） |

### 不改的部分

- `wiki_pages` 表结构完全不动
- 实体类提取逻辑不动
- 去重、taxonomy、cross-link 等现有逻辑不动
- 前端页面展示不动（关系数据先存着，后续再考虑怎么展示）

### 验证方式

1. **静态检查**：`go build` 通过，migration 执行成功
2. **单元测试**：关系表 CRUD 方法测试
3. **手动验证**：
   - 上传文档，生成实体类页面
   - 检查 wiki_page_relations 表中是否生成了合理的关系记录
   - 验证关系类型和描述是否准确
   - 验证同一对实体不会重复生成关系
4. **边界测试**：
   - 两个完全无关的实体 → 不生成关系（has_relation=false）
   - 关系方向正确性（A 监管 B → B 被监管 A）
   - 概念页面之间是否也生成关系（按配置控制）

---

## 阶段三：关系前端展示（Wiki 页面底部）

### 背景与目标

`wiki_page_relations` 表已经有了结构化关系数据，需要在 Wiki 实体类页面底部展示出来，让用户能看到当前实体类与其他实体类之间的语义关系。

**目标**：在 Wiki 页面底部（来源文档上方）增加一个"本体关系"区块，用箭头图的形式展示当前页面作为源和目标的所有实体-实体关系。

### 设计原则

1. **只展示实体-实体关系**：源和目标都是 `entity` 类型的关系才显示，concept 相关的不显示（后续要删概念）
2. **正向+反向分两组展示**：「指向其他」和「被指向」
3. **箭头图风格**：关系标签在箭头上，源实体 ─关系─▶ 目标实体
4. **可点击跳转**：源和目标实体名都可点击跳转到对应 Wiki 页面
5. **hover 浮窗**：鼠标移到关系标签/箭头上时，显示完整的关系描述（description）
6. **位置**：放在「被链接页面」下方，「来源文档」上方

### 后端 API 设计

#### 新增接口：获取页面的实体关系

**路由**：`GET /api/v1/knowledge/:kb_id/wiki/relations/:slug`

**响应格式**：
```json
{
  "code": 0,
  "message": "success",
  "data": {
    "outgoing": [
      {
        "target_slug": "entity/insurance-products",
        "target_title": "保险产品类",
        "relation_type": "regulates",
        "relation_label": "监管",
        "description": "保险监管机构对保险产品的开发、销售、费率等进行监督管理。",
        "confidence": 0.92
      }
    ],
    "incoming": [
      {
        "source_slug": "entity/legal-acts",
        "source_title": "法律行为与程序类",
        "relation_type": "governs",
        "reverse_label": "受...规范",
        "description": "法律行为与程序类规范法律主体的行为方式和程序要求。",
        "confidence": 0.88
      }
    ]
  }
}
```

**过滤条件**：
- `source_page_type = 'entity' AND target_page_type = 'entity'`
- 按 confidence 倒序排列
- 最多 50 条（初期不分页）

#### 后端改动文件

| 文件 | 改动类型 | 说明 |
|------|----------|------|
| `internal/types/interfaces/wiki_page.go` | 新增方法签名 | `GetPageRelations(ctx, kbID, slug) (outgoing, incoming []WikiPageRelation, err error)` |
| `internal/application/service/wiki_page.go` | 新增方法实现 | 查询 wiki_page_relations 表，分别查出源和目标 |
| `internal/router/routes_knowledge.go` | 新增路由 | 注册 `/wiki/relations/:slug` 路由 |
| `internal/interfaces/handler/wiki.go` | 新增 handler | 处理关系查询请求，返回 JSON |

### 前端展示设计

#### 展示位置

在 Wiki 页面底部 footer 区域，**被链接页面（in_links）下方，来源文档（sources）上方**：

```
页面正文内容
─────────────────────────────────────────
被链接页面：...          ← 现有
─────────────────────────────────────────
本体关系                  ← 新增（本阶段）

【指向其他】
        监管
  法律主体类 ───────▶ 法律行为与程序类

        承担
  法律主体类 ───────▶ 法律责任类

【被指向】
        规范
  法律法规类 ───────▶ 法律主体类
─────────────────────────────────────────
来源文档：...            ← 现有
```

#### 视觉样式

- 关系标签在**箭头上方居中**显示，用浅色背景小标签样式
- 箭头用细线 + 箭头符号
- 左右两个实体名都是蓝色链接，可点击跳转
- 当前页面的实体名**加粗**突出显示
- 关系标签 + 箭头 hover 时显示 tooltip（完整 description）

#### 前端改动文件

| 文件 | 改动类型 | 说明 |
|------|----------|------|
| `frontend/src/api/knowledge/wiki.ts` | 新增 API 方法 | `getWikiPageRelations(kbID, slug)` |
| `frontend/src/views/knowledge/wiki/WikiBrowser.vue` | 新增展示 | 页面底部关系区块 + 数据加载逻辑 |

### 数据加载时机

- 切换页面（selectedPage 变化）时，加载该页面的关系数据
- 如果页面类型不是 entity，不加载（直接空数组）
- 加载失败静默降级（不显示关系区块，不影响页面主体展示）

### 验证方式

1. **静态检查**：前后端都能 build 通过
2. **手动验证**：
   - 打开一个实体类页面，底部能看到「本体关系」区块
   - 「指向其他」组：显示当前页 → 目标实体 的关系，关系标签在箭头上
   - 「被指向」组：显示源实体 → 当前页 的关系，关系标签在箭头上
   - 实体名可点击跳转到对应页面
   - hover 关系标签/箭头，浮窗显示 description
   - 概念页面底部不显示关系区块
   - 没有关系的实体页面底部不显示关系区块
