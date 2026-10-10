package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Tencent/WeKnora/internal/agent"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"golang.org/x/sync/errgroup"
)

const (
	// maxRunesPerCitationBatch bounds the size of a single chunk-citation
	// batch by rune count (a fast approximation of token count). Small enough
	// that batches stay comfortably inside the LLM's context and output
	// budgets, large enough that most short/medium documents classify in a
	// single batch. Tune via benchmarks if needed.
	maxRunesPerCitationBatch = 12000

	// maxCitationBatchConcurrency limits parallelism across chunk-citation
	// batches so a single long document can't saturate the synthesis model.
	maxCitationBatchConcurrency = 4
)

// citationBatchResult is the JSON shape we expect back from one invocation of
// WikiChunkCitationPrompt.
type citationBatchResult struct {
	Citations map[string][]string   `json:"citations"`
	NewSlugs  []newSlugFromCitation `json:"new_slugs"`
}

// newSlugFromCitation is the shape of an entry in the "new_slugs" array of
// WikiChunkCitationPrompt. Mirrors extractedItem but also carries a "type"
// tag because this prompt emits entities and concepts in a single array.
type newSlugFromCitation struct {
	Type         string   `json:"type"`
	Name         string   `json:"name"`
	Slug         string   `json:"slug"`
	Aliases      []string `json:"aliases"`
	Description  string   `json:"description"`
	Details      string   `json:"details"`
	SourceChunks []string `json:"source_chunks"`
}

// citationPipelineOutcome carries the raw numbers produced by the Pass
// 0 + classification flow so callers (mapOneDocument) can log a single
// unified stat line.
type citationPipelineOutcome struct {
	CandidateCount int
	ChunkCount     int
	BatchCount     int
	CitedChunks    int
	UncitedSlugs   int
	NewSlugCount   int
}

// extractCandidateSlugs runs Pass 0 of the chunk-cited pipeline: it scans the
// full (reconstructed) document text and returns a lightweight skeleton of
// every significant entity/concept. Unlike the legacy single-shot extraction,
// this pass explicitly does NOT ask the LLM to paraphrase full facts per item;
// those will come from the chunk-citation pass instead.
//
// Returns (entities, concepts, slugItems, error). On LLM or parse failure it
// returns an error — the caller can then fall back to the legacy extractor.
// WikiCandidateSlugPrompt（候选slug提取，第一遍）
func (s *wikiIngestService) extractCandidateSlugs(
	ctx context.Context,
	chatModel chat.Chat,
	kbID string,
	content, lang string,
	oldPageSlugs map[string]bool,
	batchCtx *WikiBatchContext,
) ([]extractedItem, []extractedItem, map[string]extractedItem, error) {
	var prevSlugsText string
	if len(oldPageSlugs) > 0 {
		var sb strings.Builder
		for slug := range oldPageSlugs {
			if !strings.HasPrefix(slug, "entity/") && !strings.HasPrefix(slug, "concept/") {
				continue
			}
			fmt.Fprintf(&sb, "- %s\n", slug)
		}
		prevSlugsText = sb.String()
	}
	if prevSlugsText == "" {
		prevSlugsText = "(none — this is a new document)"
	}

	granularity := batchCtx.ExtractionGranularity.Normalize()
	raw, err := s.generateWithTemplate(ctx, chatModel, agent.WikiCandidateSlugPrompt, map[string]string{
		"Content":             content,
		"Language":            lang,
		"PreviousSlugs":       prevSlugsText,
		"Granularity":         string(granularity),
		"GranularityGuidance": agent.WikiGranularityGuidance(string(granularity)),
		"CustomInstructions":  batchCtx.ExtractionInstructions,
		"InstructionScope":    "wiki_extraction",
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("candidate slug extraction failed: %w", err)
	}

	raw = cleanLLMJSON(raw)

	var result combinedExtraction
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		logger.Warnf(ctx, "wiki ingest: failed to parse candidate slug JSON: %v\nRaw: %s", err, raw)
		return nil, nil, nil, fmt.Errorf("parse candidate slug JSON: %w", err)
	}

	result.Entities, result.Concepts = s.deduplicateExtractedBatch(
		ctx, chatModel, kbID, result.Entities, result.Concepts, batchCtx,
	)

	slugItems := make(map[string]extractedItem, len(result.Entities)+len(result.Concepts))
	for _, item := range result.Entities {
		if item.Slug != "" && item.Name != "" {
			slugItems[item.Slug] = item
		}
	}
	for _, item := range result.Concepts {
		if item.Slug != "" && item.Name != "" {
			slugItems[item.Slug] = item
		}
	}

	return result.Entities, result.Concepts, slugItems, nil
}

// chunkBatch groups chunks that will be sent in a single WikiChunkCitationPrompt call.
type chunkBatch struct {
	chunks       []*types.Chunk
	handles      *modelcontext.HandleTable
	totalRuneLen int
}

// splitChunksIntoCitationBatches partitions chunks into batches whose total
// rune count stays under maxRunesPerCitationBatch. Chunk order (by ChunkIndex)
// is preserved, and a chunk that by itself exceeds the budget occupies its
// own batch so we never silently drop content. Each chunk is assigned a short
// handle ("c000", "c001", ...) that the prompt uses in place of the raw UUID.
// The invocation-local table translates model output back to stable chunk IDs
// before any result reaches application state.
func splitChunksIntoCitationBatches(chunks []*types.Chunk) []chunkBatch {
	// Only cite text chunks — image/ocr chunks are already merged into the
	// text content via reconstructEnrichedContent and the LLM doesn't see
	// them as standalone units.
	filtered := make([]*types.Chunk, 0, len(chunks))
	for _, c := range chunks {
		if c == nil || c.Content == "" {
			continue
		}
		if c.ChunkType != types.ChunkTypeText && c.ChunkType != "" {
			continue
		}
		filtered = append(filtered, c)
	}
	if len(filtered) == 0 {
		return nil
	}

	// Preserve document order for human-readable citation
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].ChunkIndex == filtered[j].ChunkIndex {
			return filtered[i].StartAt < filtered[j].StartAt
		}
		return filtered[i].ChunkIndex < filtered[j].ChunkIndex
	})

	var batches []chunkBatch
	newBatch := func() chunkBatch {
		return chunkBatch{handles: modelcontext.NewHandleTable("c", 3, 0)}
	}
	current := newBatch()

	flush := func() {
		if len(current.chunks) == 0 {
			return
		}
		batches = append(batches, current)
		current = newBatch()
	}

	for _, c := range filtered {
		runeLen := len([]rune(c.Content))
		// If adding this chunk would exceed the budget and the current batch
		// isn't empty, flush first so the oversized chunk is not merged with
		// already-queued ones. Oversized chunks still go through — they just
		// get their own batch.
		if len(current.chunks) > 0 && current.totalRuneLen+runeLen > maxRunesPerCitationBatch {
			flush()
		}

		current.handles.Register(c.ID)
		current.chunks = append(current.chunks, c)
		current.totalRuneLen += runeLen
	}
	flush()

	return batches
}

// renderCandidateSlugsXML renders candidate slugs as a compact list suitable
// for the prompt's <candidate_slugs> block.
func renderCandidateSlugsXML(entities, concepts []extractedItem) string {
	var sb strings.Builder
	write := func(item extractedItem, kind string) {
		aliases := ""
		if len(item.Aliases) > 0 {
			aliases = fmt.Sprintf(" aliases=%q", strings.Join(item.Aliases, ", "))
		}
		fmt.Fprintf(&sb, "- slug: %s, type: %s, name: %q%s, description: %s\n",
			item.Slug, kind, item.Name, aliases, item.Description)
	}
	for _, item := range entities {
		if item.Slug == "" || item.Name == "" {
			continue
		}
		write(item, "entity")
	}
	for _, item := range concepts {
		if item.Slug == "" || item.Name == "" {
			continue
		}
		write(item, "concept")
	}
	return sb.String()
}

// renderChunksXML formats one batch's chunks into the <chunks> block, using
// per-batch handles (c000, c001, ...) instead of raw UUIDs.
func renderChunksXML(batch chunkBatch) string {
	var sb strings.Builder
	for _, c := range batch.chunks {
		handle, _ := batch.handles.Handle(c.ID)
		fmt.Fprintf(&sb, "<c id=%q index=\"%d\">\n%s\n</c>\n", handle, c.ChunkIndex, c.Content)
	}
	return sb.String()
}

// classifyChunkCitations runs Pass 1..N of the chunk-cited pipeline: given a
// set of candidate slugs (from Pass 0) and the document's chunks, it asks the
// LLM which chunks substantively discuss each candidate. Results across
// batches are merged into a single slug → union(chunk_id) map, and any
// "new_slugs" that Pass 0 missed are collected separately.
//
// Returns (citations, newSlugs, batchCount). citations is keyed by slug and
// contains real chunk UUIDs (already translated from batch handles). newSlugs
// likewise carry real chunk UUIDs in SourceChunks.
// WikiChunkCitationPrompt（chunk引用分配，第二遍及以后）
func (s *wikiIngestService) classifyChunkCitations(
	ctx context.Context,
	chatModel chat.Chat,
	candidatesXML string,
	chunks []*types.Chunk,
	lang string,
	batchCtx *WikiBatchContext,
) (map[string][]string, []newSlugFromCitation, int) {
	batches := splitChunksIntoCitationBatches(chunks)
	if len(batches) == 0 || strings.TrimSpace(candidatesXML) == "" {
		return map[string][]string{}, nil, 0
	}

	// Merge state. Using sets keyed by (slug, chunkID) to dedup across
	// batches; order is re-imposed from chunk ChunkIndex at the end.
	var mu sync.Mutex
	citationSet := make(map[string]map[string]bool) // slug → set of real chunk IDs
	var newSlugsAll []newSlugFromCitation

	eg, ectx := errgroup.WithContext(ctx)
	eg.SetLimit(maxCitationBatchConcurrency)

	for bi := range batches {
		batch := batches[bi]
		batchIdx := bi
		eg.Go(func() error {
			chunksXML := renderChunksXML(batch)
			raw, err := s.generateWithTemplate(ectx, chatModel, agent.WikiChunkCitationPrompt, map[string]string{
				"CandidateSlugs": candidatesXML,
				"ChunksXML":      chunksXML,
				"Language":       lang,
			})
			if err != nil {
				logger.Warnf(ectx, "wiki ingest: citation batch %d failed: %v", batchIdx, err)
				return nil // don't abort peer batches
			}
			raw = cleanLLMJSON(raw)

			var parsed citationBatchResult
			if jerr := json.Unmarshal([]byte(raw), &parsed); jerr != nil {
				logger.Warnf(ectx, "wiki ingest: citation batch %d parse failed: %v\nRaw: %s", batchIdx, jerr, raw)
				return nil
			}

			// Translate handles → real chunk UUIDs; drop unknown handles.
			mu.Lock()
			defer mu.Unlock()

			for slug, handleList := range parsed.Citations {
				if slug == "" {
					continue
				}
				set, ok := citationSet[slug]
				if !ok {
					set = make(map[string]bool)
					citationSet[slug] = set
				}
				for _, handle := range handleList {
					realID, known := batch.handles.Resolve(handle)
					if !known {
						logger.Warnf(ectx, "wiki ingest: citation batch %d referenced unknown chunk handle %q for slug %s", batchIdx, handle, slug)
						continue
					}
					set[realID] = true
				}
			}

			for _, ns := range parsed.NewSlugs {
				if ns.Slug == "" || ns.Name == "" {
					continue
				}
				real := make([]string, 0, len(ns.SourceChunks))
				for _, handle := range ns.SourceChunks {
					if id, ok := batch.handles.Resolve(handle); ok {
						real = append(real, id)
					}
				}
				ns.SourceChunks = real
				newSlugsAll = append(newSlugsAll, ns)
			}
			return nil
		})
	}
	_ = eg.Wait()

	// Build a stable chunk-order so the final citations come out in document order.
	chunkOrder := make(map[string]int, len(chunks))
	for _, c := range chunks {
		chunkOrder[c.ID] = c.ChunkIndex
	}

	out := make(map[string][]string, len(citationSet))
	for slug, set := range citationSet {
		ids := make([]string, 0, len(set))
		for id := range set {
			ids = append(ids, id)
		}
		sort.SliceStable(ids, func(i, j int) bool {
			return chunkOrder[ids[i]] < chunkOrder[ids[j]]
		})
		out[slug] = ids
	}

	return out, newSlugsAll, len(batches)
}

// resolveCitedChunks loads the content of every chunk referenced by the
// given additions in a single batched query per knowledge ID, returning a
// map[chunkID]content. Missing / out-of-tenant chunk IDs are silently skipped
// (logged at warn level) so the Reduce phase can gracefully fall back to the
// Details paraphrase.
func (s *wikiIngestService) resolveCitedChunks(
	ctx context.Context,
	tenantID uint64,
	additions []SlugUpdate,
) map[string]string {
	// Group chunk IDs by knowledge ID — chunk repo queries are scoped by
	// tenant but not by knowledge, so we could fetch everything in one go.
	// We batch per knowledge anyway to keep the IN(...) list bounded for
	// large multi-knowledge reduces.
	byKnowledge := make(map[string]map[string]bool)
	for _, add := range additions {
		if add.KnowledgeID == "" {
			continue
		}
		for _, chunkID := range add.SourceChunks {
			if chunkID == "" {
				continue
			}
			set, ok := byKnowledge[add.KnowledgeID]
			if !ok {
				set = make(map[string]bool)
				byKnowledge[add.KnowledgeID] = set
			}
			set[chunkID] = true
		}
	}
	if len(byKnowledge) == 0 {
		return nil
	}

	out := make(map[string]string)
	for _, idSet := range byKnowledge {
		ids := make([]string, 0, len(idSet))
		for id := range idSet {
			ids = append(ids, id)
		}
		if len(ids) == 0 {
			continue
		}
		chunks, err := s.chunkRepo.ListChunksByID(ctx, tenantID, ids)
		if err != nil {
			logger.Warnf(ctx, "wiki ingest: failed to resolve cited chunks: %v", err)
			continue
		}
		for _, c := range chunks {
			if c == nil || c.Content == "" {
				continue
			}
			out[c.ID] = c.Content
		}
	}
	return out
}

// collectCitedChunkContent materializes the verbatim content of every
// referenced chunk, concatenated in the order provided. Chunk IDs that can't
// be resolved are silently dropped (logged upstream).
func collectCitedChunkContent(chunkIDs []string, contentByID map[string]string) string {
	if len(chunkIDs) == 0 || len(contentByID) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, id := range chunkIDs {
		content, ok := contentByID[id]
		if !ok || strings.TrimSpace(content) == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(content)
	}
	return sb.String()
}

// mergeCitationsIntoItems backfills SourceChunks on every extractedItem from
// the citation map, and augments the item slices with any genuinely new slugs
// the citation pass discovered. Items whose slug is not in citations are left
// untouched — Reduce will fall back to Description/Details for them.
//
// Returns the updated entity and concept slices plus a count of candidates
// that ended with zero citations (for observability).
func mergeCitationsIntoItems(
	entities, concepts []extractedItem,
	citations map[string][]string,
	newSlugs []newSlugFromCitation,
) ([]extractedItem, []extractedItem, int) {
	uncited := 0

	for i := range entities {
		ids := citations[entities[i].Slug]
		entities[i].SourceChunks = ids
		if len(ids) == 0 {
			uncited++
		}
	}
	for i := range concepts {
		ids := citations[concepts[i].Slug]
		concepts[i].SourceChunks = ids
		if len(ids) == 0 {
			uncited++
		}
	}

	// Append new_slugs discovered by the citation pass, avoiding duplicates.
	existingSlugs := make(map[string]bool, len(entities)+len(concepts))
	for _, e := range entities {
		existingSlugs[e.Slug] = true
	}
	for _, c := range concepts {
		existingSlugs[c.Slug] = true
	}

	// Aggregate per-slug across batches so the same "newly discovered" slug
	// surfacing in multiple batches merges into a single item.
	type mergedNew struct {
		item extractedItem
		typ  string
	}
	merged := make(map[string]*mergedNew)
	slugOrder := []string{}

	for _, ns := range newSlugs {
		if ns.Slug == "" || ns.Name == "" {
			continue
		}
		if existingSlugs[ns.Slug] {
			// Treat as citation for existing candidate
			continue
		}
		kind := strings.TrimSpace(strings.ToLower(ns.Type))
		if kind == "" {
			if strings.HasPrefix(ns.Slug, "concept/") {
				kind = "concept"
			} else {
				kind = "entity"
			}
		}
		existing, ok := merged[ns.Slug]
		if !ok {
			existing = &mergedNew{
				item: extractedItem{
					Name:         ns.Name,
					Slug:         ns.Slug,
					Aliases:      append([]string(nil), ns.Aliases...),
					Description:  ns.Description,
					Details:      ns.Details,
					SourceChunks: append([]string(nil), ns.SourceChunks...),
				},
				typ: kind,
			}
			merged[ns.Slug] = existing
			slugOrder = append(slugOrder, ns.Slug)
			continue
		}
		// Union source chunks
		seen := make(map[string]bool, len(existing.item.SourceChunks))
		for _, id := range existing.item.SourceChunks {
			seen[id] = true
		}
		for _, id := range ns.SourceChunks {
			if !seen[id] {
				existing.item.SourceChunks = append(existing.item.SourceChunks, id)
				seen[id] = true
			}
		}
	}

	for _, slug := range slugOrder {
		m := merged[slug]
		if m.typ == "concept" {
			concepts = append(concepts, m.item)
		} else {
			entities = append(entities, m.item)
		}
	}

	return entities, concepts, uncited
}

// ============================================================
// 法规场景本体（Regulation Scenario Ontology）提取函数
// ============================================================

// generateRegulationPages 生成法规详情页 + 精简摘要页 + 法规元数据。
// 一次LLM调用输出JSON，包含三个字段：
//   - detail_content: 法规详情页（entity类型，"法规"目录下）
//   - regulation_meta: 结构化元数据
//
// 这是法规场景体系A的Phase 1——摘要式生成（整文处理），
// 不是chunk级提取。
//
// 注意：法规精简摘要页不再在这里生成，而是走通用 summary 生成机制，
// 只是法规场景下使用精简版提示词（WikiRegulationSummaryPrompt）。
func (s *wikiIngestService) generateRegulationPages(
	ctx context.Context,
	chatModel chat.Chat,
	content, lang, docTitle string,
	batchCtx *WikiBatchContext,
) (detailContent string, meta regulationDocMeta, err error) {
	const maxAttempts = 3
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		raw, genErr := s.generateWithTemplate(ctx, chatModel,
			agent.WikiRegulationPageGenPrompt, map[string]string{
				"Content":            content,
				"Language":           lang,
				"DocTitle":           docTitle,
				"CustomInstructions": batchCtx.ContentInstructions,
			})
		if genErr != nil {
			lastErr = fmt.Errorf("generate regulation pages: %w", genErr)
			continue
		}

		raw = cleanLLMJSON(raw)
		if raw == "" {
			lastErr = fmt.Errorf("generate regulation pages: empty response")
			continue
		}

		// 尝试提取第一个完整的 JSON 对象（LLM 有时会在 JSON 后输出多余内容）
		raw = extractFirstJSONObject(raw)
		if raw == "" {
			lastErr = fmt.Errorf("generate regulation pages: no valid JSON object found")
			continue
		}

		var parsed struct {
			RegulationMeta regulationDocMeta `json:"regulation_meta"`
			DetailContent  string            `json:"detail_content"`
		}
		if parseErr := json.Unmarshal([]byte(raw), &parsed); parseErr != nil {
			preview := raw
			if len(preview) > 500 {
				preview = preview[:500] + "..."
			}
			logger.Warnf(ctx, "wiki ingest: regulation pages parse failed (attempt %d/%d), raw preview: %s",
				attempt, maxAttempts, preview)
			lastErr = fmt.Errorf("parse regulation pages: %w", parseErr)
			continue
		}

		logger.Infof(ctx, "wiki ingest: regulation pages generated (attempt %d) — short_title=%q, doc_number=%q, detail_len=%d",
			attempt, parsed.RegulationMeta.ShortTitle, parsed.RegulationMeta.DocNumber, len(parsed.DetailContent))

		return parsed.DetailContent, parsed.RegulationMeta, nil
	}

	return "", regulationDocMeta{}, lastErr
}

// extractFirstJSONObject 从字符串中提取第一个完整的 JSON 对象。
// LLM 有时会在合法 JSON 后面追加说明文字或多余的 }，导致解析失败。
// 这个函数通过括号匹配找到第一个 { 到配对的 } 的子串。
// 如果找不到合法 JSON，返回空字符串。
func extractFirstJSONObject(s string) string {
	s = strings.TrimSpace(s)

	// 找到第一个 {
	first := strings.Index(s, "{")
	if first < 0 {
		return ""
	}

	// 括号匹配
	depth := 0
	inString := false
	escape := false
	for i := first; i < len(s); i++ {
		ch := s[i]
		if escape {
			escape = false
			continue
		}
		if ch == '\\' {
			escape = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if ch == '{' {
			depth++
		} else if ch == '}' {
			depth--
			if depth == 0 {
				return s[first : i+1]
			}
		}
	}

	return ""
}

// extractRegulationCandidateSlugs 从法规文档中提取制度性条款和核心业务对象。
// 这是法规场景体系A的Phase 2——chunk级提取。
//
// 返回值：
//   - clauses: 制度性条款列表（entity/clause-xxx）
//   - businessObjects: 核心业务对象列表（entity/<name>）
//   - slugItems: 统一的 slug -> extractedItem 映射（包含条款和业务对象）
func (s *wikiIngestService) extractRegulationCandidateSlugs(
	ctx context.Context,
	chatModel chat.Chat,
	kbID string,
	content, lang string,
	oldPageSlugs map[string]bool,
	batchCtx *WikiBatchContext,
	docTitle string,
) (clauses []extractedItem, businessObjects []extractedItem,
	slugItems map[string]extractedItem, err error) {

	// 构造旧 slug 列表文本（只包含 entity/ 前缀的页面，
	// 因为法规场景的条款和业务对象都是 entity/ 前缀）
	var prevSlugsText string
	if len(oldPageSlugs) > 0 {
		var sb strings.Builder
		for slug := range oldPageSlugs {
			if !strings.HasPrefix(slug, "entity/") {
				continue
			}
			fmt.Fprintf(&sb, "- %s\n", slug)
		}
		prevSlugsText = sb.String()
	}
	if prevSlugsText == "" {
		prevSlugsText = "(none — this is a new document)"
	}

	raw, err := s.generateWithTemplate(ctx, chatModel,
		agent.WikiRegulationExtractPrompt, map[string]string{
			"Content":            content,
			"Language":           lang,
			"PreviousSlugs":      prevSlugsText,
			"DocTitle":           docTitle,
			"CustomInstructions": batchCtx.ExtractionInstructions,
		})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("extract regulation candidates: %w", err)
	}

	raw = cleanLLMJSON(raw)
	if raw == "" {
		return nil, nil, nil, fmt.Errorf("extract regulation candidates: empty response")
	}

	var result regulationExtractionResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, nil, nil, fmt.Errorf("parse regulation candidates: %w", err)
	}

	// 去重：业务对象 → entities 通道，条款 → concepts 通道
	// （复用现有 deduplicateExtractedBatch 机制）
	result.BusinessObjects, result.Clauses = s.deduplicateExtractedBatch(
		ctx, chatModel, kbID, result.BusinessObjects, result.Clauses, batchCtx,
	)

	// 过滤内容不足的条款和业务对象。
	// 判断标准：
	//   1. description 为空 → 过滤（连一句话概述都没有，完全没价值）
	//   2. details 字符数 < 100 → 过滤（内容太少，生成的页面大部分是"暂无"空壳）
	// description 只判断是否为空，不限制字数（有一句话概述就够）。
	clauseCountBefore := len(result.Clauses)
	boCountBefore := len(result.BusinessObjects)
	result.Clauses = filterInsufficientExtractedItems(result.Clauses)
	result.BusinessObjects = filterInsufficientExtractedItems(result.BusinessObjects)
	logger.Infof(ctx, "wiki ingest: before filter — clauses=%d, business_objects=%d",
		clauseCountBefore, boCountBefore)
	if len(result.Clauses) < clauseCountBefore || len(result.BusinessObjects) < boCountBefore {
		logger.Infof(ctx, "wiki ingest: filtered insufficient items — clauses %d→%d, business_objects %d→%d",
			clauseCountBefore, len(result.Clauses), boCountBefore, len(result.BusinessObjects))
	} else {
		logger.Infof(ctx, "wiki ingest: no items filtered — all items passed")
	}

	// 组装统一的 slugItems 映射
	slugItems = make(map[string]extractedItem,
		len(result.Clauses)+len(result.BusinessObjects))
	for _, item := range result.Clauses {
		slugItems[item.Slug] = item
	}
	for _, item := range result.BusinessObjects {
		slugItems[item.Slug] = item
	}

	return result.Clauses, result.BusinessObjects, slugItems, nil
}

// filterInsufficientExtractedItems 过滤内容不足的提取项。
//
// 过滤规则：
//   1. description 为空 → 过滤（连一句话概述都没有，完全没价值）
//   2. details 字符数 < 100 → 过滤（内容太少，生成的页面大部分是"暂无"空壳）
//
// description 只判断是否为空，不限制字数（有一句话概述就够）。
// 主要看 details 有没有足够的实质内容，details 太少的话，
// reduce 阶段用编辑模板生成的页面会大面积出现"暂无"，没有保存价值。
func filterInsufficientExtractedItems(items []extractedItem) []extractedItem {
	if len(items) == 0 {
		return items
	}
	const minDetailsLen = 200 // details 最少字符数
	filtered := make([]extractedItem, 0, len(items))
	for _, item := range items {
		// 规则1：description 为空 → 过滤
		if item.Description == "" {
			continue
		}
		// 规则2：details 字符数 < 100 → 过滤
		if len(item.Details) < minDetailsLen {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

// removeRegulationJumpLines 从摘要内容中删除所有包含"查看完整法规详情"的行。
// LLM 生成摘要时可能会自己生成跳转链接（格式不一定正确），
// 注入正确链接前先清理掉旧的，确保最终只有一个正确的跳转链接。
func removeRegulationJumpLines(content string) string {
	lines := strings.Split(content, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		if !strings.Contains(line, "查看完整法规详情") {
			filtered = append(filtered, line)
		}
	}
	result := strings.Join(filtered, "\n")
	// 清理末尾多余的空行
	return strings.TrimRight(result, "\n")
}

// clauseQualityCheckResult 单页质量检查结果
type clauseQualityCheckResult struct {
	Slug    string `json:"slug"`
	IsValid bool   `json:"is_valid"`
	Reason  string `json:"reason"`
}

// checkClausePageQuality 批量检查条款页面质量，删除无效页面（空壳/占位/内容不足）。
// 只处理 entity/clause-* 前缀的页面，其他类型页面直接跳过。
// 返回被删除的 slug 列表（用于后续空目录清理等）。
//
// 设计：
//   - 分批处理（默认每批 clauseQualityBatchSize 个），控制单次 LLM 调用的 token 量
//   - LLM 返回 JSON 数组，每个元素含 slug + is_valid + reason
//   - 解析失败的批次记录 warning 但不删除（保守策略：不删不确定的）
func (s *wikiIngestService) checkClausePageQuality(
	ctx context.Context,
	kbID string,
	affectedSlugs []string,
	chatModel chat.Chat,
	batchCtx *WikiBatchContext,
) []string {
	if len(affectedSlugs) == 0 || chatModel == nil {
		return nil
	}
	// 只处理法规场景
	if batchCtx == nil || batchCtx.SceneType != types.WikiSceneTypeRegulation {
		return nil
	}

	// 筛选出条款页 slug
	clauseSlugs := make([]string, 0, len(affectedSlugs))
	for _, slug := range affectedSlugs {
		if strings.HasPrefix(slug, "entity/clause-") {
			clauseSlugs = append(clauseSlugs, slug)
		}
	}
	if len(clauseSlugs) == 0 {
		return nil
	}

	logger.Infof(ctx, "wiki clause quality: checking %d clause pages", len(clauseSlugs))

	// 批量加载页面完整内容
	type pageContent struct {
		Slug    string
		Title   string
		Content string
	}
	var pages []pageContent
	if s.db != nil {
		type row struct {
			Slug    string
			Title   string
			Content string
		}
		var rows []row
		if err := s.db.WithContext(ctx).
			Model(&types.WikiPage{}).
			Select("slug", "title", "content").
			Where("knowledge_base_id = ? AND slug IN ? AND status = ?",
				kbID, clauseSlugs, types.WikiPageStatusPublished).
			Scan(&rows).Error; err != nil {
			logger.Warnf(ctx, "wiki clause quality: load pages failed: %v", err)
			return nil
		}
		for _, r := range rows {
			pages = append(pages, pageContent{Slug: r.Slug, Title: r.Title, Content: r.Content})
		}
	}
	if len(pages) == 0 {
		return nil
	}

	// 分批质检
	const batchSize = 20 // 每批最多 20 个页面
	var deletedSlugs []string

	for i := 0; i < len(pages); i += batchSize {
		end := i + batchSize
		if end > len(pages) {
			end = len(pages)
		}
		batch := pages[i:end]

		// 构造 <pages> 内容：每个页面用 <page> 包裹
		var pagesXML strings.Builder
		for _, p := range batch {
			// 内容截断：每个页面最多 2000 字符，避免 token 爆炸
			content := p.Content
			if len(content) > 2000 {
				content = content[:2000] + "\n...（内容过长，已截断）"
			}
			fmt.Fprintf(&pagesXML, "<page slug=\"%s\" title=\"%s\">\n%s\n</page>\n\n",
				p.Slug, p.Title, content)
		}

		// 调用 LLM
		raw, err := s.generateWithTemplate(ctx, chatModel,
			agent.WikiClauseQualityCheckPrompt, map[string]string{
				"Pages": pagesXML.String(),
			})
		if err != nil {
			logger.Warnf(ctx, "wiki clause quality: LLM call failed for batch %d-%d: %v",
				i, end, err)
			continue
		}

		// 提取首个 JSON 数组（容错：LLM 可能在 JSON 前后输出多余文字）
		jsonStr := extractFirstJSONArray(raw)
		if jsonStr == "" {
			logger.Warnf(ctx, "wiki clause quality: failed to parse JSON from LLM output (len=%d)", len(raw))
			continue
		}

		var results []clauseQualityCheckResult
		if err := json.Unmarshal([]byte(jsonStr), &results); err != nil {
			logger.Warnf(ctx, "wiki clause quality: JSON unmarshal failed: %v", err)
			continue
		}

		// 处理结果：删除无效页面
		for _, r := range results {
			if r.IsValid {
				continue
			}
			// 安全校验：只删条款页
			if !strings.HasPrefix(r.Slug, "entity/clause-") {
				continue
			}
			// 执行删除
			if s.wikiService != nil {
				if delErr := s.wikiService.DeletePage(ctx, kbID, r.Slug); delErr != nil {
					logger.Warnf(ctx, "wiki clause quality: delete page %s failed: %v", r.Slug, delErr)
					continue
				}
				deletedSlugs = append(deletedSlugs, r.Slug)
				logger.Infof(ctx, "wiki clause quality: deleted invalid clause page %s (reason: %s)",
					r.Slug, r.Reason)
			}
		}
	}

	logger.Infof(ctx, "wiki clause quality: checked %d pages, deleted %d invalid pages",
		len(pages), len(deletedSlugs))
	return deletedSlugs
}

// extractFirstJSONArray 从字符串中提取第一个完整的 JSON 数组（[ ... ]）。
// 用于 LLM 输出在 JSON 前后有多余解释文字的情况。
// 与 extractFirstJSONObject 类似，但匹配数组而非对象。
func extractFirstJSONArray(s string) string {
	s = strings.TrimSpace(s)
	first := strings.Index(s, "[")
	if first < 0 {
		return ""
	}
	depth := 0
	inString := false
	escape := false
	for i := first; i < len(s); i++ {
		ch := s[i]
		if escape {
			escape = false
			continue
		}
		if ch == '\\' && inString {
			escape = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if ch == '[' {
			depth++
			continue
		}
		if ch == ']' {
			depth--
			if depth == 0 {
				return s[first : i+1]
			}
		}
	}
	return ""
}
