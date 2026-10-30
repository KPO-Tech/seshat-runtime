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

use crate::frame::{CAP_FS_CONFINE, CAP_LIMITS, CAP_NETWORK_DENY, LEVEL_DEGRADED, LEVEL_NONE};
use crate::process::Process;

/// The three states a session can be in, spelled out rather than inferred from
/// two `Option`s: a session either has an identity, or it does not and nothing
/// will run.
#[cfg(windows)]
enum Identity {
    Confined(crate::appcontainer::AppContainer),
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
    /// Creates the identity this session will run under.
    ///
    /// A failure becomes `level: none`, which the runtime treats as a refusal to
    /// execute. Falling back to an unconfined run is the one thing this must
    /// never do.
    pub fn detect() -> Self {
        #[cfg(windows)]
        let identity = {
            let name = format!("seshat.sandboxd.{}", std::process::id());
            match crate::appcontainer::AppContainer::create_replacing(&name) {
                Ok(profile) => Identity::Confined(profile),
                Err(err) => Identity::Refused(err.to_string()),
            }
        };
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
            Identity::Confined(_) => LEVEL_DEGRADED,
            Identity::Refused(_) => LEVEL_NONE,
            #[cfg(test)]
            Identity::Direct => LEVEL_DEGRADED,
        };
        #[cfg(not(windows))]
        let level = LEVEL_DEGRADED;

        level
    }

    pub fn capabilities(&self) -> Vec<&'static str> {
        #[cfg(windows)]
        let capabilities = match &self.identity {
            Identity::Confined(_) => vec![CAP_LIMITS, CAP_FS_CONFINE, CAP_NETWORK_DENY],
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
            Identity::Refused(refusal) => {
                vec![format!("no app container identity: {refusal}")]
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
            Identity::Refused(refusal) => Err(io::Error::other(format!(
                "no app container identity: {refusal}"
            ))),
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
        if let Identity::Confined(profile) = &self.identity {
            let granted = self
                .granted
                .lock()
                .map(|granted| granted.clone())
                .unwrap_or_default();
            for dir in granted {
                // Best effort: a destructor cannot report anything, and the
                // grant is inert once the profile that named it is gone.
                let _ = profile.revoke(std::path::Path::new(&dir));
            }
        }
    }
}

/// Builds the `lpCommandLine` `CreateProcessW` takes: the interpreter, its
/// arguments, and the client's command last.
///
/// The command is appended as written, without a second layer of quoting: it is
/// a shell command string, not an argv element, and `cmd /C` expects exactly
/// that. Quoting it would make the shell strip those quotes back off.
#[cfg(windows)]
fn command_line(program: &str, args: &[String], command: &str) -> String {
    let mut line = String::with_capacity(program.len() + command.len() + args.len() * 8 + 2);
    push_argument(&mut line, program);
    for arg in args {
        line.push(' ');
        push_argument(&mut line, arg);
    }
    line.push(' ');
    line.push_str(command);
    line
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

#[cfg(test)]
mod tests {
    use super::*;

    #[cfg(windows)]
    #[test]
    fn a_program_with_a_space_is_quoted_and_the_command_is_not() {
        let args = vec!["/C".to_string()];
        let line = command_line(
            "C:\\Program Files\\Git\\cmd\\git.exe",
            &args,
            "status --porcelain",
        );
        assert_eq!(
            line,
            "\"C:\\Program Files\\Git\\cmd\\git.exe\" /C status --porcelain"
        );
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
        let confinement = Confinement::detect();
        assert_eq!(
            confinement.level(),
            LEVEL_DEGRADED,
            "reasons: {:?}",
            confinement.reasons()
        );
        let capabilities = confinement.capabilities();
        assert!(capabilities.contains(&CAP_FS_CONFINE), "{capabilities:?}");
        assert!(capabilities.contains(&CAP_NETWORK_DENY), "{capabilities:?}");
        assert_eq!(confinement.reasons().len(), 3);
    }
}
