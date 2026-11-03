package sandbox

// EnvironmentKind describes where a tool executes.
// The policy layer should know this, even if the actual backend runtime
// (local process, sidecar, microVM, remote host) is implemented elsewhere.
type EnvironmentKind string

const (
	EnvironmentLocal  EnvironmentKind = "local"
	EnvironmentRemote EnvironmentKind = "remote"
	// EnvironmentSandboxed runs commands through the seshat-sandboxd sidecar,
	// the Rust half of the runtime. Unlike EnvironmentLocal it has an OS-level
	// boundary, with no daemon: the sidecar is
	// a process the runtime starts and stops with the session.
	EnvironmentSandboxed EnvironmentKind = "sandboxd"
	// EnvironmentMicroVM runs commands in a Linux microVM per project, through the msb
	// command line. See docs/adr/0003-microvm-environment.md.
	EnvironmentMicroVM EnvironmentKind = "microvm"
)
