//! `seshat-sandboxd` — the execution sandbox sidecar of the Seshat runtime.
//!
//! The Go engine owns the agent loop, the tools, the providers and the
//! permission *policy* (who approves). This process owns the other half: given a
//! command, a working directory and limits, it runs the command and reports what
//! it did — including, once the OS backends land, every attempt it refused.
//!
//! It never sees a prompt, a model, a token or a session. That is the point: the
//! surface an attacker could reach through it is a command line, not a
//! conversation.

#[cfg(windows)]
mod appcontainer;
mod exec;
mod frame;
mod server;

use std::io::{self, Write};

fn main() {
    // Say once, on the diagnostics channel, whether this host can hand out the
    // identity a confined run needs. It is a probe, not a claim: the sidecar
    // still reports `degraded` until it actually launches a command inside one.
    #[cfg(windows)]
    match appcontainer::probe() {
        Ok(sid) => {
            let _ = writeln!(
                io::stderr(),
                "seshat-sandboxd: appcontainer identity: {sid}"
            );
        }
        Err(err) => {
            let _ = writeln!(
                io::stderr(),
                "seshat-sandboxd: appcontainer unavailable: {err}"
            );
        }
    }

    if let Err(err) = server::serve() {
        // stderr, never stdout: stdout is the protocol channel and a stray
        // message there would be read as a corrupt frame.
        let _ = writeln!(io::stderr(), "seshat-sandboxd: {err}");
        std::process::exit(1);
    }
}
