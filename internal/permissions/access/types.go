package access

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
