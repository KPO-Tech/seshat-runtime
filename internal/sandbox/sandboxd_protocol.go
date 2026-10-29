package sandbox

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// Wire protocol of the seshat-sandboxd sidecar (the Rust half of the sandbox).
//
// A frame is a 4-byte big-endian length followed by exactly that many bytes of
// UTF-8 JSON. JSON rather than protobuf on purpose: this boundary carries shell
// output, which is arbitrary bytes and therefore base64 in the payloads, and the
// protocol has to stay readable with a pipe and a hex dump when a sandbox
// refuses a command at 2 a.m. The price is a schema written twice — here and in
// rust/sandboxd/src/frame.rs — paid for by docs/sandboxd-protocol.md and by the
// shared testdata frames both sides must parse identically.
//
// Adding a message means touching both sides and bumping sandboxdProtocolMinor;
// sandboxdProtocolMajor only moves when an old peer would misread a frame
// instead of rejecting it.
const (
	sandboxdProtocolMajor = 1
	sandboxdProtocolMinor = 0

	sandboxdTypeHello    = "hello"
	sandboxdTypeHelloAck = "hello_ack"
	sandboxdTypeRun      = "run"
	sandboxdTypeChunk    = "chunk"
	sandboxdTypeExit     = "exit"
	sandboxdTypeCancel   = "cancel"
	sandboxdTypeError    = "error"

	sandboxdStreamStdout = "stdout"
	sandboxdStreamStderr = "stderr"

	// sandboxdLevelFull means every confinement asked for is applied;
	// sandboxdLevelDegraded means a subset is, and the reasons are named;
	// sandboxdLevelNone means nothing is confined and execution must be refused.
	sandboxdLevelFull     = "full"
	sandboxdLevelDegraded = "degraded"
	sandboxdLevelNone     = "none"

	// sandboxdMaxFrameBytes bounds what a peer can make this process allocate.
	sandboxdMaxFrameBytes = 8 << 20
)

// sandboxdFrame is one protocol message. Exactly one payload is set, named after
// Type. Unknown fields are rejected on decode: a peer sending a field this side
// does not know may also mean something different by the ones it does.
type sandboxdFrame struct {
	Type     string             `json:"type"`
	Hello    *sandboxdHello     `json:"hello,omitempty"`
	HelloAck *sandboxdHelloAck  `json:"hello_ack,omitempty"`
	Run      *sandboxdRun       `json:"run,omitempty"`
	Chunk    *sandboxdChunk     `json:"chunk,omitempty"`
	Exit     *sandboxdExit      `json:"exit,omitempty"`
	Cancel   *sandboxdCancel    `json:"cancel,omitempty"`
	Error    *sandboxdErrorBody `json:"error,omitempty"`
}

type sandboxdHello struct {
	ProtocolMajor uint32 `json:"protocol_major"`
	ProtocolMinor uint32 `json:"protocol_minor"`
	ClientVersion string `json:"client_version"`
}

type sandboxdHelloAck struct {
	ProtocolMajor uint32   `json:"protocol_major"`
	ProtocolMinor uint32   `json:"protocol_minor"`
	ServerVersion string   `json:"server_version"`
	Level         string   `json:"level"`
	Capabilities  []string `json:"capabilities"`
	// DegradedReasons is empty when Level is full, and names every missing
	// capability otherwise. A sandbox that silently assumes it is confining
	// is worse than no sandbox: it changes what people believe.
	DegradedReasons []string `json:"degraded_reasons"`
}

type sandboxdRun struct {
	RunID string `json:"run_id"`
	// Command is a shell command string, not an argv: the tools issue shell
	// commands, and Shell says which interpreter runs it.
	Command        string            `json:"command"`
	Shell          []string          `json:"shell"`
	WorkDir        string            `json:"work_dir"`
	Env            map[string]string `json:"env"`
	AllowNetwork   bool              `json:"allow_network"`
	TimeoutMS      uint64            `json:"timeout_ms"`
	MaxOutputBytes uint64            `json:"max_output_bytes"`
}

type sandboxdChunk struct {
	RunID  string `json:"run_id"`
	Stream string `json:"stream"`
	// Data is standard base64: shell output is not necessarily valid UTF-8.
	Data string `json:"data"`
}

type sandboxdExit struct {
	RunID       string   `json:"run_id"`
	ExitCode    int32    `json:"exit_code"`
	DurationMS  uint64   `json:"duration_ms"`
	TimedOut    bool     `json:"timed_out"`
	Truncated   bool     `json:"truncated"`
	StdoutBytes uint64   `json:"stdout_bytes"`
	StderrBytes uint64   `json:"stderr_bytes"`
	Violations  []string `json:"violations"`
}

type sandboxdCancel struct {
	RunID string `json:"run_id"`
}

type sandboxdErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeSandboxdFrame writes one length-prefixed frame.
func writeSandboxdFrame(w io.Writer, frame *sandboxdFrame) error {
	body, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("sandboxd: encode %s frame: %w", frame.Type, err)
	}
	if len(body) > sandboxdMaxFrameBytes {
		return fmt.Errorf("sandboxd: %s frame is %d bytes, over the %d limit", frame.Type, len(body), sandboxdMaxFrameBytes)
	}
	out := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(out[:4], uint32(len(body)))
	copy(out[4:], body)
	if _, err := w.Write(out); err != nil {
		return fmt.Errorf("sandboxd: write %s frame: %w", frame.Type, err)
	}
	return nil
}

// readSandboxdFrame reads one length-prefixed frame. io.EOF means the sidecar
// closed its stdout, which is how a clean shutdown looks.
func readSandboxdFrame(r io.Reader) (*sandboxdFrame, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > sandboxdMaxFrameBytes {
		return nil, fmt.Errorf("sandboxd: frame length %d outside 1..%d", size, sandboxdMaxFrameBytes)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("sandboxd: truncated frame body: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var frame sandboxdFrame
	if err := dec.Decode(&frame); err != nil {
		return nil, fmt.Errorf("sandboxd: decode %q frame: %w", string(body), err)
	}
	return &frame, nil
}
