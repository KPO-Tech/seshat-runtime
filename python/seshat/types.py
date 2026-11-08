"""Typed values returned by the Seshat client.

The client never hands a raw protobuf message to a caller: every reply is turned
into one of the dataclasses below, so a program reads ``result.content`` instead
of a generated field name, and nothing here depends on a private protobuf
runtime.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Any, Dict, List, Optional

from ._proto import seshat_pb2 as pb


def _datetime(value: Any) -> Optional[datetime]:
    """Read a protobuf Timestamp as an aware datetime, or None when it is not
    set (a zero timestamp is not a date)."""
    if value is None:
        return None
    seconds = getattr(value, "seconds", 0)
    nanos = getattr(value, "nanos", 0)
    if not seconds and not nanos:
        return None
    return value.ToDatetime(tzinfo=timezone.utc)


@dataclass(frozen=True)
class TokenUsage:
    """What a request cost."""

    input_tokens: int = 0
    output_tokens: int = 0
    total_tokens: int = 0
    cached_tokens: int = 0
    cache_read_input_tokens: int = 0
    cache_creation_input_tokens: int = 0

    @classmethod
    def from_proto(cls, value: Optional[pb.TokenUsage]) -> Optional["TokenUsage"]:
        if value is None:
            return None
        return cls(
            input_tokens=value.input_tokens,
            output_tokens=value.output_tokens,
            total_tokens=value.total_tokens,
            cached_tokens=value.cached_tokens,
            cache_read_input_tokens=value.cache_read_input_tokens,
            cache_creation_input_tokens=value.cache_creation_input_tokens,
        )


@dataclass(frozen=True)
class ContentBlock:
    """One block of a message.

    ``kind`` says which fields are set: ``text``, ``thinking``, ``tool_use``,
    ``tool_result`` or ``image``.
    """

    kind: str = "text"
    text: str = ""
    thinking: str = ""
    tool_use_id: str = ""
    name: str = ""
    input: Dict[str, Any] = field(default_factory=dict)
    content: str = ""
    is_error: bool = False
    media_type: str = ""
    data: str = ""
    source_type: str = ""

    @classmethod
    def from_proto(cls, value: pb.ContentBlock) -> "ContentBlock":
        which = value.WhichOneof("block")
        if which == "text":
            return cls(kind="text", text=value.text.text)
        if which == "thinking":
            return cls(kind="thinking", thinking=value.thinking.thinking)
        if which == "tool_use":
            return cls(kind="tool_use", tool_use_id=value.tool_use.id, name=value.tool_use.name, input=dict(value.tool_use.input))
        if which == "tool_result":
            return cls(
                kind="tool_result",
                tool_use_id=value.tool_result.tool_use_id,
                content=value.tool_result.content,
                is_error=value.tool_result.is_error,
            )
        if which == "image":
            return cls(
                kind="image",
                media_type=value.image.media_type,
                data=value.image.data,
                source_type=value.image.source_type,
            )
        return cls(kind=value.type or "text")


@dataclass(frozen=True)
class Message:
    """One message of a conversation."""

    id: str = ""
    role: str = ""
    content: List[ContentBlock] = field(default_factory=list)
    timestamp: Optional[datetime] = None
    turn_id: str = ""

    @property
    def text(self) -> str:
        """The text blocks of the message, joined."""
        return "".join(block.text for block in self.content if block.kind == "text")

    @property
    def tool_uses(self) -> List[ContentBlock]:
        return [block for block in self.content if block.kind == "tool_use"]

    @classmethod
    def from_proto(cls, value: pb.Message) -> "Message":
        return cls(
            id=value.id,
            role=value.role,
            content=[ContentBlock.from_proto(block) for block in value.content],
            timestamp=_datetime(value.timestamp),
            turn_id=value.turn_id,
        )


@dataclass(frozen=True)
class ToolUse:
    """The model asking for a tool call."""

    id: str = ""
    name: str = ""
    input: Dict[str, Any] = field(default_factory=dict)

    @classmethod
    def from_proto(cls, value: pb.ToolUse) -> "ToolUse":
        return cls(id=value.id, name=value.name, input=dict(value.input))


@dataclass(frozen=True)
class ToolResult:
    """What a tool call returned."""

    tool_use_id: str = ""
    content: str = ""
    is_error: bool = False

    @classmethod
    def from_proto(cls, value: pb.ToolResult) -> "ToolResult":
        return cls(tool_use_id=value.tool_use_id, content=value.content, is_error=value.is_error)


@dataclass(frozen=True)
class TurnResult:
    """The outcome of one turn."""

    session_id: str = ""
    content: str = ""
    thinking: str = ""
    messages: List[Message] = field(default_factory=list)
    usage: Optional[TokenUsage] = None
    stop_reason: str = ""
    turn_number: int = 0
    is_complete: bool = False
    compacted: bool = False
    compact_pre_tokens: int = 0
    compact_post_tokens: int = 0
    tool_uses: List[ToolUse] = field(default_factory=list)
    tool_results: List[ToolResult] = field(default_factory=list)
    total_tokens: int = 0

    @classmethod
    def from_proto(cls, value: pb.TurnResult) -> "TurnResult":
        return cls(
            session_id=value.session_id,
            content=value.content,
            thinking=value.thinking,
            messages=[Message.from_proto(message) for message in value.messages],
            usage=TokenUsage.from_proto(value.usage) if value.HasField("usage") else None,
            stop_reason=value.stop_reason,
            turn_number=value.turn_number,
            is_complete=value.is_complete,
            compacted=value.compacted,
            compact_pre_tokens=value.compact_pre_tokens,
            compact_post_tokens=value.compact_post_tokens,
            tool_uses=[ToolUse.from_proto(use) for use in value.tool_uses],
            tool_results=[ToolResult.from_proto(result) for result in value.tool_results],
            total_tokens=value.total_tokens,
        )


@dataclass(frozen=True)
class SessionInfo:
    """A stored conversation, as the list of conversations shows it."""

    id: str = ""
    title: str = ""
    status: str = ""
    created_at: Optional[datetime] = None
    updated_at: Optional[datetime] = None
    total_turns: int = 0
    total_tokens: int = 0
    preview: str = ""
    subagent: bool = False
    parent_id: str = ""

    @classmethod
    def from_proto(cls, value: pb.SessionInfo) -> "SessionInfo":
        return cls(
            id=value.id,
            title=value.title,
            status=value.status,
            created_at=_datetime(value.created_at),
            updated_at=_datetime(value.updated_at),
            total_turns=value.total_turns,
            total_tokens=value.total_tokens,
            preview=value.preview,
            subagent=value.subagent,
            parent_id=value.parent_id,
        )


@dataclass(frozen=True)
class SessionSnapshot:
    """A conversation attached to the server, as the server sees it."""

    id: str = ""
    title: str = ""
    status: str = ""
    model: str = ""
    working_dir: str = ""
    turn_number: int = 0
    total_tokens: int = 0
    permission_mode: str = ""
    execution_mode: str = ""
    tools: List[str] = field(default_factory=list)
    auto_mode_available: bool = False
    created_at: Optional[datetime] = None
    updated_at: Optional[datetime] = None

    @classmethod
    def from_proto(cls, value: pb.Session) -> "SessionSnapshot":
        return cls(
            id=value.id,
            title=value.title,
            status=value.status,
            model=value.model,
            working_dir=value.working_dir,
            turn_number=value.turn_number,
            total_tokens=value.total_tokens,
            permission_mode=value.permission_mode,
            execution_mode=value.execution_mode,
            tools=list(value.tools),
            auto_mode_available=value.auto_mode_available,
            created_at=_datetime(value.created_at),
            updated_at=_datetime(value.updated_at),
        )


@dataclass(frozen=True)
class SessionState:
    """The live state of a session."""

    id: str = ""
    title: str = ""
    status: str = ""
    model: str = ""
    root_path: str = ""
    turn_number: int = 0
    total_tokens: int = 0
    permission_mode: str = ""
    execution_mode: str = ""
    tools: List[str] = field(default_factory=list)
    auto_mode_available: bool = False
    compact_count: int = 0
    max_tokens: int = 0

    @classmethod
    def from_proto(cls, value: pb.SessionState) -> "SessionState":
        metadata = value.metadata
        return cls(
            id=metadata.id,
            title=metadata.title,
            status=metadata.status,
            model=metadata.model,
            root_path=metadata.root_path,
            turn_number=value.turn_number,
            total_tokens=value.total_tokens,
            permission_mode=value.permission_mode,
            execution_mode=value.execution_mode,
            tools=list(value.tools),
            auto_mode_available=value.auto_mode_available,
            compact_count=metadata.compact_count,
            max_tokens=metadata.max_tokens,
        )


@dataclass(frozen=True)
class ToolInfo:
    """A tool of a session."""

    name: str = ""
    description: str = ""
    enabled: bool = False
    deferred: bool = False
    category: str = ""

    @classmethod
    def from_proto(cls, value: pb.ToolInfo) -> "ToolInfo":
        return cls(
            name=value.name,
            description=value.description,
            enabled=value.enabled,
            deferred=value.deferred,
            category=value.category,
        )


@dataclass(frozen=True)
class Skill:
    """A skill the runtime can load."""

    name: str = ""
    description: str = ""
    source: str = ""
    when_to_use: str = ""
    allowed_tools: List[str] = field(default_factory=list)
    content: str = ""
    path: str = ""

    @classmethod
    def from_proto(cls, value: pb.Skill) -> "Skill":
        return cls(
            name=value.name,
            description=value.description,
            source=value.source,
            when_to_use=value.when_to_use,
            allowed_tools=list(value.allowed_tools),
            content=value.content,
            path=value.path,
        )


@dataclass(frozen=True)
class MCPServer:
    """An MCP server the runtime knows."""

    name: str = ""
    status: str = ""
    tool_count: int = 0
    last_error: str = ""
    type: str = ""
    url: str = ""
    enabled: bool = False

    @classmethod
    def from_proto(cls, value: pb.MCPServer) -> "MCPServer":
        return cls(
            name=value.name,
            status=value.status,
            tool_count=value.tool_count,
            last_error=value.last_error,
            type=value.type,
            url=value.url,
            enabled=value.enabled,
        )


@dataclass(frozen=True)
class ModelInfo:
    """A model of the provider registry."""

    id: str = ""
    name: str = ""
    provider: str = ""
    max_tokens: int = 0
    context_window: int = 0

    @classmethod
    def from_proto(cls, value: pb.ModelInfo) -> "ModelInfo":
        return cls(
            id=value.id,
            name=value.name,
            provider=value.provider,
            max_tokens=value.max_tokens,
            context_window=value.context_window,
        )


@dataclass(frozen=True)
class ProviderInfo:
    """A provider and whether this server has a credential for it."""

    name: str = ""
    configured: bool = False
    model_count: int = 0

    @classmethod
    def from_proto(cls, value: pb.ProviderInfo) -> "ProviderInfo":
        return cls(name=value.name, configured=value.configured, model_count=value.model_count)


@dataclass(frozen=True)
class Health:
    """The liveness answer of the server."""

    status: str = ""
    version: str = ""
    uptime: str = ""
    uptime_seconds: int = 0

    @classmethod
    def from_proto(cls, value: pb.HealthCheckResponse) -> "Health":
        return cls(
            status=value.status,
            version=value.version,
            uptime=value.uptime,
            uptime_seconds=value.uptime_seconds,
        )


@dataclass(frozen=True)
class ServerInfo:
    """What the server is and what it can do."""

    version: str = ""
    uptime_seconds: int = 0
    model: str = ""
    working_dir: str = ""
    permission_mode: str = ""
    sandbox_kind: str = ""
    providers_configured: List[str] = field(default_factory=list)
    providers_available: List[str] = field(default_factory=list)
    memory_enabled: bool = False
    hooks_enabled: bool = False
    monitoring_enabled: bool = False
    tool_search_enabled: bool = False
    session_persistence_enabled: bool = False
    stdio_mcp_allowed: bool = False
    auth_required: bool = False
    tls_enabled: bool = False
    active_sessions: int = 0

    @classmethod
    def from_proto(cls, value: pb.GetServerInfoResponse) -> "ServerInfo":
        return cls(
            version=value.version,
            uptime_seconds=value.uptime_seconds,
            model=value.model,
            working_dir=value.working_dir,
            permission_mode=value.permission_mode,
            sandbox_kind=value.sandbox_kind,
            providers_configured=list(value.providers_configured),
            providers_available=list(value.providers_available),
            memory_enabled=value.memory_enabled,
            hooks_enabled=value.hooks_enabled,
            monitoring_enabled=value.monitoring_enabled,
            tool_search_enabled=value.tool_search_enabled,
            session_persistence_enabled=value.session_persistence_enabled,
            stdio_mcp_allowed=value.stdio_mcp_allowed,
            auth_required=value.auth_required,
            tls_enabled=value.tls_enabled,
            active_sessions=value.active_sessions,
        )


@dataclass(frozen=True)
class Image:
    """An image attached to a message, base64 encoded."""

    data: str
    media_type: str = "image/png"
    source_type: str = "base64"


@dataclass(frozen=True)
class MemoryEntity:
    """A node of the long-term memory graph."""

    id: str = ""
    user_id: str = ""
    name: str = ""
    entity_type: str = ""
    observations: List[str] = field(default_factory=list)
    created_at: Optional[datetime] = None
    updated_at: Optional[datetime] = None

    @classmethod
    def from_proto(cls, value: pb.MemoryEntity) -> "MemoryEntity":
        return cls(
            id=value.id,
            user_id=value.user_id,
            name=value.name,
            entity_type=value.entity_type,
            observations=list(value.observations),
            created_at=_datetime(value.created_at),
            updated_at=_datetime(value.updated_at),
        )


@dataclass(frozen=True)
class MemoryRelation:
    """A directed edge between two memory entities, by name."""

    id: str = ""
    user_id: str = ""
    source: str = ""
    target: str = ""
    relation_type: str = ""
    created_at: Optional[datetime] = None

    @classmethod
    def from_proto(cls, value: pb.MemoryRelation) -> "MemoryRelation":
        return cls(
            id=value.id,
            user_id=value.user_id,
            source=getattr(value, "from", ""),
            target=value.to,
            relation_type=value.relation_type,
            created_at=_datetime(value.created_at),
        )


@dataclass(frozen=True)
class MemoryGraph:
    """Entities and the relations that touch them."""

    entities: List[MemoryEntity] = field(default_factory=list)
    relations: List[MemoryRelation] = field(default_factory=list)

    @classmethod
    def from_proto(cls, value: pb.MemoryGraph) -> "MemoryGraph":
        return cls(
            entities=[MemoryEntity.from_proto(entity) for entity in value.entities],
            relations=[MemoryRelation.from_proto(relation) for relation in value.relations],
        )


@dataclass(frozen=True)
class ObservationResult:
    """Observations actually added to an entity, after deduplication."""

    entity_name: str = ""
    added_observations: List[str] = field(default_factory=list)

    @classmethod
    def from_proto(cls, value: pb.ObservationResult) -> "ObservationResult":
        return cls(entity_name=value.entity_name, added_observations=list(value.added_observations))


@dataclass(frozen=True)
class WorkflowValidation:
    """Whether a workflow definition is valid."""

    valid: bool = False
    diagnostics: str = ""
    name: str = ""
    node_count: int = 0

    @classmethod
    def from_proto(cls, value: pb.WorkflowValidationResponse) -> "WorkflowValidation":
        return cls(
            valid=value.valid,
            diagnostics=value.diagnostics,
            name=value.name,
            node_count=value.node_count,
        )


@dataclass(frozen=True)
class WorkflowNodeResult:
    """The outcome of one node of a workflow."""

    id: str = ""
    agent: str = ""
    success: bool = False
    output: str = ""
    error: str = ""
    started_at: Optional[datetime] = None
    finished_at: Optional[datetime] = None

    @classmethod
    def from_proto(cls, value: pb.WorkflowNodeResult) -> "WorkflowNodeResult":
        return cls(
            id=value.id,
            agent=value.agent,
            success=value.success,
            output=value.output,
            error=value.error,
            started_at=_datetime(value.started_at),
            finished_at=_datetime(value.finished_at),
        )


@dataclass(frozen=True)
class WorkflowRun:
    """The outcome of a workflow run."""

    success: bool = False
    results: Dict[str, WorkflowNodeResult] = field(default_factory=dict)
    order: List[str] = field(default_factory=list)

    @classmethod
    def from_proto(cls, value: pb.WorkflowRunResponse) -> "WorkflowRun":
        return cls(
            success=value.success,
            results={key: WorkflowNodeResult.from_proto(node) for key, node in value.results.items()},
            order=list(value.order),
        )
