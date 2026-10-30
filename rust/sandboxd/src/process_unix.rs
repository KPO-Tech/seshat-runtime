//! The Unix child: `std::process::Child` plus the process group it leads.

// The tree kill is one `kill(2)` call: the standard library does not wrap it
// and the `kill(1)` command line reads `-1234` as a signal name or as a process
// group depending on the value, which silently signalled nothing.
#![allow(unsafe_code)]

use std::collections::BTreeMap;
use std::io::{self, Read};
use std::process::{Child, Command, Stdio};
use std::sync::mpsc;
use std::thread;
use std::time::Duration;

pub struct Process {
    child: Child,
}

impl Process {
    pub fn take_stdout(&mut self) -> Option<Box<dyn Read + Send>> {
        self.child
            .stdout
            .take()
            .map(|pipe| Box::new(pipe) as Box<dyn Read + Send>)
    }

    pub fn take_stderr(&mut self) -> Option<Box<dyn Read + Send>> {
        self.child
            .stderr
            .take()
            .map(|pipe| Box::new(pipe) as Box<dyn Read + Send>)
    }

    pub fn try_wait(&mut self) -> io::Result<Option<i32>> {
        Ok(self
            .child
            .try_wait()?
            .map(|status| status.code().unwrap_or(-1)))
    }

    pub fn wait(&mut self) -> io::Result<i32> {
        Ok(self.child.wait()?.code().unwrap_or(-1))
    }

    pub fn terminate(&mut self) {
        signal_group(self.child.id());
        // The group signal covers the tree; this covers the direct child even
        // if the group signal was refused.
        let _ = self.child.kill();
    }
}

pub fn spawn(
    program: &str,
    args: &[String],
    env: &BTreeMap<String, String>,
    work_dir: &str,
) -> io::Result<Process> {
    let mut cmd = Command::new(program);
    cmd.args(args);
    cmd.stdin(Stdio::null());
    cmd.stdout(Stdio::piped());
    cmd.stderr(Stdio::piped());

    // Nothing of the sidecar's own environment reaches the command: a variable
    // the client did not send cannot leak, including its own API keys.
    cmd.env_clear();
    for (key, value) in env {
        cmd.env(key, value);
    }

    if !work_dir.is_empty() {
        cmd.current_dir(work_dir);
    }

    // Its own process group, so the whole tree can be signalled at once. Safe
    // API since Rust 1.64: no raw syscall, no unsafe block, no libc dependency.
    std::os::unix::process::CommandExt::process_group(&mut cmd, 0);

    Ok(Process {
        child: cmd.spawn()?,
    })
}

/// Signals the child's whole process group.
///
/// The child leads its own group (`spawn`), so a negated pid names everything
/// it started. This is `kill(2)` and not `kill(1)`: the command line tool
/// reads `-1234` as a *signal name* when that number is a valid signal and as a
/// *process group* otherwise, so whether the tree died depended on how large
/// the pid happened to be. It worked on a machine whose pids were in the
/// thousands and silently signalled nothing on one where the group leader got
/// pid 41 — the shell died, its child kept the pipe open, and every killed run
/// waited out the whole post-kill grace.
fn signal_group(pid: u32) {
    // SAFETY: `kill` takes an integer pid and an integer signal and touches no
    // memory belonging to this process. A negative pid names the process group
    // created in `spawn`. A failure (the group is already gone) is reported
    // through the return value, which is ignored on purpose: the caller also
    // signals the direct child.
    unsafe {
        libc::kill(-(pid as i32), libc::SIGKILL);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The group signal must reach the processes the shell started, not only
    /// the shell.
    ///
    /// It is asserted on the pipes rather than on a duration. A survivor keeps
    /// the write end open, so `read_to_end` never returns; a duration threshold
    /// can be met by a run that leaked the process, which is exactly how a tree
    /// kill that only reached the shell went unnoticed.
    #[test]
    fn the_group_signal_reaches_the_processes_the_shell_started() {
        let mut command = Command::new("/bin/sh");
        // The marker makes the test wait until the shell really has a child of
        // its own, instead of racing the signal against the spawn.
        command.args(["-c", "sleep 10 & echo started; wait"]);
        command.stdin(Stdio::null());
        command.stdout(Stdio::piped());
        command.stderr(Stdio::null());
        std::os::unix::process::CommandExt::process_group(&mut command, 0);

        let mut child = command.spawn().expect("spawn the shell");
        let leader = child.id();
        let mut stdout = child.stdout.take().expect("stdout");

        let mut marker = [0u8; 8];
        let _ = stdout.read(&mut marker);

        signal_group(leader);

        let (closed_tx, closed_rx) = mpsc::channel();
        let reader = thread::spawn(move || {
            let mut sink = Vec::new();
            let _ = stdout.read_to_end(&mut sink);
            let _ = closed_tx.send(());
        });

        let closed = closed_rx.recv_timeout(Duration::from_secs(2)).is_ok();
        let _ = child.kill();
        let _ = child.wait();
        let _ = reader.join();

        assert!(
            closed,
            "a process the shell started still held the pipe: the group signal did not reach it"
        );
    }
}
