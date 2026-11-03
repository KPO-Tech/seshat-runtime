package sdk

import "github.com/KPO-Tech/seshat-runtime/internal/sandbox"

// RemoteExecutor lets a host application (e.g. seshat-backend, driven by
// Electron's terminal relay) route a session's bash-tool execution through
// an external channel instead of the engine's own local subprocess — see
// Session.SetRemoteExecutor. Implementations satisfy
// internal/sandbox.Executor; this alias is the exported SDK surface for it.
type RemoteExecutor = sandbox.Executor

// RemoteExecRequest is the input to a RemoteExecutor.Run call.
type RemoteExecRequest = sandbox.RunRequest

// RemoteExecResult is the output of a RemoteExecutor.Run call.
type RemoteExecResult = sandbox.RunResult

// RemoteExecKind identifies a RemoteExecutor backend (RemoteExecutor.Kind).
type RemoteExecKind = sandbox.EnvironmentKind

// RemoteExecKindRemote is the Kind a RemoteExecutor implementation should
// report: it isn't the local host (EnvironmentLocal),
// it's an external channel back to the host app.
const RemoteExecKindRemote = sandbox.EnvironmentRemote

// SetRemoteExecutor registers executor as this session's remote execution
// backend — subsequent bash-tool calls in this session run through it
// instead of a local subprocess (see bash.Tool.executeCommand). A
// re-registration replaces any previous executor for the session.
func (s *Session) SetRemoteExecutor(executor RemoteExecutor) {
	if s == nil || s.session == nil {
		return
	}
	sandbox.RegisterRemoteExecutor(string(s.GetID()), executor)
}

// ClearRemoteExecutor detaches this session's remote execution backend —
// subsequent bash-tool calls fall back to local execution.
func (s *Session) ClearRemoteExecutor() {
	if s == nil || s.session == nil {
		return
	}
	sandbox.UnregisterRemoteExecutor(string(s.GetID()))
}
