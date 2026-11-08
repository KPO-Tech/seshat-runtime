"""Python client for the Seshat agent runtime.

Seshat is an open-source agent runtime written in Go. This package speaks to it
over gRPC, so a Python program drives the same engine the terminal interface
does: the same providers, tools, permissions, skills, MCP servers, memory and
persistence, with nothing reimplemented on the Python side.

Quick start::

    import seshat

    with seshat.connect() as client:          # 127.0.0.1:50051
        print(client.info().model)

        with client.create_session() as session:
            print(session.ask("summarise this repository").content)

Streaming, with approvals::

    def decide(request):
        # request.tool_name, request.tool_input, request.description
        return "allow"

    with client.create_session(permission_mode="onRequest") as session:
        for event in session.stream("delete the stale branches", on_permission=decide):
            if event.kind == "chunk":
                print(event.text, end="")
            elif event.kind == "tool_progress":
                print(f"[{event.tool_progress.tool_name}] {event.tool_progress.message}")

Start the server first::

    seshat-grpc                      # or: go run ./cmd/grpc
    SESHAT_GRPC_AUTH_TOKEN=... seshat-grpc

The server binds ``127.0.0.1:50051`` by default. See ``docs/transports.md`` of
the runtime for its environment variables.
"""

from __future__ import annotations

from . import aio, errors, events, types
from .client import Client, Memory, SearchResult, connect, default_target
from .errors import (
    Aborted,
    AlreadyExists,
    Cancelled,
    DeadlineExceeded,
    FailedPrecondition,
    Internal,
    InvalidArgument,
    NotFound,
    PermissionDenied,
    ResourceExhausted,
    SeshatError,
    Unauthenticated,
    Unavailable,
)
from .events import (
    Chunk,
    PermissionRequest,
    PromptRequest,
    SessionEvent,
    ToolProgress,
)
from .session import PromptAnswer, Session
from .types import (
    ContentBlock,
    Image,
    MCPServer,
    Message,
    ModelInfo,
    ProviderInfo,
    ServerInfo,
    SessionInfo,
    SessionSnapshot,
    SessionState,
    Skill,
    TokenUsage,
    ToolInfo,
    TurnResult,
)

__version__ = "0.1.0"

__all__ = [
    "__version__",
    # connection
    "Client",
    "Memory",
    "SearchResult",
    "connect",
    "default_target",
    # conversation
    "Session",
    "PromptAnswer",
    # events
    "SessionEvent",
    "Chunk",
    "ToolProgress",
    "PermissionRequest",
    "PromptRequest",
    # values
    "Message",
    "ContentBlock",
    "TurnResult",
    "TokenUsage",
    "SessionInfo",
    "SessionSnapshot",
    "SessionState",
    "ToolInfo",
    "Skill",
    "MCPServer",
    "ModelInfo",
    "ProviderInfo",
    "ServerInfo",
    "Image",
    # modules
    "aio",
    "errors",
    "events",
    "types",
    # errors
    "SeshatError",
    "InvalidArgument",
    "NotFound",
    "PermissionDenied",
    "Unauthenticated",
    "FailedPrecondition",
    "AlreadyExists",
    "Aborted",
    "ResourceExhausted",
    "Unavailable",
    "DeadlineExceeded",
    "Cancelled",
    "Internal",
]
