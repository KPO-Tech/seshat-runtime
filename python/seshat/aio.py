"""The asyncio client.

The same contract as :mod:`seshat.client`, on ``grpc.aio``:

    import asyncio
    import seshat.aio

    async def main():
        async with seshat.aio.connect() as client:
            session = await client.create_session()
            result = await session.ask("hello")
            print(result.content)
            async for event in session.stream("and again"):
                print(event.text, end="")

    asyncio.run(main())

Use it when the program already runs an event loop. The synchronous client is
the better choice for a script, and is what a worker that blocks on a turn
should use.
"""

from __future__ import annotations

import os
from typing import Any, AsyncIterator, Dict, List, Optional, Sequence, Tuple, Union

import grpc

from . import errors
from ._proto import seshat_pb2 as pb
from ._proto import seshat_pb2_grpc as pb_grpc
from .events import SessionEvent
from .session import _decision, _image_blocks, _to_value
from .types import Health, Image, SessionSnapshot, TurnResult


class AsyncSession:
    """A conversation, driven from an event loop."""

    def __init__(self, client: "AsyncClient", session_id: str, snapshot: Optional[SessionSnapshot] = None) -> None:
        self._client = client
        self._id = session_id
        self._snapshot = snapshot

    @property
    def id(self) -> str:
        return self._id

    async def ask(
        self,
        text: str,
        *,
        images: Optional[Sequence[Union[Image, Dict[str, Any], str]]] = None,
        on_permission: Any = None,
        on_prompt: Any = None,
        timeout: Optional[float] = None,
    ) -> TurnResult:
        """Run a turn and return its result, answering approvals through the
        handlers."""
        result: Optional[TurnResult] = None
        async for event in self.stream(
            text, images=images, on_permission=on_permission, on_prompt=on_prompt, timeout=timeout
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

    async def stream(
        self,
        text: str = "",
        *,
        images: Optional[Sequence[Union[Image, Dict[str, Any], str]]] = None,
        on_permission: Any = None,
        on_prompt: Any = None,
        timeout: Optional[float] = None,
    ) -> AsyncIterator[SessionEvent]:
        """Run a turn and yield its events."""
        request = pb.SubmitMessageRequest(session_id=self._id, text=text, images=_image_blocks(images))
        call = self._client._sessions.SubmitMessageStream(
            request, timeout=timeout, metadata=self._client.metadata()
        )
        try:
            async for proto_event in call:
                event = SessionEvent.from_proto(proto_event)
                if event.kind == "permission_request" and event.permission_request is not None:
                    request_value = event.permission_request
                    if on_permission is None:
                        await self.respond_permission(request_value.request_id, "deny", "no handler")
                    else:
                        await self.respond_permission(request_value.request_id, _decision(on_permission(request_value)))
                elif event.kind == "prompt_request" and event.prompt_request is not None:
                    request_value = event.prompt_request
                    if on_prompt is None:
                        await self.respond_prompt(request_value.request_id, cancelled=True)
                    else:
                        await self.respond_prompt(request_value.request_id, on_prompt(request_value))
                yield event
        except grpc.RpcError as exc:  # pragma: no cover - normalised below
            raise errors.from_rpc_error(exc) from None

    async def respond_permission(self, request_id: str, decision: str, reason: str = "") -> None:
        await self._client.rpc(
            self._client._sessions.RespondPermission,
            pb.RespondPermissionRequest(
                session_id=self._id, request_id=request_id, decision=decision, reason=reason
            ),
        )

    async def respond_prompt(self, request_id: str, value: Any = None, *, cancelled: bool = False) -> None:
        request = pb.RespondPromptRequest(session_id=self._id, request_id=request_id, cancelled=cancelled)
        if value is not None:
            request.value.CopyFrom(_to_value(value))
        await self._client.rpc(self._client._sessions.RespondPrompt, request)

    async def interrupt(self) -> None:
        await self._client.rpc(self._client._sessions.Interrupt, pb.InterruptRequest(session_id=self._id))

    async def close(self) -> None:
        await self._client.rpc(self._client._sessions.Close, pb.CloseSessionRequest(session_id=self._id))

    async def state(self) -> Any:
        response = await self._client.rpc(
            self._client._sessions.GetState, pb.GetStateRequest(session_id=self._id)
        )
        from .types import SessionState

        return SessionState.from_proto(response)

    async def __aenter__(self) -> "AsyncSession":
        return self

    async def __aexit__(self, *_exc: Any) -> None:
        try:
            await self.close()
        except errors.SeshatError:
            pass


class AsyncClient:
    """A connection to a Seshat server, on ``grpc.aio``."""

    def __init__(
        self,
        target: Optional[str] = None,
        *,
        token: Optional[str] = None,
        tls: Union[bool, str, bytes] = False,
        timeout: float = 300.0,
    ) -> None:
        from .client import default_target

        self.target = target or default_target()
        self._token = (token or "").strip() or os.environ.get("SESHAT_GRPC_AUTH_TOKEN", "").strip()
        self._timeout = timeout
        if tls:
            credentials = grpc.ssl_channel_credentials()
            self._channel = grpc.aio.secure_channel(self.target, credentials)
        else:
            self._channel = grpc.aio.insecure_channel(self.target)
        self._server = pb_grpc.SeshatServiceStub(self._channel)
        self._sessions = pb_grpc.SessionServiceStub(self._channel)

    def metadata(self) -> Tuple[Tuple[str, str], ...]:
        if not self._token:
            return ()
        return (("authorization", f"Bearer {self._token}"),)

    async def rpc(self, method: Any, request: Any, timeout: Optional[float] = None) -> Any:
        effective = self._timeout if timeout is None else timeout
        try:
            return await method(request, timeout=effective, metadata=self.metadata())
        except grpc.RpcError as exc:
            raise errors.from_rpc_error(exc) from None

    async def health(self) -> Health:
        return Health.from_proto(await self.rpc(self._server.HealthCheck, pb.HealthCheckRequest()))

    async def create_session(self, **kwargs: Any) -> AsyncSession:
        request = pb.CreateSessionRequest(**kwargs)
        snapshot = SessionSnapshot.from_proto(await self.rpc(self._server.CreateSession, request))
        return AsyncSession(self, snapshot.id, snapshot)

    async def session(self, session_id: str) -> AsyncSession:
        return AsyncSession(self, session_id)

    async def sessions(self, *, limit: int = 50, offset: int = 0) -> List[Any]:
        from .types import SessionInfo

        response = await self.rpc(
            self._server.ListSessions, pb.ListSessionsRequest(limit=limit, offset=offset)
        )
        return [SessionInfo.from_proto(info) for info in response.sessions]

    async def close(self) -> None:
        await self._channel.close()

    async def __aenter__(self) -> "AsyncClient":
        return self

    async def __aexit__(self, *_exc: Any) -> None:
        await self.close()


def connect(target: Optional[str] = None, **kwargs: Any) -> AsyncClient:
    """Make an async client."""
    return AsyncClient(target, **kwargs)
