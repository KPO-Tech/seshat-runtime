"""Tests of the client against a scripted server.

They run without the engine, a provider key or a network: the fake server in
``fake_server.py`` speaks the same gRPC contract, so what is checked here is the
wire behaviour of the client (field names, event parsing, the approval round trip
and error mapping).
"""

from __future__ import annotations

import asyncio

import pytest

import seshat
from seshat import errors

from .fake_server import start


@pytest.fixture(scope="module")
def server():
    running = start()
    try:
        yield running
    finally:
        running.stop()


@pytest.fixture()
def client(server):
    with seshat.connect(server.target) as connected:
        yield connected


def test_health_and_info(client):
    health = client.health()
    assert health.status == "ok"
    assert health.version == "1.16.0"

    info = client.info()
    assert info.model == "anthropic:claude-sonnet-5-5"
    assert info.memory_enabled is True
    assert "anthropic" in info.providers_configured
    assert info.sandbox_kind == "local"


def test_models_and_providers(client):
    models = client.models()
    assert [model.id for model in models] == ["claude-sonnet-5-5", "gpt-5"]
    assert models[0].context_window == 200000

    providers = {provider.name: provider for provider in client.providers()}
    assert providers["anthropic"].configured is True
    assert providers["openai"].configured is False


def test_skills(client):
    skills = client.skills()
    assert [skill.name for skill in skills] == ["commit", "review"]

    detailed = client.skill("commit")
    assert detailed.content.startswith("# Commit")
    assert detailed.allowed_tools == ["bash"]

    with pytest.raises(errors.NotFound):
        client.skill("missing")


def test_mcp(client):
    servers = client.mcp_servers()
    assert servers[0].name == "files"
    assert servers[0].tool_count == 3

    with pytest.raises(errors.PermissionDenied):
        client.connect_mcp("shell", type="stdio", command="bash")


def test_create_session_and_ask(client):
    session = client.create_session(model="anthropic:claude-sonnet-5-5")
    assert session.id == "s1"
    assert session.snapshot.title == "untitled_session_1"

    result = session.ask("hello")
    assert result.content == "hello from the fake engine"
    assert result.usage.total_tokens == 15
    assert result.messages[0].text == "hello from the fake engine"


def test_create_session_applies_agent_options(client, server):
    session = client.create_session(
        model="openai:gpt-5",
        system_prompt_template="You are a reviewer.",
        append_system_prompt="Be brief.",
        tools=["read_file"],
        working_dir="/srv/repo",
        permission_mode="never",
        max_iterations=7,
        max_turns=12,
        turn_token_budget=50000,
        max_consecutive_denials=4,
        auto_compact=False,
        enable_tool_search=True,
    )
    assert session.id

    sent = server.state.create_request
    assert sent.model == "openai:gpt-5"
    assert sent.system_prompt_template == "You are a reviewer."
    assert sent.append_system_prompt == "Be brief."
    assert list(sent.tools) == ["read_file"]
    assert sent.working_dir == "/srv/repo"
    assert sent.permission_mode == "never"
    assert sent.max_iterations == 7
    assert sent.max_turns == 12
    assert sent.turn_token_budget == 50000
    assert sent.max_consecutive_denials == 4
    # An explicit False must travel: "off" and "unset" are different answers.
    assert sent.HasField("auto_compact") is True
    assert sent.auto_compact is False
    assert sent.enable_tool_search is True


# Leaving the optional knobs out must not decide anything for the server.
def test_create_session_leaves_optional_options_unset(client, server):
    client.create_session()
    sent = server.state.create_request
    assert sent.max_iterations == 0
    assert sent.HasField("auto_compact") is False
    assert sent.HasField("enable_tool_search") is False


def test_unary_submit(client):
    session = client.session("s9")
    result = session.submit("hello")
    assert result.stop_reason == "end_turn"
    assert result.is_complete is True


def test_stream_answers_a_permission_request(client, server):
    session = client.session("s2")
    seen = []

    for event in session.stream("delete the stale branch", on_permission=lambda request: "allow"):
        seen.append(event.kind)
        if event.kind == "chunk":
            assert event.text
        assert event.sequence > 0

    assert "permission_request" in seen
    assert seen[-1] == "turn_completed"
    # The client answered the request while the stream was open.
    assert server.state.pending["perm-1"].decision == "allow"


def test_stream_denies_without_a_handler(client, server):
    session = client.session("s3")
    events = list(session.stream("delete the stale branch"))

    assert events[-1].kind == "turn_completed"
    assert events[-1].result.content == "Hello world (deny)"
    assert server.state.pending["perm-1"].decision == "deny"
    assert "no permission handler" in server.state.pending["perm-1"].reason


def test_stream_answers_a_prompt(client, server):
    session = client.session("s4")
    result = None

    for event in session.stream(
        "ask which branch to keep",
        on_permission=lambda request: "allow_session",
        on_prompt=lambda request: "main",
    ):
        if event.kind == "prompt_request":
            assert event.prompt_request.message == "Which branch should I keep?"
            assert event.prompt_request.metadata["hint"] == "main"
        if event.kind == "turn_completed":
            result = event.result

    assert result is not None
    assert "Hello world (main)" == result.content
    assert server.state.pending["ask-1"].value == "main"


def test_stream_cancels_a_prompt_without_a_handler(client):
    session = client.session("s5")
    events = list(session.stream("ask which branch to keep"))

    assert events[-1].kind == "turn_failed"
    assert "dismissed" in events[-1].error.message


def test_turn_failure_is_raised_by_ask(client):
    session = client.session("s6")
    with pytest.raises(errors.SeshatError) as raised:
        session.ask("ask which branch to keep")
    assert raised.value.code == "CANCELLED"


def test_state_tools_and_control(client, server):
    session = client.session("s7")
    state = session.state()
    assert state.tools == ["read_file", "write_file"]
    assert state.auto_mode_available is True

    tools = session.tools()
    assert tools[0].name == "read_file"
    assert tools[1].deferred is True

    assert session.compact() == {"pre_tokens": 900, "post_tokens": 300}

    session.set_permission_mode("bypass")
    assert server.state.permission_mode == "bypass"

    session.set_working_directory("/tmp/other")
    assert server.state.working_directory == "/tmp/other"

    session.interrupt()
    assert server.state.interrupted == ["s7"]

    session.close()
    assert server.state.closed == ["s7"]


def test_messages_and_history(client):
    session = client.session("s8")
    assert session.messages()[0].id == "m1"
    history = session.history()
    assert history["archived"][0].id == "m0"
    assert history["current"][0].role == "assistant"


def test_sessions_and_search(client):
    client.create_session()
    sessions = client.sessions()
    assert sessions and sessions[0].title.startswith("untitled_session")

    found = client.search_sessions("hello")
    assert len(found.ids) >= 1

    client.delete_session("s1")


def test_not_found_maps_to_a_python_error(client):
    with pytest.raises(errors.NotFound):
        client.get_session("does-not-exist")


def test_memory(client):
    memory = client.memory()
    created = memory.upsert_entities(
        [{"name": "Seshat", "entity_type": "project", "observations": ["an agent runtime"]}]
    )
    assert created[0].name == "Seshat"

    graph = memory.search("Seshat")
    assert graph.entities[0].observations == ["an agent runtime"]
    assert graph.relations[0].source == "a"

    assert "runtime" in memory.context("what is Seshat?")

    added = memory.add_observations([{"entity_name": "Seshat", "contents": ["Go"]}])
    assert added[0].added_observations == ["Go"]


def test_workflows(client):
    assert client.validate_workflow("nodes: []").valid is True
    assert client.validate_workflow("name: demo").valid is False

    run = client.run_workflow("name: demo\nnodes: []")
    assert run.success is True
    assert run.results["one"].output == "ok"


def test_async_client(server):
    async def run() -> str:
        async with seshat.aio.connect(server.target) as client:
            health = await client.health()
            assert health.status == "ok"
            session = await client.create_session()
            result = await session.ask("hello")
            return result.content

    assert asyncio.run(run()) == "hello from the fake engine"


def test_async_stream(server):
    async def run() -> list:
        async with seshat.aio.connect(server.target) as client:
            session = await client.session("as1")
            kinds = []
            async for event in session.stream(
                "delete the stale branch",
                on_permission=lambda request: "allow",
            ):
                kinds.append(event.kind)
            return kinds

    kinds = asyncio.run(run())
    assert kinds[-1] == "turn_completed"
    assert "permission_request" in kinds


def test_unauthenticated_metadata_is_attached(server):
    with seshat.connect(server.target, token="secret") as client:
        assert client.metadata() == (("authorization", "Bearer secret"),)


def test_default_target_reads_the_environment(monkeypatch):
    monkeypatch.setenv("SESHAT_GRPC_TARGET", "example:1234")
    assert seshat.default_target() == "example:1234"

    monkeypatch.delenv("SESHAT_GRPC_TARGET")
    monkeypatch.setenv("SESHAT_GRPC_HOST", "0.0.0.0")
    monkeypatch.setenv("SESHAT_GRPC_PORT", "5555")
    assert seshat.default_target() == "127.0.0.1:5555"
