//! The Windows child: a raw process handle and two pipe read ends.
//!
//! The launch itself is in `appcontainer.rs`, because a confined start is
//! `CreateProcessW` with a `STARTUPINFOEX` attribute list and `std::process`
//! exposes no way to pass one. What is left here is everything the run loop
//! does afterwards: wait, ask without blocking, and stop.

// Handle calls only: no pointer of ours is dereferenced, and every handle is
// owned by an `OwnedHandle` before it reaches this file.
#![allow(unsafe_code)]

use std::ffi::c_void;
use std::io::{self, Read};
#[cfg(test)]
use std::os::windows::io::FromRawHandle;
use std::os::windows::io::{AsRawHandle, OwnedHandle};
use std::process::{Command, Stdio};

const WAIT_OBJECT_0: u32 = 0;
const WAIT_TIMEOUT: u32 = 258;
const INFINITE: u32 = 0xFFFF_FFFF;

/// Enough to wait on the process and to end it, and nothing more.
#[cfg(test)]
const SYNCHRONIZE: u32 = 0x0010_0000;
#[cfg(test)]
const PROCESS_TERMINATE: u32 = 0x0001;
#[cfg(test)]
const PROCESS_QUERY_LIMITED_INFORMATION: u32 = 0x1000;

#[link(name = "kernel32")]
extern "system" {
    fn WaitForSingleObject(handle: *mut c_void, milliseconds: u32) -> u32;
    fn GetExitCodeProcess(handle: *mut c_void, code: *mut u32) -> i32;
    fn TerminateProcess(handle: *mut c_void, code: u32) -> i32;
    #[cfg(test)]
    fn OpenProcess(access: u32, inherit: i32, pid: u32) -> *mut c_void;
}

/// What a pipe looks like to this module: either a read end `CreateProcessW`
/// handed a confined child, or a `std::process` pipe from the test-only path.
type Pipe = Box<dyn Read + Send>;

pub struct Process {
    /// Closing this handle does not end the process; `terminate` does that.
    handle: OwnedHandle,
    pid: u32,
    stdout: Option<Pipe>,
    stderr: Option<Pipe>,
    /// Remembered once seen, because the run loop asks more than once.
    exit: Option<i32>,
}

impl Process {
    pub fn adopt(
        handle: OwnedHandle,
        pid: u32,
        stdout: Option<Pipe>,
        stderr: Option<Pipe>,
    ) -> Self {
        Self {
            handle,
            pid,
            stdout,
            stderr,
            exit: None,
        }
    }

    pub fn take_stdout(&mut self) -> Option<Pipe> {
        self.stdout.take()
    }

    pub fn take_stderr(&mut self) -> Option<Pipe> {
        self.stderr.take()
    }

    pub fn try_wait(&mut self) -> io::Result<Option<i32>> {
        if let Some(code) = self.exit {
            return Ok(Some(code));
        }
        // SAFETY: `handle` is a process handle owned by this struct.
        let waited = unsafe { WaitForSingleObject(self.handle.as_raw_handle(), 0) };
        match waited {
            WAIT_OBJECT_0 => {
                let code = self.exit_code()?;
                self.exit = Some(code);
                Ok(Some(code))
            }
            WAIT_TIMEOUT => Ok(None),
            other => Err(io::Error::other(format!(
                "WaitForSingleObject returned {other} for process {}",
                self.pid
            ))),
        }
    }

    pub fn wait(&mut self) -> io::Result<i32> {
        if let Some(code) = self.exit {
            return Ok(code);
        }
        // SAFETY: as above; the handle stays valid until this struct is dropped.
        let waited = unsafe { WaitForSingleObject(self.handle.as_raw_handle(), INFINITE) };
        if waited != WAIT_OBJECT_0 {
            return Err(io::Error::other(format!(
                "WaitForSingleObject returned {waited} for process {}",
                self.pid
            )));
        }
        let code = self.exit_code()?;
        self.exit = Some(code);
        Ok(code)
    }

    fn exit_code(&self) -> io::Result<i32> {
        let mut code: u32 = 0;
        // SAFETY: the handle is owned and valid, and `code` is a local the call
        // writes through.
        let ok = unsafe { GetExitCodeProcess(self.handle.as_raw_handle(), &mut code) };
        if ok == 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(code as i32)
    }

    fn kill(&mut self) {
        // SAFETY: the handle is owned by this struct. A failure means the
        // process is already gone, which is the state the caller wanted.
        unsafe {
            TerminateProcess(self.handle.as_raw_handle(), 1);
        }
    }

    /// Kills the command *and everything it started*.
    ///
    /// `Child::kill` terminates one process. A shell is a tree: killing
    /// `cmd.exe` leaves `ping.exe` running, still holding the pipes, still
    /// consuming the machine — which is how a "timeout" turns into a leak.
    /// Until a real Job Object backend exists, `taskkill /T` is the tree kill
    /// Windows gives us without unsafe code.
    pub fn terminate(&mut self) {
        let pid = self.pid.to_string();
        let _ = Command::new("taskkill")
            .args(["/T", "/F", "/PID", &pid])
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .status();
        self.kill();
    }
}

/// Starts a child with no identity at all.
///
/// Test-only, and that is the point: on Windows every production launch goes
/// through `AppContainer::launch`, and a session without an identity reports
/// `none` instead of falling back here. The run loop's own tests need a process
/// the container did not start, because they measure the deadline, the output
/// cap and the tree kill — the half of the sidecar that has nothing to do with
/// the operating system.
#[cfg(test)]
pub fn spawn(
    program: &str,
    args: &[String],
    env: &std::collections::BTreeMap<String, String>,
    work_dir: &str,
) -> io::Result<Process> {
    let mut cmd = Command::new(program);
    cmd.args(args);
    cmd.stdin(Stdio::null());
    cmd.stdout(Stdio::piped());
    cmd.stderr(Stdio::piped());
    cmd.env_clear();
    for (key, value) in env {
        cmd.env(key, value);
    }
    if !work_dir.is_empty() {
        cmd.current_dir(work_dir);
    }

    let mut child = cmd.spawn()?;
    let stdout = child
        .stdout
        .take()
        .map(|pipe| Box::new(pipe) as Box<dyn Read + Send>);
    let stderr = child
        .stderr
        .take()
        .map(|pipe| Box::new(pipe) as Box<dyn Read + Send>);
    let pid = child.id();

    // `std::process::Child` will not hand over its handle, so ask for one on the
    // child this process just created. Dropping the `Child` closes its own
    // handle; the process itself keeps running, which is what the run loop
    // expects to manage.
    // SAFETY: the pid is this process's own child, and the handle is closed by
    // the `OwnedHandle` that takes it.
    let raw = unsafe {
        OpenProcess(
            SYNCHRONIZE | PROCESS_TERMINATE | PROCESS_QUERY_LIMITED_INFORMATION,
            0,
            pid,
        )
    };
    if raw.is_null() {
        return Err(io::Error::last_os_error());
    }
    let handle = unsafe { OwnedHandle::from_raw_handle(raw) };

    Ok(Process::adopt(handle, pid, stdout, stderr))
}
