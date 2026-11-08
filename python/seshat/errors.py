"""Errors raised by the Seshat client.

A failed call raises a subclass of :class:`SeshatError` that carries the gRPC
status code, the message the server gave and the metadata of the response. A
caller that does not care about the kind catches :class:`SeshatError`.
"""

from __future__ import annotations

from typing import Any, Dict, Optional, Tuple

import grpc


class SeshatError(Exception):
    """Base class of every error this package raises."""

    #: The gRPC status code name, e.g. ``NOT_FOUND``.
    code: str = "UNKNOWN"

    def __init__(
        self,
        message: str,
        *,
        code: str = "",
        details: Optional[str] = None,
        metadata: Optional[Tuple[Tuple[str, Any], ...]] = None,
    ) -> None:
        super().__init__(message)
        self.message = message
        if code:
            self.code = code
        #: The detail string as the server sent it.
        self.details = details if details is not None else message
        #: The trailing metadata of the failed call.
        self.metadata: Dict[str, Any] = dict(metadata or ())

    def __str__(self) -> str:
        return self.message


class InvalidArgument(SeshatError):
    """The request is malformed; the server did not run it."""

    code = "INVALID_ARGUMENT"


class NotFound(SeshatError):
    """The session, skill, MCP server or pending request does not exist."""

    code = "NOT_FOUND"


class PermissionDenied(SeshatError):
    """The server refused the call, for example a stdio MCP server while
    ``SESHAT_GRPC_ALLOW_STDIO_MCP`` is not set."""

    code = "PERMISSION_DENIED"


class Unauthenticated(SeshatError):
    """The token is missing or wrong (``SESHAT_GRPC_AUTH_TOKEN``)."""

    code = "UNAUTHENTICATED"


class FailedPrecondition(SeshatError):
    """The server cannot do this in its state: no provider credential, memory
    or persistence disabled, a closed session."""

    code = "FAILED_PRECONDITION"


class AlreadyExists(SeshatError):
    """The resource already exists."""

    code = "ALREADY_EXISTS"


class Aborted(SeshatError):
    """Another turn is already running in this session."""

    code = "ABORTED"


class ResourceExhausted(SeshatError):
    """The server is at its limit (``SESHAT_GRPC_MAX_CONCURRENT_RPCS``)."""

    code = "RESOURCE_EXHAUSTED"


class Unavailable(SeshatError):
    """The server is not reachable."""

    code = "UNAVAILABLE"


class DeadlineExceeded(SeshatError):
    """The call, a turn, or a permission decision took too long."""

    code = "DEADLINE_EXCEEDED"


class Cancelled(SeshatError):
    """The call or the turn was cancelled."""

    code = "CANCELLED"


class Internal(SeshatError):
    """The server failed on its own side."""

    code = "INTERNAL"


_BY_CODE = {
    "INVALID_ARGUMENT": InvalidArgument,
    "NOT_FOUND": NotFound,
    "PERMISSION_DENIED": PermissionDenied,
    "UNAUTHENTICATED": Unauthenticated,
    "FAILED_PRECONDITION": FailedPrecondition,
    "ALREADY_EXISTS": AlreadyExists,
    "ABORTED": Aborted,
    "RESOURCE_EXHAUSTED": ResourceExhausted,
    "UNAVAILABLE": Unavailable,
    "DEADLINE_EXCEEDED": DeadlineExceeded,
    "CANCELLED": Cancelled,
    "INTERNAL": Internal,
}


def from_rpc_error(error: grpc.RpcError) -> SeshatError:
    """Turn a :class:`grpc.RpcError` into the matching :class:`SeshatError`."""
    code = error.code()
    name = code.name if isinstance(code, grpc.StatusCode) else "UNKNOWN"
    details = error.details() or str(error)
    message = details or f"{name} from the Seshat server"
    try:
        trailing = tuple(error.trailing_metadata() or ())
    except Exception:  # pragma: no cover - a transport that has no metadata
        trailing = ()
    cls = _BY_CODE.get(name, SeshatError)
    return cls(message, code=name, details=details, metadata=trailing)
