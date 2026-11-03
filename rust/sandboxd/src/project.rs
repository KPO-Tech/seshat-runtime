//! The `project` profile: an identity a real project can work under.
//!
//! The AppContainer in `appcontainer.rs` refuses reads of the leading directories
//! of a path, and that stops `git init`, `npm install` and running a Node script.
//! This identity keeps those working, without elevation: a restricted token of the
//! same user, at low integrity. A write needs the target to be labelled low, so the
//! working directory is, and nothing else is. See
//! [`docs/spike-windows-project-profile.md`](../../../docs/spike-windows-project-profile.md)
//! for the measurements, and ADR-0002 for why both profiles exist.
//!
//! What it does not do, and `Confinement::reasons` says so: confine reads, close the
//! network, or keep the command away from what DPAPI decrypts for this user.
//!
//! Named folders can still be kept from it. The restricted token carries a capability
//! SID the user's own token does not, so a deny ACE on that SID refuses the command
//! and leaves the user unaffected. It denies `FILE_READ_DATA` alone: `stat` keeps
//! working, so a workspace below a protected folder still runs its tools.

// Windows exposes these APIs only as C functions. Every `unsafe` block states what
// it relies on, and the workspace keeps `unsafe_code = deny` everywhere else.
#![allow(unsafe_code)]

use std::collections::BTreeMap;
use std::ffi::c_void;
use std::io;
use std::os::windows::io::{AsRawHandle, FromRawHandle, OwnedHandle};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::ptr;

use crate::appcontainer::{launch_in_job, wide, ProcessInformation, StartupInfo, LAUNCH_FLAGS};
use crate::process::Process;

const TOKEN_ALL_ACCESS: u32 = 0x000F_01FF;
const TOKEN_QUERY: u32 = 0x0008;
const TOKEN_USER: i32 = 1;
const TOKEN_ELEVATION: i32 = 20;
const TOKEN_INTEGRITY_LEVEL: i32 = 25;
const TOKEN_LOGON_SID: i32 = 28;
const DISABLE_MAX_PRIVILEGE: u32 = 0x01;
const LUA_TOKEN: u32 = 0x04;
const SE_GROUP_INTEGRITY: u32 = 0x20;
const LOW_INTEGRITY_SID: &str = "S-1-16-4096";
/// Everyone, Users, Authenticated Users, Interactive: what the ACLs of a stock
/// machine grant reading through. Without them the restricted check fails on
/// `C:\Program Files` and on a data volume.
const RESTRICTING_WELL_KNOWN: [&str; 4] = ["S-1-1-0", "S-1-5-32-545", "S-1-5-11", "S-1-5-4"];

const SE_FILE_OBJECT: i32 = 1;
const DACL_SECURITY_INFORMATION: u32 = 0x4;
const ACCESS_DENIED_ACE_TYPE: u8 = 1;
const INHERITED_ACE: u8 = 0x10;
const OBJECT_INHERIT_ACE: u8 = 0x1;
const CONTAINER_INHERIT_ACE: u8 = 0x2;
const FILE_READ_DATA: u32 = 0x1;
const ACL_SIZE_INFORMATION: i32 = 2;
const ACL_REVISION: u32 = 2;

/// The runtime of Git Bash, whose presence next to `bash.exe` says it is that shell.
const MSYS_DLL: &str = "msys-2.0.dll";

/// Where tools keep what they would put in `%TEMP%` or a home folder. A command here
/// cannot write those, and `LocalLow` is the one folder of a profile Windows labels
/// low for this purpose, so nothing has to be relabelled and nothing lands in the
/// project. The npm cache is shared between projects by being here.
const SCRATCH_DIRS: [&str; 6] = ["tmp", "roaming", "local", "npm-cache", "go", "cargo"];

#[link(name = "kernel32")]
extern "system" {
    fn GetCurrentProcess() -> *mut c_void;
    fn LocalFree(handle: *mut c_void) -> *mut c_void;
}

#[link(name = "advapi32")]
extern "system" {
    fn OpenProcessToken(process: *mut c_void, access: u32, token: *mut *mut c_void) -> i32;
    fn GetTokenInformation(
        token: *mut c_void,
        class: i32,
        info: *mut c_void,
        len: u32,
        needed: *mut u32,
    ) -> i32;
    fn SetTokenInformation(token: *mut c_void, class: i32, info: *const c_void, len: u32) -> i32;
    fn CreateRestrictedToken(
        token: *mut c_void,
        flags: u32,
        disable_count: u32,
        disable: *const SidAndAttributes,
        delete_count: u32,
        delete: *const c_void,
        restrict_count: u32,
        restrict: *const SidAndAttributes,
        new_token: *mut *mut c_void,
    ) -> i32;
    fn ConvertStringSidToSidW(string_sid: *const u16, sid: *mut *mut c_void) -> i32;
    fn ConvertStringSecurityDescriptorToSecurityDescriptorW(
        descriptor: *const u16,
        revision: u32,
        security_descriptor: *mut *mut c_void,
        size: *mut u32,
    ) -> i32;
    fn GetLengthSid(sid: *mut c_void) -> u32;
    fn EqualSid(a: *mut c_void, b: *mut c_void) -> i32;
    fn CreateProcessAsUserW(
        token: *mut c_void,
        application: *const u16,
        command_line: *mut u16,
        process_attributes: *const c_void,
        thread_attributes: *const c_void,
        inherit_handles: i32,
        flags: u32,
        environment: *const c_void,
        current_directory: *const u16,
        startup_info: *const StartupInfo,
        process_information: *mut ProcessInformation,
    ) -> i32;
    fn GetNamedSecurityInfoW(
        object: *const u16,
        object_type: i32,
        info: u32,
        owner: *mut *mut c_void,
        group: *mut *mut c_void,
        dacl: *mut *mut c_void,
        sacl: *mut *mut c_void,
        descriptor: *mut *mut c_void,
    ) -> u32;
    fn SetNamedSecurityInfoW(
        object: *const u16,
        object_type: i32,
        info: u32,
        owner: *mut c_void,
        group: *mut c_void,
        dacl: *mut c_void,
        sacl: *mut c_void,
    ) -> u32;
    fn GetAclInformation(acl: *mut c_void, info: *mut c_void, len: u32, class: i32) -> i32;
    fn GetAce(acl: *mut c_void, index: u32, ace: *mut *mut c_void) -> i32;
    fn DeleteAce(acl: *mut c_void, index: u32) -> i32;
    fn InitializeAcl(acl: *mut c_void, len: u32, revision: u32) -> i32;
    fn AddAccessDeniedAceEx(
        acl: *mut c_void,
        revision: u32,
        flags: u32,
        mask: u32,
        sid: *mut c_void,
    ) -> i32;
    fn AddAce(acl: *mut c_void, revision: u32, start: u32, ace: *mut c_void, len: u32) -> i32;
}

#[repr(C)]
#[derive(Clone, Copy)]
struct SidAndAttributes {
    sid: *mut c_void,
    attributes: u32,
}

#[repr(C)]
#[derive(Default)]
struct AclSizeInformation {
    ace_count: u32,
    bytes_in_use: u32,
    bytes_free: u32,
}

#[repr(C)]
struct AceHeader {
    kind: u8,
    flags: u8,
    size: u16,
}

/// The header and the mask of an allow or deny ACE; the SID starts right after.
const ACE_SID_OFFSET: usize = 8;

/// A SID `ConvertStringSidToSidW` allocated, freed when dropped.
struct Sid(*mut c_void);

impl Sid {
    fn parse(text: &str) -> io::Result<Self> {
        let wide_text = wide(text);
        let mut raw = ptr::null_mut();
        // SAFETY: `wide_text` is NUL-terminated and outlives the call; the call
        // writes one pointer, which `Drop` frees with the documented `LocalFree`.
        if unsafe { ConvertStringSidToSidW(wide_text.as_ptr(), &mut raw) } == 0 {
            return Err(io::Error::other(format!("not a SID: {text}")));
        }
        Ok(Self(raw))
    }
}

impl Drop for Sid {
    fn drop(&mut self) {
        // SAFETY: allocated by `ConvertStringSidToSidW`, freed exactly once.
        unsafe {
            LocalFree(self.0);
        }
    }
}

#[link(name = "user32")]
extern "system" {
    fn CreateDesktopW(
        desktop: *const u16,
        device: *const u16,
        mode: *const c_void,
        flags: u32,
        access: u32,
        attributes: *const DesktopAttributes,
    ) -> *mut c_void;
    fn CloseDesktop(desktop: *mut c_void) -> i32;
    fn GetProcessWindowStation() -> *mut c_void;
    fn GetUserObjectInformationW(
        object: *mut c_void,
        index: i32,
        info: *mut c_void,
        length: u32,
        needed: *mut u32,
    ) -> i32;
}

#[repr(C)]
struct DesktopAttributes {
    length: u32,
    descriptor: *mut c_void,
    inherit: i32,
}

const DESKTOP_ALL_ACCESS: u32 = 0x000F_01FF;
const UOI_NAME: i32 = 2;

/// Whether this process runs in the user's interactive session. Only `WinSta0` has a screen
/// to protect. A service or a CI runner has none, and a low-integrity process given a
/// private desktop there dies at startup with `STATUS_DLL_INIT_FAILED`.
fn in_interactive_session() -> bool {
    let mut name = [0u16; 64];
    let mut needed = 0u32;
    // SAFETY: the handle is a pseudo-handle that needs no closing, and the buffer's size in
    // bytes is passed along with it.
    let ok = unsafe {
        GetUserObjectInformationW(
            GetProcessWindowStation(),
            UOI_NAME,
            name.as_mut_ptr() as *mut c_void,
            std::mem::size_of_val(&name) as u32,
            &mut needed,
        )
    };
    if ok == 0 {
        // Unknown: keep the protection.
        return true;
    }
    let end = name.iter().position(|&c| c == 0).unwrap_or(name.len());
    let station = String::from_utf16_lossy(&name[..end]);
    eprintln!("sandboxd-diagnostic: window station {station}");
    station.eq_ignore_ascii_case("WinSta0")
}

/// A desktop of the session's own, which is what keeps a command from the user's screen: a job
/// refuses the clipboard and window handles, but a capture of the screen is not one of those.
struct PrivateDesktop {
    handle: *mut c_void,
    // What a process is told to start on, as a nul-terminated wide string.
    name: Vec<u16>,
}

// SAFETY: the handle is opaque, this struct owns it, and it is only used to close it.
unsafe impl Send for PrivateDesktop {}
// SAFETY: as above: nothing reads through the handle.
unsafe impl Sync for PrivateDesktop {}

impl PrivateDesktop {
    /// The descriptor matters. A desktop takes its DACL and label from the token that creates
    /// it, and a low-integrity process that cannot use its desktop dies at startup with
    /// `STATUS_DLL_INIT_FAILED` (0xC0000142). So it names the user, whom the restricted token
    /// carries in both lists, and a low label.
    fn create(user_sid: &str) -> io::Result<Self> {
        static NEXT: std::sync::atomic::AtomicU32 = std::sync::atomic::AtomicU32::new(0);
        let name = format!(
            "seshat-sandboxd-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        );
        let sddl = wide(&format!("D:P(A;;GA;;;{user_sid})S:(ML;;NW;;;LW)"));
        let mut descriptor = ptr::null_mut();
        // SAFETY: `sddl` is nul-terminated, and the call writes one pointer; the size is optional.
        let ok = unsafe {
            ConvertStringSecurityDescriptorToSecurityDescriptorW(
                sddl.as_ptr(),
                1,
                &mut descriptor,
                ptr::null_mut(),
            )
        };
        if ok == 0 {
            return Err(io::Error::last_os_error());
        }
        let attributes = DesktopAttributes {
            length: std::mem::size_of::<DesktopAttributes>() as u32,
            descriptor,
            inherit: 0,
        };
        let wide_name = wide(&name);
        // SAFETY: the name and attributes outlive the call; device and mode must be null.
        let handle = unsafe {
            CreateDesktopW(
                wide_name.as_ptr(),
                ptr::null(),
                ptr::null(),
                0,
                DESKTOP_ALL_ACCESS,
                &attributes,
            )
        };
        let created = if handle.is_null() {
            Err(io::Error::last_os_error())
        } else {
            Ok(())
        };
        // SAFETY: allocated by the conversion above and read by no one after the call.
        unsafe {
            LocalFree(descriptor);
        }
        created?;
        Ok(Self {
            handle,
            // No window station in the name: Windows then takes this process's, and a service or
            // a CI runner is not in `WinSta0`.
            name: wide_name,
        })
    }
}

impl Drop for PrivateDesktop {
    fn drop(&mut self) {
        // SAFETY: the handle is this struct's, closed once. A failure cannot be reported from
        // a destructor, and the desktop goes away with the process anyway.
        unsafe {
            CloseDesktop(self.handle);
        }
    }
}

/// The restricted, low-integrity token a session's commands start with.
pub struct ProjectIdentity {
    token: OwnedHandle,
    scratch: PathBuf,
    elevated: bool,
    desktop: Option<PrivateDesktop>,
}

impl ProjectIdentity {
    /// Builds the token and the scratch folders, or says why it could not.
    pub fn create(capability_sid: &str) -> io::Result<Self> {
        let token = restricted_low_token(capability_sid)?;
        let scratch = scratch_root()?;
        for dir in SCRATCH_DIRS {
            std::fs::create_dir_all(scratch.join(dir))?;
        }
        Ok(Self {
            token,
            scratch,
            elevated: is_elevated()?,
            desktop: if in_interactive_session() {
                Some(PrivateDesktop::create(&token_user_sid_string()?)?)
            } else {
                None
            },
        })
    }

    /// Whether the app runs as an administrator, which Git Bash cannot start under.
    pub fn elevated(&self) -> bool {
        self.elevated
    }

    /// Lets the identity write in `dir`, what is already there included.
    ///
    /// Labelling needs `WRITE_OWNER`, which the owner of a folder on a data volume
    /// often only has through an inherited Modify that does not include it. Then the
    /// owner gives itself full control first, which it may always do. That ACE stays:
    /// it grants the user nothing they could not take, and walking a large project
    /// twice per session to remove it would cost more than it is worth.
    pub fn grant(&self, dir: &Path) -> io::Result<()> {
        if too_wide(dir) {
            return Err(io::Error::other(format!(
                "the project sandbox cannot work in {}: it holds a whole drive or the user's profile, so nothing would be confined. Open a project folder",
                dir.display()
            )));
        }
        if icacls(dir, &["/setintegritylevel", "(OI)(CI)low", "/Q"]).is_ok() {
            return Ok(());
        }
        let user = token_user_sid_string()?;
        icacls(dir, &["/grant", &format!("*{user}:(OI)(CI)F"), "/Q"])?;
        icacls(dir, &["/setintegritylevel", "(OI)(CI)low", "/Q"])
    }

    /// The shell to start for `program`, and the folder to put first on `PATH`.
    ///
    /// Git Bash cannot start here from where it is installed. Its runtime creates a
    /// shared object named after the path of its DLL, and as soon as an ordinary Git
    /// Bash runs on the machine that object exists at medium integrity, where this
    /// token cannot open it: `NtCreateDirectoryObject ... 0xC0000022`. A copy has
    /// another path, so another object, and never meets a running Git Bash. Any other
    /// program comes back as it is.
    pub fn shell(&self, program: &str) -> io::Result<(String, Option<String>)> {
        let Some(install) = msys_install(Path::new(program)) else {
            return Ok((program.to_string(), None));
        };
        // Under an elevated app its runtime stops at `NtSetInformationToken
        // (TokenDefaultDacl), 0xC0000022`, measured on CI. Naming the user in the token's
        // DACL and making it the owner did not change that. A clear refusal beats a shell
        // that dies at startup, and an agent should not run as an administrator anyway.
        if self.elevated {
            return Err(io::Error::other(
                "Git Bash cannot run in the project sandbox while the app runs as an administrator: start it normally",
            ));
        }
        let copy = private_msys(&install, &self.scratch)?;
        let bin = copy.join("usr").join("bin");
        Ok((
            bin.join("bash.exe").display().to_string(),
            Some(bin.display().to_string()),
        ))
    }

    /// Starts `command_line` under the token, with `env` plus the scratch folders.
    pub fn launch(
        &self,
        command_line: &str,
        env: &BTreeMap<String, String>,
        work_dir: &str,
    ) -> io::Result<Process> {
        let env = with_scratch(env, &self.scratch);
        let token = self.token.as_raw_handle();
        launch_in_job(
            command_line,
            &env,
            work_dir,
            None,
            self.desktop.as_ref().map(|desktop| desktop.name.as_slice()),
            "CreateProcessAsUserW",
            |parts| {
                // SAFETY: the token is owned by `self`; every pointer in `parts` is kept
                // alive by `launch_in_job` for the duration of this call.
                unsafe {
                    CreateProcessAsUserW(
                        token,
                        ptr::null(),
                        parts.line,
                        ptr::null(),
                        ptr::null(),
                        1,
                        LAUNCH_FLAGS,
                        parts.environment,
                        parts.directory,
                        parts.startup,
                        parts.created,
                    )
                }
            },
        )
    }
}

/// The client's environment, with what a confined command must not write to its real
/// location pointed into the scratch folders. These replace what the client sent: a
/// `TEMP` the command cannot write fails late and far from its cause. `HOME` stays,
/// so that git still reads the user's name and settings.
fn with_scratch(env: &BTreeMap<String, String>, scratch: &Path) -> BTreeMap<String, String> {
    let at = |name: &str| scratch.join(name).display().to_string();
    let replaced = [
        ("TEMP", at("tmp")),
        ("TMP", at("tmp")),
        ("APPDATA", at("roaming")),
        ("LOCALAPPDATA", at("local")),
        ("npm_config_cache", at("npm-cache")),
        ("GOPATH", at("go")),
        ("CARGO_HOME", at("cargo")),
    ];
    let mut out: BTreeMap<String, String> = env
        .iter()
        .filter(|(key, _)| {
            !replaced
                .iter()
                .any(|(name, _)| key.eq_ignore_ascii_case(name))
        })
        .map(|(key, value)| (key.clone(), value.clone()))
        .collect();
    for (name, value) in replaced {
        out.insert(name.to_string(), value);
    }
    out
}

/// The install root of the Git Bash `program` belongs to: `<root>\bin\bash.exe` or
/// `<root>\usr\bin\bash.exe`, with the runtime in `<root>\usr\bin`.
fn msys_install(program: &Path) -> Option<PathBuf> {
    let name = program.file_name()?.to_str()?;
    if !name.eq_ignore_ascii_case("bash.exe") && !name.eq_ignore_ascii_case("bash") {
        return None;
    }
    program
        .ancestors()
        .skip(1)
        .take(3)
        .find(|root| root.join("usr").join("bin").join(MSYS_DLL).is_file())
        .map(Path::to_path_buf)
}

/// The copy of `<install>\usr\bin` and `<install>\etc` this version of Git uses, made
/// once: 94 MB and 4.5 s on the machine measured. A new version gets a new copy and the
/// old one is removed. The copy goes to a temporary name and is renamed last, so a
/// session that finds the folder finds all of it.
fn private_msys(install: &Path, scratch: &Path) -> io::Result<PathBuf> {
    let dll = std::fs::metadata(install.join("usr").join("bin").join(MSYS_DLL))?;
    let modified = dll
        .modified()?
        .duration_since(std::time::UNIX_EPOCH)
        .map(|age| age.as_secs())
        .unwrap_or(0);
    let name = format!("msys-{}-{modified}", dll.len());
    let target = scratch.join(&name);
    if target.is_dir() {
        return Ok(target);
    }

    let building = scratch.join(format!("{name}.{}.tmp", std::process::id()));
    copy_tree(
        &install.join("usr").join("bin"),
        &building.join("usr").join("bin"),
    )?;
    let etc = install.join("etc");
    if etc.is_dir() {
        copy_tree(&etc, &building.join("etc"))?;
    }
    if let Err(err) = std::fs::rename(&building, &target) {
        let _ = std::fs::remove_dir_all(&building);
        // Another session made the same copy first.
        if !target.is_dir() {
            return Err(err);
        }
    }
    for entry in std::fs::read_dir(scratch)?.flatten() {
        let old = entry.file_name();
        let old = old.to_string_lossy();
        if old.starts_with("msys-") && old != name && !old.ends_with(".tmp") {
            let _ = std::fs::remove_dir_all(entry.path());
        }
    }
    Ok(target)
}

fn copy_tree(from: &Path, to: &Path) -> io::Result<()> {
    std::fs::create_dir_all(to)?;
    for entry in std::fs::read_dir(from)? {
        let entry = entry?;
        let target = to.join(entry.file_name());
        if entry.file_type()?.is_dir() {
            copy_tree(&entry.path(), &target)?;
        } else {
            std::fs::copy(entry.path(), target)?;
        }
    }
    Ok(())
}

/// `%LOCALAPPDATA%\..\LocalLow\Seshat\sandbox`. The sidecar reads this one variable of
/// its own, as the AppContainer launch does: a directory name, never a secret.
fn scratch_root() -> io::Result<PathBuf> {
    let local = std::env::var_os("LOCALAPPDATA")
        .ok_or_else(|| io::Error::other("LOCALAPPDATA is not set"))?;
    let parent = Path::new(&local)
        .parent()
        .ok_or_else(|| io::Error::other("LOCALAPPDATA has no parent"))?;
    Ok(parent.join("LocalLow").join("Seshat").join("sandbox"))
}

fn restricted_low_token(capability_sid: &str) -> io::Result<OwnedHandle> {
    let mut own = ptr::null_mut();
    // SAFETY: the pseudo-handle needs no closing; the call writes one handle.
    if unsafe { OpenProcessToken(GetCurrentProcess(), TOKEN_ALL_ACCESS, &mut own) } == 0 {
        return Err(io::Error::last_os_error());
    }
    // SAFETY: the call just produced this handle and nothing else owns it.
    let own = unsafe { OwnedHandle::from_raw_handle(own) };

    let capability = Sid::parse(capability_sid)?;
    let well_known = RESTRICTING_WELL_KNOWN
        .iter()
        .map(|sid| Sid::parse(sid))
        .collect::<io::Result<Vec<_>>>()?;
    let user = token_info(&own, TOKEN_USER)?;
    let logon = token_info(&own, TOKEN_LOGON_SID)?;
    // TOKEN_USER is one SID_AND_ATTRIBUTES. TOKEN_GROUPS is a count, aligned to a
    // pointer, then the array; the logon SID is its only entry.
    let user_sid = read_pointer(&user, 0);
    let logon_sid = read_pointer(&logon, std::mem::size_of::<usize>());

    let mut restricting: Vec<SidAndAttributes> = vec![capability.0, logon_sid, user_sid]
        .into_iter()
        .map(|sid| SidAndAttributes { sid, attributes: 0 })
        .collect();
    restricting.extend(well_known.iter().map(|sid| SidAndAttributes {
        sid: sid.0,
        attributes: 0,
    }));

    let mut restricted = ptr::null_mut();
    // SAFETY: `restricting` and the SIDs it points at live past this call, which
    // copies them into the new token.
    let ok = unsafe {
        CreateRestrictedToken(
            own.as_raw_handle(),
            DISABLE_MAX_PRIVILEGE | LUA_TOKEN,
            0,
            ptr::null(),
            0,
            ptr::null(),
            restricting.len() as u32,
            restricting.as_ptr(),
            &mut restricted,
        )
    };
    if ok == 0 {
        return Err(io::Error::last_os_error());
    }
    // SAFETY: as above.
    let restricted = unsafe { OwnedHandle::from_raw_handle(restricted) };

    let low = Sid::parse(LOW_INTEGRITY_SID)?;
    let label = SidAndAttributes {
        sid: low.0,
        attributes: SE_GROUP_INTEGRITY,
    };
    // SAFETY: `low.0` is a valid SID; the length is the structure plus that SID, as
    // TOKEN_MANDATORY_LABEL is documented to be passed.
    let length = std::mem::size_of::<SidAndAttributes>() as u32 + unsafe { GetLengthSid(low.0) };
    // SAFETY: `label` and the SID live past the call, which copies them.
    let ok = unsafe {
        SetTokenInformation(
            restricted.as_raw_handle(),
            TOKEN_INTEGRITY_LEVEL,
            &label as *const SidAndAttributes as *const c_void,
            length,
        )
    };
    if ok == 0 {
        return Err(io::Error::last_os_error());
    }
    Ok(restricted)
}

/// Whether this process runs elevated.
fn is_elevated() -> io::Result<bool> {
    let mut own = ptr::null_mut();
    // SAFETY: the pseudo-handle needs no closing; the call writes one handle.
    if unsafe { OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &mut own) } == 0 {
        return Err(io::Error::last_os_error());
    }
    // SAFETY: the call just produced this handle and nothing else owns it.
    let own = unsafe { OwnedHandle::from_raw_handle(own) };
    let mut elevated = 0u32;
    let mut needed = 0u32;
    // SAFETY: TOKEN_ELEVATION is one DWORD, and `elevated` is one.
    let ok = unsafe {
        GetTokenInformation(
            own.as_raw_handle(),
            TOKEN_ELEVATION,
            &mut elevated as *mut u32 as *mut c_void,
            std::mem::size_of::<u32>() as u32,
            &mut needed,
        )
    };
    if ok == 0 {
        return Err(io::Error::last_os_error());
    }
    Ok(elevated != 0)
}

fn token_info(token: &OwnedHandle, class: i32) -> io::Result<Vec<usize>> {
    let mut needed = 0u32;
    // SAFETY: a null buffer of length zero asks for the size, which is documented.
    unsafe {
        GetTokenInformation(
            token.as_raw_handle(),
            class,
            ptr::null_mut(),
            0,
            &mut needed,
        )
    };
    // `usize` elements: the buffer holds pointers, so it must be aligned for them.
    let mut buffer = vec![0usize; (needed as usize).div_ceil(std::mem::size_of::<usize>())];
    // SAFETY: the buffer is at least `needed` bytes.
    let ok = unsafe {
        GetTokenInformation(
            token.as_raw_handle(),
            class,
            buffer.as_mut_ptr() as *mut c_void,
            needed,
            &mut needed,
        )
    };
    if ok == 0 {
        return Err(io::Error::last_os_error());
    }
    Ok(buffer)
}

fn read_pointer(buffer: &[usize], byte_offset: usize) -> *mut c_void {
    buffer[byte_offset / std::mem::size_of::<usize>()] as *mut c_void
}

/// The current user's SID as text, for the owner's own grant.
fn token_user_sid_string() -> io::Result<String> {
    let output = Command::new("whoami")
        .args(["/user", "/fo", "csv", "/nh"])
        .stdin(Stdio::null())
        .output()?;
    let text = String::from_utf8_lossy(&output.stdout);
    text.trim()
        .rsplit(',')
        .next()
        .map(|sid| sid.trim_matches('"').to_string())
        .filter(|sid| sid.starts_with("S-1-"))
        .ok_or_else(|| io::Error::other("could not read the user's SID"))
}

fn icacls(dir: &Path, args: &[&str]) -> io::Result<()> {
    let output = Command::new("icacls")
        .arg(dir)
        .args(args)
        .stdin(Stdio::null())
        .output()?;
    if output.status.success() {
        return Ok(());
    }
    let detail = String::from_utf8_lossy(&output.stdout);
    let errors = String::from_utf8_lossy(&output.stderr);
    Err(io::Error::other(format!(
        "icacls {} on {}: {} {}",
        args.join(" "),
        dir.display(),
        detail.trim(),
        errors.trim()
    )))
}

/// A DACL read from a path, with the descriptor that holds it.
struct Dacl {
    descriptor: *mut c_void,
    acl: *mut c_void,
}

impl Dacl {
    fn read(path: &[u16]) -> io::Result<Self> {
        let (mut acl, mut descriptor) = (ptr::null_mut(), ptr::null_mut());
        // SAFETY: `path` is NUL-terminated; the call writes two pointers, and the
        // descriptor they point into is freed by `Drop`.
        let code = unsafe {
            GetNamedSecurityInfoW(
                path.as_ptr(),
                SE_FILE_OBJECT,
                DACL_SECURITY_INFORMATION,
                ptr::null_mut(),
                ptr::null_mut(),
                &mut acl,
                ptr::null_mut(),
                &mut descriptor,
            )
        };
        if code != 0 {
            return Err(io::Error::from_raw_os_error(code as i32));
        }
        Ok(Self { descriptor, acl })
    }

    fn size(&self) -> io::Result<AclSizeInformation> {
        let mut info = AclSizeInformation::default();
        // SAFETY: `info` has the layout the class documents, and the ACL is valid.
        let ok = unsafe {
            GetAclInformation(
                self.acl,
                &mut info as *mut _ as *mut c_void,
                std::mem::size_of::<AclSizeInformation>() as u32,
                ACL_SIZE_INFORMATION,
            )
        };
        if ok == 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(info)
    }

    /// The index of the explicit deny ACE that names `sid`, if there is one.
    fn find_deny(&self, sid: &Sid) -> io::Result<Option<u32>> {
        for index in 0..self.size()?.ace_count {
            let mut ace = ptr::null_mut();
            // SAFETY: the index is below the count, and the call writes one pointer
            // into the ACL, which `self` keeps alive.
            if unsafe { GetAce(self.acl, index, &mut ace) } == 0 {
                return Err(io::Error::last_os_error());
            }
            // SAFETY: every ACE starts with this header.
            let header = unsafe { &*(ace as *const AceHeader) };
            if header.kind != ACCESS_DENIED_ACE_TYPE || header.flags & INHERITED_ACE != 0 {
                continue;
            }
            // SAFETY: a deny ACE holds its SID at this offset, and both SIDs are valid.
            let same =
                unsafe { EqualSid((ace as *mut u8).add(ACE_SID_OFFSET) as *mut c_void, sid.0) };
            if same != 0 {
                return Ok(Some(index));
            }
        }
        Ok(None)
    }
}

impl Drop for Dacl {
    fn drop(&mut self) {
        // SAFETY: allocated by `GetNamedSecurityInfoW`, freed exactly once.
        unsafe {
            LocalFree(self.descriptor);
        }
    }
}

fn write_dacl(path: &mut [u16], acl: *mut c_void) -> io::Result<()> {
    // SAFETY: `path` is NUL-terminated and `acl` is valid; the owner, group and SACL
    // are not being set. The system propagates the change to what inherits from it.
    let code = unsafe {
        SetNamedSecurityInfoW(
            path.as_mut_ptr(),
            SE_FILE_OBJECT,
            DACL_SECURITY_INFORMATION,
            ptr::null_mut(),
            ptr::null_mut(),
            acl,
            ptr::null_mut(),
        )
    };
    if code != 0 {
        return Err(io::Error::from_raw_os_error(code as i32));
    }
    Ok(())
}

/// Whether granting `dir` would hand a command a whole drive or the user's profile. A
/// path that cannot be resolved is left to the grant, which then fails on it.
fn too_wide(dir: &Path) -> bool {
    let Ok(dir) = dir.canonicalize() else {
        return false;
    };
    if dir.parent().is_none() {
        return true;
    }
    let Some(home) = std::env::var_os("USERPROFILE") else {
        return false;
    };
    let Ok(home) = Path::new(&home).canonicalize() else {
        return false;
    };
    let lower = |path: &Path| path.display().to_string().to_lowercase();
    Path::new(&lower(&home)).starts_with(lower(&dir))
}

/// Keeps `path` from a command carrying `capability_sid`. A path that does not exist
/// is skipped: the list names what may be on a machine. Doing it twice changes nothing.
pub fn protect(capability_sid: &str, path: &Path) -> io::Result<()> {
    if !path.exists() {
        return Ok(());
    }
    for target in protection_targets(path) {
        protect_object(capability_sid, &target)?;
    }
    Ok(())
}

/// What a named path stands for: itself and, if it is a link, what it leads to. A refusal on a
/// junction alone leaves its target readable by its real name, measured.
fn protection_targets(path: &Path) -> Vec<PathBuf> {
    let mut targets = vec![path.to_path_buf()];
    if let Ok(real) = path.canonicalize() {
        // Both sides resolved, so a path that is no link is not named twice.
        if path.is_symlink() || std::fs::symlink_metadata(path).is_ok_and(|m| is_reparse(&m)) {
            targets.push(real);
        }
    }
    targets
}

fn is_reparse(metadata: &std::fs::Metadata) -> bool {
    use std::os::windows::fs::MetadataExt;
    const FILE_ATTRIBUTE_REPARSE_POINT: u32 = 0x400;
    metadata.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0
}

fn protect_object(capability_sid: &str, path: &Path) -> io::Result<()> {
    let sid = Sid::parse(capability_sid)?;
    let mut name = wide(&path.display().to_string());
    let old = Dacl::read(&name)?;
    if old.find_deny(&sid)?.is_some() {
        return Ok(());
    }

    let info = old.size()?;
    // SAFETY: `sid.0` is a valid SID.
    let ace_size = ACE_SID_OFFSET as u32 + unsafe { GetLengthSid(sid.0) };
    // `u64` elements, so the new ACL has the alignment the API asks of it.
    let mut buffer = vec![0u64; (info.bytes_in_use + ace_size + 16).div_ceil(8) as usize];
    let new_acl = buffer.as_mut_ptr() as *mut c_void;
    let size = (buffer.len() * 8) as u32;
    // SAFETY: the buffer is `size` bytes and lives past every call below.
    if unsafe { InitializeAcl(new_acl, size, ACL_REVISION) } == 0 {
        return Err(io::Error::last_os_error());
    }
    let flags = if path.is_dir() {
        u32::from(OBJECT_INHERIT_ACE | CONTAINER_INHERIT_ACE)
    } else {
        0
    };
    // Deny ACEs come first in canonical order: this one goes in front of the rest.
    // SAFETY: the buffer has room for this ACE, by the size computed above.
    if unsafe { AddAccessDeniedAceEx(new_acl, ACL_REVISION, flags, FILE_READ_DATA, sid.0) } == 0 {
        return Err(io::Error::last_os_error());
    }
    for index in 0..info.ace_count {
        let mut ace = ptr::null_mut();
        // SAFETY: the index is below the count; the pointer is into `old`, alive here.
        if unsafe { GetAce(old.acl, index, &mut ace) } == 0 {
            return Err(io::Error::last_os_error());
        }
        // SAFETY: every ACE starts with this header.
        let header = unsafe { &*(ace as *const AceHeader) };
        // Inherited ACEs are rebuilt from the parent by the system when the DACL is set.
        if header.flags & INHERITED_ACE != 0 {
            continue;
        }
        // SAFETY: `ace` is `header.size` bytes, and the buffer has room for the old ACL.
        if unsafe { AddAce(new_acl, ACL_REVISION, u32::MAX, ace, u32::from(header.size)) } == 0 {
            return Err(io::Error::last_os_error());
        }
    }
    write_dacl(&mut name, new_acl)
}

/// Takes `protect` back. It needs nothing but the SID and the path, so it works after a
/// crash, and on a path never protected it changes nothing.
pub fn unprotect(capability_sid: &str, path: &Path) -> io::Result<()> {
    if !path.exists() {
        return Ok(());
    }
    for target in protection_targets(path) {
        unprotect_object(capability_sid, &target)?;
    }
    Ok(())
}

fn unprotect_object(capability_sid: &str, path: &Path) -> io::Result<()> {
    let sid = Sid::parse(capability_sid)?;
    let mut name = wide(&path.display().to_string());
    let dacl = Dacl::read(&name)?;
    let mut changed = false;
    while let Some(index) = dacl.find_deny(&sid)? {
        // SAFETY: the index was just found in this ACL.
        if unsafe { DeleteAce(dacl.acl, index) } == 0 {
            return Err(io::Error::last_os_error());
        }
        changed = true;
    }
    if changed {
        write_dacl(&mut name, dacl.acl)?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use std::sync::atomic::{AtomicU32, Ordering};

    const CAPABILITY: &str = "S-1-5-21-1111111111-2222222222-3333333333-4444444444";
    static NEXT: AtomicU32 = AtomicU32::new(0);

    /// A folder under the user's profile, labelled medium like a real one; the temp
    /// folder of a test run may be anywhere.
    struct Dir(PathBuf);

    impl Dir {
        fn new(tag: &str) -> Self {
            let n = NEXT.fetch_add(1, Ordering::Relaxed);
            let home = std::env::var_os("USERPROFILE").expect("USERPROFILE");
            let path = Path::new(&home).join(format!(
                "seshat-sandboxd-test-{}-{tag}-{n}",
                std::process::id()
            ));
            fs::create_dir_all(&path).expect("create test dir");
            Self(path)
        }
    }

    impl Drop for Dir {
        fn drop(&mut self) {
            let _ = unprotect(CAPABILITY, &self.0);
            let _ = fs::remove_dir_all(&self.0);
        }
    }

    fn env() -> BTreeMap<String, String> {
        let mut env = BTreeMap::new();
        for key in [
            "PATH",
            "Path",
            "SystemRoot",
            "SystemDrive",
            "ComSpec",
            "USERPROFILE",
            "HOME",
        ] {
            if let Ok(value) = std::env::var(key) {
                env.insert(key.to_string(), value);
            }
        }
        env
    }

    /// Runs a `cmd` line under the identity. Returns the exit code and what it printed.
    fn run(identity: &ProjectIdentity, line: &str, cwd: &Path) -> (i32, String) {
        let comspec = std::env::var("ComSpec").expect("ComSpec");
        let command = format!("\"{comspec}\" /d /c \"{line}\"");
        let mut child = identity
            .launch(&command, &env(), &cwd.display().to_string())
            .expect("launch");
        let mut text = String::new();
        if let Some(mut out) = child.take_stdout() {
            std::io::Read::read_to_string(&mut out, &mut text).ok();
        }
        if let Some(mut err) = child.take_stderr() {
            std::io::Read::read_to_string(&mut err, &mut text).ok();
        }
        (child.wait().expect("wait"), text)
    }

    #[test]
    fn writes_land_in_the_granted_folder_and_nowhere_else() {
        let identity = ProjectIdentity::create(CAPABILITY).expect("identity");
        let work = Dir::new("work");
        let outside = Dir::new("outside");
        fs::write(work.0.join("already-there.txt"), "x").unwrap();
        identity.grant(&work.0).expect("grant");

        let (code, _) = run(&identity, "echo ok>new.txt", &work.0);
        assert_eq!(code, 0);
        assert!(work.0.join("new.txt").exists());

        let (code, _) = run(&identity, "echo y>>already-there.txt", &work.0);
        assert_eq!(
            code, 0,
            "a file that existed before the grant must be writable"
        );

        let target = outside.0.join("escaped.txt");
        let (code, text) = run(
            &identity,
            &format!("echo no>\"{}\"", target.display()),
            &work.0,
        );
        assert_ne!(code, 0, "{text}");
        assert!(
            !target.exists(),
            "a write landed outside the granted folder"
        );
    }

    #[test]
    fn a_drive_or_the_profile_is_too_wide_and_a_folder_in_it_is_not() {
        let home = PathBuf::from(std::env::var_os("USERPROFILE").expect("USERPROFILE"));
        assert!(too_wide(&home));
        assert!(too_wide(Path::new(
            &home.display().to_string().to_uppercase()
        )));
        let parent = home.parent().expect("the profile has a parent");
        assert!(too_wide(parent));
        assert!(too_wide(home.ancestors().last().expect("the drive")));

        let inside = Dir::new("wide");
        assert!(!too_wide(&inside.0));
        assert!(!too_wide(Path::new("Z:\\does\\not\\exist")));
    }

    #[test]
    fn granting_too_wide_a_folder_is_refused_and_says_why() {
        let identity = ProjectIdentity::create(CAPABILITY).expect("identity");
        let home = PathBuf::from(std::env::var_os("USERPROFILE").expect("USERPROFILE"));
        let err = identity
            .grant(home.ancestors().last().expect("the drive"))
            .expect_err("a drive must be refused");
        assert!(err.to_string().contains("project folder"), "{err}");
    }

    /// The runtime's pipes to the sidecar are inheritable handles the sidecar received. A
    /// command holding them could forge protocol frames.
    #[test]
    fn a_command_inherits_only_its_own_pipes() {
        use crate::appcontainer::{create_pipe, SecurityAttributes};
        let identity = ProjectIdentity::create(CAPABILITY).expect("identity");
        let work = Dir::new("handles");
        identity.grant(&work.0).expect("grant");

        // An inheritable handle in this process, standing for the protocol pipe.
        let inheritable = SecurityAttributes {
            length: std::mem::size_of::<SecurityAttributes>() as u32,
            descriptor: ptr::null_mut(),
            inherit: 1,
        };
        let (read, write) = create_pipe(&inheritable).expect("pipe");
        let comspec = std::env::var("ComSpec").expect("ComSpec");
        let command =
            format!("\"{comspec}\" /d /c \"powershell -NoProfile -Command Start-Sleep 3\"");
        let mut child = identity
            .launch(&command, &env(), &work.0.display().to_string())
            .expect("launch");
        drop(write);

        // End-of-file comes at once unless the command holds a copy of the write end.
        let started = std::time::Instant::now();
        let mut sink = Vec::new();
        std::io::Read::read_to_end(&mut std::fs::File::from(read), &mut sink).ok();
        let held = started.elapsed();
        let finished = std::time::Instant::now();
        assert_eq!(child.wait().expect("wait"), 0, "the command must have run");
        assert!(
            held < std::time::Duration::from_secs(1)
                && finished.elapsed() > std::time::Duration::from_secs(1),
            "the command held an inherited handle for {held:?}, then ran {:?} more",
            finished.elapsed()
        );
    }

    /// Measured before this: a command read the clipboard, captured the screen, and saw the
    /// titles of the user's windows. Nothing from those is read or printed here.
    #[test]
    fn a_command_cannot_reach_the_users_session() {
        let identity = ProjectIdentity::create(CAPABILITY).expect("identity");
        let work = Dir::new("session");
        identity.grant(&work.0).expect("grant");
        let script = r#"
Add-Type -AssemblyName System.Drawing
Add-Type @'
using System; using System.Runtime.InteropServices;
public static class P {
 [DllImport("user32.dll")] public static extern bool OpenClipboard(IntPtr h);
 [DllImport("user32.dll")] public static extern bool CloseClipboard();
 [DllImport("user32.dll")] public static extern IntPtr GetClipboardData(uint f);
 public delegate bool E(IntPtr h, IntPtr l);
 [DllImport("user32.dll")] public static extern bool EnumWindows(E e, IntPtr l);
 [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
 [DllImport("user32.dll")] public static extern int GetWindowTextLength(IntPtr h);
 public static int Titled(){int n=0; EnumWindows((h,l)=>{ if(IsWindowVisible(h)&&GetWindowTextLength(h)>0)n++; return true;},IntPtr.Zero); return n;}
}
'@
$d=$false; if([P]::OpenClipboard([IntPtr]::Zero)){ $d=([P]::GetClipboardData(13) -ne [IntPtr]::Zero); [void][P]::CloseClipboard() }
"clipboard=$d"
"windows=" + [P]::Titled()
$colours=0
try { $b=New-Object Drawing.Bitmap 100,100; $g=[Drawing.Graphics]::FromImage($b); $g.CopyFromScreen(0,0,0,0,$b.Size); $c=@{}; for($x=0;$x -lt 100;$x+=5){for($y=0;$y -lt 100;$y+=5){$c[$b.GetPixel($x,$y).ToArgb()]=1}}; $colours=$c.Count } catch { $colours=0 }
"colours=$colours"
"#;
        fs::write(work.0.join("session.ps1"), script).unwrap();
        let (code, text) = run(
            &identity,
            "powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -STA -File session.ps1",
            &work.0,
        );
        assert_eq!(code, 0, "{text}");
        assert!(text.contains("clipboard=False"), "{text}");
        assert!(text.contains("windows=0"), "{text}");
        // A blank private desktop captures as one colour at most.
        let colours: u32 = text
            .split("colours=")
            .nth(1)
            .and_then(|rest| rest.trim().parse().ok())
            .unwrap_or(u32::MAX);
        assert!(colours <= 1, "{text}");
    }

    #[test]
    fn protecting_a_junction_also_protects_what_it_leads_to() {
        let identity = ProjectIdentity::create(CAPABILITY).expect("identity");
        let work = Dir::new("junction-work");
        identity.grant(&work.0).expect("grant");
        let secret = Dir::new("junction-secret");
        fs::write(secret.0.join("key.txt"), "HIDDEN").unwrap();
        let parent = Dir::new("junction-parent");
        let junction = parent.0.join("linked");
        let made = Command::new("cmd")
            .args(["/c", "mklink", "/J"])
            .arg(&junction)
            .arg(&secret.0)
            .stdout(Stdio::null())
            .status()
            .expect("mklink");
        assert!(made.success());

        protect(CAPABILITY, &junction).expect("protect");
        for read in [junction.join("key.txt"), secret.0.join("key.txt")] {
            let (code, text) = run(&identity, &format!("type \"{}\"", read.display()), &work.0);
            assert_ne!(code, 0, "{} was read: {text}", read.display());
        }
        assert_eq!(
            fs::read_to_string(secret.0.join("key.txt")).unwrap(),
            "HIDDEN",
            "the user is not affected"
        );

        unprotect(CAPABILITY, &junction).expect("unprotect");
        let (code, text) = run(
            &identity,
            &format!("type \"{}\"", secret.0.join("key.txt").display()),
            &work.0,
        );
        assert_eq!(code, 0, "{text}");
        assert!(text.contains("HIDDEN"), "{text}");
        let _ = fs::remove_dir(&junction);
    }

    #[test]
    fn a_junction_planted_by_a_command_does_not_reach_a_protected_folder() {
        let identity = ProjectIdentity::create(CAPABILITY).expect("identity");
        let work = Dir::new("planted-work");
        identity.grant(&work.0).expect("grant");
        let secret = Dir::new("planted-secret");
        fs::write(secret.0.join("key.txt"), "HIDDEN").unwrap();
        protect(CAPABILITY, &secret.0).expect("protect");

        let _ = run(
            &identity,
            &format!("mklink /J planted \"{}\"", secret.0.display()),
            &work.0,
        );
        let (code, text) = run(&identity, "type planted\\key.txt", &work.0);
        assert_ne!(code, 0, "{text}");
        assert!(!text.contains("HIDDEN"), "{text}");
        let _ = fs::remove_dir(work.0.join("planted"));
    }

    #[test]
    fn a_protected_folder_is_refused_to_the_command_and_not_to_the_user() {
        let identity = ProjectIdentity::create(CAPABILITY).expect("identity");
        let work = Dir::new("reader");
        let secrets = Dir::new("secrets");
        let secret = secrets.0.join("id_rsa");
        fs::write(&secret, "SECRET").unwrap();
        identity.grant(&work.0).expect("grant");

        let (code, text) = run(
            &identity,
            &format!("type \"{}\"", secret.display()),
            &work.0,
        );
        assert_eq!(code, 0, "readable before it is protected: {text}");

        protect(CAPABILITY, &secrets.0).expect("protect");
        protect(CAPABILITY, &secrets.0).expect("protecting twice changes nothing");
        let (code, text) = run(
            &identity,
            &format!("type \"{}\"", secret.display()),
            &work.0,
        );
        assert_ne!(code, 0, "a protected file was read");
        assert!(!text.contains("SECRET"), "{text}");
        assert_eq!(
            fs::read_to_string(&secret).unwrap(),
            "SECRET",
            "the user must still read it"
        );

        unprotect(CAPABILITY, &secrets.0).expect("unprotect");
        let (code, text) = run(
            &identity,
            &format!("type \"{}\"", secret.display()),
            &work.0,
        );
        assert_eq!(code, 0, "unprotect must give it back: {text}");
    }

    #[test]
    fn protecting_a_single_file_works_and_a_missing_path_is_skipped() {
        let identity = ProjectIdentity::create(CAPABILITY).expect("identity");
        let work = Dir::new("reader-file");
        let folder = Dir::new("file-owner");
        let file = folder.0.join(".git-credentials");
        fs::write(&file, "SECRET").unwrap();
        identity.grant(&work.0).expect("grant");

        protect(CAPABILITY, &file).expect("protect a file");
        let (code, text) = run(&identity, &format!("type \"{}\"", file.display()), &work.0);
        assert_ne!(code, 0, "{text}");
        unprotect(CAPABILITY, &file).expect("unprotect a file");

        protect(CAPABILITY, &folder.0.join("does-not-exist"))
            .expect("a missing path is not an error");
    }

    #[test]
    fn a_workspace_below_a_protected_folder_still_works() {
        let identity = ProjectIdentity::create(CAPABILITY).expect("identity");
        let config = Dir::new("config");
        fs::write(config.0.join("credentials"), "SECRET").unwrap();
        let workspace = config.0.join("workspaces").join("w1");
        fs::create_dir_all(&workspace).unwrap();
        protect(CAPABILITY, &config.0).expect("protect");
        identity.grant(&workspace).expect("grant");

        let (code, text) = run(&identity, "echo ok>in.txt", &workspace);
        assert_eq!(code, 0, "{text}");
        let (code, text) = run(
            &identity,
            &format!("type \"{}\"", config.0.join("credentials").display()),
            &workspace,
        );
        assert_ne!(code, 0, "{text}");
    }

    /// The spike measured this on a plain low-integrity token; this is the final one.
    #[test]
    fn a_detached_grandchild_does_not_outlive_the_job() {
        let identity = ProjectIdentity::create(CAPABILITY).expect("identity");
        let work = Dir::new("job");
        identity.grant(&work.0).expect("grant");
        let comspec = std::env::var("ComSpec").expect("ComSpec");
        // A grandchild started detached with `start`, which writes after a delay.
        let line = "start /b cmd /c \"ping -n 5 127.0.0.1 >nul & echo late>late.txt\" & ping -n 30 127.0.0.1 >nul";
        let mut child = identity
            .launch(
                &format!("\"{comspec}\" /d /c \"{line}\""),
                &env(),
                &work.0.display().to_string(),
            )
            .expect("launch");
        std::thread::sleep(std::time::Duration::from_secs(1));
        child.terminate();
        std::thread::sleep(std::time::Duration::from_secs(6));
        assert!(
            !work.0.join("late.txt").exists(),
            "the grandchild outlived the job"
        );
    }

    #[test]
    fn git_bash_runs_a_real_script_from_its_private_copy() {
        let Some(bash) = ["C:\\Program Files\\Git\\bin\\bash.exe"]
            .into_iter()
            .find(|p| Path::new(p).is_file())
        else {
            return; // No Git Bash on this machine: nothing to measure.
        };
        let identity = ProjectIdentity::create(CAPABILITY).expect("identity");
        let work = Dir::new("bash");
        identity.grant(&work.0).expect("grant");
        if identity.elevated() {
            // CI runs elevated: there the shell must refuse, and say why.
            let err = identity
                .shell(bash)
                .expect_err("Git Bash cannot run elevated");
            assert!(err.to_string().contains("administrator"), "{err}");
            return;
        }
        let (program, bin) = identity.shell(bash).expect("private copy");
        assert_ne!(program, bash);
        let bin = bin.expect("a folder for PATH");

        let mut environment = env();
        let path = environment
            .remove("PATH")
            .or_else(|| environment.remove("Path"))
            .unwrap_or_default();
        environment.insert("PATH".to_string(), format!("{bin};{path}"));
        let line = format!("\"{program}\" -c \"echo hi | tr a-z A-Z > out.txt\"");
        let mut child = identity
            .launch(&line, &environment, &work.0.display().to_string())
            .expect("launch");
        let mut said = String::new();
        if let Some(mut err) = child.take_stderr() {
            std::io::Read::read_to_string(&mut err, &mut said).ok();
        }
        assert_eq!(child.wait().expect("wait"), 0, "bash said: {said}");
        assert_eq!(
            fs::read_to_string(work.0.join("out.txt")).unwrap().trim(),
            "HI"
        );
    }

    #[test]
    fn only_git_bash_is_replaced() {
        let identity = ProjectIdentity::create(CAPABILITY).expect("identity");
        let (program, bin) = identity.shell("C:\\Windows\\System32\\cmd.exe").unwrap();
        assert_eq!(program, "C:\\Windows\\System32\\cmd.exe");
        assert!(bin.is_none());
        assert_eq!(msys_install(Path::new("D:\\elsewhere\\bash.exe")), None);
    }

    #[test]
    fn the_scratch_folders_are_writable_and_replace_what_the_client_sent() {
        let identity = ProjectIdentity::create(CAPABILITY).expect("identity");
        let work = Dir::new("scratch");
        identity.grant(&work.0).expect("grant");
        let (code, text) = run(
            &identity,
            "echo x>\"%TEMP%\\seshat-test.txt\" && del \"%TEMP%\\seshat-test.txt\"",
            &work.0,
        );
        assert_eq!(code, 0, "{text}");

        let mut client = BTreeMap::new();
        client.insert("temp".to_string(), "C:\\real".to_string());
        client.insert("KEPT".to_string(), "yes".to_string());
        let out = with_scratch(&client, Path::new("C:\\scratch"));
        assert!(!out.contains_key("temp"));
        assert_eq!(
            out.get("TEMP").map(String::as_str),
            Some("C:\\scratch\\tmp")
        );
        assert_eq!(out.get("KEPT").map(String::as_str), Some("yes"));
    }
}
