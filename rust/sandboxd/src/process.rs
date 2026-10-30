//! The process the sidecar launched, as the run loop sees it.
//!
//! The run loop needs four things from a child: its two pipes, a non-blocking
//! "has it exited", a blocking wait, and a way to stop the whole tree it
//! started. On Unix that is `std::process::Child` almost exactly. On Windows it
//! cannot be: a confined launch is `CreateProcessW` with a `STARTUPINFOEX`
//! attribute list, which `std::process::Command` does not expose, so the child
//! comes back as a raw handle and two pipe read ends.
//!
//! This module is that seam. It is also why the launch itself lives elsewhere:
//! `appcontainer.rs` owns the identity and the `CreateProcessW` call, and hands
//! the result here.

use std::io::{self, Read};

#[cfg(unix)]
#[path = "process_unix.rs"]
mod imp;

#[cfg(windows)]
#[path = "process_windows.rs"]
mod imp;

/// A child process and its two output pipes.
pub struct Process {
    imp: imp::Process,
}

impl Process {
    /// Takes the stdout pipe, once. The run loop owns the read side from then
    /// on, so a child that keeps a pipe open cannot be read by two threads.
    pub fn take_stdout(&mut self) -> Option<Box<dyn Read + Send>> {
        self.imp.take_stdout()
    }

    pub fn take_stderr(&mut self) -> Option<Box<dyn Read + Send>> {
        self.imp.take_stderr()
    }

    /// `Some(code)` once the process has exited, without blocking.
    pub fn try_wait(&mut self) -> io::Result<Option<i32>> {
        self.imp.try_wait()
    }

    /// Blocks until the process exits.
    pub fn wait(&mut self) -> io::Result<i32> {
        self.imp.wait()
    }

    /// Stops the process *and everything it started*.
    pub fn terminate(&mut self) {
        self.imp.terminate()
    }

    /// Adopts a process handle produced by a confined launch.
    #[cfg(windows)]
    pub fn from_windows(
        handle: std::os::windows::io::OwnedHandle,
        pid: u32,
        stdout: Option<std::fs::File>,
        stderr: Option<std::fs::File>,
    ) -> Self {
        Self {
            imp: imp::Process::adopt(handle, pid, boxed(stdout), boxed(stderr)),
        }
    }
}

#[cfg(windows)]
fn boxed(pipe: Option<std::fs::File>) -> Option<Box<dyn Read + Send>> {
    pipe.map(|pipe| Box::new(pipe) as Box<dyn Read + Send>)
}

/// Starts a child with no confinement at all.
///
/// On Unix this is how every run starts until that backend exists. On Windows it
/// is compiled for tests only: a session either has an identity or reports
/// `none`, because an unconfined start there would be exactly the silent
/// fallback the sidecar exists to prevent.
#[cfg(any(unix, test))]
pub fn spawn(
    program: &str,
    args: &[String],
    env: &std::collections::BTreeMap<String, String>,
    work_dir: &str,
) -> io::Result<Process> {
    Ok(Process {
        imp: imp::spawn(program, args, env, work_dir)?,
    })
}
