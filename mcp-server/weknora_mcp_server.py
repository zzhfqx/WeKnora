#!/usr/bin/env python3
"""
WeKnora MCP 服务器

一个基于模型上下文协议（Model Context Protocol）的服务器，
提供对 WeKnora 知识管理 API 的访问能力。
"""

import argparse
import asyncio
import functools
import json
import logging
import os
import re
import secrets
import sys
import threading
from typing import Any, Dict

import urllib3
import requests
from mcp.server import MCPServer
from requests.exceptions import RequestException
from upload_paths import resolve_upload_file_path, set_active_transport

# 配置 MCP 服务器的日志记录
logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

# 配置项 - 从环境变量加载，未设置时使用默认值
WEKNORA_BASE_URL = os.getenv("WEKNORA_BASE_URL", "http://localhost:8080/api/v1")
WEKNORA_API_KEY = os.getenv("WEKNORA_API_KEY", "")
# 聊天 SSE 读取超时时间（秒）。LLM 响应可能较慢，默认 300 秒。
try:
    WEKNORA_CHAT_TIMEOUT = int(os.getenv("WEKNORA_CHAT_TIMEOUT", "300"))
except ValueError:
    logger.warning("WEKNORA_CHAT_TIMEOUT 不是有效的整数，回退到默认值 300 秒。")
    WEKNORA_CHAT_TIMEOUT = 300

# 网络传输层默认配置，用于向后兼容 2.x 之前的部署版本。
SSE_MESSAGE_PATH = "/sse/messages/"
STREAMABLE_HTTP_STATELESS = True


def network_transport_auth_token() -> str:
    """SSE/HTTP 传输方式下客户端必须提供的共享密钥。"""
    return os.getenv("MCP_SERVER_AUTH_TOKEN", "").strip()


def require_network_transport_auth(transport: str) -> str:
    """SSE/HTTP 传输方式必须配置认证令牌，否则不启动。"""
    token = network_transport_auth_token()
    if transport in ("sse", "http") and not token:
        logger.error(
            "%s 传输方式要求配置 MCP_SERVER_AUTH_TOKEN。"
            "请设置一个高强度的共享密钥；客户端需通过 "
            "Authorization: Bearer <token> 或 X-MCP-Auth-Token 发送。",
            transport,
        )
        sys.exit(1)
    return token


class MCPAuthMiddleware:
    """ASGI 中间件：通过共享密钥对网络 MCP 传输进行鉴权。"""

    def __init__(self, app, token: str):
        self.app = app
        self.token = token

    async def __call__(self, scope, receive, send):
        if scope.get("type") != "http":
            await self.app(scope, receive, send)
            return

        headers = {
            k.decode("latin-1").lower(): v.decode("latin-1")
            for k, v in scope.get("headers", [])
        }
        provided = ""
        auth = headers.get("authorization", "")
        if auth.lower().startswith("bearer "):
            provided = auth[7:].strip()
        elif "x-mcp-auth-token" in headers:
            provided = headers["x-mcp-auth-token"]

        if not provided or not secrets.compare_digest(provided, self.token):
            body = b'{"error":"unauthorized"}'
            await send(
                {
                    "type": "http.response.start",
                    "status": 401,
                    "headers": [[b"content-type", b"application/json"]],
                }
            )
            await send({"type": "http.response.body", "body": body})
            return

        await self.app(scope, receive, send)


def _normalize_kb_entries(resp: object) -> list[Dict]:
    """将自有知识库和共享知识库列表的 API 响应统一打平。

    GET /knowledge-bases 返回 ``data: [{id, name, ...}, ...]``
    （参见 KnowledgeBaseHandler.buildKBListResponse）。

    GET /shared-knowledge-bases 返回 ``data: [{knowledge_base: {id, name,
    ...}, share_id, ...}, ...]``（参见 organization 处理器的 sharedKBRow）。
    """
    data = resp.get("data", resp) if isinstance(resp, dict) else resp
    if isinstance(data, dict):
        data = data.get("list", data.get("items", []))
    out: list[Dict] = []
    for item in (data or []):
        if not isinstance(item, dict):
            continue
        nested = item.get("knowledge_base")
        if isinstance(nested, dict) and nested.get("id"):
            out.append(nested)
        elif item.get("id"):
            out.append(item)
    return out


class WeKnoraClient:
    """与 WeKnora API 交互的客户端"""

    def __init__(self, base_url: str, api_key: str):
        """使用基础 URL 和认证信息初始化 WeKnora API 客户端"""
        self.base_url = base_url
        self.api_key = api_key
        # SSL 证书校验：默认开启。设置 WEKNORA_VERIFY_SSL=false 可关闭
        # （例如开发环境使用自签名证书的场景——生产环境不推荐）。
        self.verify_ssl = os.getenv("WEKNORA_VERIFY_SSL", "true").lower() != "false"
        if not self.verify_ssl:
            logger.warning(
                "SSL 证书校验已禁用（WEKNORA_VERIFY_SSL=false）。"
                "这存在安全风险，不应在生产环境中使用。"
            )
            urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)
        # MCP 2.x 在工作线程中同步执行 @mcp.tool() 处理函数；
        # 由于 requests.Session 不是线程安全的，这里使用线程本地的 Session。
        self._session_local = threading.local()

    def _new_session(self) -> requests.Session:
        session = requests.Session()
        session.verify = self.verify_ssl
        session.headers.update(
            {
                "X-API-Key": self.api_key,
                "Content-Type": "application/json",
            }
        )
        return session

    @property
    def session(self) -> requests.Session:
        if not getattr(self._session_local, "session", None):
            self._session_local.session = self._new_session()
        return self._session_local.session

    def _request(self, method: str, endpoint: str, **kwargs) -> Dict[str, Any]:
        """向 WeKnora API 发起请求

        参数:
            method: HTTP 方法（GET, POST, PUT, DELETE）
            endpoint: API 端点路径
            **kwargs: 传递给 requests 的额外参数

        返回:
            字典格式的 JSON 响应
        """
        url = f"{self.base_url}{endpoint}"
        try:
            # 使用指定的 HTTP 方法执行请求
            response = self.session.request(method, url, **kwargs)
            # 对 HTTP 错误状态码（4xx, 5xx）抛出异常
            response.raise_for_status()
            # 解析并返回 JSON 响应
            return response.json()
        except RequestException as e:
            logger.error(f"API 请求失败: {e}")
            raise

    # 租户管理 - 管理多租户配置的方法
    def create_tenant(
        self, name: str, description: str, business: str, retriever_engines: Dict
    ) -> Dict:
        """使用指定配置创建新租户"""
        data = {
            "name": name,
            "description": description,
            "business": business,
            "retriever_engines": retriever_engines,  # 检索引擎配置
        }
        return self._request("POST", "/tenants", json=data)

    def get_tenant(self, tenant_id: str) -> Dict:
        """获取租户信息"""
        return self._request("GET", f"/tenants/{tenant_id}")

    def list_tenants(self) -> Dict:
        """列出所有租户"""
        return self._request("GET", "/tenants")

    # 知识库管理 - 管理知识库的方法
    def create_knowledge_base(self, name: str, description: str, config: Dict) -> Dict:
        """创建新知识库，包含分块和模型配置"""
        data = {
            "name": name,
            "description": description,
            **config,  # 合并额外配置（分块策略、模型等）
        }
        return self._request("POST", "/knowledge-bases", json=data)

    def list_knowledge_bases(self) -> Dict:
        """列出所有知识库"""
        return self._request("GET", "/knowledge-bases")

    def list_shared_knowledge_bases(self) -> Dict:
        """列出从其他工作空间共享来的知识库"""
        return self._request("GET", "/shared-knowledge-bases")

    def get_knowledge_base(self, kb_id: str) -> Dict:
        """获取知识库详情"""
        return self._request("GET", f"/knowledge-bases/{kb_id}")

    def update_knowledge_base(self, kb_id: str, updates: Dict) -> Dict:
        """更新知识库"""
        return self._request("PUT", f"/knowledge-bases/{kb_id}", json=updates)

    def delete_knowledge_base(self, kb_id: str) -> Dict:
        """删除知识库"""
        return self._request("DELETE", f"/knowledge-bases/{kb_id}")

    # ── UUID 正则（8-4-4-4-12 位十六进制） ──────────────────────────────────
    _UUID_RE = re.compile(
        r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$",
        re.IGNORECASE,
    )

    def resolve_agent_id(self, agent_id_or_name: str) -> str:
        """将智能体的 ID 或名称解析为其规范 ID。

        如果 *agent_id_or_name* 本身就是 UUID，则原样返回。
        否则列出所有智能体，返回第一个 ``id`` 精确匹配
        或 ``name`` 大小写不敏感匹配的智能体。
        未找到匹配项时抛出 ValueError。
        """
        if self._UUID_RE.match(agent_id_or_name):
            return agent_id_or_name
        resp = self._request("GET", "/agents")
        agents = resp.get("data", resp) if isinstance(resp, dict) else resp
        if isinstance(agents, dict):
            agents = agents.get("list", agents.get("items", []))
        needle = agent_id_or_name.lower()
        for agent in (agents or []):
            if not isinstance(agent, dict):
                continue
            if agent.get("id") == agent_id_or_name:
                return agent["id"]
            if agent.get("name", "").lower() == needle:
                return agent["id"]
        raise ValueError(
            f"未找到智能体 {agent_id_or_name!r}。"
            "请使用 list_agents 查看可用的智能体 ID 和名称。"
        )

    def resolve_kb_id(self, kb_id_or_name: str) -> str:
        """将知识库名称解析为其 UUID（如有必要）。

        如果 *kb_id_or_name* 本身就是 UUID，则原样返回。
        否则列出所有知识库，返回第一个 ``name`` 匹配
        （大小写不敏感）的知识库。
        未找到匹配项时抛出 ValueError。
        """
        if self._UUID_RE.match(kb_id_or_name):
            return kb_id_or_name
        # 在自有 + 共享知识库中按名称查找
        needle = kb_id_or_name.lower()
        for source in (self.list_knowledge_bases, self.list_shared_knowledge_bases):
            for kb in _normalize_kb_entries(source()):
                if kb.get("name", "").lower() == needle:
                    return kb["id"]
        raise ValueError(
            f"未找到知识库 {kb_id_or_name!r}。"
            "请使用 list_knowledge_bases 或 list_shared_knowledge_bases 查看可用的 ID 和名称。"
        )

    def hybrid_search(self, kb_id: str, query: str, config: Dict) -> Dict:
        """执行混合检索，结合向量检索和关键词检索"""
        data = {
            "query_text": query,
            **config,  # 包含阈值和匹配数量
        }
        return self._request(
            "POST", f"/knowledge-bases/{kb_id}/hybrid-search", json=data
        )

    # 文档管理 - 创建和管理知识条目的方法
    def create_knowledge_from_file(
        self, kb_id: str, file_path: str, enable_multimodel: bool = True
    ) -> Dict:
        """从本地文件创建文档，可选开启多模态处理"""
        safe_path = resolve_upload_file_path(file_path)
        with open(safe_path, "rb") as f:
            files = {"file": f}
            data = {"enable_multimodel": str(enable_multimodel).lower()}
            # multipart/form-data 请求时临时移除 Content-Type 头
            # （requests 会自动设置带 boundary 的 Content-Type）
            headers = self.session.headers.copy()
            del headers["Content-Type"]
            # 直接使用 requests.post 而非 session，避免头信息冲突
            response = requests.post(
                f"{self.base_url}/knowledge-bases/{kb_id}/knowledge/file",
                headers=headers,
                files=files,
                data=data,
                verify=self.verify_ssl,
            )
            response.raise_for_status()
            return response.json()

    def create_knowledge_from_url(
        self, kb_id: str, url: str, enable_multimodel: bool = True
    ) -> Dict:
        """从网页 URL 创建文档，可选开启多模态处理"""
        data = {
            "url": url,  # 要抓取和处理的网页 URL
            "enable_multimodel": enable_multimodel,  # 启用图片/多模态提取
        }
        return self._request(
            "POST", f"/knowledge-bases/{kb_id}/knowledge/url", json=data
        )

    def create_knowledge_from_text(
        self,
        kb_id: str,
        title: str,
        content: str,
        tag_ids: list[str] | None = None,
        status: str = "publish",
    ) -> Dict:
        """从原始 Markdown 文本创建文档（手动录入的知识）。

        ``status`` 默认为 ``"publish"``，因此文档会立即被分块、
        向量化并可被检索，这适合没有 UI 来发布草稿的 API/MCP 调用方。
        传入 ``"draft"`` 可仅保存而不进行索引。
        """
        data = {
            "title": title,
            "content": content,
            "status": status,
        }
        if tag_ids:
            data["tag_ids"] = tag_ids
        return self._request(
            "POST", f"/knowledge-bases/{kb_id}/knowledge/manual", json=data
        )

    def update_knowledge_from_text(
        self,
        knowledge_id: str,
        content: str,
        title: str = "",
        status: str = "publish",
    ) -> Dict:
        """更新已有的手动 Markdown 文档。

        ``title`` 为空时保留原有标题。``status`` 默认为 ``"publish"``，
        因此更新后的内容会立即重新索引；传入 ``"draft"`` 可仅保存而不索引。
        """
        data = {
            "title": title,
            "content": content,
            "status": status,
        }
        return self._request("PUT", f"/knowledge/manual/{knowledge_id}", json=data)

    def list_knowledge(self, kb_id: str, page: int = 1, page_size: int = 20) -> Dict:
        """列出知识库中的文档"""
        params = {"page": page, "page_size": page_size}
        return self._request(
            "GET", f"/knowledge-bases/{kb_id}/knowledge", params=params
        )

    def get_knowledge(self, knowledge_id: str) -> Dict:
        """获取文档详情"""
        return self._request("GET", f"/knowledge/{knowledge_id}")

    def delete_knowledge(self, knowledge_id: str) -> Dict:
        """删除文档"""
        return self._request("DELETE", f"/knowledge/{knowledge_id}")

    # 模型管理 - 管理 AI 模型（大语言模型、向量模型、重排模型）的方法
    def create_model(
        self,
        name: str,
        model_type: str,
        source: str,
        description: str,
        parameters: Dict,
        is_default: bool = False,
    ) -> Dict:
        """创建新的 AI 模型配置"""
        data = {
            "name": name,
            "type": model_type,  # KnowledgeQA（问答）、Embedding（向量）或 Rerank（重排）
            "source": source,  # local（本地）、openai 等
            "description": description,
            "parameters": parameters,  # API 密钥、基础 URL 等
            "is_default": is_default,  # 设为该类型的默认模型
        }
        return self._request("POST", "/models", json=data)

    def list_models(self) -> Dict:
        """列出所有模型"""
        return self._request("GET", "/models")

    def get_model(self, model_id: str) -> Dict:
        """获取模型详情"""
        return self._request("GET", f"/models/{model_id}")

    # 会话管理 - 管理聊天会话的方法
    def create_session(
        self,
        kb_id: str,
        max_rounds: int = 5,
        enable_rewrite: bool = True,
        fallback_response: str = "Sorry, I cannot answer this question.",
        summary_model_id: str = "",
        title: str = "",
        description: str = "",
    ) -> Dict:
        """创建新的聊天会话，配置检索策略"""
        strategy = {
            "max_rounds": max_rounds,
            "enable_rewrite": enable_rewrite,
            "fallback_strategy": "FIXED_RESPONSE",
            "fallback_response": fallback_response,
            "embedding_top_k": 10,
            "keyword_threshold": 0.5,
            "vector_threshold": 0.7,
            "summary_model_id": summary_model_id,
        }
        data = {
            "knowledge_base_id": kb_id,
            "session_strategy": strategy,
        }
        if title:
            data["title"] = title
        if description:
            data["description"] = description
        return self._request("POST", "/sessions", json=data)

    def get_session(self, session_id: str) -> Dict:
        """获取会话详情"""
        return self._request("GET", f"/sessions/{session_id}")

    def list_sessions(self, page: int = 1, page_size: int = 20) -> Dict:
        """列出会话"""
        params = {"page": page, "page_size": page_size}
        return self._request("GET", "/sessions", params=params)

    def delete_session(self, session_id: str) -> Dict:
        """删除会话"""
        return self._request("DELETE", f"/sessions/{session_id}")

    # 聊天功能 - 对话交互的方法
    def _consume_sse_stream(self, url: str, body: Dict[str, Any]) -> Dict:
        """向 *url* 发送 POST 请求，请求体为 *body*，消费 SSE 流并返回组装后的结果。

        由 chat() 和 agent_chat() 共用的集中式辅助函数。
        超时设置：连接 10 秒，读取 WEKNORA_CHAT_TIMEOUT 秒——可通过环境变量配置。

        服务器发送事件（SSE）流格式：
          data: {"response_type": "answer", "content": "..."}
          data: {"response_type": "references", "knowledge_references": [...]}
          data: {"response_type": "complete"}

        我们累积回答片段并提取引用，最终以字典形式返回。
        """
        try:
            # 使用 stream=True 接收服务器发送事件，实现增量读取
            # 超时：10 秒建立连接，WEKNORA_CHAT_TIMEOUT 秒读取响应
            response = self.session.post(
                url, json=body, stream=True,
                timeout=(10, WEKNORA_CHAT_TIMEOUT),
            )
            response.raise_for_status()

            answer_chunks: list = []
            references: list = []
            debug_events: list = []

            # 使用上下文管理器确保连接能正确归还到连接池，
            # 即使在收到 'complete' 事件提前中断时也是如此。
            with response:
                for raw_line in response.iter_lines():
                    if not raw_line:
                        continue
                    if isinstance(raw_line, bytes):
                        raw_line = raw_line.decode("utf-8")
                    # 每个 SSE 事件以 "data: " 开头，后跟 JSON 数据
                    if not raw_line.startswith("data:"):
                        continue
                    payload = raw_line[5:].lstrip(" ")
                    try:
                        event_data = json.loads(payload)
                    except json.JSONDecodeError:
                        continue

                    response_type = event_data.get("response_type", "")
                    debug_events.append({"type": response_type, "content": event_data.get("content", "")[:80]})

                    # 解析不同类型的 SSE 事件：回答片段、引用、错误、完成
                    if response_type == "answer":
                        chunk = event_data.get("content", "")
                        if chunk:
                            answer_chunks.append(chunk)
                    elif response_type == "references":
                        references = event_data.get("knowledge_references") or []
                    elif response_type == "error":
                        raise RequestException(
                            f"服务器错误: {event_data.get('content', '未知错误')}"
                        )
                    elif response_type == "complete":
                        break

            return {
                "answer": "".join(answer_chunks),
                "references": references,
                "_debug_events": debug_events,
            }
        except RequestException as e:
            logger.error(f"SSE 流请求失败 ({url}): {e}")
            raise

    def chat(
        self,
        session_id: str,
        query: str,
        knowledge_base_ids: list = None,
        web_search_enabled: bool = False,
    ) -> Dict:
        """向 RAG 管线（知识问答）发送消息，返回组装后的回答。

        请提供 *knowledge_base_ids*（UUID 或名称），以便后端在调用
        LLM 生成摘要之前先检索相关的文本片段。
        如需智能体工具调用，请使用 agent_chat()。
        """
        url = f"{self.base_url}/knowledge-chat/{session_id}"
        body: Dict[str, Any] = {"query": query, "channel": "api"}
        if knowledge_base_ids:
            body["knowledge_base_ids"] = knowledge_base_ids
        if web_search_enabled:
            body["web_search_enabled"] = True
        result = self._consume_sse_stream(url, body)
        result["session_id"] = session_id
        return result

    def agent_chat(
        self,
        session_id: str,
        query: str,
        agent_id: str,
        knowledge_base_ids: list = None,
        web_search_enabled: bool = False,
    ) -> Dict:
        """向智能体管线（智能体聊天）发送消息，返回组装后的回答。

        *agent_id* 为必填项——后端使用自定义智能体配置来选择工具
        （知识检索、网页搜索、SQL 等）。
        智能体自主决定查询哪些知识库；
        传入 *knowledge_base_ids* 可覆盖或补充智能体的默认知识库。
        """
        url = f"{self.base_url}/agent-chat/{session_id}"
        body: Dict[str, Any] = {"query": query, "agent_id": agent_id, "channel": "api"}
        if knowledge_base_ids:
            body["knowledge_base_ids"] = knowledge_base_ids
        if web_search_enabled:
            body["web_search_enabled"] = True
        result = self._consume_sse_stream(url, body)
        result["session_id"] = session_id
        return result

    def list_agents(self, page: int = 1, page_size: int = 50) -> Dict:
        """列出当前租户可用的所有自定义智能体。"""
        return self._request("GET", "/agents", params={"page": page, "page_size": page_size})

    def get_agent(self, agent_id: str) -> Dict:
        """通过 UUID 获取单个智能体的完整配置。"""
        return self._request("GET", f"/agents/{agent_id}")

    # 分块管理 - 管理知识分块（文本片段）的方法
    def list_chunks(
        self, knowledge_id: str, page: int = 1, page_size: int = 20
    ) -> Dict:
        """分页列出文档的文本分块"""
        params = {"page": page, "page_size": page_size}
        return self._request("GET", f"/chunks/{knowledge_id}", params=params)

    def delete_chunk(self, knowledge_id: str, chunk_id: str) -> Dict:
        """删除一个分块"""
        return self._request("DELETE", f"/chunks/{knowledge_id}/{chunk_id}")

    # Wiki 只读 - 查询 LLM 生成的 Wiki 页面的方法
    def wiki_search(self, kb_id: str, query: str, limit: int = 10) -> Dict:
        """按全文检索查询 Wiki 页面"""
        return self._request(
            "GET",
            f"/knowledgebase/{kb_id}/wiki/search",
            params={"q": query, "limit": limit},
        )

    def wiki_read_page(self, kb_id: str, slug: str) -> Dict:
        """通过 slug 读取 Wiki 页面，返回完整 Markdown + 元数据 + 链接"""
        return self._request("GET", f"/knowledgebase/{kb_id}/wiki/pages/{slug}")

    def wiki_index_view(self, kb_id: str, limit: int = 50) -> Dict:
        """获取结构化的 Wiki 索引，按类型分组展示"""
        return self._request(
            "GET",
            f"/knowledgebase/{kb_id}/wiki/index",
            params={"limit": limit},
        )


# 初始化 MCP 服务器实例（MCP 2.x 高级 API）。
# MCPServer（原 FastMCP）会根据函数类型提示自动构建输入 schema，
# 并自动序列化普通返回值。
mcp = MCPServer("weknora-server", version="1.1.1")
# 使用配置初始化 WeKnora API 客户端
client = WeKnoraClient(WEKNORA_BASE_URL, WEKNORA_API_KEY)


# ---------------------------------------------------------------------------
# 工具注册
#
# 每个工具都是一个用 @mcp.tool() 装饰的普通函数。参数通过类型提示声明
# （框架据此推导 JSON Schema）；必填参数没有默认值。描述来自文档字符串。
# 工具返回字典或字符串，框架负责序列化和错误封装。
# 阻塞式网络 I/O（chat / agent_chat）会被提交到线程执行器，
# 以避免阻塞异步事件循环。
# ---------------------------------------------------------------------------


@mcp.tool()
def create_tenant(
    name: str,
    description: str,
    business: str,
    retriever_engines: dict | None = None,
) -> dict:
    """在 WeKnora 中创建新租户。"""
    engines = retriever_engines or {
        "engines": [
            {"retriever_type": "keywords", "retriever_engine_type": "postgres"},
            {"retriever_type": "vector", "retriever_engine_type": "postgres"},
        ]
    }
    return client.create_tenant(name, description, business, engines)


@mcp.tool()
def list_tenants() -> dict:
    """列出所有租户。"""
    return client.list_tenants()


@mcp.tool()
def create_knowledge_base(
    name: str,
    description: str,
    embedding_model_id: str = "",
    summary_model_id: str = "",
) -> dict:
    """创建新知识库。"""
    config = {
        "chunking_config": {
            "chunk_size": 1000,
            "chunk_overlap": 200,
            "separators": ["."],
            "enable_multimodal": True,
        },
        "embedding_model_id": embedding_model_id,
        "summary_model_id": summary_model_id,
    }
    return client.create_knowledge_base(name, description, config)


@mcp.tool()
def list_knowledge_bases() -> dict:
    """列出当前工作空间中的所有知识库。"""
    return client.list_knowledge_bases()


@mcp.tool()
def list_shared_knowledge_bases() -> dict:
    """列出从其他工作空间共享来的知识库。"""
    return client.list_shared_knowledge_bases()


@mcp.tool()
def get_knowledge_base(kb_id: str) -> dict:
    """获取知识库详情。"""
    return client.get_knowledge_base(kb_id)


@mcp.tool()
def delete_knowledge_base(kb_id: str) -> dict:
    """删除知识库。"""
    return client.delete_knowledge_base(kb_id)


@mcp.tool()
def hybrid_search(
    kb_id: str,
    query: str,
    vector_threshold: float = 0.5,
    keyword_threshold: float = 0.3,
    match_count: int = 5,
) -> dict:
    """在知识库中执行混合检索（向量 + 关键词）。

    kb_id 可以是 UUID 或知识库名称（自动解析）。
    使用 list_knowledge_bases 或 list_shared_knowledge_bases 查看可用的知识库。
    """
    config = {
        "vector_threshold": vector_threshold,
        "keyword_threshold": keyword_threshold,
        "match_count": match_count,
    }
    resolved = client.resolve_kb_id(kb_id)
    return client.hybrid_search(resolved, query, config)


@mcp.tool()
def create_knowledge_from_file(
    kb_id: str,
    file_path: str,
    enable_multimodel: bool = True,
) -> dict:
    """从服务器文件系统上的本地文件创建文档。"""
    return client.create_knowledge_from_file(kb_id, file_path, enable_multimodel)


@mcp.tool()
def create_knowledge_from_url(
    kb_id: str,
    url: str,
    enable_multimodel: bool = True,
) -> dict:
    """从网页 URL 创建文档。"""
    return client.create_knowledge_from_url(kb_id, url, enable_multimodel)


@mcp.tool()
def create_knowledge_from_text(
    kb_id: str,
    title: str,
    content: str,
    tag_ids: list[str] | None = None,
    status: str = "publish",
) -> dict:
    """从原始 Markdown 文本创建文档。

    当你直接有文档内容（比如摘要或粘贴的文本）而非文件路径或 URL
    时使用此方法。``kb_id`` 可以是 UUID 或知识库名称（自动解析）。
    ``title`` 和 ``content`` 为必填项。``status`` 默认为 ``"publish"``，
    因此文档会立即被索引并可被检索；传入 ``"draft"`` 可仅保存而不索引。
    """
    return client.create_knowledge_from_text(
        client.resolve_kb_id(kb_id), title, content, tag_ids=tag_ids, status=status
    )


@mcp.tool()
def update_knowledge_from_text(
    knowledge_id: str,
    content: str,
    title: str = "",
    status: str = "publish",
) -> dict:
    """更新已有的手动 Markdown 文档。

    ``knowledge_id`` 是 ``create_knowledge_from_text`` 或
    ``list_knowledge`` 返回的 ID。``content`` 为必填项。
    ``title`` 留空则保留原有标题。``status`` 默认为 ``"publish"``，
    因此新内容会重新索引；传入 ``"draft"`` 可仅保存而不索引。
    """
    return client.update_knowledge_from_text(
        knowledge_id, content, title=title, status=status
    )


@mcp.tool()
def list_knowledge(kb_id: str, page: int = 1, page_size: int = 20) -> dict:
    """列出知识库中的文档条目。"""
    return client.list_knowledge(kb_id, page, page_size)


@mcp.tool()
def get_knowledge(knowledge_id: str) -> dict:
    """获取文档详情。"""
    return client.get_knowledge(knowledge_id)


@mcp.tool()
def delete_knowledge(knowledge_id: str) -> dict:
    """删除文档。"""
    return client.delete_knowledge(knowledge_id)


@mcp.tool()
def create_model(
    name: str,
    type: str,
    description: str,
    source: str = "local",
    base_url: str = "",
    api_key: str = "",
    is_default: bool = False,
) -> dict:
    """创建新的模型配置（类型：KnowledgeQA、Embedding 或 Rerank）。"""
    parameters = {"base_url": base_url, "api_key": api_key}
    return client.create_model(name, type, source, description, parameters, is_default)


@mcp.tool()
def list_models() -> dict:
    """列出所有模型。"""
    return client.list_models()


@mcp.tool()
def get_model(model_id: str) -> dict:
    """获取模型详情。"""
    return client.get_model(model_id)


@mcp.tool()
def create_session(
    kb_id: str,
    max_rounds: int = 5,
    enable_rewrite: bool = True,
    fallback_response: str = "Sorry, I cannot answer this question.",
    summary_model_id: str = "",
    title: str = "",
    description: str = "",
) -> dict:
    """创建绑定到知识库的新聊天会话，并配置检索策略。

    kb_id 可以是 UUID 或知识库名称（自动解析）。
    """
    return client.create_session(
        kb_id=client.resolve_kb_id(kb_id),
        max_rounds=max_rounds,
        enable_rewrite=enable_rewrite,
        fallback_response=fallback_response,
        summary_model_id=summary_model_id,
        title=title,
        description=description,
    )


@mcp.tool()
def get_session(session_id: str) -> dict:
    """获取会话详情。"""
    return client.get_session(session_id)


@mcp.tool()
def list_sessions(page: int = 1, page_size: int = 20) -> dict:
    """列出聊天会话。"""
    return client.list_sessions(page, page_size)


@mcp.tool()
def delete_session(session_id: str) -> dict:
    """删除会话。"""
    return client.delete_session(session_id)


@mcp.tool()
async def chat(
    session_id: str,
    query: str,
    knowledge_base_ids: list[str] | None = None,
    web_search_enabled: bool = False,
) -> dict:
    """RAG 管线聊天：从知识库检索相关片段，再用 LLM 生成摘要回答。

    务必提供 knowledge_base_ids（名称如 'my-knowledge-base' 或 UUID），
    这样才能执行检索——不提供的话回答仅基于 LLM 自身知识。
    使用 list_knowledge_bases 或 list_shared_knowledge_bases 查看可用的知识库。
    如需多步推理或工具调用，请使用 agent_chat。
    """
    kb_ids = (
        [client.resolve_kb_id(k) for k in knowledge_base_ids]
        if knowledge_base_ids
        else None
    )
    fn = functools.partial(
        client.chat,
        session_id,
        query,
        knowledge_base_ids=kb_ids,
        web_search_enabled=web_search_enabled,
    )
    # 在异步函数内部使用 get_running_loop() 是正确的 API。
    return await asyncio.get_running_loop().run_in_executor(None, fn)


@mcp.tool()
async def agent_chat(
    session_id: str,
    query: str,
    agent_id: str,
    knowledge_base_ids: list[str] | None = None,
    web_search_enabled: bool = False,
) -> dict:
    """智能体管线聊天：智能体自主调用工具（知识检索、网页搜索、SQL 等）。

    必填：agent_id（名称或 UUID）——使用 list_agents 查看可用智能体。
    注意：许多智能体的 KBSelectionMode=none，且没有内置知识库。
    这种情况下你必须传入 knowledge_base_ids，否则智能体会报
    "no search targets available"（无可用检索目标）错误。
    调用前可使用 get_agent 查看智能体的 kb_selection_mode 和
    knowledge_bases。如果 kb_selection_mode 为 'none'，或为 'selected'
    但知识库列表为空，则务必提供 knowledge_base_ids。
    """
    resolved_agent_id = client.resolve_agent_id(agent_id)
    kb_ids = (
        [client.resolve_kb_id(k) for k in knowledge_base_ids]
        if knowledge_base_ids
        else None
    )
    # 预检查：如果未提供知识库 ID，检查智能体配置以发现
    # kb_selection_mode=none/selected-empty 的情况，从而快速失败并给出
    # 清晰的提示，而非后端晦涩的 "no search targets available" 错误。
    if not kb_ids:
        try:
            agent_info = client.get_agent(resolved_agent_id)
            cfg = (agent_info.get("data") or agent_info).get("config") or {}
            mode = cfg.get("kb_selection_mode", "selected")
            built_in_kbs = cfg.get("knowledge_bases") or []
            needs_kbs = (mode == "none") or (
                mode in ("selected", "") and not built_in_kbs
            )
            if needs_kbs:
                all_kbs = _normalize_kb_entries(
                    client.list_knowledge_bases()
                ) + _normalize_kb_entries(client.list_shared_knowledge_bases())
                seen_ids: set[str] = set()
                unique_kbs: list[Dict] = []
                for kb in all_kbs:
                    kb_id = kb.get("id")
                    if kb_id and kb_id not in seen_ids:
                        seen_ids.add(kb_id)
                        unique_kbs.append(kb)
                kb_summary = ", ".join(
                    f"{kb.get('name')} ({kb.get('id')})" for kb in unique_kbs[:10]
                )
                raise ValueError(
                    f"智能体 '{agent_id}' 的 kb_selection_mode='{mode}'，"
                    f"且没有内置知识库。你必须提供 knowledge_base_ids。"
                    f"可用知识库：[{kb_summary}]"
                )
        except ValueError:
            raise
        except Exception as preflight_err:
            logger.warning(
                "agent_chat 预检查知识库失败（不影响主流程）: %s", preflight_err
            )
    fn = functools.partial(
        client.agent_chat,
        session_id,
        query,
        resolved_agent_id,
        knowledge_base_ids=kb_ids,
        web_search_enabled=web_search_enabled,
    )
    return await asyncio.get_running_loop().run_in_executor(None, fn)


@mcp.tool()
def list_agents(page: int = 1, page_size: int = 50) -> dict:
    """列出当前租户可用的所有自定义智能体。

    调用 agent_chat 之前，可使用此方法发现智能体的 ID、名称
    及其知识库选择模式。
    """
    return client.list_agents(page=page, page_size=page_size)


@mcp.tool()
def get_agent(agent_id: str) -> dict:
    """通过 UUID 或名称获取单个智能体的完整配置。

    请关注 kb_selection_mode 和 knowledge_bases 字段：如果 kb_selection_mode
    为 'none'，或为 'selected' 但 knowledge_bases 列表为空，
    则调用 agent_chat 时必须传入 knowledge_base_ids。
    """
    resolved_id = client.resolve_agent_id(agent_id)
    return client.get_agent(resolved_id)


@mcp.tool()
def list_chunks(knowledge_id: str, page: int = 1, page_size: int = 20) -> dict:
    """列出文档的分块（文本片段）。"""
    return client.list_chunks(knowledge_id, page, page_size)


@mcp.tool()
def delete_chunk(knowledge_id: str, chunk_id: str) -> dict:
    """删除一个分块。"""
    return client.delete_chunk(knowledge_id, chunk_id)


@mcp.tool()
def wiki_search(kb_id: str, query: str, limit: int = 10) -> dict:
    """按全文检索查询 Wiki 页面。

    返回匹配的 Wiki 页面，包含标题、slug、摘要和内容片段。
    """
    return client.wiki_search(kb_id, query, limit)


@mcp.tool()
def wiki_read_page(kb_id: str, slug: str) -> dict:
    """通过 slug 读取 Wiki 页面。

    返回完整的 Markdown 内容、元数据、入站/出站链接以及来源引用。
    slug 示例：'entity/acme-corp'、'concept/rag'。
    """
    return client.wiki_read_page(kb_id, slug)


@mcp.tool()
def wiki_index_view(kb_id: str, limit: int = 50) -> dict:
    """获取结构化的 Wiki 索引，按类型分组展示。

    返回所有 Wiki 页面的概览，按类型（实体、概念、摘要等）组织。
    """
    return client.wiki_index_view(kb_id, limit)


# ---------------------------------------------------------------------------
# 传输层
# ---------------------------------------------------------------------------


async def run_stdio():
    """使用标准输入输出传输方式运行 MCP 服务器。"""
    set_active_transport("stdio")
    await mcp.run_stdio_async()


async def run_sse(host: str, port: int):
    """使用 SSE 传输方式运行 MCP 服务器（兼容旧版 MCP 客户端）。"""
    set_active_transport("sse")
    auth_token = require_network_transport_auth("sse")
    try:
        import uvicorn
    except ImportError as e:
        raise ImportError(
            f"SSE 传输方式需要安装 'starlette' 和 'uvicorn'：pip install starlette uvicorn\n{e}"
        ) from e

    starlette_app = MCPAuthMiddleware(
        mcp.sse_app(host=host, message_path=SSE_MESSAGE_PATH),
        auth_token,
    )

    logger.info("SSE 模式 MCP 服务器启动于 %s:%d", host, port)
    logger.info("SSE 端点：    http://%s:%d/sse", host, port)
    logger.info("SSE 消息端点：http://%s:%d%s", host, port, SSE_MESSAGE_PATH)
    config = uvicorn.Config(starlette_app, host=host, port=port, log_level="info")
    server = uvicorn.Server(config)
    await server.serve()


async def run_http(host: str, port: int):
    """使用流式 HTTP 传输方式运行 MCP 服务器（MCP 2025-03-26 规范）。"""
    set_active_transport("http")
    auth_token = require_network_transport_auth("http")
    try:
        import uvicorn
    except ImportError as e:
        raise ImportError(
            f"HTTP 传输方式需要安装 'starlette' 和 'uvicorn'：pip install starlette uvicorn\n{e}"
        ) from e

    starlette_app = MCPAuthMiddleware(
        mcp.streamable_http_app(host=host, stateless_http=STREAMABLE_HTTP_STATELESS),
        auth_token,
    )

    logger.info("流式 HTTP 模式 MCP 服务器启动于 %s:%d", host, port)
    logger.info("MCP 端点：http://%s:%d/mcp", host, port)
    config = uvicorn.Config(starlette_app, host=host, port=port, log_level="info")
    server = uvicorn.Server(config)
    await server.serve()


# 向后兼容的别名，供 run_server.py 使用
run = run_stdio



def main():
    """主入口 - 支持 stdio、sse 和 http 三种传输方式。

    传输方式选择（优先级从高到低）：
      1. --transport 命令行参数
      2. MCP_TRANSPORT 环境变量
      3. 默认值：stdio
    """
    parser = argparse.ArgumentParser(description="WeKnora MCP 服务器")
    parser.add_argument(
        "--transport",
        choices=["stdio", "sse", "http"],
        default=os.getenv("MCP_TRANSPORT", "stdio"),
        help="传输方式：stdio（默认）、sse 或 http",
    )
    parser.add_argument(
        "--host",
        default=os.getenv("MCP_HOST", "127.0.0.1"),
        help="网络传输方式的绑定地址（默认：127.0.0.1）",
    )
    parser.add_argument(
        "--port",
        type=int,
        default=int(os.getenv("MCP_PORT", "8000")),
        help="网络传输方式的绑定端口（默认：8000）",
    )
    args = parser.parse_args()

    if args.transport == "stdio":
        asyncio.run(run_stdio())
    elif args.transport == "sse":
        asyncio.run(run_sse(args.host, args.port))
    elif args.transport == "http":
        asyncio.run(run_http(args.host, args.port))


if __name__ == "__main__":
    main()
