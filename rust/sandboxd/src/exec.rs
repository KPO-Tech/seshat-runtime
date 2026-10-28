//! Running one command.
//!
//! Phase 0 does not confine the filesystem or the network yet — that is the
//! next phase, behind this same entry point. What it already enforces is the
//! part that has nothing to do with the operating system: a deadline, an output
//! cap that never hides the real size, and an environment built from the
//! request alone instead of inherited from the parent.

use std::io::{self, Read};
use std::process::{Child, Command, ExitStatus, Stdio};
use std::sync::mpsc::{self, Receiver, RecvTimeoutError, Sender};
use std::thread;
use std::time::{Duration, Instant};

use crate::frame::{Run, STREAM_STDERR, STREAM_STDOUT};

/// How often the loop wakes up to check the deadline and the cancel channel.
const POLL_INTERVAL: Duration = Duration::from_millis(20);

/// How long the sidecar keeps draining pipes after the child was killed, so a
/// process that ignores SIGKILL-for-children cannot hold the run open forever.
const POST_KILL_GRACE: Duration = Duration::from_secs(5);

/// The shell used when the request does not name one.
const DEFAULT_UNIX_SHELL: [&str; 2] = ["/bin/sh", "-c"];
const DEFAULT_WINDOWS_SHELL: [&str; 2] = ["cmd.exe", "/C"];

/// Exit code reported for a run that hit its deadline, matching what a shell
/// reports for a timeout so callers do not need a second convention.
const TIMEOUT_EXIT_CODE: i32 = 124;

/// What the client needs to know once the command is over.
#[derive(Debug, Clone)]
pub struct Outcome {
    pub exit_code: i32,
    pub duration_ms: u64,
    pub timed_out: bool,
    pub truncated: bool,
    pub stdout_bytes: u64,
    pub stderr_bytes: u64,
}

impl Outcome {
    /// A run that never started.
    pub fn failed(duration: Duration) -> Self {
        Self {
            exit_code: -1,
            duration_ms: duration.as_millis() as u64,
            timed_out: false,
            truncated: false,
            stdout_bytes: 0,
            stderr_bytes: 0,
        }
    }
}

/// One read from a child pipe: the stream it came from and the bytes.
type Event = (&'static str, Vec<u8>);

/// Run `run`, handing every produced chunk to `sink`.
///
/// `cancel` is the channel the protocol reader writes to when the client sends
/// a `cancel` for this run; a message on it kills the child exactly as a
/// deadline does.
pub fn execute<F>(run: &Run, sink: &mut F, cancel: &Receiver<()>) -> Outcome
where
    F: FnMut(&str, &[u8]) -> io::Result<()>,
{
    let started = Instant::now();
    let mut child = match spawn(run) {
        Ok(child) => child,
        Err(_) => return Outcome::failed(started.elapsed()),
    };

    let (tx, rx) = mpsc::channel::<Event>();
    let readers = pump_output(&mut child, tx);

    let deadline = if run.timeout_ms == 0 {
        None
    } else {
        Some(started + Duration::from_millis(run.timeout_ms))
    };

    let mut outcome = Outcome {
        exit_code: -1,
        duration_ms: 0,
        timed_out: false,
        truncated: false,
        stdout_bytes: 0,
        stderr_bytes: 0,
    };
    let mut status: Option<ExitStatus> = None;
    let mut killed_at: Option<Instant> = None;
    let mut drained = false;

    loop {
        match rx.recv_timeout(POLL_INTERVAL) {
            Ok((stream, data)) => {
                emit(stream, &data, run.max_output_bytes, &mut outcome, sink);
            }
            Err(RecvTimeoutError::Timeout) => {
                if status.is_none() {
                    match child.try_wait() {
                        Ok(Some(code)) => status = Some(code),
                        Ok(None) => {}
                        Err(_) => break,
                    }
                }

                let cancelled = cancel.try_recv().is_ok();
                let expired = deadline.is_some_and(|at| Instant::now() >= at);
                if (cancelled || expired) && killed_at.is_none() {
                    outcome.timed_out = expired;
                    terminate(&mut child);
                    killed_at = Some(Instant::now());
                }

                // A grandchild can keep the pipes open after its parent is
                // gone. Stop waiting once the grace ran out, and report what
                // was actually written.
                if let Some(at) = killed_at {
                    if at.elapsed() > POST_KILL_GRACE {
                        break;
                    }
                }
            }
            Err(RecvTimeoutError::Disconnected) => {
                drained = true;
                break;
            }
        }
    }

    // Only join when the pipes actually reached EOF: a survivor that still
    // holds them must not be able to hang the session's reader.
    if drained {
        for reader in readers {
            let _ = reader.join();
        }
    }

    if let Some(code) = status.or_else(|| child.wait().ok()) {
        outcome.exit_code = code.code().unwrap_or(-1);
    }
    if outcome.timed_out {
        outcome.exit_code = TIMEOUT_EXIT_CODE;
    }
    outcome.duration_ms = started.elapsed().as_millis() as u64;
    outcome
}

fn emit<F>(stream: &str, data: &[u8], max_output_bytes: u64, outcome: &mut Outcome, sink: &mut F)
where
    F: FnMut(&str, &[u8]) -> io::Result<()>,
{
    let counter = if stream == STREAM_STDERR {
        &mut outcome.stderr_bytes
    } else {
        &mut outcome.stdout_bytes
    };
    let already = *counter;
    let total = already.saturating_add(data.len() as u64);
    *counter = total;

    if max_output_bytes > 0 && already >= max_output_bytes {
        // Still counted, so the client learns the real size and can tell the
        // model that it is looking at part of the output.
        outcome.truncated = true;
        return;
    }

    let take = if max_output_bytes > 0 {
        data.len().min((max_output_bytes - already) as usize)
    } else {
        data.len()
    };
    if take == 0 {
        return;
    }
    if take < data.len() {
        outcome.truncated = true;
    }
    let _ = sink(stream, &data[..take]);
}

fn spawn(run: &Run) -> io::Result<Child> {
    let (program, mut args) = shell_for(run);
    args.push(run.command.clone());

    let mut cmd = Command::new(program);
    cmd.args(&args);
    cmd.stdin(Stdio::null());
    cmd.stdout(Stdio::piped());
    cmd.stderr(Stdio::piped());

    // Nothing of the sidecar's own environment reaches the command: a variable
    // the client did not send cannot leak, including its own API keys.
    cmd.env_clear();
    for (key, value) in &run.env {
        cmd.env(key, value);
    }

    if !run.work_dir.is_empty() {
        cmd.current_dir(&run.work_dir);
    }

    configure_process_group(&mut cmd);

    cmd.spawn()
}

/// Put the child in its own process group (Unix) so the whole tree can be
/// signalled at once. On Windows the equivalent is done at kill time.
#[cfg(unix)]
fn configure_process_group(cmd: &mut Command) {
    // Safe API since 1.64: no raw syscall, no unsafe block, no libc dependency.
    std::os::unix::process::CommandExt::process_group(cmd, 0);
}

#[cfg(not(unix))]
fn configure_process_group(_cmd: &mut Command) {}

/// Kill the command *and everything it started*.
///
/// `Child::kill` terminates one process. A shell is a tree: killing `cmd.exe`
/// leaves `ping.exe` running, still holding the pipes, still consuming the
/// machine — which is how a "timeout" turns into a leak. Until a real Job
/// Object backend exists, `taskkill /T` is the tree kill Windows gives us
/// without unsafe code; on Unix the child leads its own process group and the
/// group is signalled.
fn terminate(child: &mut Child) {
    let pid = child.id().to_string();

    #[cfg(windows)]
    {
        let _ = Command::new("taskkill")
            .args(["/T", "/F", "/PID", &pid])
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .status();
    }

    #[cfg(unix)]
    {
        let _ = Command::new("kill")
            .args(["-KILL", &format!("-{pid}")])
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .status();
    }

    // The group signal covers the tree; this covers the direct child even if
    // the group signal was refused.
    let _ = child.kill();
}

fn shell_for(run: &Run) -> (String, Vec<String>) {
    if !run.shell.is_empty() {
        let mut parts = run.shell.clone();
        let program = parts.remove(0);
        return (program, parts);
    }
    if cfg!(windows) {
        let mut parts: Vec<String> = DEFAULT_WINDOWS_SHELL
            .iter()
            .map(|s| s.to_string())
            .collect();
        let program = parts.remove(0);
        return (program, parts);
    }
    let mut parts: Vec<String> = DEFAULT_UNIX_SHELL.iter().map(|s| s.to_string()).collect();
    let program = parts.remove(0);
    (program, parts)
}

fn pump_output(child: &mut Child, tx: Sender<Event>) -> Vec<thread::JoinHandle<()>> {
    let mut handles = Vec::with_capacity(2);

    if let Some(stdout) = child.stdout.take() {
        handles.push(reader_thread(stdout, STREAM_STDOUT, tx.clone()));
    }
    if let Some(stderr) = child.stderr.take() {
        handles.push(reader_thread(stderr, STREAM_STDERR, tx));
    }
    handles
}

fn reader_thread<R>(
    mut source: R,
    stream: &'static str,
    tx: Sender<Event>,
) -> thread::JoinHandle<()>
where
    R: Read + Send + 'static,
{
    thread::spawn(move || {
        let mut buffer = [0u8; 8192];
        loop {
            match source.read(&mut buffer) {
                Ok(0) => break,
                Ok(n) => {
                    if tx.send((stream, buffer[..n].to_vec())).is_err() {
                        break;
                    }
                }
                Err(ref err) if err.kind() == io::ErrorKind::Interrupted => continue,
                Err(_) => break,
            }
        }
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn run_of(command: &str, timeout_ms: u64, max_output_bytes: u64) -> Run {
        Run {
            run_id: "r1".to_string(),
            command: command.to_string(),
            shell: Vec::new(),
            work_dir: String::new(),
            env: Default::default(),
            allow_network: false,
            timeout_ms,
            max_output_bytes,
        }
    }

    /// The sidecar never inherits its environment, so a test that needs a
    /// program found by name (or a shell that can find one) has to send one —
    /// exactly as the Go client does.
    fn run_with_path(command: &str, timeout_ms: u64) -> Run {
        let mut run = run_of(command, timeout_ms, 0);
        for key in ["PATH", "Path", "SystemRoot", "SystemDrive", "TEMP", "TMP"] {
            if let Ok(value) = std::env::var(key) {
                run.env.insert(key.to_string(), value);
            }
        }
        run
    }

    #[test]
    fn a_command_streams_its_output_and_reports_its_code() {
        let (_tx, rx) = mpsc::channel();
        let mut seen = Vec::new();
        let outcome = execute(
            &run_of("echo hello", 0, 0),
            &mut |stream, data| {
                seen.push((stream.to_string(), data.to_vec()));
                Ok(())
            },
            &rx,
        );

        assert_eq!(outcome.exit_code, 0);
        let text: String = seen
            .iter()
            .map(|(_, data)| String::from_utf8_lossy(data).into_owned())
            .collect();
        assert_eq!(text.trim(), "hello");
        assert!(!outcome.truncated);
    }

    #[test]
    fn a_command_that_times_out_reports_124() {
        let (_tx, rx) = mpsc::channel();
        let command = if cfg!(windows) {
            "ping -n 10 127.0.0.1 >NUL"
        } else {
            "sleep 10"
        };
        let outcome = execute(&run_with_path(command, 300), &mut |_, _| Ok(()), &rx);

        assert!(outcome.timed_out, "expected the deadline to fire");
        assert_eq!(outcome.exit_code, TIMEOUT_EXIT_CODE);
        assert!(outcome.duration_ms < 5000, "took {}ms", outcome.duration_ms);
    }

    #[test]
    fn the_output_cap_cuts_the_stream_but_still_counts_every_byte() {
        let (_tx, rx) = mpsc::channel();
        let mut delivered = 0usize;
        let command = if cfg!(windows) {
            "for /L %i in (1,1,500) do @echo 0123456789012345678901234567890123456789"
        } else {
            "for i in $(seq 1 500); do echo 0123456789012345678901234567890123456789; done"
        };
        let outcome = execute(
            &run_of(command, 0, 100),
            &mut |_, data| {
                delivered += data.len();
                Ok(())
            },
            &rx,
        );

        assert_eq!(outcome.exit_code, 0);
        assert!(outcome.truncated, "expected the cap to be reported");
        assert_eq!(delivered, 100);
        assert!(
            outcome.stdout_bytes > 100,
            "real size must survive the cap, got {}",
            outcome.stdout_bytes
        );
    }

    #[test]
    fn a_cancel_message_stops_the_command() {
        let (tx, rx) = mpsc::channel();
        thread::spawn(move || {
            thread::sleep(Duration::from_millis(200));
            let _ = tx.send(());
        });

        let command = if cfg!(windows) {
            "ping -n 10 127.0.0.1 >NUL"
        } else {
            "sleep 10"
        };
        let outcome = execute(&run_with_path(command, 0), &mut |_, _| Ok(()), &rx);

        // Cancelling is not a timeout: the caller asked for it.
        assert!(!outcome.timed_out);
        assert!(outcome.duration_ms < 5000, "took {}ms", outcome.duration_ms);
    }
}
