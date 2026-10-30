//! Running one command.
//!
//! What this layer enforces has nothing to do with the operating system: a
//! deadline, an output cap that never hides the real size, and an environment
//! built from the request alone instead of inherited from the parent. The
//! confinement belongs to `confinement.rs` — this file only asks it for a
//! process, so the same run loop serves a host that confines and one that does
//! not.
//!
//! `Outcome::error` exists because a command that never started used to look
//! exactly like one that printed nothing and exited -1. A refusal the client
//! cannot read is the failure mode `docs/sandboxd-protocol.md` forbids.

use std::io::{self, Read};
use std::sync::mpsc::{self, Receiver, RecvTimeoutError, Sender};
use std::thread;
use std::time::{Duration, Instant};

use crate::confinement::Confinement;
use crate::frame::{Run, STREAM_STDERR, STREAM_STDOUT};
use crate::process::Process;

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
    /// Why the command never started, when it never started.
    pub error: Option<String>,
}

impl Outcome {
    /// A run that never started, with the reason it did not.
    pub fn failed(duration: Duration, error: String) -> Self {
        Self {
            exit_code: -1,
            duration_ms: duration.as_millis() as u64,
            timed_out: false,
            truncated: false,
            stdout_bytes: 0,
            stderr_bytes: 0,
            error: Some(error),
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
pub fn execute<F>(
    run: &Run,
    sink: &mut F,
    cancel: &Receiver<()>,
    confinement: &Confinement,
) -> Outcome
where
    F: FnMut(&str, &[u8]) -> io::Result<()>,
{
    let started = Instant::now();
    let mut child = match spawn(run, confinement) {
        Ok(child) => child,
        Err(err) => return Outcome::failed(started.elapsed(), err.to_string()),
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
        error: None,
    };
    let mut status: Option<i32> = None;
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
                    child.terminate();
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
        outcome.exit_code = code;
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

fn spawn(run: &Run, confinement: &Confinement) -> io::Result<Process> {
    let (program, args) = shell_for(run);
    confinement.spawn(&program, &args, &run.command, &run.env, &run.work_dir)
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

fn pump_output(child: &mut Process, tx: Sender<Event>) -> Vec<thread::JoinHandle<()>> {
    let mut handles = Vec::with_capacity(2);

    if let Some(stdout) = child.take_stdout() {
        handles.push(reader_thread(stdout, STREAM_STDOUT, tx.clone()));
    }
    if let Some(stderr) = child.take_stderr() {
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

    /// These tests measure the deadline, the cap and the tree kill, not the
    /// identity: they run without one, which `Confinement::direct` documents as
    /// test-only. The confined path has its own tests in `appcontainer.rs`.
    fn unconfined() -> Confinement {
        Confinement::direct()
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
            &unconfined(),
        );

        assert_eq!(outcome.exit_code, 0);
        assert!(outcome.error.is_none(), "{:?}", outcome.error);
        let text: String = seen
            .iter()
            .map(|(_, data)| String::from_utf8_lossy(data).into_owned())
            .collect();
        assert_eq!(text.trim(), "hello");
        assert!(!outcome.truncated);
    }

    /// A command that leaves a process of its own behind, so a test can tell a
    /// tree kill from a signal sent to the one process the sidecar spawned.
    /// Under `cmd.exe` the shell forks any command it runs; under `sh` the
    /// background job is what forces the fork — a plain `sleep 10` is exec'd in
    /// place, which is why the earlier version of these tests passed on a
    /// machine where the group kill did not work at all.
    fn tree_command() -> &'static str {
        if cfg!(windows) {
            "ping -n 10 127.0.0.1 >NUL"
        } else {
            "sh -c 'sleep 10 & wait'"
        }
    }

    #[test]
    fn a_command_that_times_out_reports_124() {
        let (_tx, rx) = mpsc::channel();
        let outcome = execute(
            &run_with_path(tree_command(), 300),
            &mut |_, _| Ok(()),
            &rx,
            &unconfined(),
        );

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
            &unconfined(),
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

        let outcome = execute(
            &run_with_path(tree_command(), 0),
            &mut |_, _| Ok(()),
            &rx,
            &unconfined(),
        );

        // Cancelling is not a timeout: the caller asked for it.
        assert!(!outcome.timed_out);
        assert!(outcome.duration_ms < 5000, "took {}ms", outcome.duration_ms);
    }

    /// A command that cannot be started must say so. Before `Outcome::error`
    /// existed, a missing interpreter produced exit -1 and no message at all.
    #[test]
    fn a_command_that_cannot_start_reports_why() {
        let (_tx, rx) = mpsc::channel();
        let mut run = run_of("echo hello", 0, 0);
        run.shell = vec!["/nonexistent/interpreter".to_string(), "-c".to_string()];

        let outcome = execute(&run, &mut |_, _| Ok(()), &rx, &unconfined());

        assert_eq!(outcome.exit_code, -1);
        let error = outcome.error.expect("the reason must survive");
        // The wording is the platform's: Unix says "No such file or directory",
        // Windows says "The system cannot find the path specified". What is
        // asserted is that the reason reaches the caller at all, after the path
        // is named where the platform names it.
        assert!(
            error.contains("interpreter")
                || error.contains("No such file")
                || error.contains("cannot find"),
            "unexpected message: {error}"
        );
    }
}
