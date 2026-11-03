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
use std::os::windows::io::{AsRawHandle, FromRawHandle, OwnedHandle};
#[cfg(test)]
use std::process::{Command, Stdio};
use std::ptr;

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
/// `AssignProcessToJobObject` takes these two rights on the process it is
/// handed; a handle opened without `PROCESS_SET_QUOTA` fails with error 5.
#[cfg(test)]
const PROCESS_SET_QUOTA: u32 = 0x0100;

/// The only job limit this side sets: when the last handle to the job closes,
/// every process still in it is terminated. That is what makes a survivor
/// impossible rather than unlikely.
const JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE: u32 = 0x0000_2000;
const JOB_OBJECT_EXTENDED_LIMIT_INFORMATION: i32 = 9;
const JOB_OBJECT_BASIC_UI_RESTRICTIONS: i32 = 4;
/// Everything a job can refuse of the user's session. Measured before this: a command in the
/// `project` profile read the clipboard and the titles of the user's windows.
const JOB_OBJECT_UILIMIT_ALL: u32 = 0x0000_00FF;

#[link(name = "kernel32")]
extern "system" {
    fn WaitForSingleObject(handle: *mut c_void, milliseconds: u32) -> u32;
    fn GetExitCodeProcess(handle: *mut c_void, code: *mut u32) -> i32;
    fn TerminateProcess(handle: *mut c_void, code: u32) -> i32;
    #[cfg(test)]
    fn OpenProcess(access: u32, inherit: i32, pid: u32) -> *mut c_void;
    fn CreateJobObjectW(attributes: *const c_void, name: *const u16) -> *mut c_void;
    fn SetInformationJobObject(job: *mut c_void, class: i32, info: *const c_void, len: u32) -> i32;
    fn AssignProcessToJobObject(job: *mut c_void, process: *mut c_void) -> i32;
    fn TerminateJobObject(job: *mut c_void, code: u32) -> i32;
}

/// `JOBOBJECT_BASIC_LIMIT_INFORMATION`, as the API lays it out.
#[repr(C)]
#[derive(Default)]
struct BasicLimitInformation {
    per_process_user_time: i64,
    per_job_user_time: i64,
    limit_flags: u32,
    minimum_working_set: usize,
    maximum_working_set: usize,
    active_process_limit: u32,
    affinity: usize,
    priority_class: u32,
    scheduling_class: u32,
}

#[repr(C)]
#[derive(Default)]
struct IoCounters {
    read_operations: u64,
    write_operations: u64,
    other_operations: u64,
    read_bytes: u64,
    write_bytes: u64,
    other_bytes: u64,
}

/// `JOBOBJECT_EXTENDED_LIMIT_INFORMATION`: the basic limits and the accounting
/// fields the API expects behind them. Only `limit_flags` is set here, and the
/// rest being zero is what "no limit" means to the kernel.
#[repr(C)]
#[derive(Default)]
struct ExtendedLimitInformation {
    basic: BasicLimitInformation,
    io: IoCounters,
    process_memory_limit: usize,
    job_memory_limit: usize,
    peak_process_memory: usize,
    peak_job_memory: usize,
}

/// The job object a run's processes live in.
///
/// A job is the only handle Windows gives on "this command and everything it
/// started": children inherit it, `TerminateJobObject` ends all of them at
/// once, and `KILL_ON_JOB_CLOSE` ends the tree even when the run finished
/// normally and nobody remembered to kill it. Closing this handle is the
/// cleanup — there is nothing else to call.
pub struct Job {
    handle: OwnedHandle,
}

impl Job {
    /// Creates an unnamed job that kills its members when it is closed.
    pub fn new() -> io::Result<Self> {
        // SAFETY: an unnamed job with default security attributes; both
        // arguments are documented as optional.
        let raw = unsafe { CreateJobObjectW(ptr::null(), ptr::null()) };
        if raw.is_null() {
            return Err(io::Error::last_os_error());
        }
        // SAFETY: the call just produced this handle and nothing else owns it.
        let job = Self {
            handle: unsafe { OwnedHandle::from_raw_handle(raw) },
        };

        let limits = ExtendedLimitInformation {
            basic: BasicLimitInformation {
                limit_flags: JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
                ..BasicLimitInformation::default()
            },
            ..ExtendedLimitInformation::default()
        };
        // SAFETY: the job handle is owned above, and `limits` is a local that
        // outlives the call and is exactly the size passed.
        let ok = unsafe {
            SetInformationJobObject(
                job.handle.as_raw_handle(),
                JOB_OBJECT_EXTENDED_LIMIT_INFORMATION,
                &limits as *const ExtendedLimitInformation as *const c_void,
                std::mem::size_of::<ExtendedLimitInformation>() as u32,
            )
        };
        if ok == 0 {
            return Err(io::Error::last_os_error());
        }

        let restrictions: u32 = std::env::var("ZZ_UI")
            .ok()
            .and_then(|v| u32::from_str_radix(&v, 16).ok())
            .unwrap_or(JOB_OBJECT_UILIMIT_ALL);
        // SAFETY: the job handle is owned above; this information class is one DWORD of flags.
        let ok = unsafe {
            SetInformationJobObject(
                job.handle.as_raw_handle(),
                JOB_OBJECT_BASIC_UI_RESTRICTIONS,
                &restrictions as *const u32 as *const c_void,
                std::mem::size_of::<u32>() as u32,
            )
        };
        if ok == 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(job)
    }

    /// Puts a process — and every process it starts from then on — in this job.
    ///
    /// Nested jobs are supported, so this works even when the sidecar itself
    /// was started inside one.
    pub fn assign(&self, process: &OwnedHandle) -> io::Result<()> {
        // SAFETY: both handles are owned by the caller and stay valid across
        // the call.
        let ok = unsafe {
            AssignProcessToJobObject(self.handle.as_raw_handle(), process.as_raw_handle())
        };
        if ok == 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(())
    }
}

/// What a pipe looks like to this module: either a read end `CreateProcessW`
/// handed a confined child, or a `std::process` pipe from the test-only path.
type Pipe = Box<dyn Read + Send>;

pub struct Process {
    /// Closing this handle does not end the process; the job does that.
    handle: OwnedHandle,
    /// Everything this run started lives here. Dropping it ends the tree.
    job: Job,
    pid: u32,
    stdout: Option<Pipe>,
    stderr: Option<Pipe>,
    /// Remembered once seen, because the run loop asks more than once.
    exit: Option<i32>,
}

impl Process {
    pub fn adopt(
        job: Job,
        handle: OwnedHandle,
        pid: u32,
        stdout: Option<Pipe>,
        stderr: Option<Pipe>,
    ) -> Self {
        Self {
            job,
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
    /// The job is the answer the kernel gives: `TerminateJobObject` ends every
    /// member at once, and `taskkill /T` is no longer part of this path.
    pub fn terminate(&mut self) {
        // SAFETY: both handles are owned by this struct and stay valid here.
        unsafe {
            TerminateJobObject(self.job.handle.as_raw_handle(), 1);
        }
        // Belt and braces: a member that was somehow not in the job (it cannot
        // be, because the launch fails closed) would still be ended here.
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
            SYNCHRONIZE | PROCESS_TERMINATE | PROCESS_QUERY_LIMITED_INFORMATION | PROCESS_SET_QUOTA,
            0,
            pid,
        )
    };
    if raw.is_null() {
        return Err(io::Error::last_os_error());
    }
    let handle = unsafe { OwnedHandle::from_raw_handle(raw) };

    // The run loop's tests measure the tree kill too, so this path gets the
    // same job a confined launch does.
    let job = Job::new()?;
    job.assign(&handle)?;

    Ok(Process::adopt(job, handle, pid, stdout, stderr))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::BTreeMap;
    use std::time::{Duration, Instant};

    fn cmd() -> String {
        match std::env::var("SystemRoot") {
            Ok(root) => format!("{root}\\System32\\cmd.exe"),
            Err(_) => "cmd.exe".to_string(),
        }
    }

    fn shell_env() -> BTreeMap<String, String> {
        let mut env = BTreeMap::new();
        for key in [
            "SystemRoot",
            "windir",
            "SystemDrive",
            "PATH",
            "Path",
            "COMPUTERNAME",
            "USERNAME",
            "USERPROFILE",
        ] {
            if let Ok(value) = std::env::var(key) {
                env.insert(key.to_string(), value);
            }
        }
        env
    }

    /// A survivor is what a tree kill is for: `start /B` detaches a process
    /// from the shell it came from, so killing the shell leaves it running.
    /// The job kills it anyway, because what it belongs to is the job.
    ///
    /// The two markers make this a measurement rather than an assumption:
    /// `started.txt` proves the grandchild really ran, and `late.txt` would
    /// appear about four seconds later if it survived.
    #[test]
    fn a_grandchild_does_not_outlive_the_run() {
        let root = std::env::temp_dir().join(format!("seshat-job-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&root);
        std::fs::create_dir_all(&root).expect("create the scratch directory");
        let started = root.join("started.txt");
        let late = root.join("late.txt");

        // A batch file rather than a quoted one-liner: `std::process::Command`
        // quotes and escapes an argument its own way, and `cmd /C` does not read
        // backslashes the way that escaping writes them.
        std::fs::write(
            root.join("late.cmd"),
            "@echo off\r\necho started>started.txt\r\nping -n 5 127.0.0.1 >NUL\r\necho late>late.txt\r\n",
        )
        .expect("write the grandchild script");
        let command = "start /B late.cmd";
        let work_dir = root.to_string_lossy();
        let mut process = spawn(
            &cmd(),
            &["/C".to_string(), command.to_string()],
            &shell_env(),
            work_dir.as_ref(),
        )
        .expect("spawn the shell");

        assert_eq!(process.wait().expect("wait for the shell"), 0);

        // Wait for the grandchild to prove it started, while it is still alive.
        let deadline = Instant::now() + Duration::from_secs(5);
        while !started.exists() && Instant::now() < deadline {
            std::thread::sleep(Duration::from_millis(20));
        }
        assert!(
            started.exists(),
            "the grandchild never ran, so this test would prove nothing"
        );

        // Ending the run closes the job handle, and that is what kills the tree.
        drop(process);

        std::thread::sleep(Duration::from_secs(6));
        assert!(
            !late.exists(),
            "the grandchild survived the run: {} exists",
            late.display()
        );
        let _ = std::fs::remove_dir_all(&root);
    }
}
