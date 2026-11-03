//! What this host can actually enforce, and how a run is launched under it.
//!
//! The handshake reports `level` from here, never from what the client asked
//! for. A capability that can be *created* is not a capability that is
//! *applied*, so the Windows backend only claims the filesystem once every
//! spawn goes through the identity that was created — and says what it still
//! cannot do, per [`docs/spike-windows-toolchain.md`](../../../docs/spike-windows-toolchain.md).
//!
//! On Windows the identity is created once per session, its package SID is
//! granted each working directory the session runs in, and both are taken back
//! when the session ends. On every other platform this reports honestly that
//! nothing is confined yet rather than pretending.

use std::collections::BTreeMap;
use std::io;

#[cfg(windows)]
use std::sync::Mutex;

use crate::frame::{Hello, CAP_LIMITS, LEVEL_NONE};
#[cfg(windows)]
use crate::frame::{
    CAP_FS_CONFINE, CAP_JOB_OBJECT, CAP_NETWORK_DENY, CAP_READ_DENY, LEVEL_DEGRADED,
    PROFILE_PROJECT, PROFILE_STRICT,
};
use crate::process::Process;

/// The three states a session can be in, spelled out rather than inferred from
/// two `Option`s: a session either has an identity, or it does not and nothing
/// will run.
#[cfg(windows)]
enum Identity {
    Confined(crate::appcontainer::AppContainer),
    /// The `project` profile: see ADR-0002.
    Project(crate::project::ProjectIdentity),
    /// No identity could be created, and the reason. The runtime refuses to
    /// execute against `none`; this side never falls back to the host path.
    Refused(String),
    /// No identity, by construction. Tests only.
    #[cfg(test)]
    Direct,
}

pub struct Confinement {
    #[cfg(windows)]
    identity: Identity,
    /// Working directories already granted, so a session pays for the traversal
    /// once per directory instead of once per command.
    #[cfg(windows)]
    granted: Mutex<std::collections::BTreeSet<String>>,
}

impl Confinement {
    /// Nothing can run: the handshake failed before a profile was asked for.
    pub fn none() -> Self {
        #[cfg(windows)]
        let none = Self {
            identity: Identity::Refused("no handshake".to_string()),
            granted: Mutex::new(std::collections::BTreeSet::new()),
        };
        #[cfg(not(windows))]
        let none = Self {};

        none
    }

    /// Creates the identity the client's handshake asks for.
    ///
    /// A failure becomes `level: none`, which the runtime treats as a refusal to
    /// execute. Falling back to an unconfined run is the one thing this must
    /// never do. It happens before the answer to the handshake, so `level`
    /// describes something that exists rather than something that might.
    pub fn detect(hello: &Hello) -> Self {
        #[cfg(windows)]
        let identity = match hello.profile.as_str() {
            "" | PROFILE_STRICT => {
                let name = format!("seshat.sandboxd.{}", std::process::id());
                match crate::appcontainer::AppContainer::create_replacing(&name) {
                    Ok(profile) => Identity::Confined(profile),
                    Err(err) => Identity::Refused(err.to_string()),
                }
            }
            PROFILE_PROJECT => match project_identity(hello) {
                Ok(identity) => Identity::Project(identity),
                Err(err) => Identity::Refused(err.to_string()),
            },
            other => Identity::Refused(format!("unknown profile: {other}")),
        };
        #[cfg(not(windows))]
        let _ = hello;
        #[cfg(not(windows))]
        let detected = Self {};

        #[cfg(windows)]
        let detected = Self {
            identity,
            granted: Mutex::new(std::collections::BTreeSet::new()),
        };

        detected
    }

    /// An identity-free launch, for the run loop's own tests.
    ///
    /// Production never gets here: `detect()` is the only constructor the
    /// sidecar uses, and on Windows a session without a profile reports `none`
    /// instead of running. The confined path has its own tests, which measure
    /// the identity rather than the deadline and the output cap.
    #[cfg(test)]
    pub fn direct() -> Self {
        #[cfg(windows)]
        let direct = Self {
            identity: Identity::Direct,
            granted: Mutex::new(std::collections::BTreeSet::new()),
        };
        #[cfg(not(windows))]
        let direct = Self {};

        direct
    }

    pub fn level(&self) -> &'static str {
        #[cfg(windows)]
        let level = match &self.identity {
            Identity::Confined(_) | Identity::Project(_) => LEVEL_DEGRADED,
            Identity::Refused(_) => LEVEL_NONE,
            #[cfg(test)]
            Identity::Direct => LEVEL_DEGRADED,
        };
        // No backend here: Linux and macOS get their isolation from the microVM backend of the
        // runtime (ADR-0003). `none` makes the client refuse rather than run unconfined.
        #[cfg(not(windows))]
        let level = LEVEL_NONE;

        level
    }

    pub fn capabilities(&self) -> Vec<&'static str> {
        #[cfg(windows)]
        let capabilities = match &self.identity {
            Identity::Confined(_) => {
                vec![CAP_LIMITS, CAP_FS_CONFINE, CAP_NETWORK_DENY, CAP_JOB_OBJECT]
            }
            Identity::Project(_) => vec![CAP_LIMITS, CAP_FS_CONFINE, CAP_READ_DENY, CAP_JOB_OBJECT],
            Identity::Refused(_) => Vec::new(),
            #[cfg(test)]
            Identity::Direct => vec![CAP_LIMITS],
        };
        #[cfg(not(windows))]
        let capabilities = vec![CAP_LIMITS];

        capabilities
    }

    /// Every missing capability, named. Windows reports `degraded` even with an
    /// identity, because three limits are measured rather than assumed.
    pub fn reasons(&self) -> Vec<String> {
        #[cfg(windows)]
        let reasons = match &self.identity {
            Identity::Confined(_) => vec![
                "git cannot initialise inside the identity: it stats the leading directories of \
                 the working directory, which a package is not allowed to read"
                    .to_string(),
                "a toolchain installed under the user profile is not readable by the identity; one \
                 installed machine-wide is"
                    .to_string(),
                "%TEMP% is not writable inside the identity: temporary files must go to the \
                 working directory"
                    .to_string(),
            ],
            Identity::Project(identity) => {
                let mut reasons = vec![
                    "reads are not confined: only the folders the client named are refused"
                        .to_string(),
                    "the network is open".to_string(),
                    "secrets of this user remain reachable through DPAPI and the Credential Manager"
                        .to_string(),
                ];
                if !identity.has_private_desktop() {
                    reasons.push(
                        "commands share the user's desktop, so they can capture the screen"
                            .to_string(),
                    );
                }
                if identity.elevated() {
                    reasons.push(
                        "Git Bash cannot start while the app runs as an administrator".to_string(),
                    );
                }
                reasons
            }
            Identity::Refused(refusal) => {
                vec![format!("no identity: {refusal}")]
            }
            #[cfg(test)]
            Identity::Direct => Vec::new(),
        };
        #[cfg(not(windows))]
        let reasons = vec![
            "filesystem confinement is not implemented on this platform".to_string(),
            "network confinement is not implemented on this platform".to_string(),
        ];

        reasons
    }

    /// Starts `command` under this host's confinement.
    ///
    /// On Windows the working directory is granted to the session's package SID
    /// first; a launch without that grant would start and then fail on its first
    /// write, which is a worse error than a refusal here.
    pub fn spawn(
        &self,
        program: &str,
        args: &[String],
        command: &str,
        env: &BTreeMap<String, String>,
        work_dir: &str,
    ) -> io::Result<Process> {
        #[cfg(windows)]
        let spawned = match &self.identity {
            Identity::Confined(profile) => {
                if !work_dir.is_empty() {
                    self.grant(profile, work_dir)?;
                }
                let line = command_line(program, args, command);
                profile.launch(&line, env, work_dir)
            }
            Identity::Project(identity) => {
                if !work_dir.is_empty() {
                    self.grant_project(identity, work_dir)?;
                }
                let (shell, bin) = identity.shell(program)?;
                let env = match bin {
                    Some(bin) => with_path_first(env, &bin),
                    None => env.clone(),
                };
                let line = command_line(&shell, args, command);
                identity.launch(&line, &env, work_dir)
            }
            Identity::Refused(refusal) => Err(io::Error::other(format!("no identity: {refusal}"))),
            #[cfg(test)]
            Identity::Direct => {
                let mut argv = args.to_vec();
                argv.push(command.to_string());
                crate::process::spawn(program, &argv, env, work_dir)
            }
        };
        #[cfg(not(windows))]
        let spawned = {
            let mut argv = args.to_vec();
            argv.push(command.to_string());
            crate::process::spawn(program, &argv, env, work_dir)
        };

        spawned
    }

    #[cfg(windows)]
    fn grant_project(
        &self,
        identity: &crate::project::ProjectIdentity,
        work_dir: &str,
    ) -> io::Result<()> {
        let mut granted = self
            .granted
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        if granted.contains(work_dir) {
            return Ok(());
        }
        identity.grant(std::path::Path::new(work_dir))?;
        granted.insert(work_dir.to_string());
        Ok(())
    }

    #[cfg(windows)]
    fn grant(&self, profile: &crate::appcontainer::AppContainer, work_dir: &str) -> io::Result<()> {
        let mut granted = self
            .granted
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        if granted.contains(work_dir) {
            return Ok(());
        }
        profile.grant(std::path::Path::new(work_dir))?;
        granted.insert(work_dir.to_string());
        Ok(())
    }
}

impl Drop for Confinement {
    fn drop(&mut self) {
        #[cfg(windows)]
        {
            let granted = self
                .granted
                .lock()
                .map(|granted| granted.clone())
                .unwrap_or_default();
            for dir in granted {
                // Best effort: a destructor cannot report anything. The grant is inert
                // once the profile that named it is gone. The project profile's label
                // stays: another session may be working in this folder, and taking it
                // back would stop that session's writes.
                let dir = std::path::Path::new(&dir);
                if let Identity::Confined(profile) = &self.identity {
                    let _ = profile.revoke(dir);
                }
            }
        }
    }
}

/// `env` with `dir` first on the search path, whichever spelling the client used.
#[cfg(windows)]
fn with_path_first(env: &BTreeMap<String, String>, dir: &str) -> BTreeMap<String, String> {
    let mut out = BTreeMap::new();
    let mut path = String::new();
    for (key, value) in env {
        if key.eq_ignore_ascii_case("PATH") {
            path = value.clone();
        } else {
            out.insert(key.clone(), value.clone());
        }
    }
    out.insert("PATH".to_string(), format!("{dir};{path}"));
    out
}

/// The `project` identity, with what it must not read protected first: a session that
/// cannot keep its promise on one folder does not start.
#[cfg(windows)]
fn project_identity(hello: &Hello) -> io::Result<crate::project::ProjectIdentity> {
    if hello.capability_sid.is_empty() {
        return Err(io::Error::other(
            "the project profile needs a capability SID",
        ));
    }
    for path in &hello.deny_read {
        crate::project::protect(&hello.capability_sid, std::path::Path::new(path))
            .map_err(|err| io::Error::other(format!("could not protect {path}: {err}")))?;
    }
    crate::project::ProjectIdentity::create(&hello.capability_sid)
}

/// Builds the `lpCommandLine` `CreateProcessW` takes: the interpreter, its
/// arguments, and the client's command last.
///
/// `cmd /C` takes the rest of the line as written, so for it the command is appended
/// as it is: quoting it would make the shell strip those quotes back off. Every other
/// interpreter gets its arguments from the C runtime's parsing of the line, and
/// `bash -c` takes one argument as the script, so there the command must be one
/// argument: unquoted, bash would run only its first word.
#[cfg(windows)]
fn command_line(program: &str, args: &[String], command: &str) -> String {
    let mut line = String::with_capacity(program.len() + command.len() + args.len() * 8 + 2);
    push_argument(&mut line, program);
    for arg in args {
        line.push(' ');
        push_argument(&mut line, arg);
    }
    line.push(' ');
    if is_cmd(program) {
        line.push_str(command);
    } else {
        push_quoted(&mut line, command);
    }
    line
}

#[cfg(windows)]
fn is_cmd(program: &str) -> bool {
    let name = program
        .trim_matches('"')
        .rsplit(['\\', '/'])
        .next()
        .unwrap_or(program);
    name.eq_ignore_ascii_case("cmd.exe") || name.eq_ignore_ascii_case("cmd")
}

/// One argument, by the rules `CommandLineToArgvW` and the C runtime read back: a
/// quote is escaped with a backslash, and backslashes double only before a quote.
#[cfg(windows)]
fn push_quoted(line: &mut String, argument: &str) {
    line.push('"');
    let mut backslashes = 0;
    for c in argument.chars() {
        match c {
            '\\' => backslashes += 1,
            '"' => {
                line.extend(std::iter::repeat_n('\\', backslashes * 2 + 1));
                line.push('"');
                backslashes = 0;
            }
            _ => {
                line.extend(std::iter::repeat_n('\\', backslashes));
                line.push(c);
                backslashes = 0;
            }
        }
    }
    line.extend(std::iter::repeat_n('\\', backslashes * 2));
    line.push('"');
}

#[cfg(windows)]
fn push_argument(line: &mut String, argument: &str) {
    let needs_quotes = argument.contains(' ') || argument.contains('\t');
    if needs_quotes && !(argument.starts_with('"') && argument.ends_with('"')) {
        line.push('"');
        line.push_str(argument);
        line.push('"');
    } else {
        line.push_str(argument);
    }
}

#[cfg(all(test, windows))]
mod tests {
    use super::*;

    #[cfg(windows)]
    #[test]
    fn cmd_takes_the_command_as_written() {
        let args = vec!["/C".to_string()];
        let line = command_line("C:\\Windows\\System32\\cmd.exe", &args, "echo \"a b\" > f");
        assert_eq!(line, "C:\\Windows\\System32\\cmd.exe /C echo \"a b\" > f");
    }

    /// Unquoted, `bash -c` ran only the first word: `git status` printed git's help and
    /// `echo x > f` wrote nothing and exited 0. Found by running the real chain.
    #[cfg(windows)]
    #[test]
    fn any_other_interpreter_takes_the_command_as_one_argument() {
        let args = vec!["-c".to_string()];
        let line = command_line(
            "C:\\Program Files\\Git\\usr\\bin\\bash.exe",
            &args,
            "echo \"a b\" > f",
        );
        assert_eq!(
            line,
            "\"C:\\Program Files\\Git\\usr\\bin\\bash.exe\" -c \"echo \\\"a b\\\" > f\""
        );
    }

    #[cfg(windows)]
    #[test]
    fn backslashes_double_only_before_a_quote() {
        let quoted = |text: &str| {
            let mut line = String::new();
            push_quoted(&mut line, text);
            line
        };
        assert_eq!(quoted("C:\\dir\\f"), "\"C:\\dir\\f\"");
        assert_eq!(quoted("C:\\dir\\"), "\"C:\\dir\\\\\"");
        assert_eq!(quoted("a\\\"b"), "\"a\\\\\\\"b\"");
    }

    /// A host with no identity reports that, rather than claiming a confinement
    /// it cannot apply.
    #[cfg(windows)]
    #[test]
    fn an_identity_that_could_not_be_created_is_reported_as_none() {
        let confinement = Confinement {
            identity: Identity::Refused("denied".to_string()),
            granted: Mutex::new(std::collections::BTreeSet::new()),
        };
        assert_eq!(confinement.level(), LEVEL_NONE);
        assert!(confinement.capabilities().is_empty());
        assert!(confinement.reasons()[0].contains("denied"));
    }

    #[cfg(windows)]
    #[test]
    fn a_session_with_an_identity_claims_both_confinements_and_names_its_limits() {
        let confinement = Confinement::detect(&Hello::default());
        assert_eq!(
            confinement.level(),
            LEVEL_DEGRADED,
            "reasons: {:?}",
            confinement.reasons()
        );
        let capabilities = confinement.capabilities();
        assert!(capabilities.contains(&CAP_FS_CONFINE), "{capabilities:?}");
        assert!(capabilities.contains(&CAP_NETWORK_DENY), "{capabilities:?}");
        assert!(capabilities.contains(&CAP_JOB_OBJECT), "{capabilities:?}");
        assert_eq!(confinement.reasons().len(), 3);
    }

    #[cfg(windows)]
    #[test]
    fn the_project_profile_names_what_it_leaves_open_and_claims_no_network_confinement() {
        let confinement = Confinement::detect(&Hello {
            profile: PROFILE_PROJECT.to_string(),
            capability_sid: "S-1-5-21-1111111111-2222222222-3333333333-4444444444".to_string(),
            ..Hello::default()
        });
        assert_eq!(
            confinement.level(),
            LEVEL_DEGRADED,
            "reasons: {:?}",
            confinement.reasons()
        );
        let capabilities = confinement.capabilities();
        assert!(capabilities.contains(&CAP_FS_CONFINE), "{capabilities:?}");
        assert!(
            !capabilities.contains(&CAP_NETWORK_DENY),
            "{capabilities:?}"
        );
        let reasons = confinement.reasons().join(" ");
        assert!(reasons.contains("network"), "{reasons}");
        assert!(reasons.contains("reads"), "{reasons}");
    }

    /// A dev server has a sidecar of its own, on the folder the main sidecar works in, so
    /// one session ending must not take the folder from the other.
    #[cfg(windows)]
    #[test]
    fn a_session_ending_leaves_the_folder_to_the_one_still_using_it() {
        let home = std::env::var_os("USERPROFILE").expect("USERPROFILE");
        let dir = std::path::Path::new(&home).join(format!(
            "seshat-sandboxd-test-shared-{}",
            std::process::id()
        ));
        std::fs::create_dir_all(&dir).expect("create the folder");
        let work = dir.display().to_string();

        let mut env = BTreeMap::new();
        for key in ["PATH", "SystemRoot", "SystemDrive", "ComSpec"] {
            if let Ok(value) = std::env::var(key) {
                env.insert(key.to_string(), value);
            }
        }
        let comspec = std::env::var("ComSpec").expect("ComSpec");
        let args = ["/d".to_string(), "/c".to_string()];
        let write = |confinement: &Confinement, file: &str| {
            confinement
                .spawn(&comspec, &args, &format!("echo x>{file}"), &env, &work)
                .expect("spawn")
                .wait()
                .expect("wait")
        };

        let hello = Hello {
            profile: PROFILE_PROJECT.to_string(),
            capability_sid: "S-1-5-21-1111111111-2222222222-3333333333-4444444444".to_string(),
            ..Hello::default()
        };
        let main = Confinement::detect(&hello);
        let server = Confinement::detect(&hello);
        assert_eq!(write(&main, "main.txt"), 0);
        assert_eq!(write(&server, "server.txt"), 0);
        drop(server);
        let after = write(&main, "after.txt");
        drop(main);
        let _ = std::fs::remove_dir_all(&dir);
        assert_eq!(after, 0, "the session still running lost the folder");
    }

    #[cfg(windows)]
    #[test]
    fn a_profile_that_cannot_be_applied_refuses_the_session() {
        for hello in [
            Hello {
                profile: "root".to_string(),
                ..Hello::default()
            },
            Hello {
                profile: PROFILE_PROJECT.to_string(),
                ..Hello::default()
            },
            Hello {
                profile: PROFILE_PROJECT.to_string(),
                capability_sid: "not-a-sid".to_string(),
                ..Hello::default()
            },
        ] {
            let confinement = Confinement::detect(&hello);
            assert_eq!(confinement.level(), LEVEL_NONE, "{:?}", hello.profile);
            assert!(confinement.capabilities().is_empty());
        }
    }
}
