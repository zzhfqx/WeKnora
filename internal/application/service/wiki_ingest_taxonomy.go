package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/agent"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

// wikiTaxonomyItem is one entity/concept page to be filed into the directory.
type wikiTaxonomyItem struct {
	slug     string
	title    string
	pageType string
	about    string
}

// planBatchTaxonomy assigns a directory path to every entity/concept slug in the
// batch in ONE planning pass (chunked for large batches), so the whole set lands
// on a single coherent tree that reuses existing folders. This replaces per-page,
// parallel CATEGORY invention — which couldn't converge, worst of all on the
// founding batch when the KB has no folders to anchor on. The returned map is
// keyed by slug; an entry may be an empty slice when the item is unclassifiable.
// Reduce applies these only to pages that don't already have a category.
func (s *wikiIngestService) planBatchTaxonomy(
	ctx context.Context,
	chatModel chat.Chat,
	kb *types.KnowledgeBase,
	slugUpdates map[string][]SlugUpdate,
	lang string,
) map[string][]string {
	if kb == nil {
		return nil
	}
	items := collectTaxonomyItems(slugUpdates)
	if len(items) == 0 {
		return nil
	}

	// ===== 法规场景：双体系目录规划 =====
	// 体系A（法规场景）：规则式固定目录
	// 体系B（通用提取）：LLM动态分类目录（复用现有逻辑，但注入保留目录名）
	if kb.WikiConfig.IsRegulationScene() {
		return s.planRegulationSceneTaxonomy(ctx, chatModel, kb, slugUpdates, items, lang)
	}

	// Existing folders anchor reuse. Errors (e.g. a dialect without the query)
	// just mean the plan designs a fresh tree — no fatal dependency.
	var pool [][]string
	if s.wikiService != nil {
		paths, err := s.wikiService.ListDistinctCategoryPaths(ctx, kb.ID, wikiTaxonomyFolderPoolMax)
		if err != nil {
			logger.Warnf(ctx, "wiki ingest: list category paths for plan failed: %v", err)
		} else {
			pool = paths
		}
	}

	// Preprocess the pool down to the folders relevant to THIS batch, so the
	// planner reuses established folders without every prompt carrying the whole
	// directory. Small/healthy taxonomies are fed whole (best recall, no cost).
	existing := s.selectRelevantFolders(ctx, kb, items, pool)

	result := make(map[string][]string, len(items))
	for start := 0; start < len(items); start += wikiTaxonomyPlanChunkSize {
		end := start + wikiTaxonomyPlanChunkSize
		if end > len(items) {
			end = len(items)
		}
		chunk := items[start:end]

		tree := formatExistingTaxonomyForPrompt(existing)
		if strings.TrimSpace(tree) == "" {
			tree = wikiTaxonomyEmptyTreeHint
		}

		var itemsBlock strings.Builder
		for _, it := range chunk {
			fmt.Fprintf(&itemsBlock, "- slug: %s | title: %s | type: %s | about: %s\n",
				it.slug, it.title, it.pageType, previewText(it.about, 120))
		}

		raw, err := s.generateWithTemplate(ctx, chatModel, agent.WikiTaxonomyPlanPrompt, map[string]string{
			"ExistingTaxonomy": tree,
			"Items":            itemsBlock.String(),
			"Language":         lang,
		})
		if err != nil {
			logger.Warnf(ctx, "wiki ingest: taxonomy plan call failed (%d items): %v", len(chunk), err)
			continue
		}

		for slug, path := range parseTaxonomyAssignments(raw) {
			clean := types.CleanWikiCategoryPath(path)
			result[slug] = clean
			if len(clean) > 0 {
				existing = append(existing, clean) // feed forward so later chunks converge
			}
		}
	}
	return result
}

// resolvePlannedFolders reifies the planner's per-slug paths into real
// wiki_folders rows and returns a slug -> folder id map. Folder creation is
// done here, sequentially and before the parallel reduce phase, so reduce only
// assigns pre-resolved ids and never races two goroutines into creating the
// same folder. Distinct paths are resolved once and cached. Blank paths (and
// any resolution failure) map to the root and are simply omitted.
// 法规场景下，三个固定顶层目录（法规/条款/业务对象）会被设置较小的 sort_order，
// 确保它们始终置顶并按指定顺序排列。
func (s *wikiIngestService) resolvePlannedFolders(
	ctx context.Context, kb *types.KnowledgeBase, planned map[string][]string,
) map[string]string {
	if kb == nil || len(planned) == 0 || s.wikiService == nil {
		return nil
	}
	pathCache := make(map[string]string) // "a/b" -> folder id
	out := make(map[string]string, len(planned))
	for slug, path := range planned {
		clean := types.CleanWikiCategoryPath(path)
		if len(clean) == 0 {
			continue
		}
		key := strings.Join(clean, "/")
		fid, ok := pathCache[key]
		if !ok {
			resolved, _, err := s.wikiService.FindOrCreateFolderPath(ctx, kb.ID, kb.TenantID, clean)
			if err != nil {
				logger.Warnf(ctx, "wiki ingest: resolve folder %q failed: %v", key, err)
				pathCache[key] = "" // negative-cache so we don't retry per slug
				continue
			}
			fid = resolved
			pathCache[key] = fid
		}
		if fid != "" {
			out[slug] = fid
		}
	}

	// 法规场景：设置三个固定顶层目录的 sort_order，确保置顶并按指定顺序排列
	// 法规=1, 条款=2, 业务对象=3，其他目录使用默认值(0或更高)排在后面
	if kb.WikiConfig.IsRegulationScene() {
		s.applyRegulationTopFolderSortOrder(ctx, kb, pathCache)
	}

	return out
}

// applyRegulationTopFolderSortOrder 设置法规场景三个固定顶层目录的排序权重。
// 列表查询按 sort_order ASC, name ASC 排序，因此较小的值排前面。
// 三个固定目录使用负值（-3/-2/-1），确保排在所有默认 sort_order=0 的目录之前。
// 同时处理当前批次新创建的目录和历史已存在的目录。
func (s *wikiIngestService) applyRegulationTopFolderSortOrder(
	ctx context.Context, kb *types.KnowledgeBase, pathCache map[string]string,
) {
	if s.db == nil {
		return
	}
	folderOrders := map[string]int{
		"法规":     -3,
		"条款":     -2,
		"业务对象": -1,
	}
	now := time.Now()
	for name, order := range folderOrders {
		result := s.db.WithContext(ctx).
			Model(&types.WikiFolder{}).
			Where("knowledge_base_id = ? AND parent_id = ? AND name = ?",
				kb.ID, types.WikiFolderRootID, name).
			Updates(map[string]interface{}{
				"sort_order": order,
				"updated_at": now,
			})
		if result.Error != nil {
			logger.Warnf(ctx, "wiki ingest: update sort_order for folder %q failed: %v", name, result.Error)
		}
	}
}

// selectRelevantFolders narrows the existing folder pool to the subset worth
// showing the planner for THIS batch. A healthy navigation directory is small,
// so it is fed whole (perfect reuse recall, no embedding cost). Only once folders
// are numerous does similarity preprocessing kick in: all level-1 folders are
// always kept as coarse anchors, and each item pulls in its nearest deeper
// folders by embedding similarity. KBs without an embedding model (wiki-only)
// fall back to a capped feed-all.
func (s *wikiIngestService) selectRelevantFolders(
	ctx context.Context, kb *types.KnowledgeBase, items []wikiTaxonomyItem, pool [][]string,
) [][]string {
	if len(pool) <= wikiTaxonomyFeedAllMaxFolders {
		return pool
	}

	// Split into always-kept level-1 anchors and the deeper candidate folders
	// that similarity selects among.
	l1Seen := make(map[string]struct{})
	var l1Paths, deeper [][]string
	for _, p := range pool {
		if len(p) == 0 {
			continue
		}
		if _, ok := l1Seen[p[0]]; !ok {
			l1Seen[p[0]] = struct{}{}
			l1Paths = append(l1Paths, []string{p[0]})
		}
		if len(p) >= 2 {
			deeper = append(deeper, p)
		}
	}

	// Gate purely on whether an embedding model is configured — NOT on
	// NeedsEmbeddingModel(), which is false for wiki-only KBs that may still
	// opt into an embedding model purely for directory/taxonomy similarity.
	if strings.TrimSpace(kb.EmbeddingModelID) == "" || len(deeper) == 0 {
		return capFolders(pool, wikiTaxonomyPromptMaxPaths)
	}

	embedder, err := s.modelService.GetEmbeddingModel(ctx, kb.EmbeddingModelID)
	if err != nil {
		logger.Warnf(ctx, "wiki ingest: taxonomy plan embed model unavailable, feeding all folders: %v", err)
		return capFolders(pool, wikiTaxonomyPromptMaxPaths)
	}

	folderTexts := make([]string, len(deeper))
	for i, p := range deeper {
		folderTexts[i] = strings.Join(p, " / ")
	}
	itemTexts := make([]string, len(items))
	for i, it := range items {
		itemTexts[i] = strings.TrimSpace(it.title + " " + previewText(it.about, 120))
	}

	folderVecs, err := embedder.BatchEmbed(ctx, folderTexts)
	if err != nil {
		logger.Warnf(ctx, "wiki ingest: taxonomy plan folder embed failed, feeding all folders: %v", err)
		return capFolders(pool, wikiTaxonomyPromptMaxPaths)
	}
	itemVecs, err := embedder.BatchEmbed(ctx, itemTexts)
	if err != nil {
		logger.Warnf(ctx, "wiki ingest: taxonomy plan item embed failed, feeding all folders: %v", err)
		return capFolders(pool, wikiTaxonomyPromptMaxPaths)
	}

	selected := append(l1Paths, selectFoldersByVectors(deeper, folderVecs, itemVecs, wikiTaxonomyRelevantTopK)...)
	return capFolders(selected, wikiTaxonomyPromptMaxPaths)
}

// selectFoldersByVectors returns the deeper folders that rank in any item's
// top-K by cosine similarity, preserving the input order for determinism.
func selectFoldersByVectors(deeper [][]string, folderVecs, itemVecs [][]float32, topK int) [][]string {
	if len(deeper) != len(folderVecs) || len(itemVecs) == 0 || topK <= 0 {
		return nil
	}
	chosen := make(map[int]struct{})
	for _, iv := range itemVecs {
		type scored struct {
			idx int
			sim float64
		}
		ranking := make([]scored, 0, len(folderVecs))
		for fi, fv := range folderVecs {
			ranking = append(ranking, scored{fi, cosineSimilarity(iv, fv)})
		}
		sort.SliceStable(ranking, func(a, b int) bool { return ranking[a].sim > ranking[b].sim })
		for k := 0; k < topK && k < len(ranking); k++ {
			chosen[ranking[k].idx] = struct{}{}
		}
	}
	out := make([][]string, 0, len(chosen))
	for i := range deeper {
		if _, ok := chosen[i]; ok {
			out = append(out, deeper[i])
		}
	}
	return out
}

// cosineSimilarity returns the cosine of two equal-length vectors, or 0 for empty
// / mismatched / zero-norm inputs.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// capFolders truncates a folder list to at most max entries (max <= 0 = no cap).
func capFolders(paths [][]string, max int) [][]string {
	if max > 0 && len(paths) > max {
		return paths[:max]
	}
	return paths
}

// planRegulationSceneTaxonomy 为法规场景执行双体系目录规划：
//   - 体系A（法规场景固定目录）：法规/条款/业务对象 三个顶层目录，规则式分配
//   - 体系B（通用动态目录）：复用现有LLM规划逻辑，但注入保留目录名，
//     确保通用LLM不会把页面分配到三个固定目录下
func (s *wikiIngestService) planRegulationSceneTaxonomy(
	ctx context.Context,
	chatModel chat.Chat,
	kb *types.KnowledgeBase,
	slugUpdates map[string][]SlugUpdate,
	allItems []wikiTaxonomyItem,
	lang string,
) map[string][]string {
	result := make(map[string][]string, len(allItems))

	// ===== Step 1: 体系A —— 规则式固定目录分配 =====
	// 法规详情页 → "法规"
	// 条款页 → "条款"
	// 业务对象页 → "业务对象"
	var genericItems []wikiTaxonomyItem
	for _, it := range allItems {
		slug := it.slug
		switch {
		case isRegulationRegulationSlug(slug):
			result[slug] = []string{"法规"}
		case isRegulationClauseSlug(slug):
			result[slug] = []string{"条款"}
		case isRegulationBusinessObjectSlug(slug):
			result[slug] = []string{"业务对象"}
		default:
			// 体系B的页面，走LLM动态分类
			genericItems = append(genericItems, it)
		}
	}

	// 统计体系A各类页面数量
	regCount, clauseCount, boCount := 0, 0, 0
	for slug := range result {
		switch {
		case isRegulationRegulationSlug(slug):
			regCount++
		case isRegulationClauseSlug(slug):
			clauseCount++
		default:
			boCount++
		}
	}
	logger.Infof(ctx, "wiki taxonomy: regulation scene — 法规=%d, 条款=%d, 业务对象=%d, generic=%d",
		regCount, clauseCount, boCount, len(genericItems))

	// ===== Step 2: 体系B —— LLM动态分类目录（复用现有逻辑）=====
	if len(genericItems) == 0 {
		return result
	}

	// 加载现有目录池
	var pool [][]string
	if s.wikiService != nil {
		paths, err := s.wikiService.ListDistinctCategoryPaths(ctx, kb.ID, wikiTaxonomyFolderPoolMax)
		if err != nil {
			logger.Warnf(ctx, "wiki ingest: list category paths for plan failed: %v", err)
		} else {
			pool = paths
		}
	}

	// 过滤掉保留目录（法规/条款/业务对象），避免LLM把通用页面分到这些目录
	filteredPool := filterReservedFolders(pool, types.RegulationFixedTopFolders)

	existing := s.selectRelevantFolders(ctx, kb, genericItems, filteredPool)

	// 构建保留目录提示文本
	reservedFoldersText := buildReservedFoldersText(types.RegulationFixedTopFolders)

	for start := 0; start < len(genericItems); start += wikiTaxonomyPlanChunkSize {
		end := start + wikiTaxonomyPlanChunkSize
		if end > len(genericItems) {
			end = len(genericItems)
		}
		chunk := genericItems[start:end]

		tree := formatExistingTaxonomyForPrompt(existing)
		if strings.TrimSpace(tree) == "" {
			tree = wikiTaxonomyEmptyTreeHint
		}

		var itemsBlock strings.Builder
		for _, it := range chunk {
			fmt.Fprintf(&itemsBlock, "- slug: %s | title: %s | type: %s | about: %s\n",
				it.slug, it.title, it.pageType, previewText(it.about, 120))
		}

		raw, err := s.generateWithTemplate(ctx, chatModel, agent.WikiTaxonomyPlanPrompt, map[string]string{
			"ExistingTaxonomy": tree,
			"Items":            itemsBlock.String(),
			"Language":         lang,
			"ReservedFolders":  reservedFoldersText,
		})
		if err != nil {
			logger.Warnf(ctx, "wiki ingest: regulation taxonomy plan call failed (%d items): %v", len(chunk), err)
			continue
		}

		for slug, path := range parseTaxonomyAssignments(raw) {
			clean := types.CleanWikiCategoryPath(path)
			// 二次校验：确保通用页面不会被分到保留目录下
			clean = sanitizePathAgainstReserved(clean, types.RegulationFixedTopFolders)
			result[slug] = clean
			if len(clean) > 0 {
				existing = append(existing, clean) // 让后续的chunk复用
			}
		}
	}

	return result
}

// filterReservedFolders 从目录池中过滤掉保留目录及其子目录。
// 确保LLM规划时看不到保留目录，不会尝试把通用页面分配到那里。
func filterReservedFolders(pool [][]string, reserved []string) [][]string {
	if len(reserved) == 0 {
		return pool
	}
	reservedSet := make(map[string]bool, len(reserved))
	for _, r := range reserved {
		reservedSet[r] = true
	}
	filtered := make([][]string, 0, len(pool))
	for _, path := range pool {
		if len(path) == 0 {
			filtered = append(filtered, path)
			continue
		}
		if !reservedSet[path[0]] {
			filtered = append(filtered, path)
		}
	}
	return filtered
}

// buildReservedFoldersText 构建保留目录的提示文本，注入到taxonomy prompt中。
func buildReservedFoldersText(reserved []string) string {
	if len(reserved) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("以下目录名是系统保留目录，**绝对不能**把任何页面分配到这些目录下（包括不能使用这些名称作为顶层或子目录名）：\n")
	for _, name := range reserved {
		fmt.Fprintf(&sb, "- %s\n", name)
	}
	sb.WriteString("如果模型认为某个条目应该属于这些领域，请为其创建其他合适的分类目录名称。\n")
	return sb.String()
}

// sanitizePathAgainstReserved 校验并清理路径，确保不会命中保留目录。
// 如果路径的第一层是保留目录名，则置空（让页面落到根目录）。
func sanitizePathAgainstReserved(path []string, reserved []string) []string {
	if len(path) == 0 {
		return path
	}
	for _, r := range reserved {
		if path[0] == r {
			return nil // 命中保留目录，置空
		}
	}
	return path
}

// collectTaxonomyItems extracts the entity/concept pages from a batch's slug
// updates, in deterministic slug order so chunk boundaries are stable. Summary
// and retract-only slugs are skipped (they carry no directory category).
func collectTaxonomyItems(slugUpdates map[string][]SlugUpdate) []wikiTaxonomyItem {
	slugs := make([]string, 0, len(slugUpdates))
	for slug := range slugUpdates {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)

	items := make([]wikiTaxonomyItem, 0, len(slugs))
	for _, slug := range slugs {
		for _, u := range slugUpdates[slug] {
			if u.Type != types.WikiPageTypeEntity && u.Type != types.WikiPageTypeConcept {
				continue
			}
			title := strings.TrimSpace(u.Item.Name)
			if title == "" {
				title = slug
			}
			items = append(items, wikiTaxonomyItem{
				slug:     slug,
				title:    title,
				pageType: u.Type,
				about:    strings.TrimSpace(u.Item.Description),
			})
			break // one entry per slug is enough for classification
		}
	}
	return items
}

// parseTaxonomyAssignments parses the planning LLM's JSON into a slug → path map.
// Malformed output yields nil; individual entries with a blank slug are dropped.
func parseTaxonomyAssignments(raw string) map[string][]string {
	raw = cleanLLMJSON(raw)
	if raw == "" {
		return nil
	}
	var parsed struct {
		Assignments []struct {
			Slug string   `json:"slug"`
			Path []string `json:"path"`
		} `json:"assignments"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	out := make(map[string][]string, len(parsed.Assignments))
	for _, a := range parsed.Assignments {
		slug := strings.TrimSpace(a.Slug)
		if slug == "" {
			continue
		}
		out[slug] = a.Path
	}
	return out
}
