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
#[cfg(windows)]
mod project;
mod server;

use std::io::{self, Write};

fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let command = match args.first().map(String::as_str) {
        Some("protect") => Some(protect as fn(&[String]) -> io::Result<()>),
        Some("unprotect") => Some(unprotect as fn(&[String]) -> io::Result<()>),
        _ => None,
    };
    if let Some(command) = command {
        if let Err(err) = command(&args[1..]) {
            let _ = writeln!(io::stderr(), "seshat-sandboxd: {err}");
            std::process::exit(1);
        }
        return;
    }
    if let Err(err) = server::serve() {
        // stderr, never stdout: stdout is the protocol channel and a stray
        // message there would be read as a corrupt frame.
        let _ = writeln!(io::stderr(), "seshat-sandboxd: {err}");
        std::process::exit(1);
    }
}

/// `seshat-sandboxd protect <capability-sid> <path>...`: writes the deny ACEs of the
/// `project` profile ahead of a session. A session's handshake is bounded, and a first
/// protection walks every file below the path, so a large folder is protected here, when
/// the client can wait and say what failed. A session then finds it done.
#[cfg(windows)]
fn protect(args: &[String]) -> io::Result<()> {
    let Some((sid, paths)) = args.split_first() else {
        return Err(io::Error::other(
            "usage: protect <capability-sid> <path>...",
        ));
    };
    for path in paths {
        project::protect(sid, std::path::Path::new(path))
            .map_err(|err| io::Error::other(format!("could not protect {path}: {err}")))?;
    }
    Ok(())
}

#[cfg(not(windows))]
fn protect(_args: &[String]) -> io::Result<()> {
    Err(io::Error::other(
        "nothing can be protected on this platform",
    ))
}

/// `seshat-sandboxd unprotect <capability-sid> <path>...`: takes back the deny ACEs of
/// the `project` profile. It is a command and not a frame because it has to work with
/// no session, after a crash, and when the sandbox is being turned off.
#[cfg(windows)]
fn unprotect(args: &[String]) -> io::Result<()> {
    let Some((sid, paths)) = args.split_first() else {
        return Err(io::Error::other(
            "usage: unprotect <capability-sid> <path>...",
        ));
    };
    for path in paths {
        project::unprotect(sid, std::path::Path::new(path))?;
    }
    Ok(())
}

#[cfg(not(windows))]
fn unprotect(_args: &[String]) -> io::Result<()> {
    Err(io::Error::other("nothing is protected on this platform"))
}
