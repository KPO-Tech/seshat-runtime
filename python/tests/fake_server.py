"""A scripted Seshat server, for the tests.

It implements the same services as ``cmd/grpc`` with fixed answers: no engine, no
provider, no network. The point is to exercise the client over the real wire
format, including the permission and prompt round trips, which is where a client
usually breaks.
"""

from __future__ import annotations

import threading
from concurrent import futures
from typing import Any, Dict, List, Optional

import grpc
from google.protobuf import timestamp_pb2

from seshat._proto import seshat_pb2 as pb
from seshat._proto import seshat_pb2_grpc as pb_grpc


def _from_value(value: Any) -> Any:
    """Read back a ``google.protobuf.Value`` as a Python value."""
    kind = value.WhichOneof("kind")
    if kind in (None, "null_value"):
        return None
    if kind == "struct_value":
        return {key: _from_value(item) for key, item in value.struct_value.fields.items()}
    if kind == "list_value":
        return [_from_value(item) for item in value.list_value.values]
    return getattr(value, kind)


def _now() -> timestamp_pb2.Timestamp:
    stamp = timestamp_pb2.Timestamp()
    stamp.GetCurrentTime()
    return stamp


class Pending:
    """One request waiting for the client's answer."""

    def __init__(self) -> None:
        self.event = threading.Event()
        self.decision = ""
        self.reason = ""
        self.value: Any = None
        self.cancelled = False


class FakeState:
    """What the fake server remembers between calls."""

    def __init__(self) -> None:
        self.sessions: Dict[str, pb.Session] = {}
        self.pending: Dict[str, Pending] = {}
        self.permission_mode = ""
        self.working_directory = ""
        self.closed: List[str] = []
        self.interrupted: List[str] = []
        self.deleted: List[str] = []
        self.memory: List[pb.MemoryEntity] = []
        self.create_request: Optional[pb.CreateSessionRequest] = None

    def session(self, session_id: str) -> pb.Session:
        found = self.sessions.get(session_id)
        if found is None:
            found = pb.Session(
                id=session_id,
                title="untitled_session_1",
                status="active",
                model="fake:model",
                working_dir="/tmp/project",
                permission_mode="onRequest",
                execution_mode="execute",
                tools=["read_file", "write_file"],
                created_at=_now(),
                updated_at=_now(),
            )
            self.sessions[session_id] = found
        return found


class FakeSeshat(pb_grpc.SeshatServiceServicer):
    def __init__(self, state: FakeState) -> None:
        self.state = state

    def HealthCheck(self, request, context):
        return pb.HealthCheckResponse(status="ok", version="1.16.0", uptime="1m0s", uptime_seconds=60)

    def GetServerInfo(self, request, context):
        return pb.GetServerInfoResponse(
            version="1.16.0",
            uptime_seconds=60,
            model="anthropic:claude-sonnet-5-5",
            working_dir="/tmp/project",
            permission_mode="onRequest",
            sandbox_kind="local",
            providers_configured=["anthropic"],
            providers_available=["anthropic", "openai"],
            memory_enabled=True,
            hooks_enabled=True,
            monitoring_enabled=True,
            session_persistence_enabled=True,
            auth_required=False,
            active_sessions=len(self.state.sessions),
        )

    def GetModels(self, request, context):
        return pb.GetModelsResponse(
            models=[
                pb.ModelInfo(
                    id="claude-sonnet-5-5",
                    name="Claude Sonnet 5.5",
                    provider="anthropic",
                    max_tokens=64000,
                    context_window=200000,
                ),
                pb.ModelInfo(id="gpt-5", name="GPT-5", provider="openai", max_tokens=32000),
            ],
            providers=[
                pb.ProviderInfo(name="anthropic", configured=True, model_count=1),
                pb.ProviderInfo(name="openai", configured=False, model_count=1),
            ],
        )

    def ListSkills(self, request, context):
        skills = [
            pb.Skill(name="commit", description="Write a commit", source="bundled", when_to_use="before a commit"),
            pb.Skill(name="review", description="Review a diff", source="file"),
        ]
        if request.include_content:
            skills = [
                pb.Skill(
                    name=skill.name,
                    description=skill.description,
                    source=skill.source,
                    when_to_use=skill.when_to_use,
                    content="# body",
                )
                for skill in skills
            ]
        if request.source_filter and request.source_filter != "all":
            skills = [skill for skill in skills if skill.source == request.source_filter]
        return pb.ListSkillsResponse(skills=skills, total_count=len(skills))

    def GetSkillDetails(self, request, context):
        if request.name != "commit":
            context.abort(grpc.StatusCode.NOT_FOUND, f"skill {request.name!r} not found")
        skill = pb.Skill(
            name="commit",
            description="Write a commit",
            source="bundled",
            allowed_tools=["bash"],
            content="# Commit\n\nWrite a good message.",
            path="/skills/commit",
        )
        return pb.GetSkillDetailsResponse(skill=skill, prompt=skill.content)

    def ListMCP(self, request, context):
        return pb.ListMCPResponse(
            servers=[pb.MCPServer(name="files", status="connected", tool_count=3, type="http")],
            total_count=1,
        )

    def ConnectMCP(self, request, context):
        if request.type == "stdio":
            context.abort(grpc.StatusCode.PERMISSION_DENIED, "a stdio MCP server is a command line")
        return pb.ConnectMCPResponse(
            success=True,
            server=pb.MCPServer(name=request.name, status="connected", type=request.type),
        )

    def DisconnectMCP(self, request, context):
        return pb.DisconnectMCPResponse(success=True)

    def ReconnectMCP(self, request, context):
        return pb.ReconnectMCPResponse(
            success=True,
            server=pb.MCPServer(name=request.name, status="connected"),
        )

    def CreateSession(self, request, context):
        self.state.create_request = request
        session = pb.Session(
            id=f"s{len(self.state.sessions) + 1}",
            title=f"untitled_session_{len(self.state.sessions) + 1}",
            status="active",
            model=request.model or "fake:model",
            working_dir=request.working_dir or "/tmp/project",
            permission_mode=request.permission_mode or "onRequest",
            execution_mode="execute",
            tools=["read_file", "write_file"],
            created_at=_now(),
            updated_at=_now(),
        )
        self.state.sessions[session.id] = session
        return session

    def ListSessions(self, request, context):
        infos = [
            pb.SessionInfo(
                id=session.id,
                title=session.title,
                status=session.status,
                created_at=session.created_at,
                updated_at=session.updated_at,
                total_turns=2,
                total_tokens=1234,
                preview="hello",
            )
            for session in self.state.sessions.values()
        ]
        return pb.ListSessionsResponse(sessions=infos, total_count=len(infos))

    def GetSession(self, request, context):
        if request.session_id not in self.state.sessions:
            context.abort(grpc.StatusCode.NOT_FOUND, f"session {request.session_id!r} not found")
        return self.state.sessions[request.session_id]

    def SearchSessions(self, request, context):
        ids = [session.id for session in self.state.sessions.values()]
        return pb.SearchSessionsResponse(session_ids=ids)

    def DeleteSession(self, request, context):
        self.state.deleted.append(request.session_id)
        self.state.sessions.pop(request.session_id, None)
        return pb.DeleteSessionResponse(success=True)


class FakeSessions(pb_grpc.SessionServiceServicer):
    """The session service, with a scripted turn.

    A turn sends ``turn_started``, two chunks, and then either a permission
    request (when the client asked for one) or a prompt request, waits for the
    answer, and completes.
    """

    def __init__(self, state: FakeState) -> None:
        self.state = state

    def _result(self, session_id: str, content: str) -> pb.TurnResult:
        return pb.TurnResult(
            session_id=session_id,
            content=content,
            messages=[
                pb.Message(
                    id="m1",
                    role="assistant",
                    content=[pb.ContentBlock(type="text", text=pb.TextBlock(text=content))],
                )
            ],
            usage=pb.TokenUsage(input_tokens=10, output_tokens=5, total_tokens=15),
            stop_reason="end_turn",
            turn_number=1,
            is_complete=True,
            total_tokens=15,
        )

    def SubmitMessage(self, request, context):
        return self._result(request.session_id, "hello from the fake engine")

    def SubmitMessageStream(self, request, context):
        """A turn scripted by the message it carries.

        * ``delete ...``: a tool call that needs approval.
        * ``ask ...``: a tool that asks the user a question.
        * anything else: two chunks and a result.
        """
        session_id = request.session_id
        text = (request.text or "").lower()
        sequence = 0

        def event(event_type: str, **payload) -> pb.SessionEvent:
            nonlocal sequence
            sequence += 1
            return pb.SessionEvent(
                event_id=f"e{sequence}",
                session_id=session_id,
                sequence=sequence,
                type=event_type,
                turn_number=1 if sequence > 1 else 0,
                **payload,
            )

        yield event(
            "turn.started",
            turn_started=pb.TurnStarted(turn_number=1, turn_id="t1", model="fake:model"),
        )
        yield event(
            "response.chunk",
            chunk=pb.ChunkDelta(type="content_block_delta", delta_type="text_delta", delta="Hello"),
        )

        content = "hello from the fake engine"

        if "delete" in text:
            # An approval: the client answers with another RPC while this stream
            # is open, which is the round trip worth testing.
            pending = Pending()
            self.state.pending["perm-1"] = pending
            yield event(
                "tool.permission_required",
                permission_request=pb.PermissionRequest(
                    request_id="perm-1",
                    session_id=session_id,
                    tool_name="delete_branch",
                    description="Allow tool 'delete_branch'?",
                    tool_input={"branch": "stale"},
                ),
            )
            if not pending.event.wait(timeout=5):
                context.abort(grpc.StatusCode.DEADLINE_EXCEEDED, "the client did not answer")
            if pending.decision == "deny":
                yield event(
                    "tool.progress",
                    tool_progress=pb.ToolProgress(
                        tool_name="delete_branch", stage="failed", message=f"denied: {pending.reason}"
                    ),
                )
            else:
                yield event(
                    "tool.progress",
                    tool_progress=pb.ToolProgress(
                        tool_name="delete_branch",
                        stage="completed",
                        message="done",
                        percent_complete=100,
                    ),
                )
            content = f"Hello world ({pending.decision})"

        elif "ask" in text:
            pending = Pending()
            self.state.pending["ask-1"] = pending
            yield event(
                "prompt.request",
                prompt_request=pb.PromptRequest(
                    request_id="ask-1",
                    session_id=session_id,
                    type="text",
                    message="Which branch should I keep?",
                    metadata={"hint": "main"},
                ),
            )
            if not pending.event.wait(timeout=5):
                context.abort(grpc.StatusCode.DEADLINE_EXCEEDED, "the client did not answer")
            if pending.cancelled:
                yield event(
                    "turn.failed",
                    turn_failed=pb.TurnFailed(code="CANCELLED", message="the question was dismissed"),
                )
                return
            content = f"Hello world ({pending.value})"

        yield event(
            "response.chunk",
            chunk=pb.ChunkDelta(type="content_block_delta", delta_type="text_delta", delta=" world"),
        )
        yield event(
            "turn.completed",
            turn_completed=pb.TurnCompleted(result=self._result(session_id, content)),
        )

    def Subscribe(self, request, context):
        yield pb.SessionEvent(event_id="s1", session_id=request.session_id, sequence=1, type="turn.started")

    def GetMessages(self, request, context):
        return pb.GetMessagesResponse(messages=[pb.Message(id="m1", role="user")])

    def GetHistory(self, request, context):
        return pb.GetHistoryResponse(
            archived=[pb.Message(id="m0", role="user")],
            current=[pb.Message(id="m1", role="assistant")],
        )

    def GetState(self, request, context):
        session = self.state.session(request.session_id)
        return pb.SessionState(
            metadata=pb.SessionMetadata(
                id=session.id, title=session.title, status=session.status, model=session.model
            ),
            permission_mode=self.state.permission_mode or session.permission_mode,
            execution_mode="execute",
            turn_number=1,
            total_tokens=15,
            tools=session.tools,
            auto_mode_available=True,
        )

    def Interrupt(self, request, context):
        self.state.interrupted.append(request.session_id)
        return pb.Ack()

    def Compact(self, request, context):
        return pb.CompactResponse(pre_tokens=900, post_tokens=300)

    def Close(self, request, context):
        self.state.closed.append(request.session_id)
        return pb.Ack()

    def SetPermissionMode(self, request, context):
        self.state.permission_mode = request.mode
        return pb.Ack()

    def SetWorkingDirectory(self, request, context):
        self.state.working_directory = request.path
        return pb.Ack()

    def SetSystemPrompt(self, request, context):
        return pb.Ack()

    def SetPlanMode(self, request, context):
        return pb.Ack()

    def ApprovePlan(self, request, context):
        return pb.Ack()

    def ListTools(self, request, context):
        return pb.ListToolsResponse(
            tools=[
                pb.ToolInfo(name="read_file", description="Read a file", enabled=True, category="file"),
                pb.ToolInfo(name="write_file", description="Write a file", enabled=False, deferred=True),
            ],
            total_count=2,
        )

    def SetTools(self, request, context):
        return pb.Ack()

    def RespondPermission(self, request, context):
        pending = self.state.pending.get(request.request_id)
        if pending is None:
            context.abort(grpc.StatusCode.NOT_FOUND, "no permission request is waiting with this id")
        pending.decision = request.decision
        pending.reason = request.reason
        pending.event.set()
        return pb.Ack()

    def RespondPrompt(self, request, context):
        pending = self.state.pending.get(request.request_id)
        if pending is None:
            context.abort(grpc.StatusCode.NOT_FOUND, "no prompt request is waiting with this id")
        pending.value = _from_value(request.value) if request.HasField("value") else None
        pending.cancelled = request.cancelled
        pending.event.set()
        return pb.Ack()


class FakeMemory(pb_grpc.MemoryServiceServicer):
    def __init__(self, state: FakeState) -> None:
        self.state = state

    def UpsertEntities(self, request, context):
        created = []
        for entity in request.entities:
            stored = pb.MemoryEntity(
                id=f"e{len(self.state.memory) + 1}",
                user_id=request.user_id,
                name=entity.name,
                entity_type=entity.entity_type,
                observations=entity.observations,
            )
            self.state.memory.append(stored)
            created.append(stored)
        return pb.UpsertEntitiesResponse(created=created)

    def AddObservations(self, request, context):
        return pb.AddObservationsResponse(
            results=[
                pb.ObservationResult(entity_name=item.entity_name, added_observations=item.contents)
                for item in request.observations
            ]
        )

    def SearchMemory(self, request, context):
        matches = [
            entity for entity in self.state.memory if request.query.lower() in entity.name.lower()
        ]
        return pb.MemoryGraph(
            entities=matches,
            relations=[pb.MemoryRelation(id="r1", **{"from": "a", "to": "b"}, relation_type="uses")],
        )

    def OpenEntities(self, request, context):
        return pb.MemoryGraph()

    def RetrieveContext(self, request, context):
        return pb.RetrieveContextResponse(context="## Memory\n\nSeshat is a runtime.", found=True)


class FakeWorkflow(pb_grpc.WorkflowServiceServicer):
    def ValidateWorkflow(self, request, context):
        if "nodes" not in request.definition:
            return pb.WorkflowValidationResponse(valid=False, diagnostics="a workflow has at least one node")
        return pb.WorkflowValidationResponse(valid=True, name="demo", node_count=2)

    def DraftWorkflow(self, request, context):
        return pb.DraftWorkflowResponse(rendered="name: demo\nnodes: []\n")

    def RunWorkflow(self, request, context):
        return pb.WorkflowRunResponse(
            success=True,
            results={"one": pb.WorkflowNodeResult(id="one", success=True, output="ok")},
            order=["one"],
        )


class FakeServer:
    """A running fake server on a free port."""

    def __init__(self) -> None:
        self.state = FakeState()
        self._server = grpc.server(futures.ThreadPoolExecutor(max_workers=8))
        pb_grpc.add_SeshatServiceServicer_to_server(FakeSeshat(self.state), self._server)
        pb_grpc.add_SessionServiceServicer_to_server(FakeSessions(self.state), self._server)
        pb_grpc.add_MemoryServiceServicer_to_server(FakeMemory(self.state), self._server)
        pb_grpc.add_WorkflowServiceServicer_to_server(FakeWorkflow(), self._server)
        port = self._server.add_insecure_port("127.0.0.1:0")
        self.target = f"127.0.0.1:{port}"

    def start(self) -> "FakeServer":
        self._server.start()
        return self

    def stop(self) -> None:
        self._server.stop(grace=0).wait()


def start() -> FakeServer:
    return FakeServer().start()
