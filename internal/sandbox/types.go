package sandbox

// EnvironmentKind describes where a tool executes.
// The policy layer should know this, even if the actual backend runtime
// (local process, docker, remote host) is implemented elsewhere.
type EnvironmentKind string

const (
	EnvironmentLocal  EnvironmentKind = "local"
	EnvironmentDocker EnvironmentKind = "docker"
	EnvironmentRemote EnvironmentKind = "remote"
	// EnvironmentSandboxed runs commands through the seshat-sandboxd sidecar,
	// the Rust half of the runtime. Unlike EnvironmentLocal it has an OS-level
	// boundary, and unlike EnvironmentDocker it needs no daemon: the sidecar is
	// a process the runtime starts and stops with the session.
	EnvironmentSandboxed EnvironmentKind = "sandboxd"
)

// AccessKind describes the resource access being requested.
type AccessKind string

const (
	AccessRead     AccessKind = "read"
	AccessWrite    AccessKind = "write"
	AccessCreate   AccessKind = "create"
	AccessDelete   AccessKind = "delete"
	AccessSearch   AccessKind = "search"
	AccessExecute  AccessKind = "execute"
	AccessEscalate AccessKind = "escalate"
)

// Decision is the normalized outcome of a sandbox/policy check.
type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionAsk   Decision = "ask"
	DecisionDeny  Decision = "deny"
)

// DecisionResult is the normalized output of a policy decision.
type DecisionResult struct {
	Decision Decision
	Reason   string
}

// PathDecision includes the resolved path for filesystem checks.
type PathDecision struct {
	DecisionResult
	ResolvedPath string
}
