"""The Seshat client: one channel, four gRPC services, one Python API.

    import seshat

    with seshat.connect() as client:
        print(client.info().model)
        with client.create_session() as session:
            print(session.ask("hello").content)

The client talks to ``cmd/grpc`` of the Seshat runtime, which runs the engine.
Nothing is reimplemented here: the same tools, providers, permissions and
persistence the terminal interface uses answer these calls.
"""

from __future__ import annotations

import os
from typing import Any, Dict, Iterable, List, Optional, Sequence, Tuple, Union

import grpc

from . import errors
from ._proto import seshat_pb2 as pb
from ._proto import seshat_pb2_grpc as pb_grpc
from .session import Session
from .types import (
    MCPServer,
    MemoryEntity,
    MemoryGraph,
    Health,
    ModelInfo,
    ObservationResult,
    ProviderInfo,
    ServerInfo,
    SessionInfo,
    SessionSnapshot,
    Skill,
    WorkflowRun,
    WorkflowValidation,
)

DEFAULT_PORT = 50051
#: How long a unary call may take when the caller does not say. A turn streams,
#: so this bounds the short calls; ``stream`` and ``ask`` take their own.
DEFAULT_TIMEOUT = float(os.environ.get("SESHAT_GRPC_TIMEOUT", "300"))


def default_target() -> str:
    """The address to connect to: ``SESHAT_GRPC_TARGET``, or the host and port
    the server reads from its own environment."""
    explicit = os.environ.get("SESHAT_GRPC_TARGET")
    if explicit:
        return explicit
    host = (os.environ.get("SESHAT_GRPC_HOST") or "127.0.0.1").strip()
    if host in ("", "0.0.0.0", "::"):
        host = "127.0.0.1"
    port = (os.environ.get("SESHAT_GRPC_PORT") or str(DEFAULT_PORT)).strip()
    return f"{host}:{port}"


class SearchResult:
    """The answer to a transcript search: matching ids, and the conversations
    themselves when the server could read them."""

    def __init__(self, ids: List[str], sessions: List[SessionInfo]) -> None:
        self.ids = ids
        self.sessions = sessions

    def __iter__(self) -> Iterable[str]:
        return iter(self.ids)

    def __len__(self) -> int:
        return len(self.ids)

    def __repr__(self) -> str:
        return f"SearchResult(ids={self.ids!r})"


class Memory:
    """The long-term memory graph, for one user (blank for the shared user).

    Reach it with :meth:`Client.memory`::

        memory = client.memory()
        memory.upsert_entities([{"name": "Seshat", "entity_type": "project",
                                 "observations": ["an agent runtime"]}])
        print(memory.context("what is Seshat?"))
    """

    def __init__(self, client: "Client", user_id: str = "") -> None:
        self._client = client
        self.user_id = user_id

    def upsert_entities(self, entities: Sequence[Dict[str, Any]]) -> List[MemoryEntity]:
        """Create entities that do not exist yet, deduplicated by name."""
        request = pb.UpsertEntitiesRequest(
            user_id=self.user_id,
            entities=[
                pb.EntityInput(
                    name=entity.get("name", ""),
                    entity_type=entity.get("entity_type", ""),
                    observations=list(entity.get("observations", [])),
                )
                for entity in entities
            ],
        )
        response = self._client.rpc(self._client._memory.UpsertEntities, request)
        return [MemoryEntity.from_proto(entity) for entity in response.created]

    def add_observations(self, observations: Sequence[Dict[str, Any]]) -> List[ObservationResult]:
        """Append observations to existing entities."""
        request = pb.AddObservationsRequest(
            user_id=self.user_id,
            observations=[
                pb.ObservationInput(
                    entity_name=observation.get("entity_name", ""),
                    contents=list(observation.get("contents", [])),
                )
                for observation in observations
            ],
        )
        response = self._client.rpc(self._client._memory.AddObservations, request)
        return [ObservationResult.from_proto(result) for result in response.results]

    def search(self, query: str) -> MemoryGraph:
        """Search entity names, types and observations."""
        response = self._client.rpc(
            self._client._memory.SearchMemory,
            pb.SearchMemoryRequest(user_id=self.user_id, query=query),
        )
        return MemoryGraph.from_proto(response)

    def open(self, names: Sequence[str]) -> MemoryGraph:
        """Open specific entities by exact name."""
        response = self._client.rpc(
            self._client._memory.OpenEntities,
            pb.OpenEntitiesRequest(user_id=self.user_id, names=list(names)),
        )
        return MemoryGraph.from_proto(response)

    def context(self, query: str, max_tokens: int = 0) -> str:
        """The Markdown block the runtime injects for a query, capped in tokens."""
        response = self._client.rpc(
            self._client._memory.RetrieveContext,
            pb.RetrieveContextRequest(user_id=self.user_id, query=query, max_tokens=max_tokens),
        )
        return response.context


class Client:
    """A connection to a Seshat gRPC server.

    :param target: ``host:port``; defaults to :func:`default_target`.
    :param token: the bearer token when the server set
        ``SESHAT_GRPC_AUTH_TOKEN``; defaults to that variable.
    :param tls: turn TLS on, or name the certificate to trust.
    :param timeout: the default timeout of a unary call, in seconds.
    """

    def __init__(
        self,
        target: Optional[str] = None,
        *,
        token: Optional[str] = None,
        tls: Union[bool, str, bytes] = False,
        root_certificates: Optional[Union[str, bytes]] = None,
        private_key: Optional[Union[str, bytes]] = None,
        certificate_chain: Optional[Union[str, bytes]] = None,
        timeout: float = DEFAULT_TIMEOUT,
        options: Optional[Sequence[Tuple[str, Any]]] = None,
        max_message_size: Optional[int] = None,
    ) -> None:
        self.target = target or default_target()
        self._token = (token if token is not None else os.environ.get("SESHAT_GRPC_AUTH_TOKEN") or "").strip()
        self._timeout = timeout

        channel_options: List[Tuple[str, Any]] = list(options or ())
        if max_message_size:
            channel_options.append(("grpc.max_receive_message_length", int(max_message_size)))
            channel_options.append(("grpc.max_send_message_length", int(max_message_size)))

        if tls:
            credentials = self._credentials(tls, root_certificates, private_key, certificate_chain)
            self._channel = grpc.secure_channel(self.target, credentials, options=channel_options)
        else:
            self._channel = grpc.insecure_channel(self.target, options=channel_options)

        self._server = pb_grpc.SeshatServiceStub(self._channel)
        self._sessions = pb_grpc.SessionServiceStub(self._channel)
        self._memory = pb_grpc.MemoryServiceStub(self._channel)
        self._workflow = pb_grpc.WorkflowServiceStub(self._channel)

    @staticmethod
    def _credentials(
        tls: Union[bool, str, bytes],
        root_certificates: Optional[Union[str, bytes]],
        private_key: Optional[Union[str, bytes]],
        certificate_chain: Optional[Union[str, bytes]],
    ) -> grpc.ChannelCredentials:
        def read(value: Optional[Union[str, bytes]]) -> Optional[bytes]:
            if value is None:
                return None
            if isinstance(value, bytes):
                return value
            with open(value, "rb") as handle:
                return handle.read()

        roots = read(root_certificates)
        if roots is None and isinstance(tls, (str, bytes)):
            roots = read(tls)
        key = read(private_key)
        chain = read(certificate_chain)
        return grpc.ssl_channel_credentials(
            root_certificates=roots,
            private_key=key,
            certificate_chain=chain,
        )

    # -- plumbing ----------------------------------------------------------

    def metadata(self) -> Tuple[Tuple[str, str], ...]:
        """The metadata every call carries: the bearer token, when there is one."""
        if not self._token:
            return ()
        return (("authorization", f"Bearer {self._token}"),)

    def rpc(self, method: Any, request: Any, *, timeout: Optional[float] = None) -> Any:
        """Call one unary method and normalise its error."""
        effective = self._timeout if timeout is None else timeout
        try:
            return method(request, timeout=effective, metadata=self.metadata())
        except grpc.RpcError as exc:
            raise errors.from_rpc_error(exc) from None

    def stream_call(self, method: Any, request: Any, timeout: Optional[float] = None) -> Any:
        """Open a server stream. Errors surface while iterating it."""
        try:
            return method(request, timeout=timeout, metadata=self.metadata())
        except grpc.RpcError as exc:
            raise errors.from_rpc_error(exc) from None

    def close(self) -> None:
        """Close the channel. The server keeps its conversations."""
        self._channel.close()

    def __enter__(self) -> "Client":
        return self

    def __exit__(self, *_exc: Any) -> None:
        self.close()

    def __repr__(self) -> str:
        return f"Client(target={self.target!r})"

    # -- the server --------------------------------------------------------

    def health(self) -> Health:
        """Liveness, version and uptime. Works without a token."""
        return Health.from_proto(self.rpc(self._server.HealthCheck, pb.HealthCheckRequest()))

    def info(self) -> ServerInfo:
        """What this server is: model, working directory, providers, features."""
        return ServerInfo.from_proto(self.rpc(self._server.GetServerInfo, pb.GetServerInfoRequest()))

    def models(self) -> List[ModelInfo]:
        """Every model of the provider registry."""
        response = self.rpc(self._server.GetModels, pb.GetModelsRequest())
        return [ModelInfo.from_proto(model) for model in response.models]

    def providers(self) -> List[ProviderInfo]:
        """The providers, and whether this server has a credential for each."""
        response = self.rpc(self._server.GetModels, pb.GetModelsRequest())
        return [ProviderInfo.from_proto(provider) for provider in response.providers]

    def skills(self, source: str = "", *, include_content: bool = False) -> List[Skill]:
        """The skills of the runtime, optionally with their content."""
        request = pb.ListSkillsRequest(source_filter=source, include_content=include_content)
        response = self.rpc(self._server.ListSkills, request)
        return [Skill.from_proto(skill) for skill in response.skills]

    def skill(self, name: str, *, include_content: bool = True) -> Skill:
        """One skill, by name; raises :class:`seshat.errors.NotFound`."""
        request = pb.GetSkillDetailsRequest(name=name, include_content=include_content)
        response = self.rpc(self._server.GetSkillDetails, request)
        return Skill.from_proto(response.skill)

    def mcp_servers(self) -> List[MCPServer]:
        """The MCP servers the server knows and their status."""
        response = self.rpc(self._server.ListMCP, pb.ListMCPRequest())
        return [MCPServer.from_proto(server) for server in response.servers]

    def connect_mcp(
        self,
        name: str,
        *,
        type: str = "stdio",
        command: str = "",
        args: Optional[Sequence[str]] = None,
        url: str = "",
        env: Optional[Dict[str, str]] = None,
        timeout: int = 0,
        persist: bool = True,
    ) -> Optional[MCPServer]:
        """Connect an MCP server.

        A ``stdio`` server is a command line on the server's host: the server
        refuses it unless it runs with ``SESHAT_GRPC_ALLOW_STDIO_MCP=true``.
        """
        request = pb.ConnectMCPRequest(
            name=name,
            type=type,
            command=command,
            args=list(args or ()),
            url=url,
            env=dict(env or {}),
            timeout=timeout,
            persist=persist,
        )
        response = self.rpc(self._server.ConnectMCP, request, timeout=120)
        if not response.success:
            raise errors.SeshatError(response.error or f"could not connect {name}")
        return MCPServer.from_proto(response.server) if response.HasField("server") else None

    def disconnect_mcp(self, name: str) -> None:
        """Disconnect a running MCP server."""
        response = self.rpc(self._server.DisconnectMCP, pb.DisconnectMCPRequest(name=name))
        if not response.success:
            raise errors.SeshatError(response.error or f"could not disconnect {name}")

    def reconnect_mcp(self, name: str) -> Optional[MCPServer]:
        """Reconnect a configured MCP server."""
        response = self.rpc(self._server.ReconnectMCP, pb.ReconnectMCPRequest(name=name), timeout=120)
        if not response.success:
            raise errors.SeshatError(response.error or f"could not reconnect {name}")
        return MCPServer.from_proto(response.server) if response.HasField("server") else None

    # -- sessions ----------------------------------------------------------

    def create_session(
        self,
        *,
        model: str = "",
        max_tokens: int = 0,
        api_key: str = "",
        working_dir: str = "",
        permission_mode: str = "",
        system_prompt_template: str = "",
        append_system_prompt: str = "",
        tools: Optional[Sequence[str]] = None,
        max_iterations: int = 0,
        max_turns: int = 0,
        turn_token_budget: int = 0,
        max_consecutive_denials: int = 0,
        auto_compact: Optional[bool] = None,
        enable_tool_search: Optional[bool] = None,
    ) -> Session:
        """Open a conversation: describe the agent, not just name a model.

        ``system_prompt_template`` replaces the default system prompt and
        ``append_system_prompt`` adds to it; ``tools`` restricts the agent to a
        set of tool names; ``working_dir`` is the folder its file and shell tools
        work in. The limits (``max_iterations``, ``max_turns``,
        ``turn_token_budget``, ``max_consecutive_denials``) and the two booleans
        (``auto_compact``, ``enable_tool_search``) are optional: left at ``0`` /
        ``None``, the server's own configuration decides.

        ``permission_mode`` defaults to the server's choice for a network client,
        ``onRequest``: the server asks, through a permission request, and this
        client decides. Pass ``bypass``, ``auto``, ``acceptEdits``, ``never`` or
        ``granular`` for another rule.
        """
        request = pb.CreateSessionRequest(
            model=model,
            max_tokens=max_tokens,
            api_key=api_key,
            working_dir=working_dir,
            permission_mode=permission_mode,
            system_prompt_template=system_prompt_template,
            append_system_prompt=append_system_prompt,
            tools=list(tools or ()),
            max_iterations=max_iterations,
            max_turns=max_turns,
            turn_token_budget=turn_token_budget,
            max_consecutive_denials=max_consecutive_denials,
        )
        # The two booleans are `optional` on the wire, so that "off" and "unset"
        # are different answers: only send them when the caller said.
        if auto_compact is not None:
            request.auto_compact = auto_compact
        if enable_tool_search is not None:
            request.enable_tool_search = enable_tool_search
        snapshot = SessionSnapshot.from_proto(self.rpc(self._server.CreateSession, request))
        return Session(self, snapshot.id, snapshot)

    def session(self, session_id: str) -> Session:
        """A handle on a conversation by id, without a call.

        The first method used on it attaches the conversation, loading it from
        its stored transcript when this server process does not hold it.
        """
        return Session(self, session_id)

    def get_session(self, session_id: str) -> Session:
        """Open an existing conversation now, and return its handle."""
        request = pb.GetSessionRequest(session_id=session_id)
        snapshot = SessionSnapshot.from_proto(self.rpc(self._server.GetSession, request))
        return Session(self, snapshot.id, snapshot)

    def sessions(
        self,
        *,
        limit: int = 50,
        offset: int = 0,
        include_subagent: bool = False,
    ) -> List[SessionInfo]:
        """Stored conversations, most recently updated first."""
        request = pb.ListSessionsRequest(limit=limit, offset=offset, include_subagent=include_subagent)
        response = self.rpc(self._server.ListSessions, request)
        return [SessionInfo.from_proto(info) for info in response.sessions]

    def search_sessions(self, query: str, *, limit: int = 20) -> SearchResult:
        """Conversations whose stored transcript contains ``query``."""
        request = pb.SearchSessionsRequest(query=query, limit=limit)
        response = self.rpc(self._server.SearchSessions, request)
        return SearchResult(
            ids=list(response.session_ids),
            sessions=[SessionInfo.from_proto(info) for info in response.sessions],
        )

    def delete_session(self, session_id: str) -> None:
        """Delete a conversation and the artifacts it stored."""
        self.rpc(self._server.DeleteSession, pb.DeleteSessionRequest(session_id=session_id))

    # -- memory ------------------------------------------------------------

    def memory(self, user_id: str = "") -> Memory:
        """The long-term memory of a user (blank for the shared one)."""
        return Memory(self, user_id)

    # -- workflows ---------------------------------------------------------

    def validate_workflow(self, definition: str, *, format: str = "") -> WorkflowValidation:
        """Check a workflow definition written as YAML or JSON."""
        request = pb.WorkflowDefinitionRequest(definition=definition, format=format)
        return WorkflowValidation.from_proto(self.rpc(self._workflow.ValidateWorkflow, request))

    def draft_workflow(self, definition: str, *, format: str = "") -> str:
        """Normalise a definition and render it back in ``format``."""
        response = self.rpc(
            self._workflow.DraftWorkflow,
            pb.DraftWorkflowRequest(definition=definition, format=format),
        )
        return response.rendered

    def run_workflow(self, definition: str, *, max_parallel: int = 0, model: str = "") -> WorkflowRun:
        """Run a workflow: each node is a turn of an agent, run when its needs
        have succeeded."""
        request = pb.RunWorkflowRequest(definition=definition, max_parallel=max_parallel, model=model)
        return WorkflowRun.from_proto(self.rpc(self._workflow.RunWorkflow, request, timeout=None))


def connect(target: Optional[str] = None, **kwargs: Any) -> Client:
    """Make a client; the same as :class:`Client`, as a function.

        with seshat.connect() as client:
            ...
    """
    return Client(target, **kwargs)
