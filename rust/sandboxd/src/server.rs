//! The frame loop.
//!
//! One session per process: the Go side spawns this binary, performs a
//! handshake, sends runs, and kills the process when the session ends. There is
//! no listening socket and no shared daemon, so there is nothing to authenticate
//! and nothing to discover.
//!
//! Three threads, one writer: a reader thread turns stdin into frames, a worker
//! thread runs the command and turns its pipes into chunks, and this loop is the
//! only thing that ever writes to stdout. That is what lets a `cancel` be acted
//! on while a run is streaming — the run does not own the read side.

use std::io::{self, BufWriter, Read, StdoutLock, Write};
use std::sync::mpsc::{self, RecvTimeoutError, Sender};
use std::sync::Arc;
use std::thread;
use std::time::Duration;

use base64::engine::general_purpose::STANDARD as BASE64;
use base64::Engine as _;

use crate::confinement::Confinement;
use crate::exec;
use crate::frame::{
    Cancel, Chunk, Exit, Frame, HelloAck, Run, CAP_LIMITS, PROTOCOL_MAJOR, PROTOCOL_MINOR,
    TYPE_CANCEL, TYPE_HELLO, TYPE_RUN,
};

/// A frame larger than this is a broken peer, not a big command: shell output
/// travels in chunks, never as one giant message.
const MAX_FRAME_BYTES: usize = 8 * 1024 * 1024;

/// How often the run loop gives the protocol channel a turn while a command
/// streams, so a `cancel` is honoured within tens of milliseconds.
const CANCEL_POLL: Duration = Duration::from_millis(20);

const SERVER_VERSION: &str = env!("CARGO_PKG_VERSION");

type ProtocolMessage = io::Result<Frame>;

/// What the run worker reports while a command executes.
enum RunEvent {
    Chunk(&'static str, Vec<u8>),
    Done(exec::Outcome),
}

/// Serve one client until stdin closes or the client stops reading.
pub fn serve() -> io::Result<()> {
    // Until a client asks for a profile there is no identity, and every refusal
    // of the handshake below reports that.
    let confinement = Arc::new(Confinement::none());

    let (protocol_tx, protocol_rx) = mpsc::channel::<ProtocolMessage>();
    spawn_reader(protocol_tx);

    let stdout = io::stdout();
    let mut out = BufWriter::new(stdout.lock());

    let hello = match protocol_rx.recv() {
        Ok(Ok(frame)) if frame.kind == TYPE_HELLO => frame.hello,
        Ok(Ok(other)) => {
            return refuse(
                &mut out,
                "expected_hello",
                &format!("first frame was {:?}, not a handshake", other.kind),
                &confinement,
            );
        }
        Ok(Err(err)) => {
            return refuse(&mut out, "bad_frame", &err.to_string(), &confinement);
        }
        Err(_) => return Ok(()),
    };

    let Some(hello) = hello else {
        return refuse(
            &mut out,
            "malformed_hello",
            "handshake carried no payload",
            &confinement,
        );
    };

    if hello.protocol_major != PROTOCOL_MAJOR {
        return refuse(
            &mut out,
            "protocol_mismatch",
            &format!(
                "client speaks v{}.{}, this side speaks v{PROTOCOL_MAJOR}.{PROTOCOL_MINOR}",
                hello.protocol_major, hello.protocol_minor
            ),
            &confinement,
        );
    }

    // Created here, before answering, so `level` describes something that already
    // exists rather than something that might be created later.
    let confinement = Arc::new(Confinement::detect(&hello));
    eprintln!(
        "seshat-sandboxd: level={} capabilities={:?}",
        confinement.level(),
        confinement.capabilities()
    );
    for reason in confinement.reasons() {
        eprintln!("seshat-sandboxd: degraded: {reason}");
    }

    write_frame(&mut out, &Frame::hello_ack(capabilities(&confinement)))?;

    let mut last_run_id: Option<String> = None;
    loop {
        match protocol_rx.recv() {
            Ok(Ok(frame)) => {
                if frame.kind == TYPE_RUN {
                    match frame.run {
                        Some(run) if run.run_id.is_empty() => {
                            write_frame(
                                &mut out,
                                &Frame::error("malformed_run", "run_id is required"),
                            )?;
                        }
                        Some(run) if Some(&run.run_id) == last_run_id.as_ref() => {
                            write_frame(
                                &mut out,
                                &Frame::error(
                                    "duplicate_run_id",
                                    "a run_id is single use per session",
                                ),
                            )?;
                        }
                        Some(run) => {
                            last_run_id = Some(run.run_id.clone());
                            run_one(&mut out, &protocol_rx, run, &confinement)?;
                        }
                        None => {
                            write_frame(
                                &mut out,
                                &Frame::error("malformed_run", "run carried no payload"),
                            )?;
                        }
                    }
                    continue;
                }

                if frame.kind == TYPE_CANCEL {
                    // A cancel outside a run has nothing to stop: report it
                    // rather than validating it silently.
                    let run_id = frame.cancel.map(|c| c.run_id).unwrap_or_default();
                    write_frame(
                        &mut out,
                        &Frame::error("idle_cancel", &format!("no run in flight for {run_id:?}")),
                    )?;
                    continue;
                }

                write_frame(
                    &mut out,
                    &Frame::error("unexpected_frame", &format!("unexpected {:?}", frame.kind)),
                )?;
            }
            Ok(Err(err)) => return refuse(&mut out, "bad_frame", &err.to_string(), &confinement),
            Err(_) => return Ok(()),
        }
    }
}

/// Run `run` to completion, streaming chunks out, and stop early if the client
/// sends a `cancel` for it.
fn run_one(
    out: &mut BufWriter<StdoutLock<'static>>,
    protocol_rx: &mpsc::Receiver<ProtocolMessage>,
    run: Run,
    confinement: &Arc<Confinement>,
) -> io::Result<()> {
    let run_id = run.run_id.clone();
    let (event_tx, event_rx) = mpsc::channel::<RunEvent>();
    let (cancel_tx, cancel_rx) = mpsc::channel::<()>();
    let confinement = Arc::clone(confinement);

    thread::spawn(move || {
        let mut sink = |stream: &str, data: &[u8]| -> io::Result<()> {
            let _ = event_tx.send(RunEvent::Chunk(
                if stream == crate::frame::STREAM_STDERR {
                    crate::frame::STREAM_STDERR
                } else {
                    crate::frame::STREAM_STDOUT
                },
                data.to_vec(),
            ));
            Ok(())
        };
        let outcome = exec::execute(&run, &mut sink, &cancel_rx, &confinement);
        // Same channel as the chunks, so the outcome can never be lost to a
        // race with the sender being dropped.
        let _ = event_tx.send(RunEvent::Done(outcome));
    });

    let mut outcome: Option<exec::Outcome> = None;
    while outcome.is_none() {
        match event_rx.recv_timeout(CANCEL_POLL) {
            Ok(RunEvent::Chunk(stream, data)) => {
                write_frame(
                    out,
                    &Frame::chunk(Chunk {
                        run_id: run_id.clone(),
                        stream: stream.to_string(),
                        data: BASE64.encode(data),
                    }),
                )?;
            }
            Ok(RunEvent::Done(done)) => outcome = Some(done),
            Err(RecvTimeoutError::Timeout) => {
                if let Ok(Ok(frame)) = protocol_rx.try_recv() {
                    if frame.kind == TYPE_CANCEL {
                        if let Some(Cancel { run_id: target }) = frame.cancel {
                            if target == run_id {
                                let _ = cancel_tx.send(());
                            }
                        }
                    }
                }
            }
            Err(RecvTimeoutError::Disconnected) => break,
        }
    }

    let outcome = outcome.unwrap_or_else(|| {
        exec::Outcome::failed(
            Duration::ZERO,
            "the run ended without an outcome".to_string(),
        )
    });

    // A command that never started is reported before the exit frame: the exit
    // frame alone would carry -1 and no reason, which is exactly the silent
    // refusal the protocol forbids.
    if let Some(error) = &outcome.error {
        write_frame(out, &Frame::error("spawn_failed", error))?;
    }

    write_frame(
        out,
        &Frame::exit(Exit {
            run_id,
            exit_code: outcome.exit_code,
            duration_ms: outcome.duration_ms,
            timed_out: outcome.timed_out,
            truncated: outcome.truncated,
            stdout_bytes: outcome.stdout_bytes,
            stderr_bytes: outcome.stderr_bytes,
            violations: Vec::new(),
        }),
    )
}

/// What this build can actually enforce, on this host, right now.
///
/// The answer comes from the confinement that was created at startup: a
/// capability that can be created is not a capability that is applied, and
/// reporting `full` before every spawn goes through the identity would make the
/// one honest signal in the protocol a lie.
fn capabilities(confinement: &Confinement) -> HelloAck {
    let mut capabilities = confinement.capabilities();
    if capabilities.is_empty() {
        capabilities.push(CAP_LIMITS);
    }
    HelloAck {
        protocol_major: PROTOCOL_MAJOR,
        protocol_minor: PROTOCOL_MINOR,
        server_version: SERVER_VERSION.to_string(),
        level: confinement.level().to_string(),
        capabilities: capabilities.into_iter().map(str::to_string).collect(),
        degraded_reasons: confinement.reasons(),
    }
}

fn refuse(
    out: &mut BufWriter<StdoutLock<'static>>,
    code: &str,
    message: &str,
    confinement: &Confinement,
) -> io::Result<()> {
    write_frame(out, &Frame::error(code, message))?;
    let _ = write_frame(
        out,
        &Frame {
            hello_ack: Some(HelloAck {
                level: "none".to_string(),
                degraded_reasons: vec![format!("handshake refused: {code}")],
                ..capabilities(confinement)
            }),
            ..Frame::new("hello_ack")
        },
    );
    Ok(())
}

fn spawn_reader(tx: Sender<ProtocolMessage>) {
    thread::spawn(move || {
        let stdin = io::stdin();
        let mut reader = stdin.lock();
        loop {
            match read_frame(&mut reader) {
                Ok(Some(frame)) => {
                    if tx.send(Ok(frame)).is_err() {
                        break;
                    }
                }
                Ok(None) => break,
                Err(err) => {
                    let _ = tx.send(Err(err));
                    break;
                }
            }
        }
    });
}

fn read_frame<R: Read>(reader: &mut R) -> io::Result<Option<Frame>> {
    let mut header = [0u8; 4];
    match reader.read_exact(&mut header) {
        Ok(()) => {}
        Err(err) if err.kind() == io::ErrorKind::UnexpectedEof => return Ok(None),
        Err(err) => return Err(err),
    }
    let len = u32::from_be_bytes(header) as usize;
    if len == 0 || len > MAX_FRAME_BYTES {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            format!("frame length {len} outside 1..={MAX_FRAME_BYTES}"),
        ));
    }
    let mut body = vec![0u8; len];
    reader.read_exact(&mut body)?;
    let frame: Frame = serde_json::from_slice(&body)
        .map_err(|err| io::Error::new(io::ErrorKind::InvalidData, err))?;
    Ok(Some(frame))
}

fn write_frame<W: Write>(out: &mut W, frame: &Frame) -> io::Result<()> {
    let body =
        serde_json::to_vec(frame).map_err(|err| io::Error::new(io::ErrorKind::InvalidData, err))?;
    let len = u32::try_from(body.len())
        .map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "frame too large to send"))?;
    out.write_all(&len.to_be_bytes())?;
    out.write_all(&body)?;
    out.flush()
}
