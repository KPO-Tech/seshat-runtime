//! Wire protocol of `seshat-sandboxd`.
//!
//! A frame is a 4-byte big-endian length followed by exactly that many bytes of
//! UTF-8 JSON. JSON rather than protobuf on purpose: this boundary carries shell
//! output (arbitrary bytes, ANSI escapes, invalid UTF-8), which is why payloads
//! are base64, and the protocol has to be inspectable with a pipe and a hex
//! dump when a sandbox refuses something at 2 a.m. The cost is a schema written
//! twice — here and in `internal/sandbox/sandboxd_protocol.go` — and it is paid
//! for by `docs/sandboxd-protocol.md` plus the shared testdata frames both
//! sides must parse identically.
//!
//! Do not add a message to one side without the other, and bump
//! `PROTOCOL_MINOR` when you do; `PROTOCOL_MAJOR` only moves when an old peer
//! would misread a frame rather than reject it.

use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};

/// Refused at handshake when the two sides disagree: a minor mismatch is usable,
/// a major mismatch is not.
pub const PROTOCOL_MAJOR: u32 = 1;
pub const PROTOCOL_MINOR: u32 = 0;

pub const TYPE_HELLO: &str = "hello";
pub const TYPE_HELLO_ACK: &str = "hello_ack";
pub const TYPE_RUN: &str = "run";
pub const TYPE_CHUNK: &str = "chunk";
pub const TYPE_EXIT: &str = "exit";
pub const TYPE_CANCEL: &str = "cancel";
pub const TYPE_ERROR: &str = "error";

pub const STREAM_STDOUT: &str = "stdout";
pub const STREAM_STDERR: &str = "stderr";

// Values of the protocol that no build emits yet. They are named and spelled
// here from the first version so that adding a backend never renames a token a
// client already matches on.
#[allow(dead_code)]
/// How much isolation is *actually* applied, as opposed to what was requested.
pub const LEVEL_FULL: &str = "full";
pub const LEVEL_DEGRADED: &str = "degraded";
pub const LEVEL_NONE: &str = "none";

pub const CAP_LIMITS: &str = "limits";
#[allow(dead_code)]
pub const CAP_FS_CONFINE: &str = "fs_confine";
#[allow(dead_code)]
pub const CAP_NETWORK_DENY: &str = "network_deny";
#[allow(dead_code)]
pub const CAP_LANDLOCK: &str = "landlock";
#[allow(dead_code)]
pub const CAP_SECCOMP: &str = "seccomp";
#[allow(dead_code)]
pub const CAP_JOB_OBJECT: &str = "job_object";

/// One protocol message. Exactly one payload member is set, named after `kind`.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Frame {
    #[serde(rename = "type")]
    pub kind: String,

    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub hello: Option<Hello>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub hello_ack: Option<HelloAck>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub run: Option<Run>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub chunk: Option<Chunk>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub exit: Option<Exit>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cancel: Option<Cancel>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<ErrorBody>,
}

impl Frame {
    pub fn new(kind: &str) -> Self {
        Self {
            kind: kind.to_string(),
            ..Self::default()
        }
    }

    pub fn hello_ack(ack: HelloAck) -> Self {
        Self {
            hello_ack: Some(ack),
            ..Self::new(TYPE_HELLO_ACK)
        }
    }

    pub fn chunk(chunk: Chunk) -> Self {
        Self {
            chunk: Some(chunk),
            ..Self::new(TYPE_CHUNK)
        }
    }

    pub fn exit(exit: Exit) -> Self {
        Self {
            exit: Some(exit),
            ..Self::new(TYPE_EXIT)
        }
    }

    pub fn error(code: &str, message: &str) -> Self {
        Self {
            error: Some(ErrorBody {
                code: code.to_string(),
                message: message.to_string(),
            }),
            ..Self::new(TYPE_ERROR)
        }
    }
}

/// First frame the client sends.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Hello {
    pub protocol_major: u32,
    pub protocol_minor: u32,
    pub client_version: String,
}

/// Answer to `Hello`. `level` says what this process can enforce on this host,
/// never what the client asked for.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct HelloAck {
    pub protocol_major: u32,
    pub protocol_minor: u32,
    pub server_version: String,
    pub level: String,
    pub capabilities: Vec<String>,
    /// Empty when `level` is `full`; every missing capability is named here, so
    /// a degraded sandbox is a reported fact rather than a silent assumption.
    pub degraded_reasons: Vec<String>,
}

/// A command to execute. `command` is a shell command string, not an argv: the
/// engine's tools issue shell commands, and `shell` says which interpreter runs
/// it (`["/bin/sh", "-c"]` when empty).
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Run {
    pub run_id: String,
    pub command: String,
    pub shell: Vec<String>,
    pub work_dir: String,
    /// Complete environment of the child. The sidecar never inherits its own,
    /// so a variable the client did not name cannot reach the command.
    pub env: BTreeMap<String, String>,
    pub allow_network: bool,
    /// Zero means no deadline.
    pub timeout_ms: u64,
    /// Zero means no cap. A cap that is hit sets `truncated` on the exit frame;
    /// it never ends the command and never hides the real size.
    pub max_output_bytes: u64,
}

/// Streamed output. `data` is standard base64: shell output is arbitrary bytes.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Chunk {
    pub run_id: String,
    pub stream: String,
    pub data: String,
}

/// Terminal frame of a run, sent exactly once and always last.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Exit {
    pub run_id: String,
    pub exit_code: i32,
    pub duration_ms: u64,
    pub timed_out: bool,
    pub truncated: bool,
    /// Real byte counts, including what was cut by `max_output_bytes`.
    pub stdout_bytes: u64,
    pub stderr_bytes: u64,
    /// Confinement attempts that were refused. Empty until a real backend
    /// exists; the field is in the protocol from the start so that adding a
    /// backend never changes the contract.
    pub violations: Vec<String>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Cancel {
    pub run_id: String,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ErrorBody {
    pub code: String,
    pub message: String,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_run_frame_round_trips_through_json() {
        let mut env = BTreeMap::new();
        env.insert("PATH".to_string(), "/usr/bin".to_string());
        let frame = Frame {
            run: Some(Run {
                run_id: "r1".to_string(),
                command: "echo hello".to_string(),
                shell: vec!["/bin/sh".to_string(), "-c".to_string()],
                work_dir: "/tmp".to_string(),
                env,
                allow_network: false,
                timeout_ms: 1000,
                max_output_bytes: 1024,
            }),
            ..Frame::new(TYPE_RUN)
        };

        let json = serde_json::to_string(&frame).expect("serialises");
        assert!(json.starts_with(r#"{"type":"run","run":{"#), "{json}");
        let back: Frame = serde_json::from_str(&json).expect("parses");
        assert_eq!(back.run.expect("run payload").command, "echo hello");
    }

    #[test]
    fn an_unknown_field_is_rejected_rather_than_ignored() {
        // A peer that sends something this side does not know is a peer whose
        // other messages may mean something else too: fail loudly.
        let json = r#"{"type":"hello","hello":{"protocol_major":1,"protocol_minor":0,"client_version":"x"},"surprise":1}"#;
        assert!(serde_json::from_str::<Frame>(json).is_err());
    }
}
