import datetime

from google.protobuf import struct_pb2 as _struct_pb2
from google.protobuf import timestamp_pb2 as _timestamp_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class Ack(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class TokenUsage(_message.Message):
    __slots__ = ("input_tokens", "output_tokens", "total_tokens", "cached_tokens", "cache_read_input_tokens", "cache_creation_input_tokens")
    INPUT_TOKENS_FIELD_NUMBER: _ClassVar[int]
    OUTPUT_TOKENS_FIELD_NUMBER: _ClassVar[int]
    TOTAL_TOKENS_FIELD_NUMBER: _ClassVar[int]
    CACHED_TOKENS_FIELD_NUMBER: _ClassVar[int]
    CACHE_READ_INPUT_TOKENS_FIELD_NUMBER: _ClassVar[int]
    CACHE_CREATION_INPUT_TOKENS_FIELD_NUMBER: _ClassVar[int]
    input_tokens: int
    output_tokens: int
    total_tokens: int
    cached_tokens: int
    cache_read_input_tokens: int
    cache_creation_input_tokens: int
    def __init__(self, input_tokens: _Optional[int] = ..., output_tokens: _Optional[int] = ..., total_tokens: _Optional[int] = ..., cached_tokens: _Optional[int] = ..., cache_read_input_tokens: _Optional[int] = ..., cache_creation_input_tokens: _Optional[int] = ...) -> None: ...

class TextBlock(_message.Message):
    __slots__ = ("text",)
    TEXT_FIELD_NUMBER: _ClassVar[int]
    text: str
    def __init__(self, text: _Optional[str] = ...) -> None: ...

class ThinkingBlock(_message.Message):
    __slots__ = ("thinking",)
    THINKING_FIELD_NUMBER: _ClassVar[int]
    thinking: str
    def __init__(self, thinking: _Optional[str] = ...) -> None: ...

class ImageBlock(_message.Message):
    __slots__ = ("source_type", "media_type", "data")
    SOURCE_TYPE_FIELD_NUMBER: _ClassVar[int]
    MEDIA_TYPE_FIELD_NUMBER: _ClassVar[int]
    DATA_FIELD_NUMBER: _ClassVar[int]
    source_type: str
    media_type: str
    data: str
    def __init__(self, source_type: _Optional[str] = ..., media_type: _Optional[str] = ..., data: _Optional[str] = ...) -> None: ...

class ToolUse(_message.Message):
    __slots__ = ("id", "name", "input")
    ID_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    INPUT_FIELD_NUMBER: _ClassVar[int]
    id: str
    name: str
    input: _struct_pb2.Struct
    def __init__(self, id: _Optional[str] = ..., name: _Optional[str] = ..., input: _Optional[_Union[_struct_pb2.Struct, _Mapping]] = ...) -> None: ...

class ToolResult(_message.Message):
    __slots__ = ("tool_use_id", "content", "is_error")
    TOOL_USE_ID_FIELD_NUMBER: _ClassVar[int]
    CONTENT_FIELD_NUMBER: _ClassVar[int]
    IS_ERROR_FIELD_NUMBER: _ClassVar[int]
    tool_use_id: str
    content: str
    is_error: bool
    def __init__(self, tool_use_id: _Optional[str] = ..., content: _Optional[str] = ..., is_error: _Optional[bool] = ...) -> None: ...

class ContentBlock(_message.Message):
    __slots__ = ("type", "text", "thinking", "tool_use", "tool_result", "image")
    TYPE_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    THINKING_FIELD_NUMBER: _ClassVar[int]
    TOOL_USE_FIELD_NUMBER: _ClassVar[int]
    TOOL_RESULT_FIELD_NUMBER: _ClassVar[int]
    IMAGE_FIELD_NUMBER: _ClassVar[int]
    type: str
    text: TextBlock
    thinking: ThinkingBlock
    tool_use: ToolUse
    tool_result: ToolResult
    image: ImageBlock
    def __init__(self, type: _Optional[str] = ..., text: _Optional[_Union[TextBlock, _Mapping]] = ..., thinking: _Optional[_Union[ThinkingBlock, _Mapping]] = ..., tool_use: _Optional[_Union[ToolUse, _Mapping]] = ..., tool_result: _Optional[_Union[ToolResult, _Mapping]] = ..., image: _Optional[_Union[ImageBlock, _Mapping]] = ...) -> None: ...

class Message(_message.Message):
    __slots__ = ("id", "role", "content", "timestamp", "turn_id")
    ID_FIELD_NUMBER: _ClassVar[int]
    ROLE_FIELD_NUMBER: _ClassVar[int]
    CONTENT_FIELD_NUMBER: _ClassVar[int]
    TIMESTAMP_FIELD_NUMBER: _ClassVar[int]
    TURN_ID_FIELD_NUMBER: _ClassVar[int]
    id: str
    role: str
    content: _containers.RepeatedCompositeFieldContainer[ContentBlock]
    timestamp: _timestamp_pb2.Timestamp
    turn_id: str
    def __init__(self, id: _Optional[str] = ..., role: _Optional[str] = ..., content: _Optional[_Iterable[_Union[ContentBlock, _Mapping]]] = ..., timestamp: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., turn_id: _Optional[str] = ...) -> None: ...

class ChunkDelta(_message.Message):
    __slots__ = ("type", "delta_type", "delta", "index", "partial_json", "content_block", "stop_reason", "usage")
    TYPE_FIELD_NUMBER: _ClassVar[int]
    DELTA_TYPE_FIELD_NUMBER: _ClassVar[int]
    DELTA_FIELD_NUMBER: _ClassVar[int]
    INDEX_FIELD_NUMBER: _ClassVar[int]
    PARTIAL_JSON_FIELD_NUMBER: _ClassVar[int]
    CONTENT_BLOCK_FIELD_NUMBER: _ClassVar[int]
    STOP_REASON_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    type: str
    delta_type: str
    delta: str
    index: int
    partial_json: str
    content_block: ContentBlock
    stop_reason: str
    usage: TokenUsage
    def __init__(self, type: _Optional[str] = ..., delta_type: _Optional[str] = ..., delta: _Optional[str] = ..., index: _Optional[int] = ..., partial_json: _Optional[str] = ..., content_block: _Optional[_Union[ContentBlock, _Mapping]] = ..., stop_reason: _Optional[str] = ..., usage: _Optional[_Union[TokenUsage, _Mapping]] = ...) -> None: ...

class ToolProgress(_message.Message):
    __slots__ = ("tool_name", "tool_use_id", "stage", "message", "percent_complete", "metadata")
    TOOL_NAME_FIELD_NUMBER: _ClassVar[int]
    TOOL_USE_ID_FIELD_NUMBER: _ClassVar[int]
    STAGE_FIELD_NUMBER: _ClassVar[int]
    MESSAGE_FIELD_NUMBER: _ClassVar[int]
    PERCENT_COMPLETE_FIELD_NUMBER: _ClassVar[int]
    METADATA_FIELD_NUMBER: _ClassVar[int]
    tool_name: str
    tool_use_id: str
    stage: str
    message: str
    percent_complete: float
    metadata: _struct_pb2.Struct
    def __init__(self, tool_name: _Optional[str] = ..., tool_use_id: _Optional[str] = ..., stage: _Optional[str] = ..., message: _Optional[str] = ..., percent_complete: _Optional[float] = ..., metadata: _Optional[_Union[_struct_pb2.Struct, _Mapping]] = ...) -> None: ...

class SessionInfo(_message.Message):
    __slots__ = ("id", "title", "status", "created_at", "updated_at", "total_turns", "total_tokens", "preview", "subagent", "parent_id")
    ID_FIELD_NUMBER: _ClassVar[int]
    TITLE_FIELD_NUMBER: _ClassVar[int]
    STATUS_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_FIELD_NUMBER: _ClassVar[int]
    UPDATED_AT_FIELD_NUMBER: _ClassVar[int]
    TOTAL_TURNS_FIELD_NUMBER: _ClassVar[int]
    TOTAL_TOKENS_FIELD_NUMBER: _ClassVar[int]
    PREVIEW_FIELD_NUMBER: _ClassVar[int]
    SUBAGENT_FIELD_NUMBER: _ClassVar[int]
    PARENT_ID_FIELD_NUMBER: _ClassVar[int]
    id: str
    title: str
    status: str
    created_at: _timestamp_pb2.Timestamp
    updated_at: _timestamp_pb2.Timestamp
    total_turns: int
    total_tokens: int
    preview: str
    subagent: bool
    parent_id: str
    def __init__(self, id: _Optional[str] = ..., title: _Optional[str] = ..., status: _Optional[str] = ..., created_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., updated_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., total_turns: _Optional[int] = ..., total_tokens: _Optional[int] = ..., preview: _Optional[str] = ..., subagent: _Optional[bool] = ..., parent_id: _Optional[str] = ...) -> None: ...

class Session(_message.Message):
    __slots__ = ("id", "title", "status", "created_at", "updated_at", "model", "working_dir", "turn_number", "total_tokens", "permission_mode", "execution_mode", "tools", "auto_mode_available", "info")
    ID_FIELD_NUMBER: _ClassVar[int]
    TITLE_FIELD_NUMBER: _ClassVar[int]
    STATUS_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_FIELD_NUMBER: _ClassVar[int]
    UPDATED_AT_FIELD_NUMBER: _ClassVar[int]
    MODEL_FIELD_NUMBER: _ClassVar[int]
    WORKING_DIR_FIELD_NUMBER: _ClassVar[int]
    TURN_NUMBER_FIELD_NUMBER: _ClassVar[int]
    TOTAL_TOKENS_FIELD_NUMBER: _ClassVar[int]
    PERMISSION_MODE_FIELD_NUMBER: _ClassVar[int]
    EXECUTION_MODE_FIELD_NUMBER: _ClassVar[int]
    TOOLS_FIELD_NUMBER: _ClassVar[int]
    AUTO_MODE_AVAILABLE_FIELD_NUMBER: _ClassVar[int]
    INFO_FIELD_NUMBER: _ClassVar[int]
    id: str
    title: str
    status: str
    created_at: _timestamp_pb2.Timestamp
    updated_at: _timestamp_pb2.Timestamp
    model: str
    working_dir: str
    turn_number: int
    total_tokens: int
    permission_mode: str
    execution_mode: str
    tools: _containers.RepeatedScalarFieldContainer[str]
    auto_mode_available: bool
    info: SessionInfo
    def __init__(self, id: _Optional[str] = ..., title: _Optional[str] = ..., status: _Optional[str] = ..., created_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., updated_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., model: _Optional[str] = ..., working_dir: _Optional[str] = ..., turn_number: _Optional[int] = ..., total_tokens: _Optional[int] = ..., permission_mode: _Optional[str] = ..., execution_mode: _Optional[str] = ..., tools: _Optional[_Iterable[str]] = ..., auto_mode_available: _Optional[bool] = ..., info: _Optional[_Union[SessionInfo, _Mapping]] = ...) -> None: ...

class SessionMetadata(_message.Message):
    __slots__ = ("id", "title", "status", "created_at", "updated_at", "root_path", "model", "total_turns", "total_tokens", "max_tokens", "compact_count", "last_compacted_at")
    ID_FIELD_NUMBER: _ClassVar[int]
    TITLE_FIELD_NUMBER: _ClassVar[int]
    STATUS_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_FIELD_NUMBER: _ClassVar[int]
    UPDATED_AT_FIELD_NUMBER: _ClassVar[int]
    ROOT_PATH_FIELD_NUMBER: _ClassVar[int]
    MODEL_FIELD_NUMBER: _ClassVar[int]
    TOTAL_TURNS_FIELD_NUMBER: _ClassVar[int]
    TOTAL_TOKENS_FIELD_NUMBER: _ClassVar[int]
    MAX_TOKENS_FIELD_NUMBER: _ClassVar[int]
    COMPACT_COUNT_FIELD_NUMBER: _ClassVar[int]
    LAST_COMPACTED_AT_FIELD_NUMBER: _ClassVar[int]
    id: str
    title: str
    status: str
    created_at: _timestamp_pb2.Timestamp
    updated_at: _timestamp_pb2.Timestamp
    root_path: str
    model: str
    total_turns: int
    total_tokens: int
    max_tokens: int
    compact_count: int
    last_compacted_at: _timestamp_pb2.Timestamp
    def __init__(self, id: _Optional[str] = ..., title: _Optional[str] = ..., status: _Optional[str] = ..., created_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., updated_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., root_path: _Optional[str] = ..., model: _Optional[str] = ..., total_turns: _Optional[int] = ..., total_tokens: _Optional[int] = ..., max_tokens: _Optional[int] = ..., compact_count: _Optional[int] = ..., last_compacted_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ...) -> None: ...

class SessionState(_message.Message):
    __slots__ = ("metadata", "permission_mode", "execution_mode", "turn_number", "total_tokens", "tools", "auto_mode_available")
    METADATA_FIELD_NUMBER: _ClassVar[int]
    PERMISSION_MODE_FIELD_NUMBER: _ClassVar[int]
    EXECUTION_MODE_FIELD_NUMBER: _ClassVar[int]
    TURN_NUMBER_FIELD_NUMBER: _ClassVar[int]
    TOTAL_TOKENS_FIELD_NUMBER: _ClassVar[int]
    TOOLS_FIELD_NUMBER: _ClassVar[int]
    AUTO_MODE_AVAILABLE_FIELD_NUMBER: _ClassVar[int]
    metadata: SessionMetadata
    permission_mode: str
    execution_mode: str
    turn_number: int
    total_tokens: int
    tools: _containers.RepeatedScalarFieldContainer[str]
    auto_mode_available: bool
    def __init__(self, metadata: _Optional[_Union[SessionMetadata, _Mapping]] = ..., permission_mode: _Optional[str] = ..., execution_mode: _Optional[str] = ..., turn_number: _Optional[int] = ..., total_tokens: _Optional[int] = ..., tools: _Optional[_Iterable[str]] = ..., auto_mode_available: _Optional[bool] = ...) -> None: ...

class TurnResult(_message.Message):
    __slots__ = ("session_id", "content", "messages", "usage", "stop_reason", "turn_number", "is_complete", "compacted", "compact_pre_tokens", "compact_post_tokens", "tool_uses", "tool_results", "total_tokens", "thinking")
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    CONTENT_FIELD_NUMBER: _ClassVar[int]
    MESSAGES_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    STOP_REASON_FIELD_NUMBER: _ClassVar[int]
    TURN_NUMBER_FIELD_NUMBER: _ClassVar[int]
    IS_COMPLETE_FIELD_NUMBER: _ClassVar[int]
    COMPACTED_FIELD_NUMBER: _ClassVar[int]
    COMPACT_PRE_TOKENS_FIELD_NUMBER: _ClassVar[int]
    COMPACT_POST_TOKENS_FIELD_NUMBER: _ClassVar[int]
    TOOL_USES_FIELD_NUMBER: _ClassVar[int]
    TOOL_RESULTS_FIELD_NUMBER: _ClassVar[int]
    TOTAL_TOKENS_FIELD_NUMBER: _ClassVar[int]
    THINKING_FIELD_NUMBER: _ClassVar[int]
    session_id: str
    content: str
    messages: _containers.RepeatedCompositeFieldContainer[Message]
    usage: TokenUsage
    stop_reason: str
    turn_number: int
    is_complete: bool
    compacted: bool
    compact_pre_tokens: int
    compact_post_tokens: int
    tool_uses: _containers.RepeatedCompositeFieldContainer[ToolUse]
    tool_results: _containers.RepeatedCompositeFieldContainer[ToolResult]
    total_tokens: int
    thinking: str
    def __init__(self, session_id: _Optional[str] = ..., content: _Optional[str] = ..., messages: _Optional[_Iterable[_Union[Message, _Mapping]]] = ..., usage: _Optional[_Union[TokenUsage, _Mapping]] = ..., stop_reason: _Optional[str] = ..., turn_number: _Optional[int] = ..., is_complete: _Optional[bool] = ..., compacted: _Optional[bool] = ..., compact_pre_tokens: _Optional[int] = ..., compact_post_tokens: _Optional[int] = ..., tool_uses: _Optional[_Iterable[_Union[ToolUse, _Mapping]]] = ..., tool_results: _Optional[_Iterable[_Union[ToolResult, _Mapping]]] = ..., total_tokens: _Optional[int] = ..., thinking: _Optional[str] = ...) -> None: ...

class PermissionRequest(_message.Message):
    __slots__ = ("request_id", "session_id", "turn_id", "tool_name", "description", "tool_input", "tool_use_id", "permission_mode", "working_directory", "stage", "expires_at", "agent_tool_use_id")
    REQUEST_ID_FIELD_NUMBER: _ClassVar[int]
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    TURN_ID_FIELD_NUMBER: _ClassVar[int]
    TOOL_NAME_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    TOOL_INPUT_FIELD_NUMBER: _ClassVar[int]
    TOOL_USE_ID_FIELD_NUMBER: _ClassVar[int]
    PERMISSION_MODE_FIELD_NUMBER: _ClassVar[int]
    WORKING_DIRECTORY_FIELD_NUMBER: _ClassVar[int]
    STAGE_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_AT_FIELD_NUMBER: _ClassVar[int]
    AGENT_TOOL_USE_ID_FIELD_NUMBER: _ClassVar[int]
    request_id: str
    session_id: str
    turn_id: str
    tool_name: str
    description: str
    tool_input: _struct_pb2.Struct
    tool_use_id: str
    permission_mode: str
    working_directory: str
    stage: str
    expires_at: _timestamp_pb2.Timestamp
    agent_tool_use_id: str
    def __init__(self, request_id: _Optional[str] = ..., session_id: _Optional[str] = ..., turn_id: _Optional[str] = ..., tool_name: _Optional[str] = ..., description: _Optional[str] = ..., tool_input: _Optional[_Union[_struct_pb2.Struct, _Mapping]] = ..., tool_use_id: _Optional[str] = ..., permission_mode: _Optional[str] = ..., working_directory: _Optional[str] = ..., stage: _Optional[str] = ..., expires_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., agent_tool_use_id: _Optional[str] = ...) -> None: ...

class PromptOption(_message.Message):
    __slots__ = ("label", "value", "description")
    LABEL_FIELD_NUMBER: _ClassVar[int]
    VALUE_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    label: str
    value: _struct_pb2.Value
    description: str
    def __init__(self, label: _Optional[str] = ..., value: _Optional[_Union[_struct_pb2.Value, _Mapping]] = ..., description: _Optional[str] = ...) -> None: ...

class PromptRequest(_message.Message):
    __slots__ = ("request_id", "session_id", "turn_id", "type", "message", "options", "default_value", "metadata", "expires_at", "agent_tool_use_id")
    REQUEST_ID_FIELD_NUMBER: _ClassVar[int]
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    TURN_ID_FIELD_NUMBER: _ClassVar[int]
    TYPE_FIELD_NUMBER: _ClassVar[int]
    MESSAGE_FIELD_NUMBER: _ClassVar[int]
    OPTIONS_FIELD_NUMBER: _ClassVar[int]
    DEFAULT_VALUE_FIELD_NUMBER: _ClassVar[int]
    METADATA_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_AT_FIELD_NUMBER: _ClassVar[int]
    AGENT_TOOL_USE_ID_FIELD_NUMBER: _ClassVar[int]
    request_id: str
    session_id: str
    turn_id: str
    type: str
    message: str
    options: _containers.RepeatedCompositeFieldContainer[PromptOption]
    default_value: _struct_pb2.Value
    metadata: _struct_pb2.Struct
    expires_at: _timestamp_pb2.Timestamp
    agent_tool_use_id: str
    def __init__(self, request_id: _Optional[str] = ..., session_id: _Optional[str] = ..., turn_id: _Optional[str] = ..., type: _Optional[str] = ..., message: _Optional[str] = ..., options: _Optional[_Iterable[_Union[PromptOption, _Mapping]]] = ..., default_value: _Optional[_Union[_struct_pb2.Value, _Mapping]] = ..., metadata: _Optional[_Union[_struct_pb2.Struct, _Mapping]] = ..., expires_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., agent_tool_use_id: _Optional[str] = ...) -> None: ...

class TurnStarted(_message.Message):
    __slots__ = ("turn_number", "turn_id", "model", "execution_mode")
    TURN_NUMBER_FIELD_NUMBER: _ClassVar[int]
    TURN_ID_FIELD_NUMBER: _ClassVar[int]
    MODEL_FIELD_NUMBER: _ClassVar[int]
    EXECUTION_MODE_FIELD_NUMBER: _ClassVar[int]
    turn_number: int
    turn_id: str
    model: str
    execution_mode: str
    def __init__(self, turn_number: _Optional[int] = ..., turn_id: _Optional[str] = ..., model: _Optional[str] = ..., execution_mode: _Optional[str] = ...) -> None: ...

class TurnCompleted(_message.Message):
    __slots__ = ("result",)
    RESULT_FIELD_NUMBER: _ClassVar[int]
    result: TurnResult
    def __init__(self, result: _Optional[_Union[TurnResult, _Mapping]] = ...) -> None: ...

class TurnFailed(_message.Message):
    __slots__ = ("code", "message")
    CODE_FIELD_NUMBER: _ClassVar[int]
    MESSAGE_FIELD_NUMBER: _ClassVar[int]
    code: str
    message: str
    def __init__(self, code: _Optional[str] = ..., message: _Optional[str] = ...) -> None: ...

class PlanEvent(_message.Message):
    __slots__ = ("plan_id", "slug", "filename", "status", "version", "content")
    PLAN_ID_FIELD_NUMBER: _ClassVar[int]
    SLUG_FIELD_NUMBER: _ClassVar[int]
    FILENAME_FIELD_NUMBER: _ClassVar[int]
    STATUS_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    CONTENT_FIELD_NUMBER: _ClassVar[int]
    plan_id: str
    slug: str
    filename: str
    status: str
    version: int
    content: str
    def __init__(self, plan_id: _Optional[str] = ..., slug: _Optional[str] = ..., filename: _Optional[str] = ..., status: _Optional[str] = ..., version: _Optional[int] = ..., content: _Optional[str] = ...) -> None: ...

class TaskEvent(_message.Message):
    __slots__ = ("task_id", "action", "status", "subject")
    TASK_ID_FIELD_NUMBER: _ClassVar[int]
    ACTION_FIELD_NUMBER: _ClassVar[int]
    STATUS_FIELD_NUMBER: _ClassVar[int]
    SUBJECT_FIELD_NUMBER: _ClassVar[int]
    task_id: str
    action: str
    status: str
    subject: str
    def __init__(self, task_id: _Optional[str] = ..., action: _Optional[str] = ..., status: _Optional[str] = ..., subject: _Optional[str] = ...) -> None: ...

class StageEvent(_message.Message):
    __slots__ = ("stage", "label")
    STAGE_FIELD_NUMBER: _ClassVar[int]
    LABEL_FIELD_NUMBER: _ClassVar[int]
    stage: str
    label: str
    def __init__(self, stage: _Optional[str] = ..., label: _Optional[str] = ...) -> None: ...

class GoalEvent(_message.Message):
    __slots__ = ("session_id", "objective", "status", "token_budget", "tokens_used", "time_used_seconds", "created_at_ms", "updated_at_ms")
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    OBJECTIVE_FIELD_NUMBER: _ClassVar[int]
    STATUS_FIELD_NUMBER: _ClassVar[int]
    TOKEN_BUDGET_FIELD_NUMBER: _ClassVar[int]
    TOKENS_USED_FIELD_NUMBER: _ClassVar[int]
    TIME_USED_SECONDS_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_MS_FIELD_NUMBER: _ClassVar[int]
    UPDATED_AT_MS_FIELD_NUMBER: _ClassVar[int]
    session_id: str
    objective: str
    status: str
    token_budget: int
    tokens_used: int
    time_used_seconds: int
    created_at_ms: int
    updated_at_ms: int
    def __init__(self, session_id: _Optional[str] = ..., objective: _Optional[str] = ..., status: _Optional[str] = ..., token_budget: _Optional[int] = ..., tokens_used: _Optional[int] = ..., time_used_seconds: _Optional[int] = ..., created_at_ms: _Optional[int] = ..., updated_at_ms: _Optional[int] = ...) -> None: ...

class AgentEvent(_message.Message):
    __slots__ = ("event_type", "call_id", "agent_id", "agent_nickname", "agent_role", "prompt", "status", "message", "started_at_ms", "completed_at_ms")
    EVENT_TYPE_FIELD_NUMBER: _ClassVar[int]
    CALL_ID_FIELD_NUMBER: _ClassVar[int]
    AGENT_ID_FIELD_NUMBER: _ClassVar[int]
    AGENT_NICKNAME_FIELD_NUMBER: _ClassVar[int]
    AGENT_ROLE_FIELD_NUMBER: _ClassVar[int]
    PROMPT_FIELD_NUMBER: _ClassVar[int]
    STATUS_FIELD_NUMBER: _ClassVar[int]
    MESSAGE_FIELD_NUMBER: _ClassVar[int]
    STARTED_AT_MS_FIELD_NUMBER: _ClassVar[int]
    COMPLETED_AT_MS_FIELD_NUMBER: _ClassVar[int]
    event_type: str
    call_id: str
    agent_id: str
    agent_nickname: str
    agent_role: str
    prompt: str
    status: str
    message: str
    started_at_ms: int
    completed_at_ms: int
    def __init__(self, event_type: _Optional[str] = ..., call_id: _Optional[str] = ..., agent_id: _Optional[str] = ..., agent_nickname: _Optional[str] = ..., agent_role: _Optional[str] = ..., prompt: _Optional[str] = ..., status: _Optional[str] = ..., message: _Optional[str] = ..., started_at_ms: _Optional[int] = ..., completed_at_ms: _Optional[int] = ...) -> None: ...

class ProviderRetry(_message.Message):
    __slots__ = ("attempt", "max_attempts", "wait_ms", "reason")
    ATTEMPT_FIELD_NUMBER: _ClassVar[int]
    MAX_ATTEMPTS_FIELD_NUMBER: _ClassVar[int]
    WAIT_MS_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    attempt: int
    max_attempts: int
    wait_ms: int
    reason: str
    def __init__(self, attempt: _Optional[int] = ..., max_attempts: _Optional[int] = ..., wait_ms: _Optional[int] = ..., reason: _Optional[str] = ...) -> None: ...

class CompactionEvent(_message.Message):
    __slots__ = ("started", "pre_compact_tokens", "post_compact_tokens")
    STARTED_FIELD_NUMBER: _ClassVar[int]
    PRE_COMPACT_TOKENS_FIELD_NUMBER: _ClassVar[int]
    POST_COMPACT_TOKENS_FIELD_NUMBER: _ClassVar[int]
    started: bool
    pre_compact_tokens: int
    post_compact_tokens: int
    def __init__(self, started: _Optional[bool] = ..., pre_compact_tokens: _Optional[int] = ..., post_compact_tokens: _Optional[int] = ...) -> None: ...

class ContextUsage(_message.Message):
    __slots__ = ("used_tokens", "window_tokens", "auto_compact_at_tokens", "blocking_limit_tokens", "source")
    USED_TOKENS_FIELD_NUMBER: _ClassVar[int]
    WINDOW_TOKENS_FIELD_NUMBER: _ClassVar[int]
    AUTO_COMPACT_AT_TOKENS_FIELD_NUMBER: _ClassVar[int]
    BLOCKING_LIMIT_TOKENS_FIELD_NUMBER: _ClassVar[int]
    SOURCE_FIELD_NUMBER: _ClassVar[int]
    used_tokens: int
    window_tokens: int
    auto_compact_at_tokens: int
    blocking_limit_tokens: int
    source: str
    def __init__(self, used_tokens: _Optional[int] = ..., window_tokens: _Optional[int] = ..., auto_compact_at_tokens: _Optional[int] = ..., blocking_limit_tokens: _Optional[int] = ..., source: _Optional[str] = ...) -> None: ...

class BrowserEvent(_message.Message):
    __slots__ = ("action", "page_id", "url", "title", "active_page_id", "page_count", "action_count", "element_count", "heading_count", "text_length", "bytes", "persisted_path", "persisted_size", "full_page")
    ACTION_FIELD_NUMBER: _ClassVar[int]
    PAGE_ID_FIELD_NUMBER: _ClassVar[int]
    URL_FIELD_NUMBER: _ClassVar[int]
    TITLE_FIELD_NUMBER: _ClassVar[int]
    ACTIVE_PAGE_ID_FIELD_NUMBER: _ClassVar[int]
    PAGE_COUNT_FIELD_NUMBER: _ClassVar[int]
    ACTION_COUNT_FIELD_NUMBER: _ClassVar[int]
    ELEMENT_COUNT_FIELD_NUMBER: _ClassVar[int]
    HEADING_COUNT_FIELD_NUMBER: _ClassVar[int]
    TEXT_LENGTH_FIELD_NUMBER: _ClassVar[int]
    BYTES_FIELD_NUMBER: _ClassVar[int]
    PERSISTED_PATH_FIELD_NUMBER: _ClassVar[int]
    PERSISTED_SIZE_FIELD_NUMBER: _ClassVar[int]
    FULL_PAGE_FIELD_NUMBER: _ClassVar[int]
    action: str
    page_id: str
    url: str
    title: str
    active_page_id: str
    page_count: int
    action_count: int
    element_count: int
    heading_count: int
    text_length: int
    bytes: int
    persisted_path: str
    persisted_size: int
    full_page: bool
    def __init__(self, action: _Optional[str] = ..., page_id: _Optional[str] = ..., url: _Optional[str] = ..., title: _Optional[str] = ..., active_page_id: _Optional[str] = ..., page_count: _Optional[int] = ..., action_count: _Optional[int] = ..., element_count: _Optional[int] = ..., heading_count: _Optional[int] = ..., text_length: _Optional[int] = ..., bytes: _Optional[int] = ..., persisted_path: _Optional[str] = ..., persisted_size: _Optional[int] = ..., full_page: _Optional[bool] = ...) -> None: ...

class RawEvent(_message.Message):
    __slots__ = ("type", "data", "error")
    TYPE_FIELD_NUMBER: _ClassVar[int]
    DATA_FIELD_NUMBER: _ClassVar[int]
    ERROR_FIELD_NUMBER: _ClassVar[int]
    type: str
    data: _struct_pb2.Struct
    error: str
    def __init__(self, type: _Optional[str] = ..., data: _Optional[_Union[_struct_pb2.Struct, _Mapping]] = ..., error: _Optional[str] = ...) -> None: ...

class SessionEvent(_message.Message):
    __slots__ = ("event_id", "session_id", "sequence", "timestamp", "turn_id", "turn_number", "type", "agent_tool_use_id", "turn_started", "chunk", "tool_progress", "permission_request", "prompt_request", "plan", "task", "stage", "provider_retry", "compaction", "context_usage", "turn_completed", "turn_failed", "agent", "goal", "browser", "raw")
    EVENT_ID_FIELD_NUMBER: _ClassVar[int]
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    SEQUENCE_FIELD_NUMBER: _ClassVar[int]
    TIMESTAMP_FIELD_NUMBER: _ClassVar[int]
    TURN_ID_FIELD_NUMBER: _ClassVar[int]
    TURN_NUMBER_FIELD_NUMBER: _ClassVar[int]
    TYPE_FIELD_NUMBER: _ClassVar[int]
    AGENT_TOOL_USE_ID_FIELD_NUMBER: _ClassVar[int]
    TURN_STARTED_FIELD_NUMBER: _ClassVar[int]
    CHUNK_FIELD_NUMBER: _ClassVar[int]
    TOOL_PROGRESS_FIELD_NUMBER: _ClassVar[int]
    PERMISSION_REQUEST_FIELD_NUMBER: _ClassVar[int]
    PROMPT_REQUEST_FIELD_NUMBER: _ClassVar[int]
    PLAN_FIELD_NUMBER: _ClassVar[int]
    TASK_FIELD_NUMBER: _ClassVar[int]
    STAGE_FIELD_NUMBER: _ClassVar[int]
    PROVIDER_RETRY_FIELD_NUMBER: _ClassVar[int]
    COMPACTION_FIELD_NUMBER: _ClassVar[int]
    CONTEXT_USAGE_FIELD_NUMBER: _ClassVar[int]
    TURN_COMPLETED_FIELD_NUMBER: _ClassVar[int]
    TURN_FAILED_FIELD_NUMBER: _ClassVar[int]
    AGENT_FIELD_NUMBER: _ClassVar[int]
    GOAL_FIELD_NUMBER: _ClassVar[int]
    BROWSER_FIELD_NUMBER: _ClassVar[int]
    RAW_FIELD_NUMBER: _ClassVar[int]
    event_id: str
    session_id: str
    sequence: int
    timestamp: _timestamp_pb2.Timestamp
    turn_id: str
    turn_number: int
    type: str
    agent_tool_use_id: str
    turn_started: TurnStarted
    chunk: ChunkDelta
    tool_progress: ToolProgress
    permission_request: PermissionRequest
    prompt_request: PromptRequest
    plan: PlanEvent
    task: TaskEvent
    stage: StageEvent
    provider_retry: ProviderRetry
    compaction: CompactionEvent
    context_usage: ContextUsage
    turn_completed: TurnCompleted
    turn_failed: TurnFailed
    agent: AgentEvent
    goal: GoalEvent
    browser: BrowserEvent
    raw: RawEvent
    def __init__(self, event_id: _Optional[str] = ..., session_id: _Optional[str] = ..., sequence: _Optional[int] = ..., timestamp: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., turn_id: _Optional[str] = ..., turn_number: _Optional[int] = ..., type: _Optional[str] = ..., agent_tool_use_id: _Optional[str] = ..., turn_started: _Optional[_Union[TurnStarted, _Mapping]] = ..., chunk: _Optional[_Union[ChunkDelta, _Mapping]] = ..., tool_progress: _Optional[_Union[ToolProgress, _Mapping]] = ..., permission_request: _Optional[_Union[PermissionRequest, _Mapping]] = ..., prompt_request: _Optional[_Union[PromptRequest, _Mapping]] = ..., plan: _Optional[_Union[PlanEvent, _Mapping]] = ..., task: _Optional[_Union[TaskEvent, _Mapping]] = ..., stage: _Optional[_Union[StageEvent, _Mapping]] = ..., provider_retry: _Optional[_Union[ProviderRetry, _Mapping]] = ..., compaction: _Optional[_Union[CompactionEvent, _Mapping]] = ..., context_usage: _Optional[_Union[ContextUsage, _Mapping]] = ..., turn_completed: _Optional[_Union[TurnCompleted, _Mapping]] = ..., turn_failed: _Optional[_Union[TurnFailed, _Mapping]] = ..., agent: _Optional[_Union[AgentEvent, _Mapping]] = ..., goal: _Optional[_Union[GoalEvent, _Mapping]] = ..., browser: _Optional[_Union[BrowserEvent, _Mapping]] = ..., raw: _Optional[_Union[RawEvent, _Mapping]] = ...) -> None: ...

class HealthCheckRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class HealthCheckResponse(_message.Message):
    __slots__ = ("status", "version", "uptime", "uptime_seconds")
    STATUS_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    UPTIME_FIELD_NUMBER: _ClassVar[int]
    UPTIME_SECONDS_FIELD_NUMBER: _ClassVar[int]
    status: str
    version: str
    uptime: str
    uptime_seconds: int
    def __init__(self, status: _Optional[str] = ..., version: _Optional[str] = ..., uptime: _Optional[str] = ..., uptime_seconds: _Optional[int] = ...) -> None: ...

class GetServerInfoRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class GetServerInfoResponse(_message.Message):
    __slots__ = ("version", "uptime_seconds", "model", "working_dir", "permission_mode", "sandbox_kind", "providers_configured", "providers_available", "memory_enabled", "hooks_enabled", "monitoring_enabled", "tool_search_enabled", "session_persistence_enabled", "stdio_mcp_allowed", "auth_required", "tls_enabled", "active_sessions")
    VERSION_FIELD_NUMBER: _ClassVar[int]
    UPTIME_SECONDS_FIELD_NUMBER: _ClassVar[int]
    MODEL_FIELD_NUMBER: _ClassVar[int]
    WORKING_DIR_FIELD_NUMBER: _ClassVar[int]
    PERMISSION_MODE_FIELD_NUMBER: _ClassVar[int]
    SANDBOX_KIND_FIELD_NUMBER: _ClassVar[int]
    PROVIDERS_CONFIGURED_FIELD_NUMBER: _ClassVar[int]
    PROVIDERS_AVAILABLE_FIELD_NUMBER: _ClassVar[int]
    MEMORY_ENABLED_FIELD_NUMBER: _ClassVar[int]
    HOOKS_ENABLED_FIELD_NUMBER: _ClassVar[int]
    MONITORING_ENABLED_FIELD_NUMBER: _ClassVar[int]
    TOOL_SEARCH_ENABLED_FIELD_NUMBER: _ClassVar[int]
    SESSION_PERSISTENCE_ENABLED_FIELD_NUMBER: _ClassVar[int]
    STDIO_MCP_ALLOWED_FIELD_NUMBER: _ClassVar[int]
    AUTH_REQUIRED_FIELD_NUMBER: _ClassVar[int]
    TLS_ENABLED_FIELD_NUMBER: _ClassVar[int]
    ACTIVE_SESSIONS_FIELD_NUMBER: _ClassVar[int]
    version: str
    uptime_seconds: int
    model: str
    working_dir: str
    permission_mode: str
    sandbox_kind: str
    providers_configured: _containers.RepeatedScalarFieldContainer[str]
    providers_available: _containers.RepeatedScalarFieldContainer[str]
    memory_enabled: bool
    hooks_enabled: bool
    monitoring_enabled: bool
    tool_search_enabled: bool
    session_persistence_enabled: bool
    stdio_mcp_allowed: bool
    auth_required: bool
    tls_enabled: bool
    active_sessions: int
    def __init__(self, version: _Optional[str] = ..., uptime_seconds: _Optional[int] = ..., model: _Optional[str] = ..., working_dir: _Optional[str] = ..., permission_mode: _Optional[str] = ..., sandbox_kind: _Optional[str] = ..., providers_configured: _Optional[_Iterable[str]] = ..., providers_available: _Optional[_Iterable[str]] = ..., memory_enabled: _Optional[bool] = ..., hooks_enabled: _Optional[bool] = ..., monitoring_enabled: _Optional[bool] = ..., tool_search_enabled: _Optional[bool] = ..., session_persistence_enabled: _Optional[bool] = ..., stdio_mcp_allowed: _Optional[bool] = ..., auth_required: _Optional[bool] = ..., tls_enabled: _Optional[bool] = ..., active_sessions: _Optional[int] = ...) -> None: ...

class GetModelsRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class GetModelsResponse(_message.Message):
    __slots__ = ("models", "providers")
    MODELS_FIELD_NUMBER: _ClassVar[int]
    PROVIDERS_FIELD_NUMBER: _ClassVar[int]
    models: _containers.RepeatedCompositeFieldContainer[ModelInfo]
    providers: _containers.RepeatedCompositeFieldContainer[ProviderInfo]
    def __init__(self, models: _Optional[_Iterable[_Union[ModelInfo, _Mapping]]] = ..., providers: _Optional[_Iterable[_Union[ProviderInfo, _Mapping]]] = ...) -> None: ...

class ModelInfo(_message.Message):
    __slots__ = ("id", "name", "provider", "max_tokens", "context_window")
    ID_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    PROVIDER_FIELD_NUMBER: _ClassVar[int]
    MAX_TOKENS_FIELD_NUMBER: _ClassVar[int]
    CONTEXT_WINDOW_FIELD_NUMBER: _ClassVar[int]
    id: str
    name: str
    provider: str
    max_tokens: int
    context_window: int
    def __init__(self, id: _Optional[str] = ..., name: _Optional[str] = ..., provider: _Optional[str] = ..., max_tokens: _Optional[int] = ..., context_window: _Optional[int] = ...) -> None: ...

class ProviderInfo(_message.Message):
    __slots__ = ("name", "configured", "model_count")
    NAME_FIELD_NUMBER: _ClassVar[int]
    CONFIGURED_FIELD_NUMBER: _ClassVar[int]
    MODEL_COUNT_FIELD_NUMBER: _ClassVar[int]
    name: str
    configured: bool
    model_count: int
    def __init__(self, name: _Optional[str] = ..., configured: _Optional[bool] = ..., model_count: _Optional[int] = ...) -> None: ...

class ListSkillsRequest(_message.Message):
    __slots__ = ("source_filter", "include_content")
    SOURCE_FILTER_FIELD_NUMBER: _ClassVar[int]
    INCLUDE_CONTENT_FIELD_NUMBER: _ClassVar[int]
    source_filter: str
    include_content: bool
    def __init__(self, source_filter: _Optional[str] = ..., include_content: _Optional[bool] = ...) -> None: ...

class ListSkillsResponse(_message.Message):
    __slots__ = ("skills", "total_count")
    SKILLS_FIELD_NUMBER: _ClassVar[int]
    TOTAL_COUNT_FIELD_NUMBER: _ClassVar[int]
    skills: _containers.RepeatedCompositeFieldContainer[Skill]
    total_count: int
    def __init__(self, skills: _Optional[_Iterable[_Union[Skill, _Mapping]]] = ..., total_count: _Optional[int] = ...) -> None: ...

class Skill(_message.Message):
    __slots__ = ("name", "description", "source", "when_to_use", "allowed_tools", "content", "path")
    NAME_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    SOURCE_FIELD_NUMBER: _ClassVar[int]
    WHEN_TO_USE_FIELD_NUMBER: _ClassVar[int]
    ALLOWED_TOOLS_FIELD_NUMBER: _ClassVar[int]
    CONTENT_FIELD_NUMBER: _ClassVar[int]
    PATH_FIELD_NUMBER: _ClassVar[int]
    name: str
    description: str
    source: str
    when_to_use: str
    allowed_tools: _containers.RepeatedScalarFieldContainer[str]
    content: str
    path: str
    def __init__(self, name: _Optional[str] = ..., description: _Optional[str] = ..., source: _Optional[str] = ..., when_to_use: _Optional[str] = ..., allowed_tools: _Optional[_Iterable[str]] = ..., content: _Optional[str] = ..., path: _Optional[str] = ...) -> None: ...

class GetSkillDetailsRequest(_message.Message):
    __slots__ = ("name", "include_content")
    NAME_FIELD_NUMBER: _ClassVar[int]
    INCLUDE_CONTENT_FIELD_NUMBER: _ClassVar[int]
    name: str
    include_content: bool
    def __init__(self, name: _Optional[str] = ..., include_content: _Optional[bool] = ...) -> None: ...

class GetSkillDetailsResponse(_message.Message):
    __slots__ = ("skill", "prompt")
    SKILL_FIELD_NUMBER: _ClassVar[int]
    PROMPT_FIELD_NUMBER: _ClassVar[int]
    skill: Skill
    prompt: str
    def __init__(self, skill: _Optional[_Union[Skill, _Mapping]] = ..., prompt: _Optional[str] = ...) -> None: ...

class ListMCPRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class ListMCPResponse(_message.Message):
    __slots__ = ("servers", "total_count")
    SERVERS_FIELD_NUMBER: _ClassVar[int]
    TOTAL_COUNT_FIELD_NUMBER: _ClassVar[int]
    servers: _containers.RepeatedCompositeFieldContainer[MCPServer]
    total_count: int
    def __init__(self, servers: _Optional[_Iterable[_Union[MCPServer, _Mapping]]] = ..., total_count: _Optional[int] = ...) -> None: ...

class MCPServer(_message.Message):
    __slots__ = ("name", "status", "tool_count", "last_error", "type", "url", "enabled")
    NAME_FIELD_NUMBER: _ClassVar[int]
    STATUS_FIELD_NUMBER: _ClassVar[int]
    TOOL_COUNT_FIELD_NUMBER: _ClassVar[int]
    LAST_ERROR_FIELD_NUMBER: _ClassVar[int]
    TYPE_FIELD_NUMBER: _ClassVar[int]
    URL_FIELD_NUMBER: _ClassVar[int]
    ENABLED_FIELD_NUMBER: _ClassVar[int]
    name: str
    status: str
    tool_count: int
    last_error: str
    type: str
    url: str
    enabled: bool
    def __init__(self, name: _Optional[str] = ..., status: _Optional[str] = ..., tool_count: _Optional[int] = ..., last_error: _Optional[str] = ..., type: _Optional[str] = ..., url: _Optional[str] = ..., enabled: _Optional[bool] = ...) -> None: ...

class ConnectMCPRequest(_message.Message):
    __slots__ = ("name", "type", "command", "args", "url", "env", "timeout", "persist")
    class EnvEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: str
        def __init__(self, key: _Optional[str] = ..., value: _Optional[str] = ...) -> None: ...
    NAME_FIELD_NUMBER: _ClassVar[int]
    TYPE_FIELD_NUMBER: _ClassVar[int]
    COMMAND_FIELD_NUMBER: _ClassVar[int]
    ARGS_FIELD_NUMBER: _ClassVar[int]
    URL_FIELD_NUMBER: _ClassVar[int]
    ENV_FIELD_NUMBER: _ClassVar[int]
    TIMEOUT_FIELD_NUMBER: _ClassVar[int]
    PERSIST_FIELD_NUMBER: _ClassVar[int]
    name: str
    type: str
    command: str
    args: _containers.RepeatedScalarFieldContainer[str]
    url: str
    env: _containers.ScalarMap[str, str]
    timeout: int
    persist: bool
    def __init__(self, name: _Optional[str] = ..., type: _Optional[str] = ..., command: _Optional[str] = ..., args: _Optional[_Iterable[str]] = ..., url: _Optional[str] = ..., env: _Optional[_Mapping[str, str]] = ..., timeout: _Optional[int] = ..., persist: _Optional[bool] = ...) -> None: ...

class ConnectMCPResponse(_message.Message):
    __slots__ = ("success", "error", "server")
    SUCCESS_FIELD_NUMBER: _ClassVar[int]
    ERROR_FIELD_NUMBER: _ClassVar[int]
    SERVER_FIELD_NUMBER: _ClassVar[int]
    success: bool
    error: str
    server: MCPServer
    def __init__(self, success: _Optional[bool] = ..., error: _Optional[str] = ..., server: _Optional[_Union[MCPServer, _Mapping]] = ...) -> None: ...

class DisconnectMCPRequest(_message.Message):
    __slots__ = ("name",)
    NAME_FIELD_NUMBER: _ClassVar[int]
    name: str
    def __init__(self, name: _Optional[str] = ...) -> None: ...

class DisconnectMCPResponse(_message.Message):
    __slots__ = ("success", "error")
    SUCCESS_FIELD_NUMBER: _ClassVar[int]
    ERROR_FIELD_NUMBER: _ClassVar[int]
    success: bool
    error: str
    def __init__(self, success: _Optional[bool] = ..., error: _Optional[str] = ...) -> None: ...

class ReconnectMCPRequest(_message.Message):
    __slots__ = ("name",)
    NAME_FIELD_NUMBER: _ClassVar[int]
    name: str
    def __init__(self, name: _Optional[str] = ...) -> None: ...

class ReconnectMCPResponse(_message.Message):
    __slots__ = ("success", "error", "server")
    SUCCESS_FIELD_NUMBER: _ClassVar[int]
    ERROR_FIELD_NUMBER: _ClassVar[int]
    SERVER_FIELD_NUMBER: _ClassVar[int]
    success: bool
    error: str
    server: MCPServer
    def __init__(self, success: _Optional[bool] = ..., error: _Optional[str] = ..., server: _Optional[_Union[MCPServer, _Mapping]] = ...) -> None: ...

class CreateSessionRequest(_message.Message):
    __slots__ = ("model", "max_tokens", "api_key", "working_dir", "permission_mode", "system_prompt_template", "append_system_prompt", "tools", "max_iterations", "max_turns", "turn_token_budget", "max_consecutive_denials", "auto_compact", "enable_tool_search")
    MODEL_FIELD_NUMBER: _ClassVar[int]
    MAX_TOKENS_FIELD_NUMBER: _ClassVar[int]
    API_KEY_FIELD_NUMBER: _ClassVar[int]
    WORKING_DIR_FIELD_NUMBER: _ClassVar[int]
    PERMISSION_MODE_FIELD_NUMBER: _ClassVar[int]
    SYSTEM_PROMPT_TEMPLATE_FIELD_NUMBER: _ClassVar[int]
    APPEND_SYSTEM_PROMPT_FIELD_NUMBER: _ClassVar[int]
    TOOLS_FIELD_NUMBER: _ClassVar[int]
    MAX_ITERATIONS_FIELD_NUMBER: _ClassVar[int]
    MAX_TURNS_FIELD_NUMBER: _ClassVar[int]
    TURN_TOKEN_BUDGET_FIELD_NUMBER: _ClassVar[int]
    MAX_CONSECUTIVE_DENIALS_FIELD_NUMBER: _ClassVar[int]
    AUTO_COMPACT_FIELD_NUMBER: _ClassVar[int]
    ENABLE_TOOL_SEARCH_FIELD_NUMBER: _ClassVar[int]
    model: str
    max_tokens: int
    api_key: str
    working_dir: str
    permission_mode: str
    system_prompt_template: str
    append_system_prompt: str
    tools: _containers.RepeatedScalarFieldContainer[str]
    max_iterations: int
    max_turns: int
    turn_token_budget: int
    max_consecutive_denials: int
    auto_compact: bool
    enable_tool_search: bool
    def __init__(self, model: _Optional[str] = ..., max_tokens: _Optional[int] = ..., api_key: _Optional[str] = ..., working_dir: _Optional[str] = ..., permission_mode: _Optional[str] = ..., system_prompt_template: _Optional[str] = ..., append_system_prompt: _Optional[str] = ..., tools: _Optional[_Iterable[str]] = ..., max_iterations: _Optional[int] = ..., max_turns: _Optional[int] = ..., turn_token_budget: _Optional[int] = ..., max_consecutive_denials: _Optional[int] = ..., auto_compact: _Optional[bool] = ..., enable_tool_search: _Optional[bool] = ...) -> None: ...

class ListSessionsRequest(_message.Message):
    __slots__ = ("limit", "offset", "include_subagent")
    LIMIT_FIELD_NUMBER: _ClassVar[int]
    OFFSET_FIELD_NUMBER: _ClassVar[int]
    INCLUDE_SUBAGENT_FIELD_NUMBER: _ClassVar[int]
    limit: int
    offset: int
    include_subagent: bool
    def __init__(self, limit: _Optional[int] = ..., offset: _Optional[int] = ..., include_subagent: _Optional[bool] = ...) -> None: ...

class ListSessionsResponse(_message.Message):
    __slots__ = ("sessions", "total_count")
    SESSIONS_FIELD_NUMBER: _ClassVar[int]
    TOTAL_COUNT_FIELD_NUMBER: _ClassVar[int]
    sessions: _containers.RepeatedCompositeFieldContainer[SessionInfo]
    total_count: int
    def __init__(self, sessions: _Optional[_Iterable[_Union[SessionInfo, _Mapping]]] = ..., total_count: _Optional[int] = ...) -> None: ...

class GetSessionRequest(_message.Message):
    __slots__ = ("session_id",)
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    session_id: str
    def __init__(self, session_id: _Optional[str] = ...) -> None: ...

class SearchSessionsRequest(_message.Message):
    __slots__ = ("query", "limit")
    QUERY_FIELD_NUMBER: _ClassVar[int]
    LIMIT_FIELD_NUMBER: _ClassVar[int]
    query: str
    limit: int
    def __init__(self, query: _Optional[str] = ..., limit: _Optional[int] = ...) -> None: ...

class SearchSessionsResponse(_message.Message):
    __slots__ = ("session_ids", "sessions")
    SESSION_IDS_FIELD_NUMBER: _ClassVar[int]
    SESSIONS_FIELD_NUMBER: _ClassVar[int]
    session_ids: _containers.RepeatedScalarFieldContainer[str]
    sessions: _containers.RepeatedCompositeFieldContainer[SessionInfo]
    def __init__(self, session_ids: _Optional[_Iterable[str]] = ..., sessions: _Optional[_Iterable[_Union[SessionInfo, _Mapping]]] = ...) -> None: ...

class DeleteSessionRequest(_message.Message):
    __slots__ = ("session_id",)
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    session_id: str
    def __init__(self, session_id: _Optional[str] = ...) -> None: ...

class DeleteSessionResponse(_message.Message):
    __slots__ = ("success",)
    SUCCESS_FIELD_NUMBER: _ClassVar[int]
    success: bool
    def __init__(self, success: _Optional[bool] = ...) -> None: ...

class QueryRequest(_message.Message):
    __slots__ = ("prompt", "model", "tools", "stream", "context_id", "temperature", "max_tokens", "api_key")
    PROMPT_FIELD_NUMBER: _ClassVar[int]
    MODEL_FIELD_NUMBER: _ClassVar[int]
    TOOLS_FIELD_NUMBER: _ClassVar[int]
    STREAM_FIELD_NUMBER: _ClassVar[int]
    CONTEXT_ID_FIELD_NUMBER: _ClassVar[int]
    TEMPERATURE_FIELD_NUMBER: _ClassVar[int]
    MAX_TOKENS_FIELD_NUMBER: _ClassVar[int]
    API_KEY_FIELD_NUMBER: _ClassVar[int]
    prompt: str
    model: str
    tools: _containers.RepeatedScalarFieldContainer[str]
    stream: bool
    context_id: str
    temperature: float
    max_tokens: int
    api_key: str
    def __init__(self, prompt: _Optional[str] = ..., model: _Optional[str] = ..., tools: _Optional[_Iterable[str]] = ..., stream: _Optional[bool] = ..., context_id: _Optional[str] = ..., temperature: _Optional[float] = ..., max_tokens: _Optional[int] = ..., api_key: _Optional[str] = ...) -> None: ...

class QueryResponse(_message.Message):
    __slots__ = ("content", "model", "token_usage", "conversation_id", "stopped", "item_type", "chunk", "runtime_event")
    CONTENT_FIELD_NUMBER: _ClassVar[int]
    MODEL_FIELD_NUMBER: _ClassVar[int]
    TOKEN_USAGE_FIELD_NUMBER: _ClassVar[int]
    CONVERSATION_ID_FIELD_NUMBER: _ClassVar[int]
    STOPPED_FIELD_NUMBER: _ClassVar[int]
    ITEM_TYPE_FIELD_NUMBER: _ClassVar[int]
    CHUNK_FIELD_NUMBER: _ClassVar[int]
    RUNTIME_EVENT_FIELD_NUMBER: _ClassVar[int]
    content: str
    model: str
    token_usage: TokenUsage
    conversation_id: str
    stopped: bool
    item_type: str
    chunk: ChunkDelta
    runtime_event: RuntimeEvent
    def __init__(self, content: _Optional[str] = ..., model: _Optional[str] = ..., token_usage: _Optional[_Union[TokenUsage, _Mapping]] = ..., conversation_id: _Optional[str] = ..., stopped: _Optional[bool] = ..., item_type: _Optional[str] = ..., chunk: _Optional[_Union[ChunkDelta, _Mapping]] = ..., runtime_event: _Optional[_Union[RuntimeEvent, _Mapping]] = ...) -> None: ...

class RuntimeEvent(_message.Message):
    __slots__ = ("type", "session_id", "turn_id", "turn_number", "timestamp", "stop_reason", "token_usage", "chunk", "tool_name", "tool_stage", "tool_message", "tool_percent_complete", "error")
    TYPE_FIELD_NUMBER: _ClassVar[int]
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    TURN_ID_FIELD_NUMBER: _ClassVar[int]
    TURN_NUMBER_FIELD_NUMBER: _ClassVar[int]
    TIMESTAMP_FIELD_NUMBER: _ClassVar[int]
    STOP_REASON_FIELD_NUMBER: _ClassVar[int]
    TOKEN_USAGE_FIELD_NUMBER: _ClassVar[int]
    CHUNK_FIELD_NUMBER: _ClassVar[int]
    TOOL_NAME_FIELD_NUMBER: _ClassVar[int]
    TOOL_STAGE_FIELD_NUMBER: _ClassVar[int]
    TOOL_MESSAGE_FIELD_NUMBER: _ClassVar[int]
    TOOL_PERCENT_COMPLETE_FIELD_NUMBER: _ClassVar[int]
    ERROR_FIELD_NUMBER: _ClassVar[int]
    type: str
    session_id: str
    turn_id: str
    turn_number: int
    timestamp: str
    stop_reason: str
    token_usage: TokenUsage
    chunk: ChunkDelta
    tool_name: str
    tool_stage: str
    tool_message: str
    tool_percent_complete: float
    error: str
    def __init__(self, type: _Optional[str] = ..., session_id: _Optional[str] = ..., turn_id: _Optional[str] = ..., turn_number: _Optional[int] = ..., timestamp: _Optional[str] = ..., stop_reason: _Optional[str] = ..., token_usage: _Optional[_Union[TokenUsage, _Mapping]] = ..., chunk: _Optional[_Union[ChunkDelta, _Mapping]] = ..., tool_name: _Optional[str] = ..., tool_stage: _Optional[str] = ..., tool_message: _Optional[str] = ..., tool_percent_complete: _Optional[float] = ..., error: _Optional[str] = ...) -> None: ...

class SubmitMessageRequest(_message.Message):
    __slots__ = ("session_id", "text", "images", "idle_timeout_seconds")
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    IMAGES_FIELD_NUMBER: _ClassVar[int]
    IDLE_TIMEOUT_SECONDS_FIELD_NUMBER: _ClassVar[int]
    session_id: str
    text: str
    images: _containers.RepeatedCompositeFieldContainer[ImageBlock]
    idle_timeout_seconds: int
    def __init__(self, session_id: _Optional[str] = ..., text: _Optional[str] = ..., images: _Optional[_Iterable[_Union[ImageBlock, _Mapping]]] = ..., idle_timeout_seconds: _Optional[int] = ...) -> None: ...

class SubscribeRequest(_message.Message):
    __slots__ = ("session_id", "from_sequence")
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    FROM_SEQUENCE_FIELD_NUMBER: _ClassVar[int]
    session_id: str
    from_sequence: int
    def __init__(self, session_id: _Optional[str] = ..., from_sequence: _Optional[int] = ...) -> None: ...

class GetMessagesRequest(_message.Message):
    __slots__ = ("session_id",)
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    session_id: str
    def __init__(self, session_id: _Optional[str] = ...) -> None: ...

class GetMessagesResponse(_message.Message):
    __slots__ = ("messages",)
    MESSAGES_FIELD_NUMBER: _ClassVar[int]
    messages: _containers.RepeatedCompositeFieldContainer[Message]
    def __init__(self, messages: _Optional[_Iterable[_Union[Message, _Mapping]]] = ...) -> None: ...

class GetHistoryRequest(_message.Message):
    __slots__ = ("session_id",)
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    session_id: str
    def __init__(self, session_id: _Optional[str] = ...) -> None: ...

class GetHistoryResponse(_message.Message):
    __slots__ = ("archived", "current")
    ARCHIVED_FIELD_NUMBER: _ClassVar[int]
    CURRENT_FIELD_NUMBER: _ClassVar[int]
    archived: _containers.RepeatedCompositeFieldContainer[Message]
    current: _containers.RepeatedCompositeFieldContainer[Message]
    def __init__(self, archived: _Optional[_Iterable[_Union[Message, _Mapping]]] = ..., current: _Optional[_Iterable[_Union[Message, _Mapping]]] = ...) -> None: ...

class GetStateRequest(_message.Message):
    __slots__ = ("session_id",)
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    session_id: str
    def __init__(self, session_id: _Optional[str] = ...) -> None: ...

class InterruptRequest(_message.Message):
    __slots__ = ("session_id",)
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    session_id: str
    def __init__(self, session_id: _Optional[str] = ...) -> None: ...

class CompactRequest(_message.Message):
    __slots__ = ("session_id",)
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    session_id: str
    def __init__(self, session_id: _Optional[str] = ...) -> None: ...

class CompactResponse(_message.Message):
    __slots__ = ("pre_tokens", "post_tokens")
    PRE_TOKENS_FIELD_NUMBER: _ClassVar[int]
    POST_TOKENS_FIELD_NUMBER: _ClassVar[int]
    pre_tokens: int
    post_tokens: int
    def __init__(self, pre_tokens: _Optional[int] = ..., post_tokens: _Optional[int] = ...) -> None: ...

class CloseSessionRequest(_message.Message):
    __slots__ = ("session_id",)
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    session_id: str
    def __init__(self, session_id: _Optional[str] = ...) -> None: ...

class SetPermissionModeRequest(_message.Message):
    __slots__ = ("session_id", "mode")
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    MODE_FIELD_NUMBER: _ClassVar[int]
    session_id: str
    mode: str
    def __init__(self, session_id: _Optional[str] = ..., mode: _Optional[str] = ...) -> None: ...

class SetWorkingDirectoryRequest(_message.Message):
    __slots__ = ("session_id", "path")
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    PATH_FIELD_NUMBER: _ClassVar[int]
    session_id: str
    path: str
    def __init__(self, session_id: _Optional[str] = ..., path: _Optional[str] = ...) -> None: ...

class SetSystemPromptRequest(_message.Message):
    __slots__ = ("session_id", "template", "append", "append_only")
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    TEMPLATE_FIELD_NUMBER: _ClassVar[int]
    APPEND_FIELD_NUMBER: _ClassVar[int]
    APPEND_ONLY_FIELD_NUMBER: _ClassVar[int]
    session_id: str
    template: str
    append: str
    append_only: bool
    def __init__(self, session_id: _Optional[str] = ..., template: _Optional[str] = ..., append: _Optional[str] = ..., append_only: _Optional[bool] = ...) -> None: ...

class SetPlanModeRequest(_message.Message):
    __slots__ = ("session_id", "enabled")
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    ENABLED_FIELD_NUMBER: _ClassVar[int]
    session_id: str
    enabled: bool
    def __init__(self, session_id: _Optional[str] = ..., enabled: _Optional[bool] = ...) -> None: ...

class ApprovePlanRequest(_message.Message):
    __slots__ = ("session_id",)
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    session_id: str
    def __init__(self, session_id: _Optional[str] = ...) -> None: ...

class ListToolsRequest(_message.Message):
    __slots__ = ("session_id", "category_filter")
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    CATEGORY_FILTER_FIELD_NUMBER: _ClassVar[int]
    session_id: str
    category_filter: str
    def __init__(self, session_id: _Optional[str] = ..., category_filter: _Optional[str] = ...) -> None: ...

class ListToolsResponse(_message.Message):
    __slots__ = ("tools", "total_count")
    TOOLS_FIELD_NUMBER: _ClassVar[int]
    TOTAL_COUNT_FIELD_NUMBER: _ClassVar[int]
    tools: _containers.RepeatedCompositeFieldContainer[ToolInfo]
    total_count: int
    def __init__(self, tools: _Optional[_Iterable[_Union[ToolInfo, _Mapping]]] = ..., total_count: _Optional[int] = ...) -> None: ...

class ToolInfo(_message.Message):
    __slots__ = ("name", "description", "enabled", "deferred", "category")
    NAME_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    ENABLED_FIELD_NUMBER: _ClassVar[int]
    DEFERRED_FIELD_NUMBER: _ClassVar[int]
    CATEGORY_FIELD_NUMBER: _ClassVar[int]
    name: str
    description: str
    enabled: bool
    deferred: bool
    category: str
    def __init__(self, name: _Optional[str] = ..., description: _Optional[str] = ..., enabled: _Optional[bool] = ..., deferred: _Optional[bool] = ..., category: _Optional[str] = ...) -> None: ...

class SetToolsRequest(_message.Message):
    __slots__ = ("session_id", "tools")
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    TOOLS_FIELD_NUMBER: _ClassVar[int]
    session_id: str
    tools: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, session_id: _Optional[str] = ..., tools: _Optional[_Iterable[str]] = ...) -> None: ...

class RespondPermissionRequest(_message.Message):
    __slots__ = ("session_id", "request_id", "decision", "reason")
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    REQUEST_ID_FIELD_NUMBER: _ClassVar[int]
    DECISION_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    session_id: str
    request_id: str
    decision: str
    reason: str
    def __init__(self, session_id: _Optional[str] = ..., request_id: _Optional[str] = ..., decision: _Optional[str] = ..., reason: _Optional[str] = ...) -> None: ...

class RespondPromptRequest(_message.Message):
    __slots__ = ("session_id", "request_id", "value", "cancelled", "metadata")
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    REQUEST_ID_FIELD_NUMBER: _ClassVar[int]
    VALUE_FIELD_NUMBER: _ClassVar[int]
    CANCELLED_FIELD_NUMBER: _ClassVar[int]
    METADATA_FIELD_NUMBER: _ClassVar[int]
    session_id: str
    request_id: str
    value: _struct_pb2.Value
    cancelled: bool
    metadata: _struct_pb2.Struct
    def __init__(self, session_id: _Optional[str] = ..., request_id: _Optional[str] = ..., value: _Optional[_Union[_struct_pb2.Value, _Mapping]] = ..., cancelled: _Optional[bool] = ..., metadata: _Optional[_Union[_struct_pb2.Struct, _Mapping]] = ...) -> None: ...

class MemoryEntity(_message.Message):
    __slots__ = ("id", "user_id", "name", "entity_type", "observations", "created_at", "updated_at")
    ID_FIELD_NUMBER: _ClassVar[int]
    USER_ID_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    ENTITY_TYPE_FIELD_NUMBER: _ClassVar[int]
    OBSERVATIONS_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_FIELD_NUMBER: _ClassVar[int]
    UPDATED_AT_FIELD_NUMBER: _ClassVar[int]
    id: str
    user_id: str
    name: str
    entity_type: str
    observations: _containers.RepeatedScalarFieldContainer[str]
    created_at: _timestamp_pb2.Timestamp
    updated_at: _timestamp_pb2.Timestamp
    def __init__(self, id: _Optional[str] = ..., user_id: _Optional[str] = ..., name: _Optional[str] = ..., entity_type: _Optional[str] = ..., observations: _Optional[_Iterable[str]] = ..., created_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., updated_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ...) -> None: ...

class MemoryRelation(_message.Message):
    __slots__ = ("id", "user_id", "to", "relation_type", "created_at")
    ID_FIELD_NUMBER: _ClassVar[int]
    USER_ID_FIELD_NUMBER: _ClassVar[int]
    FROM_FIELD_NUMBER: _ClassVar[int]
    TO_FIELD_NUMBER: _ClassVar[int]
    RELATION_TYPE_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_FIELD_NUMBER: _ClassVar[int]
    id: str
    user_id: str
    to: str
    relation_type: str
    created_at: _timestamp_pb2.Timestamp
    def __init__(self, id: _Optional[str] = ..., user_id: _Optional[str] = ..., to: _Optional[str] = ..., relation_type: _Optional[str] = ..., created_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., **kwargs) -> None: ...

class MemoryGraph(_message.Message):
    __slots__ = ("entities", "relations")
    ENTITIES_FIELD_NUMBER: _ClassVar[int]
    RELATIONS_FIELD_NUMBER: _ClassVar[int]
    entities: _containers.RepeatedCompositeFieldContainer[MemoryEntity]
    relations: _containers.RepeatedCompositeFieldContainer[MemoryRelation]
    def __init__(self, entities: _Optional[_Iterable[_Union[MemoryEntity, _Mapping]]] = ..., relations: _Optional[_Iterable[_Union[MemoryRelation, _Mapping]]] = ...) -> None: ...

class EntityInput(_message.Message):
    __slots__ = ("name", "entity_type", "observations")
    NAME_FIELD_NUMBER: _ClassVar[int]
    ENTITY_TYPE_FIELD_NUMBER: _ClassVar[int]
    OBSERVATIONS_FIELD_NUMBER: _ClassVar[int]
    name: str
    entity_type: str
    observations: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, name: _Optional[str] = ..., entity_type: _Optional[str] = ..., observations: _Optional[_Iterable[str]] = ...) -> None: ...

class UpsertEntitiesRequest(_message.Message):
    __slots__ = ("user_id", "entities")
    USER_ID_FIELD_NUMBER: _ClassVar[int]
    ENTITIES_FIELD_NUMBER: _ClassVar[int]
    user_id: str
    entities: _containers.RepeatedCompositeFieldContainer[EntityInput]
    def __init__(self, user_id: _Optional[str] = ..., entities: _Optional[_Iterable[_Union[EntityInput, _Mapping]]] = ...) -> None: ...

class UpsertEntitiesResponse(_message.Message):
    __slots__ = ("created",)
    CREATED_FIELD_NUMBER: _ClassVar[int]
    created: _containers.RepeatedCompositeFieldContainer[MemoryEntity]
    def __init__(self, created: _Optional[_Iterable[_Union[MemoryEntity, _Mapping]]] = ...) -> None: ...

class ObservationInput(_message.Message):
    __slots__ = ("entity_name", "contents")
    ENTITY_NAME_FIELD_NUMBER: _ClassVar[int]
    CONTENTS_FIELD_NUMBER: _ClassVar[int]
    entity_name: str
    contents: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, entity_name: _Optional[str] = ..., contents: _Optional[_Iterable[str]] = ...) -> None: ...

class AddObservationsRequest(_message.Message):
    __slots__ = ("user_id", "observations")
    USER_ID_FIELD_NUMBER: _ClassVar[int]
    OBSERVATIONS_FIELD_NUMBER: _ClassVar[int]
    user_id: str
    observations: _containers.RepeatedCompositeFieldContainer[ObservationInput]
    def __init__(self, user_id: _Optional[str] = ..., observations: _Optional[_Iterable[_Union[ObservationInput, _Mapping]]] = ...) -> None: ...

class ObservationResult(_message.Message):
    __slots__ = ("entity_name", "added_observations")
    ENTITY_NAME_FIELD_NUMBER: _ClassVar[int]
    ADDED_OBSERVATIONS_FIELD_NUMBER: _ClassVar[int]
    entity_name: str
    added_observations: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, entity_name: _Optional[str] = ..., added_observations: _Optional[_Iterable[str]] = ...) -> None: ...

class AddObservationsResponse(_message.Message):
    __slots__ = ("results",)
    RESULTS_FIELD_NUMBER: _ClassVar[int]
    results: _containers.RepeatedCompositeFieldContainer[ObservationResult]
    def __init__(self, results: _Optional[_Iterable[_Union[ObservationResult, _Mapping]]] = ...) -> None: ...

class SearchMemoryRequest(_message.Message):
    __slots__ = ("user_id", "query")
    USER_ID_FIELD_NUMBER: _ClassVar[int]
    QUERY_FIELD_NUMBER: _ClassVar[int]
    user_id: str
    query: str
    def __init__(self, user_id: _Optional[str] = ..., query: _Optional[str] = ...) -> None: ...

class OpenEntitiesRequest(_message.Message):
    __slots__ = ("user_id", "names")
    USER_ID_FIELD_NUMBER: _ClassVar[int]
    NAMES_FIELD_NUMBER: _ClassVar[int]
    user_id: str
    names: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, user_id: _Optional[str] = ..., names: _Optional[_Iterable[str]] = ...) -> None: ...

class RetrieveContextRequest(_message.Message):
    __slots__ = ("user_id", "query", "max_tokens")
    USER_ID_FIELD_NUMBER: _ClassVar[int]
    QUERY_FIELD_NUMBER: _ClassVar[int]
    MAX_TOKENS_FIELD_NUMBER: _ClassVar[int]
    user_id: str
    query: str
    max_tokens: int
    def __init__(self, user_id: _Optional[str] = ..., query: _Optional[str] = ..., max_tokens: _Optional[int] = ...) -> None: ...

class RetrieveContextResponse(_message.Message):
    __slots__ = ("context", "found")
    CONTEXT_FIELD_NUMBER: _ClassVar[int]
    FOUND_FIELD_NUMBER: _ClassVar[int]
    context: str
    found: bool
    def __init__(self, context: _Optional[str] = ..., found: _Optional[bool] = ...) -> None: ...

class WorkflowDefinitionRequest(_message.Message):
    __slots__ = ("definition", "format")
    DEFINITION_FIELD_NUMBER: _ClassVar[int]
    FORMAT_FIELD_NUMBER: _ClassVar[int]
    definition: str
    format: str
    def __init__(self, definition: _Optional[str] = ..., format: _Optional[str] = ...) -> None: ...

class WorkflowValidationResponse(_message.Message):
    __slots__ = ("valid", "diagnostics", "name", "node_count")
    VALID_FIELD_NUMBER: _ClassVar[int]
    DIAGNOSTICS_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    NODE_COUNT_FIELD_NUMBER: _ClassVar[int]
    valid: bool
    diagnostics: str
    name: str
    node_count: int
    def __init__(self, valid: _Optional[bool] = ..., diagnostics: _Optional[str] = ..., name: _Optional[str] = ..., node_count: _Optional[int] = ...) -> None: ...

class DraftWorkflowRequest(_message.Message):
    __slots__ = ("definition", "format")
    DEFINITION_FIELD_NUMBER: _ClassVar[int]
    FORMAT_FIELD_NUMBER: _ClassVar[int]
    definition: str
    format: str
    def __init__(self, definition: _Optional[str] = ..., format: _Optional[str] = ...) -> None: ...

class DraftWorkflowResponse(_message.Message):
    __slots__ = ("rendered", "diagnostics")
    RENDERED_FIELD_NUMBER: _ClassVar[int]
    DIAGNOSTICS_FIELD_NUMBER: _ClassVar[int]
    rendered: str
    diagnostics: str
    def __init__(self, rendered: _Optional[str] = ..., diagnostics: _Optional[str] = ...) -> None: ...

class RunWorkflowRequest(_message.Message):
    __slots__ = ("definition", "max_parallel", "model")
    DEFINITION_FIELD_NUMBER: _ClassVar[int]
    MAX_PARALLEL_FIELD_NUMBER: _ClassVar[int]
    MODEL_FIELD_NUMBER: _ClassVar[int]
    definition: str
    max_parallel: int
    model: str
    def __init__(self, definition: _Optional[str] = ..., max_parallel: _Optional[int] = ..., model: _Optional[str] = ...) -> None: ...

class WorkflowNodeResult(_message.Message):
    __slots__ = ("id", "agent", "success", "output", "error", "started_at", "finished_at")
    ID_FIELD_NUMBER: _ClassVar[int]
    AGENT_FIELD_NUMBER: _ClassVar[int]
    SUCCESS_FIELD_NUMBER: _ClassVar[int]
    OUTPUT_FIELD_NUMBER: _ClassVar[int]
    ERROR_FIELD_NUMBER: _ClassVar[int]
    STARTED_AT_FIELD_NUMBER: _ClassVar[int]
    FINISHED_AT_FIELD_NUMBER: _ClassVar[int]
    id: str
    agent: str
    success: bool
    output: str
    error: str
    started_at: _timestamp_pb2.Timestamp
    finished_at: _timestamp_pb2.Timestamp
    def __init__(self, id: _Optional[str] = ..., agent: _Optional[str] = ..., success: _Optional[bool] = ..., output: _Optional[str] = ..., error: _Optional[str] = ..., started_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., finished_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ...) -> None: ...

class WorkflowRunResponse(_message.Message):
    __slots__ = ("success", "results", "order")
    class ResultsEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: WorkflowNodeResult
        def __init__(self, key: _Optional[str] = ..., value: _Optional[_Union[WorkflowNodeResult, _Mapping]] = ...) -> None: ...
    SUCCESS_FIELD_NUMBER: _ClassVar[int]
    RESULTS_FIELD_NUMBER: _ClassVar[int]
    ORDER_FIELD_NUMBER: _ClassVar[int]
    success: bool
    results: _containers.MessageMap[str, WorkflowNodeResult]
    order: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, success: _Optional[bool] = ..., results: _Optional[_Mapping[str, WorkflowNodeResult]] = ..., order: _Optional[_Iterable[str]] = ...) -> None: ...
