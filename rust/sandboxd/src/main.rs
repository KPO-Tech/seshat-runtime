//! `seshat-sandboxd` — the execution sandbox sidecar of the Seshat runtime.
//!
//! The Go engine owns the agent loop, the tools, the providers and the
//! permission *policy* (who approves). This process owns the other half: given a
//! command, a working directory and limits, it runs the command inside the
//! identity this host can give it, and reports what it did — including, once the
//! remaining backends land, every attempt it refused.
//!
//! It never sees a prompt, a model, a token or a session. That is the point: the
//! surface an attacker could reach through it is a command line, not a
//! conversation.

#[cfg(windows)]
mod appcontainer;
mod confinement;
mod exec;
mod frame;
mod process;
mod server;

use std::io::{self, Write};

fn main() {
    if let Err(err) = server::serve() {
        // stderr, never stdout: stdout is the protocol channel and a stray
        // message there would be read as a corrupt frame.
        let _ = writeln!(io::stderr(), "seshat-sandboxd: {err}");
        std::process::exit(1);
    }
}
