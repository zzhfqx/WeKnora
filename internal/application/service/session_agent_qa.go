package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/models/rerank"
	"github.com/Tencent/WeKnora/internal/types"
)

// AgentQA 执行基于智能体的问答，支持对话历史和流式输出。
// customAgent 为可选参数——若提供则使用自定义智能体配置，而非租户默认配置。
// summaryModelID 为可选参数——若提供则覆盖自定义智能体配置中的模型设置。
func (s *sessionService) AgentQA(
	ctx context.Context,
	req *types.QARequest,
	eventBus *event.EventBus,
) error {
	sessionID := req.Session.ID
	// 将会话 ID 写入上下文，以便有状态的沙箱后端（如 CubeSandbox）
	// 能够将脚本执行绑定到每个会话对应的 MicroVM 实例上。
	ctx = types.WithSessionID(ctx, sessionID)
	sessionJSON, err := json.Marshal(req.Session)
	if err != nil {
		logger.Errorf(ctx, "Failed to marshal session, session ID: %s, error: %v", sessionID, err)
		return fmt.Errorf("failed to marshal session: %w", err)
	}

	// AgentQA 必须提供 customAgent（处理器层已对共享智能体做过权限校验）
	if req.CustomAgent == nil {
		logger.Warnf(ctx, "Custom agent not provided for session: %s", sessionID)
		return errors.New("custom agent configuration is required for agent QA")
	}

	// 使用通用辅助方法解析检索所属的租户
	agentTenantID := s.resolveRetrievalTenantID(ctx, req)
	logger.Infof(ctx, "Start agent-based question answering, session ID: %s, agent tenant ID: %d, query: %s, session: %s",
		sessionID, agentTenantID, req.Query, string(sessionJSON))

	var tenantInfo *types.Tenant
	if v := ctx.Value(types.TenantInfoContextKey); v != nil {
		tenantInfo, _ = v.(*types.Tenant)
	}
	// 当智能体属于另一个租户（共享智能体）时，使用智能体所在租户作为知识库/模型的作用域；
	// 如有需要则加载该租户的信息
	if tenantInfo == nil || tenantInfo.ID != agentTenantID {
		if s.tenantService != nil {
			if agentTenant, err := s.tenantService.GetTenantByID(ctx, agentTenantID); err == nil && agentTenant != nil {
				tenantInfo = agentTenant
				logger.Infof(ctx, "Using agent tenant info for retrieval scope, tenant ID: %d", agentTenantID)
			}
		}
	}
	if tenantInfo == nil {
		logger.Warnf(ctx, "Tenant info not available for agent tenant %d, proceeding with defaults", agentTenantID)
		tenantInfo = &types.Tenant{ID: agentTenantID}
	}

	// 确保默认值已填充
	req.CustomAgent.EnsureDefaults()

	// 根据自定义智能体和租户信息构建 AgentConfig
	agentConfig, err := s.buildAgentConfig(ctx, req, tenantInfo, agentTenantID)
	if err != nil {
		return err
	}

	// 设置用于工具结果图片分析的 VLM 模型 ID（仅运行时使用的字段）
	if req.CustomAgent != nil && req.CustomAgent.Config.VLMModelID != "" {
		agentConfig.VLMModelID = req.CustomAgent.Config.VLMModelID
	}

	// 使用通用辅助方法解析模型 ID（AgentQA 必须有模型，找不到则报错）
	effectiveModelID, err := s.resolveChatModelID(ctx, req, agentConfig.KnowledgeBases, agentConfig.KnowledgeIDs)
	if err != nil {
		return err
	}
	if effectiveModelID == "" {
		logger.Warnf(ctx, "No summary model configured for custom agent %s", req.CustomAgent.ID)
		return errors.New("summary model (model_id) is not configured in custom agent settings")
	}

	summaryModel, err := s.modelService.GetChatModel(ctx, effectiveModelID)
	if err != nil {
		logger.Warnf(ctx, "Failed to get chat model: %v", err)
		return fmt.Errorf("failed to get chat model: %w", err)
	}

	// 模型自身的元数据决定了两件智能体无法自行推断的事情：
	// 压缩前能容纳多少历史对话，以及是否支持透传图片。
	// 在构建引擎前先解析一次——引擎在构造时会根据 MaxContextTokens
	// 来设置其内存合并器的容量。
	var agentModelSupportsVision bool
	modelContextWindow := 0
	if effectiveModelID != "" {
		if modelInfo, err := s.modelService.GetModelByID(ctx, effectiveModelID); err == nil && modelInfo != nil {
			agentModelSupportsVision = modelInfo.Parameters.SupportsVision
			modelContextWindow = modelInfo.Parameters.ContextWindow
		}
	}
	agentConfig.MaxContextTokens = types.AgentMaxContextTokens(
		agentConfig.MaxContextTokens, modelContextWindow,
	)
	logger.Infof(ctx, "Agent context window: %d tokens (model %s declares %d)",
		agentConfig.MaxContextTokens, effectiveModelID, modelContextWindow)

	// 仅当 knowledge_search 实际可用时才从自定义智能体配置中获取重排序模型。
	// 如果知识库作用域被禁用，所有知识库工具都不会生效，
	// 因此不应强制用户去配置一个根本用不到的重排序模型。
	var rerankModel rerank.Reranker
	if agentRequiresRerankModel(req.CustomAgent) {
		// 重排序模型现在仅从智能体配置中解析。
		// 之前我们会回退到租户级别的 ConversationConfig.RerankModelID，
		// 但这种做法会导致"智能体上留空，静默继承租户配置"的情况，
		// 使得排查检索质量问题时需要在租户配置和智能体配置之间反复猜测。
		// 强制智能体声明自己的重排序模型，可以让配置集中在用户实际编辑智能体的地方。
		// 如果一个纯 Wiki 智能体不需要重排序，
		// agentRequiresRerankModel() 已经会直接放行。
		rerankModelID := req.CustomAgent.Config.RerankModelID
		if rerankModelID == "" {
			logger.Warnf(ctx, "No rerank model configured for custom agent %s, but knowledge_search tool is enabled", req.CustomAgent.ID)
			return errors.New("rerank model is not configured: please set rerank_model_id on the agent")
		}

		rerankModel, err = s.modelService.GetRerankModel(ctx, rerankModelID)
		if err != nil {
			logger.Warnf(ctx, "Failed to get rerank model: %v", err)
			return fmt.Errorf("failed to get rerank model: %w", err)
		}
	} else {
		logger.Infof(ctx, "knowledge_search is unavailable for the effective agent scope, skipping rerank model initialization")
	}

	// 直接从数据库加载多轮对话历史（数据库是唯一可信来源）。
	// 每条历史助手消息上的 AgentSteps 会被展开为标准的
	// assistant_with_tool_calls + tool 消息，让模型能看到上一轮尝试过什么——
	// 但 final_answer 除外，它会作为末尾的正式助手消息重放。
	var llmContext []chat.Message
	if agentConfig.MultiTurnEnabled {
		historyTurns := agentConfig.HistoryTurns
		if historyTurns <= 0 {
			historyTurns = 5
		}
		llmContext, err = LoadAgentHistory(ctx, s.messageRepo, sessionID, historyTurns)
		if err != nil {
			logger.Warnf(ctx, "Failed to load agent history from DB: %v, continuing without history", err)
			llmContext = []chat.Message{}
		}
		logger.Infof(ctx, "Loaded %d history messages from DB (turns=%d)", len(llmContext), historyTurns)
	} else {
		logger.Infof(ctx, "Multi-turn disabled for this agent, running without history")
		llmContext = []chat.Message{}
	}

	// 在本轮对话期间持有沙箱，避免在工具调用之间因为安装完成而重建 VM。
	// 下方的暂存操作是第一次解析：如果上一轮留下了过期标记，
	// 就在此处加载新的镜像。
	releaseTurn := s.holdSandboxTurn(ctx, sessionID, agentConfig.SandboxConfigID)
	defer releaseTurn()

	// 在模型请求执行 shell 或技能之前，将所有持久化的会话附件同步到
	// 会话的远程沙箱中。持久化存储的 URL（而非临时的沙箱路径）
	// 仍然是数据的可信来源。仅在沙箱管理器声明支持会话文件系统能力时才执行，
	// 以便与具体提供商无关的远程装配逻辑保留在此处。
	var stagedAttachments []stagedSessionAttachment
	stager, ok := s.agentService.(sessionAttachmentStager)
	if !ok {
		return errors.New("agent service does not support session attachment staging")
	}
	// 探测当前会话沙箱实际运行的后端。
	// 如果基于进程全局的管理器来判断，可能检查到的后端并非
	// 该智能体所选工作区配置对应的那个后端。
	inputStore, storeErr := stager.sessionSandboxInputStore(ctx, sessionID, agentConfig.SandboxConfigID)
	if storeErr != nil {
		return fmt.Errorf("resolve sandbox file store for session %s: %w", sessionID, storeErr)
	}
	if inputStore != nil {
		sessionAttachments, loadErr := s.messageRepo.GetSessionAttachments(ctx, sessionID)
		if loadErr != nil {
			return fmt.Errorf("load session attachments for sandbox staging: %w", loadErr)
		}
		stagedAttachments, err = stager.stageSessionAttachments(ctx, sessionID, agentConfig.SandboxConfigID, req.Session.TenantID, sessionAttachments)
		if err != nil {
			return fmt.Errorf("restore session attachments into sandbox: %w", err)
		}
	}

	// 创建带 EventBus 的智能体引擎
	logger.Info(ctx, "Creating agent engine")
	engine, err := s.agentService.CreateAgentEngine(
		ctx,
		agentConfig,
		summaryModel,
		rerankModel,
		eventBus,
		sessionID,
		req.AssistantMessageID,
	)
	if err != nil {
		logger.Errorf(ctx, "Failed to create agent engine: %v", err)
		return err
	}

	// 为本轮对话召回长期记忆。与 RAG 路径类似，这是一次不经过模型的读取，
	// 智能体也可以完全选择不启用该功能。
	memoryCtx := types.ApplyAgentMemoryPreference(ctx, agentConfig.MemoryEnabled)
	if s.memoryService != nil {
		recall := s.memoryService.Recall(memoryCtx, req.Query)
		if recall.Prompt != "" {
			engine.SetMemoryPrompt(recall.Prompt)
			used := types.UsedMemoriesFromItems(recall.Items)
			if err := eventBus.Emit(ctx, event.Event{
				Type:      event.EventMemoryRecalled,
				SessionID: sessionID,
				Data:      event.MemoryRecalledData{Memories: used},
			}); err != nil {
				logger.Warnf(ctx, "Failed to emit memory recalled event: %v", err)
			}
			logger.Infof(ctx, "Injected %d long-term memories into agent context", len(used))
		}
	}

	agentQuery := req.Query
	var agentImageURLs []string
	if agentModelSupportsVision && len(req.ImageURLs) > 0 {
		agentImageURLs = req.ImageURLs
		logger.Infof(ctx, "Agent model supports vision, passing %d image(s) directly", len(agentImageURLs))
	} else if req.ImageDescription != "" {
		agentQuery = req.Query + "\n\n[用户上传图片内容]\n" + req.ImageDescription
		logger.Infof(ctx, "Agent model does not support vision, appending image description (%d chars)", len(req.ImageDescription))
	}
	if req.QuotedContext != "" {
		agentQuery += "\n\n" + req.QuotedContext
	}
	// 注入附件内容（文档、音频转写文本等），让智能体能看到上传的文件。
	// 此逻辑与 KnowledgeQA 流水线保持一致（参见 chat_pipeline/into_chat_message.go）。
	if len(req.Attachments) > 0 {
		agentQuery += req.Attachments.BuildPrompt()
		logger.Infof(ctx, "Appended %d attachment(s) to agent query", len(req.Attachments))
	}
	if manifest := buildSandboxAttachmentsPrompt(stagedAttachments); manifest != "" {
		agentQuery += manifest
		logger.Infof(ctx, "Appended %d staged sandbox attachment path(s) to agent query", len(stagedAttachments))
	}

	// 作用域封装（runtime_context / must_use）仅在智能体引擎内部的
	// 每次 LLM 调用时注入；我们有意不将它们持久化到用户消息中，
	// 这样多轮历史才能保持干净，不会被过期的 @提及作用域干扰。

	// 以流式方式异步执行智能体
	// 事件将被发送到 EventBus，并由 Handler 层处理
	logger.Info(ctx, "Executing agent with streaming")
	if _, err := engine.Execute(ctx, sessionID, req.AssistantMessageID, agentQuery, llmContext, agentImageURLs); err != nil {
		logger.Errorf(ctx, "Agent execution failed: %v", err)
		// 向该智能体使用的 EventBus 发送错误事件
		eventBus.Emit(ctx, event.Event{
			Type:      event.EventError,
			SessionID: sessionID,
			Data: event.ErrorData{
				Error:     err.Error(),
				Stage:     "agent_execution",
				SessionID: sessionID,
			},
		})
	}
	// 返回空值——事件将由 Handler 通过 EventBus 订阅来处理
	return nil
}

// buildAgentConfig 根据 QARequest 中的自定义智能体配置、租户信息，
// 以及已解析的知识库/搜索目标，构建运行时的 AgentConfig。
func (s *sessionService) buildAgentConfig(
	ctx context.Context,
	req *types.QARequest,
	tenantInfo *types.Tenant,
	agentTenantID uint64,
) (*types.AgentConfig, error) {
	customAgent := req.CustomAgent
	agentConfig := &types.AgentConfig{
		MaxIterations:               customAgent.Config.MaxIterations,
		Temperature:                 customAgent.Config.Temperature,
		WebSearchEnabled:            customAgent.Config.WebSearchEnabled && req.WebSearchEnabled,
		WebSearchMaxResults:         customAgent.Config.WebSearchMaxResults,
		WebSearchProviderID:         customAgent.Config.WebSearchProviderID,
		MultiTurnEnabled:            customAgent.Config.MultiTurnEnabled,
		HistoryTurns:                customAgent.Config.HistoryTurns,
		MemoryEnabled:               customAgent.Config.MemoryEnabled,
		MCPSelectionMode:            customAgent.Config.MCPSelectionMode,
		MCPServices:                 customAgent.Config.MCPServices,
		MCPAuthWaitTimeout:          customAgent.Config.MCPAuthWaitTimeout,
		Thinking:                    customAgent.Config.Thinking,
		CitationEnabled:             customAgent.Config.CitationEnabled,
		RetrieveKBOnlyWhenMentioned: customAgent.Config.RetrieveKBOnlyWhenMentioned,
		LLMCallTimeout:              customAgent.Config.LLMCallTimeout,
		MaxCompletionTokens:         customAgent.Config.MaxCompletionTokens,
		RetainRetrievalHistory:      customAgent.Config.RetainRetrievalHistory,
		SharedAgentReadOnly:         req.SharedAgentReadOnly,
	}

	// Falls back to global configuration if no specific timeout is set for the agent.
	if agentConfig.LLMCallTimeout == 0 && s.cfg.Agent != nil && s.cfg.Agent.LLMCallTimeout > 0 {
		agentConfig.LLMCallTimeout = s.cfg.Agent.LLMCallTimeout
	}

	// Configure skills based on CustomAgentConfig
	s.configureSkillsFromAgent(ctx, agentConfig, customAgent)

	// Then add the skills installed into the sandbox config this run boots.
	//
	// The workspace is the one on the context rather than the agent's owner,
	// because that is where resolveSandboxForExecution reads it; skillsForRun
	// picks the config the same way the sandbox resolution does.
	sandboxTenantID, _ := types.TenantIDFromContext(ctx)
	skillConfigID, tenantSkills := skillsForRun(
		ctx, s.sandboxPinner, s.sandboxConfigRepo, s.tenantSkillRepo,
		sandboxTenantID, req.Session.ID, agentConfig.SandboxConfigID,
	)
	agentConfig.TenantSkills = tenantSkills
	if len(tenantSkills) > 0 {
		// The config named here is the one the skills came from, which is the
		// pinned one whenever it differs from the agent's - the only case the
		// line is worth reading.
		logger.Infof(ctx, "Sandbox config %s offers %d installed skill(s) to this run",
			skillConfigID, len(tenantSkills))
	}

	// Resolve knowledge bases using shared helper
	kbIDs, knowledgeIDs, err := s.resolveKnowledgeBases(ctx, req)
	if err != nil {
		return nil, err
	}
	agentConfig.KnowledgeBases = kbIDs
	agentConfig.KnowledgeIDs = knowledgeIDs

	// Use custom agent's allowed tools if specified, otherwise use defaults
	if len(customAgent.Config.AllowedTools) > 0 {
		agentConfig.AllowedTools = customAgent.Config.AllowedTools
	} else {
		agentConfig.AllowedTools = tools.DefaultAllowedTools()
	}
	// Apply per-turn @Skill / @MCP scope. Each helper narrows the agent's
	// whitelist to the mentioned items and records the pinned set used for the
	// <must_use> hint, keeping all scope logic in one place per resource type.
	isSharedAgent := req.SharedAgentReadOnly
	applyPerRequestSkillScope(ctx, agentConfig, customAgent.Config.SkillsSelectionMode, req.SkillNames)
	applyPerRequestMCPScope(ctx, agentConfig, customAgent.Config.MCPServices, isSharedAgent, req.MCPServiceIDs)

	// Use custom agent's system prompt if specified
	if customAgent.Config.SystemPrompt != "" {
		agentConfig.UseCustomSystemPrompt = true
		agentConfig.SystemPrompt = customAgent.Config.SystemPrompt
	}

	logger.Infof(ctx, "Custom agent config applied: MaxIterations=%d, Temperature=%.2f, AllowedTools=%v, WebSearchEnabled=%v",
		agentConfig.MaxIterations, agentConfig.Temperature, agentConfig.AllowedTools, agentConfig.WebSearchEnabled)

	// Set web search max results from tenant config if not set (default: 5)
	if agentConfig.WebSearchMaxResults == 0 {
		agentConfig.WebSearchMaxResults = 5
		if tenantInfo.WebSearchConfig != nil && tenantInfo.WebSearchConfig.MaxResults > 0 {
			agentConfig.WebSearchMaxResults = tenantInfo.WebSearchConfig.MaxResults
		}
	}

	// Resolve web search provider ID: agent-level > tenant default (is_default=true)
	if agentConfig.WebSearchProviderID == "" {
		if defaultProvider, err := s.webSearchProviderRepo.GetDefault(ctx, tenantInfo.ID); err == nil && defaultProvider != nil {
			agentConfig.WebSearchProviderID = defaultProvider.ID
		}
	}

	logger.Infof(ctx, "Merged agent config from tenant %d and session %s", tenantInfo.ID, req.Session.ID)

	// Log knowledge bases if present
	if len(agentConfig.KnowledgeBases) > 0 || len(req.TagScopes) > 0 {
		if len(agentConfig.KnowledgeBases) > 0 {
			logger.Infof(ctx, "Agent configured with %d knowledge base(s): %v",
				len(agentConfig.KnowledgeBases), agentConfig.KnowledgeBases)
		} else {
			logger.Infof(ctx, "Agent configured with %d tag-scoped search target(s)", len(req.TagScopes))
		}
	} else {
		logger.Infof(ctx, "No knowledge bases specified for agent, running in pure agent mode")
	}

	// Build search targets using agent's tenant (handler has validated access for shared agent)
	searchTargets, err := s.buildSearchTargets(ctx, agentTenantID, agentConfig.KnowledgeBases, agentConfig.KnowledgeIDs, req.TagScopes)
	if err != nil {
		return nil, fmt.Errorf("build search targets: %w", err)
	}
	agentConfig.SearchTargets = searchTargets
	// Document tags are stored in knowledge_tag_relations, so document-KB tag
	// scopes are resolved to concrete knowledge IDs before retrieval. Preserve
	// those resolved IDs as this turn's pinned documents as well: otherwise the
	// Agent tools are correctly constrained behind the scenes, but the model only
	// sees a bound KB and does not know which documents the user explicitly chose.
	if len(req.TagScopes) > 0 {
		agentConfig.KnowledgeIDs = mergeResolvedTagKnowledgeIDs(
			agentConfig.KnowledgeIDs,
			searchTargets,
			req.TagScopes,
		)
	}
	logger.Infof(ctx, "Agent search targets built: %d targets", len(searchTargets))

	// MaxContextTokens is deliberately left unset here. The caller fills it
	// from the resolved model's declared window, which is not known yet.

	return agentConfig, nil
}

func mergeResolvedTagKnowledgeIDs(
	existing []string,
	searchTargets types.SearchTargets,
	tagScopes []types.TagScope,
) []string {
	tagKBs := make(map[string]bool, len(tagScopes))
	for _, scope := range tagScopes {
		if scope.KnowledgeBaseID != "" && len(scope.TagIDs) > 0 {
			tagKBs[scope.KnowledgeBaseID] = true
		}
	}
	if len(tagKBs) == 0 {
		return uniqueNonEmptyStrings(existing)
	}

	merged := append([]string(nil), existing...)
	for _, target := range searchTargets {
		if target == nil || !tagKBs[target.KnowledgeBaseID] || target.Type != types.SearchTargetTypeKnowledge {
			continue
		}
		merged = append(merged, target.KnowledgeIDs...)
	}
	return uniqueNonEmptyStrings(merged)
}

// applyPerRequestSkillScope records the @Skill mentions for this turn as the
// pinned set that drives the <must_use> hint. It deliberately does NOT narrow
// the allow-gate: an agent whose prompt requires a skill the user did not
// @mention must still be able to read and execute it. Mentioning a skill only
// prioritizes it, it never revokes access to the agent's configured set.
//
// It is a no-op when no skills were mentioned or skills are disabled.
func applyPerRequestSkillScope(
	ctx context.Context,
	agentConfig *types.AgentConfig,
	skillsMode string,
	requested []string,
) {
	if len(requested) == 0 {
		return
	}
	if skillsMode == "none" || skillsMode == "" {
		logger.Warnf(ctx, "Ignoring @skill mention: agent skills selection is disabled (mode=%s)", skillsMode)
		return
	}
	if !agentConfig.SkillsEnabled {
		return
	}
	// PinnedSkillNames carries only mentioned skills that are currently
	// allowed, so the <must_use> hint never directs the model at a skill it
	// cannot load. An empty AllowedSkills means all skills are allowed,
	// matching Manager.isSkillAllowed, so every mention is pinned in that case.
	agentConfig.PinnedSkillNames = pinPreservingRequestOrder(requested, agentConfig.AllowedSkills)
	logger.Infof(ctx, "Applied per-request @skill scope: requested=%v effective=%v pinned=%v",
		requested, agentConfig.AllowedSkills, agentConfig.PinnedSkillNames)
}

// applyPerRequestMCPScope pins authorized @MCP mentions for the <must_use> hint,
// preserving access to the rest of the agent's configured services. It is a
// no-op when no services were mentioned or MCP selection is disabled.
func applyPerRequestMCPScope(
	ctx context.Context,
	agentConfig *types.AgentConfig,
	agentPresetMCPs []string,
	isSharedAgent bool,
	requested []string,
) {
	if len(requested) == 0 {
		return
	}
	if agentConfig.MCPSelectionMode == "none" {
		logger.Warnf(ctx, "Ignoring @MCP mention: agent MCP selection is disabled (mode=none)")
		return
	}
	mentioned := dedupPreservingOrder(requested)
	effective, _ := resolvePerRequestMCPScope(mentioned, agentPresetMCPs, agentConfig.MCPSelectionMode, isSharedAgent)
	if len(effective) == 0 {
		logger.Warnf(ctx, "Ignoring @MCP scope outside agent preset: requested=%v agent=%v shared=%v",
			requested, agentPresetMCPs, isSharedAgent)
		return
	}
	agentConfig.PinnedMCPServiceIDs = effective
	logger.Infof(ctx, "Applied per-request @MCP priority: requested=%v mode=%s pinned=%v",
		requested, agentConfig.MCPSelectionMode, effective)
}

// resolvePerRequestMCPScope selects authorized mentions for per-turn priority.
// It does not modify the registration scope. Shared agents may only pin services
// in the agent preset, and selectionMode "none" rejects all mentions.
func resolvePerRequestMCPScope(
	mentioned, agentMCPs []string,
	selectionMode string,
	isSharedAgent bool,
) (effective []string, mode string) {
	if len(mentioned) == 0 {
		return nil, selectionMode
	}
	if isSharedAgent {
		mentioned = intersectPreservingRequestOrder(mentioned, agentMCPs)
		if len(mentioned) == 0 {
			return nil, selectionMode
		}
	}
	switch selectionMode {
	case "none":
		return nil, selectionMode
	case "selected":
		effective = intersectPreservingRequestOrder(mentioned, agentMCPs)
	case "all", "":
		effective = mentioned
	default:
		effective = mentioned
	}
	if len(effective) == 0 {
		return nil, selectionMode
	}
	return effective, "selected"
}

func intersectPreservingRequestOrder(requested []string, allowed []string) []string {
	allowedSet := make(map[string]bool, len(allowed))
	for _, value := range allowed {
		if value != "" {
			allowedSet[value] = true
		}
	}
	result := make([]string, 0, len(requested))
	seen := make(map[string]bool, len(requested))
	for _, value := range requested {
		if value == "" || seen[value] || !allowedSet[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

// pinPreservingRequestOrder returns the requested skills that are allowed,
// preserving request order. Unlike intersectPreservingRequestOrder, an empty
// allowed list is treated as "all skills allowed" (matching
// Manager.isSkillAllowed), so every requested skill is pinned.
func pinPreservingRequestOrder(requested []string, allowed []string) []string {
	allowedAll := len(allowed) == 0
	allowedSet := make(map[string]bool, len(allowed))
	for _, value := range allowed {
		if value != "" {
			allowedSet[value] = true
		}
	}
	result := make([]string, 0, len(requested))
	seen := make(map[string]bool, len(requested))
	for _, value := range requested {
		if value == "" || seen[value] {
			continue
		}
		if !allowedAll && !allowedSet[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func dedupPreservingOrder(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

// configureSkillsFromAgent turns the agent's skill picker into runtime flags.
// The skills themselves come from the sandbox image (TenantSkills), not from
// a host skill directory — that copy is not what shell_exec would find
// inside the sandbox.
func (s *sessionService) configureSkillsFromAgent(
	ctx context.Context,
	agentConfig *types.AgentConfig,
	customAgent *types.CustomAgent,
) {
	if customAgent == nil {
		return
	}
	agentConfig.SandboxConfigID = customAgent.Config.SandboxConfigID
	switch customAgent.Config.SkillsSelectionMode {
	case "all":
		agentConfig.SkillsEnabled = true
		agentConfig.AllowedSkills = nil
		logger.Infof(ctx, "SkillsSelectionMode=all: using installed sandbox skills")
	case "selected":
		if len(customAgent.Config.SelectedSkills) > 0 {
			agentConfig.SkillsEnabled = true
			agentConfig.AllowedSkills = customAgent.Config.SelectedSkills
			logger.Infof(ctx, "SkillsSelectionMode=selected: enabled %d selected skills: %v",
				len(customAgent.Config.SelectedSkills), customAgent.Config.SelectedSkills)
		} else {
			agentConfig.SkillsEnabled = false
			logger.Infof(ctx, "SkillsSelectionMode=selected but no skills selected: skills disabled")
		}
	case "none", "":
		agentConfig.SkillsEnabled = false
		logger.Infof(ctx, "SkillsSelectionMode=%s: skills disabled", customAgent.Config.SkillsSelectionMode)
	default:
		agentConfig.SkillsEnabled = false
		logger.Warnf(ctx, "Unknown SkillsSelectionMode=%s: skills disabled", customAgent.Config.SkillsSelectionMode)
	}
}
