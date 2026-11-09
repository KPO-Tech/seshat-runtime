"""A live conversation with the runtime.

A :class:`Session` is a handle on a conversation the server holds. It keeps no
state of its own: every method is one round trip, so two processes can drive the
same conversation and see the same transcript.

The interesting part is approvals. The runtime blocks a tool call while a host
decides, and over gRPC the decision is another call: a ``permission_request``
event carries a ``request_id`` and :meth:`Session.respond_permission` answers
it. :meth:`Session.stream` does that for you when you pass ``on_permission``:

    def decide(request):
        return "allow" if request.tool_name == "read_file" else "deny"

    for event in session.stream("summarise the repo", on_permission=decide):
        print(event.text, end="")
"""

from __future__ import annotations

from typing import Any, Callable, Dict, Iterator, List, Optional, Sequence, Union

import grpc

from . import errors
from ._proto import seshat_pb2 as pb
from .events import PermissionRequest, PromptRequest, SessionEvent
from .types import Image, Message, SessionSnapshot, SessionState, ToolInfo, TurnResult

PermissionHandler = Callable[[PermissionRequest], Union[str, bool, None]]
PromptHandler = Callable[[PromptRequest], Any]

#: The decision given to a permission request when no handler is set. Denying is
#: the safe default: a client that did not decide did not consent.
DEFAULT_DENIAL = "no permission handler is set on this session"


def _image_blocks(images: Optional[Sequence[Union[Image, Dict[str, Any], str]]]) -> List[pb.ImageBlock]:
    """Render images for the wire, accepting Image objects, dicts or raw base64."""
    blocks: List[pb.ImageBlock] = []
    for image in images or ():
        if isinstance(image, Image):
            blocks.append(
                pb.ImageBlock(source_type=image.source_type, media_type=image.media_type, data=image.data)
            )
        elif isinstance(image, str):
            blocks.append(pb.ImageBlock(source_type="base64", media_type="image/png", data=image))
        elif isinstance(image, dict):
            blocks.append(
                pb.ImageBlock(
                    source_type=image.get("source_type", "base64"),
                    media_type=image.get("media_type", "image/png"),
                    data=image.get("data", ""),
                )
            )
        else:  # pragma: no cover - a caller passed something else
            raise TypeError(f"cannot send an image of type {type(image).__name__}")
    return blocks


def _decision(value: Union[str, bool, None]) -> str:
    """Normalise what a handler returned into a wire decision."""
    if value is True:
        return "allow"
    if value is None or value is False:
        return "deny"
    text = str(value).strip().lower()
    if text in ("allow", "yes", "true", "approve", "approved"):
        return "allow"
    if text in ("allow_session", "always", "allow-session", "allow_for_session"):
        return "allow_session"
    return "deny"


class Session:
    """A conversation the server holds.

    Create one with :meth:`seshat.Client.create_session`, or open an existing one
    with :meth:`seshat.Client.session`. It is also a context manager, which
    closes the conversation::

        with client.create_session() as session:
            print(session.ask("hello").content)
    """

    def __init__(self, client: Any, session_id: str, snapshot: Optional[SessionSnapshot] = None) -> None:
        self._client = client
        self._id = session_id
        self._snapshot = snapshot

    # -- identity ----------------------------------------------------------

    @property
    def id(self) -> str:
        """The identifier of the conversation."""
        return self._id

    @property
    def snapshot(self) -> Optional[SessionSnapshot]:
        """The last state the server sent, when one was sent."""
        return self._snapshot

    def __repr__(self) -> str:
        title = self._snapshot.title if self._snapshot else ""
        return f"Session(id={self._id!r}, title={title!r})"

    def __enter__(self) -> "Session":
        return self

    def __exit__(self, *_exc: Any) -> None:
        try:
            self.close()
        except errors.SeshatError:
            pass

    # -- turns -------------------------------------------------------------

    def ask(
        self,
        text: str,
        *,
        images: Optional[Sequence[Union[Image, Dict[str, Any], str]]] = None,
        on_permission: Optional[PermissionHandler] = None,
        on_prompt: Optional[PromptHandler] = None,
        idle_timeout: Optional[float] = None,
        timeout: Optional[float] = None,
    ) -> TurnResult:
        """Run a turn and return its result.

        The turn streams internally so that an approval or a question can be
        answered while it runs; ``on_permission`` and ``on_prompt`` are called
        as they arrive. Without a handler a permission request is denied and a
        question is cancelled, because nobody is there to decide.
        """
        result: Optional[TurnResult] = None
        for event in self.stream(
            text,
            images=images,
            on_permission=on_permission,
            on_prompt=on_prompt,
            idle_timeout=idle_timeout,
            timeout=timeout,
        ):
            if event.kind == "turn_completed":
                result = event.result
            elif event.kind == "turn_failed":
                failure = event.error
                raise errors.SeshatError(
                    failure.message if failure else "the turn failed",
                    code=failure.code if failure else "INTERNAL",
                )
        if result is None:
            raise errors.SeshatError("the turn ended without a result")
        return result

    def stream(
        self,
        text: str = "",
        *,
        images: Optional[Sequence[Union[Image, Dict[str, Any], str]]] = None,
        on_permission: Optional[PermissionHandler] = None,
        on_prompt: Optional[PromptHandler] = None,
        idle_timeout: Optional[float] = None,
        timeout: Optional[float] = None,
    ) -> Iterator[SessionEvent]:
        """Run a turn and yield its events as they arrive.

        The last event is ``turn_completed`` (with ``event.result``) or
        ``turn_failed`` (with ``event.error``). A permission request is answered
        from this loop, so a handler may run a whole decision here.
        """
        request = pb.SubmitMessageRequest(session_id=self._id, text=text, images=_image_blocks(images))
        if idle_timeout:
            request.idle_timeout_seconds = int(idle_timeout)

        call = self._client.stream_call(self._client._sessions.SubmitMessageStream, request, timeout)
        try:
            for proto_event in call:
                event = SessionEvent.from_proto(proto_event)
                self._maybe_answer(event, on_permission, on_prompt)
                yield event
        except grpc.RpcError as exc:  # pragma: no cover - normalised below
            raise errors.from_rpc_error(exc) from None

    def submit(
        self,
        text: str = "",
        *,
        images: Optional[Sequence[Union[Image, Dict[str, Any], str]]] = None,
        timeout: Optional[float] = None,
    ) -> TurnResult:
        """Run a turn with the unary call and return its result.

        Use it for a turn nobody has to approve (``auto``, ``bypass`` or
        ``never``): a unary call cannot receive a permission request, so a turn
        that needs one waits until the server's interaction timeout.
        """
        request = pb.SubmitMessageRequest(session_id=self._id, text=text, images=_image_blocks(images))
        result = self._client.rpc(self._client._sessions.SubmitMessage, request, timeout=timeout)
        return TurnResult.from_proto(result)

    def subscribe(self, from_sequence: int = 0, *, timeout: Optional[float] = None) -> Iterator[SessionEvent]:
        """Follow the events of this session without starting a turn.

        ``from_sequence`` replays the events the server still holds after that
        sequence number, which is how a reconnecting client avoids a gap.
        """
        request = pb.SubscribeRequest(session_id=self._id, from_sequence=from_sequence)
        call = self._client.stream_call(self._client._sessions.Subscribe, request, timeout)
        try:
            for proto_event in call:
                yield SessionEvent.from_proto(proto_event)
        except grpc.RpcError as exc:  # pragma: no cover - normalised below
            raise errors.from_rpc_error(exc) from None

    def _maybe_answer(
        self,
        event: SessionEvent,
        on_permission: Optional[PermissionHandler],
        on_prompt: Optional[PromptHandler],
    ) -> None:
        """Answer a permission request or a question, if this event is one."""
        if event.kind == "permission_request" and event.permission_request is not None:
            request = event.permission_request
            if on_permission is None:
                # Nothing to ask: the safe answer is no, and the model is told
                # why rather than being left to guess.
                self.respond_permission(request.request_id, "deny", DEFAULT_DENIAL)
                return
            self.respond_permission(request.request_id, _decision(on_permission(request)))
            return

        if event.kind == "prompt_request" and event.prompt_request is not None:
            request = event.prompt_request
            if on_prompt is None:
                self.respond_prompt(request.request_id, cancelled=True)
                return
            answer = on_prompt(request)
            if isinstance(answer, PromptAnswer):
                self.respond_prompt(request.request_id, value=answer.value, cancelled=answer.cancelled)
            else:
                self.respond_prompt(request.request_id, value=answer)

    def respond_permission(self, request_id: str, decision: str, reason: str = "") -> None:
        """Answer a permission request.

        ``decision`` is ``"allow"`` for this call, ``"allow_session"`` to also
        remember it for the conversation, or ``"deny"``.
        """
        request = pb.RespondPermissionRequest(
            session_id=self._id,
            request_id=request_id,
            decision=decision,
            reason=reason,
        )
        self._client.rpc(self._client._sessions.RespondPermission, request)

    def respond_prompt(self, request_id: str, value: Any = None, *, cancelled: bool = False) -> None:
        """Answer a question a tool asked."""
        request = pb.RespondPromptRequest(session_id=self._id, request_id=request_id, cancelled=cancelled)
        if value is not None:
            request.value.CopyFrom(_to_value(value))
        self._client.rpc(self._client._sessions.RespondPrompt, request)

    # -- state -------------------------------------------------------------

    def state(self) -> SessionState:
        """The live state of the conversation."""
        request = pb.GetStateRequest(session_id=self._id)
        return SessionState.from_proto(self._client.rpc(self._client._sessions.GetState, request))

    def refresh(self) -> SessionSnapshot:
        """Re-read the conversation from the server."""
        request = pb.GetSessionRequest(session_id=self._id)
        self._snapshot = SessionSnapshot.from_proto(self._client.rpc(self._client._server.GetSession, request))
        return self._snapshot

    def messages(self) -> List[Message]:
        """The messages the model sees, in order."""
        request = pb.GetMessagesRequest(session_id=self._id)
        response = self._client.rpc(self._client._sessions.GetMessages, request)
        return [Message.from_proto(message) for message in response.messages]

    def history(self) -> Dict[str, List[Message]]:
        """The whole conversation: ``{"archived": [...], "current": [...]}``."""
        request = pb.GetHistoryRequest(session_id=self._id)
        response = self._client.rpc(self._client._sessions.GetHistory, request)
        return {
            "archived": [Message.from_proto(message) for message in response.archived],
            "current": [Message.from_proto(message) for message in response.current],
        }

    def tools(self) -> List[ToolInfo]:
        """The tools of this conversation, enabled or not."""
        request = pb.ListToolsRequest(session_id=self._id)
        response = self._client.rpc(self._client._sessions.ListTools, request)
        return [ToolInfo.from_proto(tool) for tool in response.tools]

    # -- control -----------------------------------------------------------

    def interrupt(self) -> None:
        """Stop the turn that is running."""
        self._client.rpc(self._client._sessions.Interrupt, pb.InterruptRequest(session_id=self._id))

    def compact(self) -> Dict[str, int]:
        """Compact the conversation now; returns the token counts before and after."""
        response = self._client.rpc(
            self._client._sessions.Compact, pb.CompactRequest(session_id=self._id), timeout=300
        )
        return {"pre_tokens": response.pre_tokens, "post_tokens": response.post_tokens}

    def close(self) -> None:
        """End the conversation and release what it holds."""
        self._client.rpc(self._client._sessions.Close, pb.CloseSessionRequest(session_id=self._id))

    def set_permission_mode(self, mode: str) -> None:
        """Change the permission mode from the next tool call on:
        ``bypass``, ``auto``, ``acceptEdits``, ``onRequest``, ``never`` or
        ``granular``."""
        self._client.rpc(
            self._client._sessions.SetPermissionMode,
            pb.SetPermissionModeRequest(session_id=self._id, mode=mode),
        )

    def set_working_directory(self, path: str) -> None:
        """Change the directory the file and shell tools work in."""
        self._client.rpc(
            self._client._sessions.SetWorkingDirectory,
            pb.SetWorkingDirectoryRequest(session_id=self._id, path=path),
        )

    def set_system_prompt(
        self,
        template: Optional[str] = None,
        *,
        append: Optional[str] = None,
    ) -> None:
        """Replace the system prompt (``template``) or extend it (``append``)."""
        request = pb.SetSystemPromptRequest(
            session_id=self._id,
            append_only=append is not None,
            append=append or "",
            template=template or "",
        )
        self._client.rpc(self._client._sessions.SetSystemPrompt, request)

    def set_plan_mode(self, enabled: bool) -> None:
        """Enter (True) or leave (False) plan mode."""
        self._client.rpc(
            self._client._sessions.SetPlanMode,
            pb.SetPlanModeRequest(session_id=self._id, enabled=enabled),
        )

    def approve_plan(self) -> None:
        """Record that the user accepted the plan the model submitted."""
        self._client.rpc(self._client._sessions.ApprovePlan, pb.ApprovePlanRequest(session_id=self._id))

    def restrict_tools(self, names: Sequence[str]) -> None:
        """Keep only these tools in the conversation.

        A tool unregistered here is not offered again in this conversation, so
        create a new session to get it back.
        """
        self._client.rpc(
            self._client._sessions.SetTools,
            pb.SetToolsRequest(session_id=self._id, tools=list(names)),
        )


def _to_value(value: Any) -> Any:
    """Wrap a Python value for a ``google.protobuf.Value`` field.

    A ``Value`` is a message, so it is built field by field: a dict becomes a
    struct, a list a list value, and every other value its scalar form.
    """
    from google.protobuf.struct_pb2 import Value

    wrapped = Value()
    if isinstance(value, dict):
        for key, item in value.items():
            wrapped.struct_value.fields[key].CopyFrom(_to_value(item))
    elif isinstance(value, (list, tuple)):
        for item in value:
            wrapped.list_value.values.add().CopyFrom(_to_value(item))
    elif isinstance(value, bool):
        wrapped.bool_value = value
    elif isinstance(value, (int, float)):
        wrapped.number_value = float(value)
    elif value is None:
        wrapped.null_value = 0
    else:
        wrapped.string_value = str(value)
    return wrapped


class PromptAnswer:
    """What a prompt handler returns when it also decides to cancel."""

    def __init__(self, value: Any = None, *, cancelled: bool = False) -> None:
        self.value = value
        self.cancelled = cancelled
