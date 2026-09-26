
# 法规场景本体建模实施计划

## Context

用户提出了一种全新的法规场景本体建模思路，与之前的"L1领域分类→L2实体类"自顶向下分类学本体完全不同。新思路是**自底向上、从文档出发**的场景化本体：

- **一级目录（3个）**：法规 / 条款 / 业务对象
- **二级目录**：每篇文档（法规下）/ 条款分类（条款下，可有可无）/ 业务对象分类（业务对象下，可有可无）
- **三级实体（Wiki页面）**：文档摘要 / 具体条款 / 业务对象实体，每个对应一个Wiki页面，带可跳转的slug

**最终目标**：把生成的数据真正入库，在 WeKnora 前端 Wiki 页面中呈现出来，验证效果后再修改代码实现自动化。

## 当前进展

- ✅ **第一步（数据结构）已完成**：`法规场景本体结构_v1.json`（11法规 + 37条款 + 18业务对象 + 98关系）
- ✅ **第二步（页面示例）已完成**：`法规场景_Wiki页面示例/`（4个Markdown示例页）
- ⏳ **第三步（数据入库）待实施**：将JSON数据导入现有Wiki数据库
- ⏳ **第四步（前端验证）待实施**：在前端知识库中查看效果

## 现有系统结构（已确认）

### 数据库表（Postgres，端口65432，库名WeKnora，tenant_id=10000）

**wiki_pages** — 页面主表（无需加字段，完全兼容）
- 关键字段：`id`(UUID), `tenant_id`, `knowledge_base_id`, `slug`(唯一), `title`, `page_type`, `content`, `summary`, `aliases`, `folder_id`, `category_path`(JSON), `depth`, `source_refs`(JSON), `in_links`, `out_links`, `page_metadata`(JSON)
- page_type 支持：summary / entity / concept / index / synthesis / comparison

**wiki_folders** — 目录文件夹表（无需加字段，完全兼容）
- 关键字段：`id`, `tenant_id`, `knowledge_base_id`, `parent_id`(""=根), `name`, `path`, `depth`, `sort_order`
- 页面通过 `wiki_pages.folder_id` 挂到文件夹下
- `category_path` / `depth` 是从 folder 链反规范化的缓存

**wiki_page_relations** — 结构化关系表（无需加字段，完全兼容）
- 关键字段：`source_slug`, `target_slug`, `relation_type`, `relation_label`, `reverse_label`, `description`, `confidence`, `source_page_type`, `target_page_type`

**结论：三张表都不需要新增字段，现有结构完全满足法规场景本体需求。**

### 前端渲染逻辑

- 左侧面板按 **page_type Tab** 切换（摘要/实体/概念...），每个 Tab 下展示该类型的**目录树**
- 目录树基于 `wiki_folders` 表的层级结构 + 每个文件夹下的页面
- 页面内容支持 `[[slug|显示名称]]` 格式的 wiki 链接，点击可跳转
- 关系图谱可视化（overview/ego 模式）

### 目标知识库

使用已有的"法律法规库"知识库（id: `82b8c8c8-519f-4a2e-ac1d-09f2dd8a17d9`）做演示，或者新建一个专门的法规场景本体知识库。

## 实施步骤

### 第一步：生成法规场景本体结构数据（JSON） ✅ 已完成

输出：`指南/变更文档/建模文档/建模Plan/法规场景本体结构_v1.json`
- 11份法规文档
- 37条条款（3份试点文档，平铺）
- 18个业务对象
- 98条关系

### 第二步：生成Wiki页面内容示例（Markdown）✅ 已完成

输出：`指南/变更文档/建模文档/建模Plan/法规场景_Wiki页面示例/`
- 4个示例页面（法规摘要页、条款页、2个业务对象页）

### 第三步：生成完整的页面 Markdown 内容

为所有 66 个三级实体（11摘要 + 37条款 + 18业务对象）生成完整的 Markdown 页面内容，使用 `[[slug|名称]]` 格式互链。

**产出**：一个 Python 脚本，输入 JSON 结构数据，输出每个实体的完整 Markdown 内容。

**页面内容规范**：
- **法规摘要页**（page_type: summary）：文档基本信息表 + 核心内容摘要 + 相关条款列表 + 涉及业务对象
- **条款页**（page_type: concept）：来源法规 + 条款内容 + 相关业务对象 + 关联条款
- **业务对象页**（page_type: entity）：定义/描述 + 属性表 + 规定于哪些条款 + 涉及哪些法规 + 相关业务对象

### 第四步：数据导入数据库

编写一个 Python 导入脚本，将 JSON 结构 + 页面内容写入 Postgres 数据库。

**导入内容**：
1. **wiki_folders**：创建目录结构
   - L1根目录：法规 / 条款 / 业务对象（3个一级文件夹）
   - L2子目录：法规下的11个文档文件夹（条款和业务对象当前无L2）
   
2. **wiki_pages**：创建66个页面
   - 11个 summary 页面（文档摘要）挂在 `法规/<文档名>` 文件夹下
   - 37个 concept 页面（条款）挂在 `条款` 文件夹下（平铺）
   - 18个 entity 页面（业务对象）挂在 `业务对象` 文件夹下（平铺）
   - 1个 index 页面（知识库首页）
   - 正确设置 `folder_id`, `category_path`, `depth`, `source_refs`, `in_links`, `out_links`

3. **wiki_page_relations**：创建98条关系
   - 条款 → from_document → 法规
   - 业务对象 → defined_in → 条款
   - 业务对象 → mentioned_in → 法规

**关键技术点**：
- UUID 使用 `uuid.uuid4()` 生成
- `category_path` 和 `depth` 根据 folder 链正确计算
- `in_links` / `out_links` 根据页面内容中的 `[[slug]]` 链接解析填充
- `wiki_path` 按规范生成（用于排序）
- 使用 `ON CONFLICT (slug) DO UPDATE` 支持重复执行

### 第五步：前端效果验证

1. 启动/确认 WeKnora 前端可访问（`http://localhost:8080`）
2. 进入目标知识库的 Wiki 页面
3. 验证以下效果：
   - ✅ 左侧三个 Tab（摘要/概念/实体）正确显示
   - ✅ "摘要" Tab 下显示 `法规` 目录 → 11个文档子目录 → 每个子目录下1个摘要页
   - ✅ "概念" Tab 下显示 `条款` 目录 → 37个条款页（平铺）
   - ✅ "实体" Tab 下显示 `业务对象` 目录 → 18个实体页（平铺）
   - ✅ 页面内容正确渲染，`[[slug|名称]]` 链接可点击跳转
   - ✅ 关系图谱能看到节点和连线
   - ✅ 页面面包屑（category_path）正确显示

### 第六步：输出改造方案文档（供后续代码自动化使用）

**输出文件**：`指南/变更文档/建模文档/建模Plan/法规场景本体改造方案.md`

内容：
- 结构设计说明
- 数据模型分析（结论：现有表无需新增字段）
- 代码改造点清单（文件+函数+修改内容）
  - 提取层改造：新增条款+业务对象提取
  - 目录生成改造：改为"法规/条款/业务对象"三层
  - 关系生成改造：新增三类关系
  - 提示词改造清单
- 实施优先级

## 关键决策点

1. **page_type 映射**：条款用 `concept` 类型（复用现有Tab），业务对象用 `entity` 类型，法规模块用 `summary` 类型 → **无需新增 page_type**
2. **slug 前缀**：法规 `summary/doc-xxx-...`、条款 `clause/xxx-c-xxx`、业务对象 `entity/xxx` → 符合现有 slug 命名空间规范
3. **目标知识库**：新建一个干净的"法规场景本体演示"知识库，避免污染现有数据
4. **不修改表结构**：现有 `wiki_pages` / `wiki_folders` / `wiki_page_relations` 三张表完全够用，不需要加字段

## 验证方式

1. **数据库验证**：SQL 查询确认 pages / folders / relations 数量正确
2. **前端验证**：打开浏览器查看目录树、页面内容、链接跳转、关系图谱
3. **完整性验证**：所有 slug 唯一、关系的两端都存在、页面间链接可达

## 注意事项

1. 导入脚本要幂等（可重复执行，不会产生重复数据）
2. 严格使用 UUID 作为主键，避免 ID 冲突
3. `category_path` 必须与 folder 链一致，否则前端目录显示异常
4. `in_links` / `out_links` 需同步维护，否则死链检测和图谱会有问题
