"""Events of a session.

``SubmitMessageStream`` and ``Subscribe`` send :class:`SessionEvent` messages.
Each one carries the engine's own type in ``type`` (``"response.chunk"``,
``"tool.progress"``, …) and a typed ``payload``. ``kind`` names the payload,
which is what most code switches on:

    for event in session.stream("hello"):
        if event.kind == "chunk":
            print(event.text, end="")
        elif event.kind == "permission_request":
            ...
"""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime
from typing import Any, Dict, List, Optional

from ._proto import seshat_pb2 as pb
from .types import ContentBlock, TokenUsage, TurnResult, _datetime


@dataclass(frozen=True)
class Chunk:
    """One piece of a model response as it streams in."""

    type: str = ""
    delta_type: str = ""
    delta: str = ""
    index: Optional[int] = None
    partial_json: str = ""
    content_block: Optional[ContentBlock] = None
    stop_reason: str = ""
    usage: Optional[TokenUsage] = None

    @classmethod
    def from_proto(cls, value: pb.ChunkDelta) -> "Chunk":
        return cls(
            type=value.type,
            delta_type=value.delta_type,
            delta=value.delta,
            index=value.index if value.HasField("index") else None,
            partial_json=value.partial_json,
            content_block=ContentBlock.from_proto(value.content_block) if value.HasField("content_block") else None,
            stop_reason=value.stop_reason,
            usage=TokenUsage.from_proto(value.usage) if value.HasField("usage") else None,
        )


@dataclass(frozen=True)
class ToolProgress:
    """How a running tool is doing."""

    tool_name: str = ""
    tool_use_id: str = ""
    stage: str = ""
    message: str = ""
    percent_complete: float = 0.0
    metadata: Dict[str, Any] = field(default_factory=dict)

    @classmethod
    def from_proto(cls, value: pb.ToolProgress) -> "ToolProgress":
        return cls(
            tool_name=value.tool_name,
            tool_use_id=value.tool_use_id,
            stage=value.stage,
            message=value.message,
            percent_complete=value.percent_complete,
            metadata=dict(value.metadata),
        )


@dataclass(frozen=True)
class PermissionRequest:
    """A tool call waiting for the client's decision.

    Answer it with ``session.respond_permission(request.request_id, "allow")``,
    or let ``session.stream(on_permission=...)`` do it for you.
    """

    request_id: str = ""
    session_id: str = ""
    turn_id: str = ""
    tool_name: str = ""
    description: str = ""
    tool_input: Dict[str, Any] = field(default_factory=dict)
    tool_use_id: str = ""
    permission_mode: str = ""
    working_directory: str = ""
    stage: str = ""
    expires_at: Optional[datetime] = None
    agent_tool_use_id: str = ""

    @classmethod
    def from_proto(cls, value: pb.PermissionRequest) -> "PermissionRequest":
        return cls(
            request_id=value.request_id,
            session_id=value.session_id,
            turn_id=value.turn_id,
            tool_name=value.tool_name,
            description=value.description,
            tool_input=dict(value.tool_input),
            tool_use_id=value.tool_use_id,
            permission_mode=value.permission_mode,
            working_directory=value.working_directory,
            stage=value.stage,
            expires_at=_datetime(value.expires_at),
            agent_tool_use_id=value.agent_tool_use_id,
        )


@dataclass(frozen=True)
class PromptOption:
    """One choice of a choice prompt."""

    label: str = ""
    value: Any = None
    description: str = ""

    @classmethod
    def from_proto(cls, value: pb.PromptOption) -> "PromptOption":
        return cls(
            label=value.label,
            value=value.value.AsInterface() if value.HasField("value") else None,
            description=value.description,
        )


@dataclass(frozen=True)
class PromptRequest:
    """A question a tool asks the user.

    Answer it with ``session.respond_prompt(request.request_id, value)``, or let
    ``session.stream(on_prompt=...)`` do it for you.
    """

    request_id: str = ""
    session_id: str = ""
    turn_id: str = ""
    type: str = ""
    message: str = ""
    options: List[PromptOption] = field(default_factory=list)
    default: Any = None
    metadata: Dict[str, Any] = field(default_factory=dict)
    expires_at: Optional[datetime] = None
    agent_tool_use_id: str = ""

    @classmethod
    def from_proto(cls, value: pb.PromptRequest) -> "PromptRequest":
        return cls(
            request_id=value.request_id,
            session_id=value.session_id,
            turn_id=value.turn_id,
            type=value.type,
            message=value.message,
            options=[PromptOption.from_proto(option) for option in value.options],
            default=value.default_value.AsInterface() if value.HasField("default_value") else None,
            metadata=dict(value.metadata),
            expires_at=_datetime(value.expires_at),
            agent_tool_use_id=value.agent_tool_use_id,
        )


@dataclass(frozen=True)
class TurnStarted:
    """A turn opened."""

    turn_number: int = 0
    turn_id: str = ""
    model: str = ""
    execution_mode: str = ""


@dataclass(frozen=True)
class TurnFailed:
    """A turn that errored."""

    code: str = ""
    message: str = ""


@dataclass(frozen=True)
class PlanEvent:
    """A plan the model submitted, or a change to its status."""

    plan_id: str = ""
    slug: str = ""
    filename: str = ""
    status: str = ""
    version: int = 0
    content: str = ""


@dataclass(frozen=True)
class TaskEvent:
    """A change to the model's task list."""

    task_id: str = ""
    action: str = ""
    status: str = ""
    subject: str = ""


@dataclass(frozen=True)
class StageEvent:
    """A named phase of pre-generation setup."""

    stage: str = ""
    label: str = ""


@dataclass(frozen=True)
class ProviderRetry:
    """The runtime waiting before asking the provider again."""

    attempt: int = 0
    max_attempts: int = 0
    wait_ms: int = 0
    reason: str = ""


@dataclass(frozen=True)
class Compaction:
    """A compaction of the conversation."""

    started: bool = False
    pre_compact_tokens: int = 0
    post_compact_tokens: int = 0


@dataclass(frozen=True)
class ContextUsage:
    """How full the model's context window is."""

    used_tokens: int = 0
    window_tokens: int = 0
    auto_compact_at_tokens: int = 0
    blocking_limit_tokens: int = 0
    source: str = ""


@dataclass(frozen=True)
class BrowserEvent:
    """Browser activity."""

    action: str = ""
    page_id: str = ""
    url: str = ""
    title: str = ""
    bytes: int = 0
    persisted_path: str = ""


@dataclass(frozen=True)
class AgentEvent:
    """A multi-agent collaboration event."""

    event_type: str = ""
    call_id: str = ""
    agent_id: str = ""
    agent_nickname: str = ""
    agent_role: str = ""
    status: str = ""
    message: str = ""


@dataclass(frozen=True)
class GoalEvent:
    """The long-running goal of an agent."""

    objective: str = ""
    status: str = ""
    token_budget: Optional[int] = None
    tokens_used: int = 0
    time_used_seconds: int = 0


@dataclass(frozen=True)
class RawEvent:
    """An event this package does not name yet.

    ``type`` is the engine's own type and ``data`` its JSON payload, so a newer
    server still reaches an older client instead of being dropped.
    """

    type: str = ""
    data: Dict[str, Any] = field(default_factory=dict)
    error: str = ""


@dataclass(frozen=True)
class SessionEvent:
    """One thing that happened in a session."""

    event_id: str = ""
    session_id: str = ""
    sequence: int = 0
    timestamp: Optional[datetime] = None
    turn_id: str = ""
    turn_number: int = 0
    #: The engine's own type, e.g. ``"response.chunk"``.
    type: str = ""
    #: The payload this message carries, e.g. ``"chunk"``.
    kind: str = "unknown"
    payload: Any = None
    agent_tool_use_id: str = ""

    # -- conveniences ------------------------------------------------------

    @property
    def text(self) -> str:
        """The text delta of a chunk, or an empty string."""
        if isinstance(self.payload, Chunk):
            return self.payload.delta
        return ""

    @property
    def chunk(self) -> Optional[Chunk]:
        return self.payload if isinstance(self.payload, Chunk) else None

    @property
    def tool_progress(self) -> Optional[ToolProgress]:
        return self.payload if isinstance(self.payload, ToolProgress) else None

    @property
    def permission_request(self) -> Optional[PermissionRequest]:
        return self.payload if isinstance(self.payload, PermissionRequest) else None

    @property
    def prompt_request(self) -> Optional[PromptRequest]:
        return self.payload if isinstance(self.payload, PromptRequest) else None

    @property
    def result(self) -> Optional[TurnResult]:
        """The result of the turn, on ``turn_completed``."""
        if isinstance(self.payload, TurnResult):
            return self.payload
        return None

    @property
    def error(self) -> Optional[TurnFailed]:
        """The failure, on ``turn_failed``."""
        return self.payload if isinstance(self.payload, TurnFailed) else None

    @property
    def is_final(self) -> bool:
        return self.kind in ("turn_completed", "turn_failed")

    # -- parsing -----------------------------------------------------------

    @classmethod
    def from_proto(cls, value: pb.SessionEvent) -> "SessionEvent":
        which = value.WhichOneof("payload") or "unknown"
        payload: Any = None
        if which == "turn_started":
            started = value.turn_started
            payload = TurnStarted(
                turn_number=started.turn_number,
                turn_id=started.turn_id,
                model=started.model,
                execution_mode=started.execution_mode,
            )
        elif which == "chunk":
            payload = Chunk.from_proto(value.chunk)
        elif which == "tool_progress":
            payload = ToolProgress.from_proto(value.tool_progress)
        elif which == "permission_request":
            payload = PermissionRequest.from_proto(value.permission_request)
        elif which == "prompt_request":
            payload = PromptRequest.from_proto(value.prompt_request)
        elif which == "plan":
            plan = value.plan
            payload = PlanEvent(
                plan_id=plan.plan_id,
                slug=plan.slug,
                filename=plan.filename,
                status=plan.status,
                version=plan.version,
                content=plan.content,
            )
        elif which == "task":
            task = value.task
            payload = TaskEvent(task_id=task.task_id, action=task.action, status=task.status, subject=task.subject)
        elif which == "stage":
            payload = StageEvent(stage=value.stage.stage, label=value.stage.label)
        elif which == "provider_retry":
            retry = value.provider_retry
            payload = ProviderRetry(
                attempt=retry.attempt,
                max_attempts=retry.max_attempts,
                wait_ms=retry.wait_ms,
                reason=retry.reason,
            )
        elif which == "compaction":
            compaction = value.compaction
            payload = Compaction(
                started=compaction.started,
                pre_compact_tokens=compaction.pre_compact_tokens,
                post_compact_tokens=compaction.post_compact_tokens,
            )
        elif which == "context_usage":
            usage = value.context_usage
            payload = ContextUsage(
                used_tokens=usage.used_tokens,
                window_tokens=usage.window_tokens,
                auto_compact_at_tokens=usage.auto_compact_at_tokens,
                blocking_limit_tokens=usage.blocking_limit_tokens,
                source=usage.source,
            )
        elif which == "turn_completed":
            payload = TurnResult.from_proto(value.turn_completed.result)
        elif which == "turn_failed":
            payload = TurnFailed(code=value.turn_failed.code, message=value.turn_failed.message)
        elif which == "agent":
            agent = value.agent
            payload = AgentEvent(
                event_type=agent.event_type,
                call_id=agent.call_id,
                agent_id=agent.agent_id,
                agent_nickname=agent.agent_nickname,
                agent_role=agent.agent_role,
                status=agent.status,
                message=agent.message,
            )
        elif which == "goal":
            goal = value.goal
            payload = GoalEvent(
                objective=goal.objective,
                status=goal.status,
                token_budget=goal.token_budget if goal.HasField("token_budget") else None,
                tokens_used=goal.tokens_used,
                time_used_seconds=goal.time_used_seconds,
            )
        elif which == "browser":
            browser = value.browser
            payload = BrowserEvent(
                action=browser.action,
                page_id=browser.page_id,
                url=browser.url,
                title=browser.title,
                bytes=browser.bytes,
                persisted_path=browser.persisted_path,
            )
        elif which == "raw":
            raw = value.raw
            payload = RawEvent(type=raw.type, data=dict(raw.data), error=raw.error)

        return cls(
            event_id=value.event_id,
            session_id=value.session_id,
            sequence=value.sequence,
            timestamp=_datetime(value.timestamp),
            turn_id=value.turn_id,
            turn_number=value.turn_number,
            type=value.type,
            kind=which,
            payload=payload,
            agent_tool_use_id=value.agent_tool_use_id,
        )
