// 这是一个最简单的 LLM 调用测试脚本
// 目的：帮你理解 WeKnora 是怎么调用 LLM 的，和你们 Python 版的 llm_client_0512.py 做对比
//
// 运行方式（在 WeKnora 根目录执行）：
//   go run cmd/llm_demo/main.go
//
// 方式一（推荐）：用环境变量，密钥不写在代码里
//   export LLM_BASE_URL="https://ark.cn-beijing.volces.com/api/v3"
//   export LLM_API_KEY="你的key"
//   export LLM_MODEL="Doubao-Seed-2.0-lite"
//
// 方式二（快速测试）：直接改下面代码里的默认值，跟 Python 一样写死

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

func main() {
	fmt.Println("========== WeKnora LLM 调用最简单示例 ==========")
	fmt.Println()

	// ============================================================
	// 第 1 步：配置模型参数（像 Python env_config 一样写死就行）
	// 对应 Python 版：env_config 里的 llm_base_url / llm_api_key / llm_model_name
	// ============================================================
	// 方式 A：直接写死在代码里，和 Python 一样
	baseURL := "https://api.siliconflow.cn/v1/"
	apiKey := "sk-bhylajoiyteiwjdjlldwcpepjpunqiydemjjgxsjmebsjdol"
	modelName := "deepseek-ai/DeepSeek-V4-Flash"

	// 方式 B：如果设置了环境变量就用环境变量的，否则用上面的默认值
	if envURL := os.Getenv("LLM_BASE_URL"); envURL != "" {
		baseURL = envURL
	}
	if envKey := os.Getenv("LLM_API_KEY"); envKey != "" {
		apiKey = envKey
	}
	if envModel := os.Getenv("LLM_MODEL"); envModel != "" {
		modelName = envModel
	}

	if apiKey == "" {
		fmt.Println("❌ 请在代码第 40 行填入你的 API Key，或者设置环境变量 LLM_API_KEY")
		os.Exit(1)
	}

	// ChatConfig 就是 Python 版里 config dict 的 Go 版本
	config := &chat.ChatConfig{
		Source:    types.ModelSourceRemote, // 远程模型（对应 Ollama 本地模型）
		BaseURL:   baseURL,
		APIKey:    apiKey,
		ModelName: modelName,
		ModelID:   "demo-model",
		Provider:  "openai", // 硅基流动是标准 OpenAI 兼容，用 openai 即可
	}

	fmt.Printf("📋 配置：baseURL=%s, model=%s\n", baseURL, modelName)
	fmt.Println()

	// ============================================================
	// 第 2 步：创建聊天实例
	// 对应 Python 版：client = AsyncOpenAI(base_url=..., api_key=...)
	// ============================================================
	chatClient, err := chat.NewChat(config, nil)
	if err != nil {
		fmt.Printf("❌ 创建聊天实例失败：%v\n", err)
		os.Exit(1)
	}
	fmt.Println("✅ 聊天实例创建成功")
	fmt.Printf("   模型名称: %s\n", chatClient.GetModelName())
	fmt.Printf("   模型 ID: %s\n", chatClient.GetModelID())
	fmt.Println()

	// ============================================================
	// 第 3 步：构造消息（最简单的一问一答）
	// 对应 Python 版：messages = [{"role": "user", "content": "你好"}]
	// ============================================================
	messages := []chat.Message{
		{
			Role:    "system", // 系统提示词，对应 Python 的 system role
			Content: "你是一个 helpful 的 AI 助手，用中文回答问题。",
		},
		{
			Role:    "user", // 用户消息
			Content: "你好，请用一句话介绍你自己。",
		},
	}

	fmt.Println("💬 发送消息：")
	for _, m := range messages {
		fmt.Printf("   [%s] %s\n", m.Role, m.Content)
	}
	fmt.Println()

	// ============================================================
	// 第 4 步：调用 LLM（非流式）
	// 对应 Python 版：response = await client.chat.completions.create(...)
	// ============================================================
	fmt.Println("⏳ 正在调用 LLM...")

	// ChatOptions 对应 Python 版的 temperature / max_tokens 等参数
	opts := &chat.ChatOptions{
		Temperature: 0.7, // 温度，和 Python 一样
		MaxTokens:   200, // 最大输出 token
		// Thinking:    &thinking, // 是否启用思考模式，和 Python 的 think 参数类似
	}

	ctx := context.Background() // Go 里的上下文，类似 Python 的超时控制

	// ============================================================
	// 第 4 步：调用 LLM（支持流式/非流式切换）
	// 设置 LLM_STREAM=true 环境变量启用流式，默认非流式
	// ============================================================
	// useStream := os.Getenv("LLM_STREAM") == "true"
	useStream := true // 默认流式，方便调试

	if useStream {
		fmt.Println("⏳ 正在调用 LLM（流式）...")
		stream, err := chatClient.ChatStream(ctx, messages, opts)
		if err != nil {
			fmt.Printf("❌ 调用失败：%v\n", err)
			os.Exit(1)
		}

		// ============================================================
		// 第 5 步：流式输出结果
		// ============================================================
		fmt.Println()
		fmt.Print("🤖 流式回复：")
		var fullContent string // 累加所有 chunk 的完整内容

		for chunk := range stream {
			fmt.Print(chunk.Content)
			fullContent += chunk.Content // 同时累加到 fullContent
			// Done=true 表示流式结束，FinishReason=incomplete 表示异常中断
			if chunk.Done && chunk.FinishReason == "incomplete" {
				fmt.Printf("\n⚠️  流式异常中断: %s\n", chunk.FinishReason)
			}
		}

		// 流式结束后输出完整内容
		fmt.Println()
		fmt.Println()
		fmt.Println("📄 完整内容：")
		fmt.Println("   ", fullContent)
		fmt.Println("==================end===================")

	} else {
		fmt.Println("⏳ 正在调用 LLM（非流式）...")
		resp, err := chatClient.Chat(ctx, messages, opts)
		if err != nil {
			fmt.Printf("❌ 调用失败：%v\n", err)
			os.Exit(1)
		}

		// ============================================================
		// 第 5 步：非流式输出结果
		// ============================================================
		fmt.Println()
		fmt.Println("🤖 回复：")
		fmt.Println("   Content:", resp.Content)
		fmt.Println("   ReasoningContent:", resp.ReasoningContent)
		fmt.Println("   FinishReason:", resp.FinishReason)

		// 调试：把整个响应结构体打印出来，看看内容到底在哪个字段里
		fmt.Println()
		fmt.Println("🔍 完整响应结构（调试用）：")
		fmt.Printf("   %+v\n", resp)
		fmt.Println()

		// Token 用量统计，对应 Python 的 usage
		// 注意：Go 里结构体是值类型，不是指针，用 TotalTokens > 0 判断是否有数据
		if resp.Usage.TotalTokens > 0 {
			fmt.Println("📊 Token 用量：")
			fmt.Printf("   输入: %d tokens\n", resp.Usage.PromptTokens)
			fmt.Printf("   输出: %d tokens\n", resp.Usage.CompletionTokens)
			fmt.Printf("   总计: %d tokens\n", resp.Usage.TotalTokens)
		}
	}

	fmt.Println()
	fmt.Println("========== 测试完成 ==========")
}

/*
============================================================
Python vs Go 对照速查表
============================================================

Python (llm_client_0512.py)        Go (WeKnora)
----------------------------------  -----------------------
env_config.llm_base_url             ChatConfig.BaseURL
env_config.llm_api_key              ChatConfig.APIKey
env_config.llm_model_name           ChatConfig.ModelName
AsyncOpenAI(base_url=..., api_key=) NewChat(config, nil)
messages = [{"role":..., "content":}]  []chat.Message{{Role:..., Content:...}}
client.chat.completions.create(...)    chatClient.Chat(ctx, messages, opts)
temperature                          opts.Temperature
max_tokens                           opts.MaxTokens
stream=True                          ChatStream() 方法
acall_base()                         Chat() 方法
usage.prompt_tokens                  resp.Usage.PromptTokens
async def + await                    普通函数 + context.Context

============================================================
核心概念对应：
============================================================

1. Chat 接口（interface）
   └─ 相当于 Python 里定义的抽象基类，规定了所有 LLM 客户端都必须实现的方法
   └─ 方法：Chat() 非流式、ChatStream() 流式、GetModelName() 等

2. NewChat() 工厂函数
   └─ 相当于 Python 里根据配置选择不同客户端（OpenAI / Ollama / Anthropic）
   └─ 根据 Source 字段决定用 Ollama 还是远程 API

3. ChatConfig
   └─ 就是配置结构体，对应 Python 里的 config 字典
   └─ 所有字段都有明确类型，不会有拼写错误（Python 里字典 key 拼错了运行时才发现）

4. context.Context
   └─ Go 里用来传超时、取消信号、trace 信息的标准方式
   └─ 类似 Python 里的 asyncio.Task / timeout 机制
*/
