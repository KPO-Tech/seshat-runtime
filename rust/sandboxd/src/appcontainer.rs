//! The Windows security identity a sandboxed command runs under, and the launch
//! that puts a command inside it.
//!
//! An AppContainer is the one identity Windows hands to a normal user: no
//! elevation, no local account created, and a package SID that no ACL grants
//! anything to until we say so. That is what makes a *path* rule enforceable
//! without administrator rights — a deny ACE has to name an identity, and the
//! only identity available for free is this one. See
//! [`docs/spike-windows-confinement.md`](../../../docs/spike-windows-confinement.md)
//! and [`docs/spike-windows-toolchain.md`](../../../docs/spike-windows-toolchain.md)
//! for the measurements behind every claim made here.
//!
//! What this module does: creates the profile, reads its SID, grants the run's
//! working directory to that SID, launches the command with
//! `CreateProcessW` inside the identity, and deletes the profile when the
//! session ends. The environment block is built here rather than inherited,
//! because that is the only way to guarantee that nothing of the sidecar's own
//! environment reaches a model-issued command.
//!
//! What it deliberately does not do: elevate, install anything, or write an ACE
//! on a directory outside the run's working directory. Every refusal that
//! follows from that is reported by `Confinement`, not hidden here.

// Windows exposes these APIs only as C functions; the standard library has no
// wrapper. Every `unsafe` block below states what it relies on, and the
// workspace keeps `unsafe_code = deny` everywhere else.
#![allow(unsafe_code)]

use std::collections::BTreeMap;
use std::ffi::c_void;
use std::fs::File;
use std::io;
use std::os::windows::io::{AsRawHandle, FromRawHandle, OwnedHandle};
use std::path::Path;
use std::process::{Command, Stdio};
use std::ptr;

use crate::process::{Job, Process};

/// `ERROR_ALREADY_EXISTS`, which `CreateAppContainerProfile` returns wrapped in
/// an HRESULT when a profile of that name survived a crash.
const ERROR_ALREADY_EXISTS: i32 = 183;

const CREATE_UNICODE_ENVIRONMENT: u32 = 0x0000_0400;
/// The child is created suspended so it cannot start a process before it is
/// in the job object; `ResumeThread` starts it once it is.
const CREATE_SUSPENDED: u32 = 0x0000_0004;
const EXTENDED_STARTUPINFO_PRESENT: u32 = 0x0008_0000;
const STARTF_USESTDHANDLES: u32 = 0x0000_0100;
const HANDLE_FLAG_INHERIT: u32 = 0x0000_0001;

/// `ProcThreadAttributeSecurityCapabilities` with the thread-attribute flag.
const PROC_THREAD_ATTRIBUTE_SECURITY_CAPABILITIES: usize = 0x0002_0009;

const GENERIC_READ: u32 = 0x8000_0000;
const FILE_SHARE_READ: u32 = 0x0000_0001;
const FILE_SHARE_WRITE: u32 = 0x0000_0002;
const OPEN_EXISTING: u32 = 3;

/// Modify, as an inherited right on directories and their contents: the same
/// `(OI)(CI)M` the measurement granted, so a write inside the working directory
/// is allowed and nothing else is.
const MODIFY: &str = "M";

#[link(name = "userenv")]
extern "system" {
    fn CreateAppContainerProfile(
        app_container_name: *const u16,
        display_name: *const u16,
        description: *const u16,
        // PSID_AND_ATTRIBUTES array; we ask for no capabilities, so null.
        capabilities: *const c_void,
        capability_count: u32,
        sid: *mut *mut c_void,
    ) -> i32;
    fn DeriveAppContainerSidFromAppContainerName(name: *const u16, sid: *mut *mut c_void) -> i32;
    fn DeleteAppContainerProfile(name: *const u16) -> i32;
}

#[link(name = "advapi32")]
extern "system" {
    fn ConvertSidToStringSidW(sid: *const c_void, string_sid: *mut *mut u16) -> i32;
    fn FreeSid(sid: *mut c_void) -> *mut c_void;
}

#[link(name = "kernel32")]
extern "system" {
    fn LocalFree(handle: *mut c_void) -> *mut c_void;
    fn CreatePipe(
        read: *mut *mut c_void,
        write: *mut *mut c_void,
        attributes: *const SecurityAttributes,
        size: u32,
    ) -> i32;
    fn SetHandleInformation(handle: *mut c_void, mask: u32, flags: u32) -> i32;
    fn CreateFileW(
        name: *const u16,
        access: u32,
        share: u32,
        attributes: *const SecurityAttributes,
        disposition: u32,
        flags: u32,
        template: *mut c_void,
    ) -> *mut c_void;
    fn CreateProcessW(
        application: *const u16,
        command_line: *mut u16,
        process_attributes: *const SecurityAttributes,
        thread_attributes: *const SecurityAttributes,
        inherit_handles: i32,
        flags: u32,
        environment: *const c_void,
        current_directory: *const u16,
        startup_info: *const StartupInfo,
        process_information: *mut ProcessInformation,
    ) -> i32;
    fn InitializeProcThreadAttributeList(
        list: *mut c_void,
        attribute_count: u32,
        flags: u32,
        size: *mut usize,
    ) -> i32;
    fn UpdateProcThreadAttribute(
        list: *mut c_void,
        flags: u32,
        attribute: usize,
        value: *const c_void,
        size: usize,
        previous: *mut c_void,
        returned: *mut usize,
    ) -> i32;
    fn DeleteProcThreadAttributeList(list: *mut c_void);
    fn ResumeThread(thread: *mut c_void) -> u32;
    fn TerminateProcess(process: *mut c_void, code: u32) -> i32;
}

/// `INVALID_HANDLE_VALUE`, as the API returns it.
const INVALID_HANDLE_VALUE: *mut c_void = -1isize as *mut c_void;

#[repr(C)]
struct SecurityAttributes {
    length: u32,
    descriptor: *mut c_void,
    inherit: i32,
}

#[repr(C)]
struct StartupInfo {
    length: u32,
    reserved: *mut u16,
    desktop: *mut u16,
    title: *mut u16,
    x: u32,
    y: u32,
    x_size: u32,
    y_size: u32,
    x_chars: u32,
    y_chars: u32,
    fill_attribute: u32,
    flags: u32,
    show_window: u16,
    reserved2: u16,
    reserved2_ptr: *mut u8,
    std_input: *mut c_void,
    std_output: *mut c_void,
    std_error: *mut c_void,
}

#[repr(C)]
struct StartupInfoEx {
    startup: StartupInfo,
    attribute_list: *mut c_void,
}

#[repr(C)]
struct ProcessInformation {
    process: *mut c_void,
    thread: *mut c_void,
    process_id: u32,
    thread_id: u32,
}

#[repr(C)]
struct SecurityCapabilities {
    app_container_sid: *mut c_void,
    capabilities: *mut c_void,
    capability_count: u32,
    reserved: u32,
}

/// A live AppContainer profile, deleted when it goes out of scope.
///
/// Deleting on drop is the point: a profile left behind is a security identity
/// that outlives the run that created it.
pub struct AppContainer {
    name: Vec<u16>,
}

impl AppContainer {
    /// Creates a profile under `name`, which must be unique per profile.
    ///
    /// The name is what the profile is looked up by later: it is not a secret and
    /// not a path.
    pub fn create(name: &str) -> io::Result<Self> {
        let wide = wide(name);
        let mut sid: *mut c_void = ptr::null_mut();

        // SAFETY: every pointer is either a NUL-terminated UTF-16 buffer that
        // outlives the call, or an out-parameter the API fills. No capability
        // array is passed, which the API allows when the count is zero.
        let hr = unsafe {
            CreateAppContainerProfile(
                wide.as_ptr(),
                wide.as_ptr(),
                wide.as_ptr(),
                ptr::null(),
                0,
                &mut sid,
            )
        };
        if hr != 0 {
            return Err(hresult_to_io(hr));
        }
        // The SID itself is only needed by the launch step, which asks for it
        // again by name. Free the copy this call handed us.
        unsafe {
            LocalFree(sid);
        }

        Ok(Self { name: wide })
    }

    /// Creates the profile, replacing one that a crashed session left behind.
    ///
    /// The name carries the running process id, so a collision means exactly
    /// that: the same id, from a run that died without dropping its profile.
    pub fn create_replacing(name: &str) -> io::Result<Self> {
        match Self::create(name) {
            Ok(profile) => Ok(profile),
            Err(err) if err.raw_os_error() == Some(ERROR_ALREADY_EXISTS) => {
                let wide = wide(name);
                // SAFETY: `wide` is a NUL-terminated buffer that outlives the
                // call. A failure here is reported by the second create.
                unsafe {
                    DeleteAppContainerProfile(wide.as_ptr());
                }
                Self::create(name)
            }
            Err(err) => Err(err),
        }
    }

    /// The SID string, e.g. `S-1-15-2-2797...`. Used in ACL grants and in
    /// diagnostics; never parsed by us.
    pub fn sid_string(&self) -> io::Result<String> {
        let sid = self.sid()?;
        let mut raw: *mut u16 = ptr::null_mut();
        // SAFETY: `sid` is a valid SID owned by the guard above; the string it
        // returns is NUL-terminated and must be freed with `LocalFree`.
        let ok = unsafe { ConvertSidToStringSidW(sid.as_ptr(), &mut raw) };
        if ok == 0 {
            return Err(io::Error::last_os_error());
        }

        // SAFETY: `raw` points at the NUL-terminated string ConvertSidToStringSidW
        // produced, which stays valid until the LocalFree below.
        let text = unsafe { copy_wide_string(raw) };
        unsafe {
            LocalFree(raw as *mut c_void);
        }
        Ok(text)
    }

    fn sid(&self) -> io::Result<OwnedSid> {
        let mut sid: *mut c_void = ptr::null_mut();

        // SAFETY: `name` is the NUL-terminated buffer kept by `create`, and
        // `sid` is an out-parameter the API fills.
        let hr = unsafe { DeriveAppContainerSidFromAppContainerName(self.name.as_ptr(), &mut sid) };
        if hr != 0 {
            return Err(hresult_to_io(hr));
        }
        Ok(OwnedSid { raw: sid })
    }

    /// Grants the package SID modify rights on `dir` and on what it already
    /// contains, inheriting to what will be created there.
    ///
    /// One ACE on the directory itself would cover only files created *after*
    /// the grant; a working directory is normally a repository full of files
    /// that already exist, and a sandbox in which the agent cannot edit those is
    /// not usable. `icacls /T` applies the same right to the existing tree in one
    /// traversal, and `revoke` undoes it with the same shape.
    pub fn grant(&self, dir: &Path) -> io::Result<()> {
        let sid = self.sid_string()?;
        self.icacls(dir, "/grant", &format!("*{sid}:(OI)(CI){MODIFY}"))
    }

    /// Takes the grant back.
    pub fn revoke(&self, dir: &Path) -> io::Result<()> {
        let sid = self.sid_string()?;
        self.icacls(dir, "/remove:g", &format!("*{sid}"))
    }

    fn icacls(&self, dir: &Path, verb: &str, spec: &str) -> io::Result<()> {
        let output = Command::new("icacls")
            .arg(dir)
            .arg(verb)
            .arg(spec)
            // Recurse into what already exists, continue past a file we may not
            // touch, and say nothing unless something failed.
            .args(["/T", "/C", "/Q"])
            .stdin(Stdio::null())
            .output()?;
        if output.status.success() {
            return Ok(());
        }
        let detail = String::from_utf8_lossy(&output.stdout);
        let detail = detail.trim();
        Err(io::Error::other(format!(
            "icacls {verb} on {} failed: {}",
            dir.display(),
            if detail.is_empty() {
                "no detail"
            } else {
                detail
            }
        )))
    }

    /// Launches `command_line` inside the identity, with `env` and nothing else
    /// as its environment.
    ///
    /// The command's stdin is the null device: the protocol has no stdin, and a
    /// command that blocks on input would hold the session open.
    pub fn launch(
        &self,
        command_line: &str,
        env: &BTreeMap<String, String>,
        work_dir: &str,
    ) -> io::Result<Process> {
        let sid = self.sid()?;
        let mut attributes =
            AttributeList::new().map_err(|e| step("InitializeProcThreadAttributeList", e))?;
        // The attribute list keeps a pointer to this value and `CreateProcessW`
        // reads it while launching, so it has to outlive the whole call. A local
        // inside `set_security_capabilities` would be stack memory nobody owns
        // by then: the launch fails with ERROR_INVALID_PARAMETER (87), and with
        // a different stack layout the garbage SID crashes the process outright.
        let capabilities = SecurityCapabilities {
            app_container_sid: sid.as_ptr(),
            capabilities: ptr::null_mut(),
            capability_count: 0,
            reserved: 0,
        };
        attributes
            .set_security_capabilities(&capabilities)
            .map_err(|e| step("UpdateProcThreadAttribute", e))?;

        // The handles a confined command is allowed to inherit: its two output
        // pipes and the null device. `inherit: 1` here, and inheritance turned
        // back off on the read ends below, so the child cannot hold a pipe the
        // sidecar needs to see close.
        let inherited = SecurityAttributes {
            length: std::mem::size_of::<SecurityAttributes>() as u32,
            descriptor: ptr::null_mut(),
            inherit: 1,
        };
        let (out_read, out_write) = create_pipe(&inherited)?;
        let (err_read, err_write) = create_pipe(&inherited)?;
        set_inherit(&out_read, false)?;
        set_inherit(&err_read, false)?;
        let stdin = open_null(&inherited)?;

        // SAFETY: STARTUPINFOEXW is a plain-old-data structure; zero is the
        // documented initial state before the fields we set.
        let mut info: StartupInfoEx = unsafe { std::mem::zeroed() };
        info.startup.length = std::mem::size_of::<StartupInfoEx>() as u32;
        info.startup.flags = STARTF_USESTDHANDLES;
        info.startup.std_input = stdin.as_raw_handle();
        info.startup.std_output = out_write.as_raw_handle();
        info.startup.std_error = err_write.as_raw_handle();
        info.attribute_list = attributes.as_ptr();

        let block = environment_block(env);
        let mut line = wide(command_line);
        let directory = if work_dir.is_empty() {
            None
        } else {
            Some(wide(work_dir))
        };

        // SAFETY: zero is the documented initial state, and the call fills it.
        let mut created: ProcessInformation = unsafe { std::mem::zeroed() };
        // SAFETY: every pointer below is either a NUL-terminated UTF-16 buffer,
        // the environment block, or the attribute list, each kept alive by a
        // local that outlives the call. `inherit_handles` is 1 because the three
        // handles handed to the child are marked inheritable, and only those.
        let ok = unsafe {
            CreateProcessW(
                ptr::null(),
                line.as_mut_ptr(),
                ptr::null(),
                ptr::null(),
                1,
                EXTENDED_STARTUPINFO_PRESENT | CREATE_UNICODE_ENVIRONMENT | CREATE_SUSPENDED,
                block.as_ptr() as *const c_void,
                directory.as_ref().map_or(ptr::null(), |wide| wide.as_ptr()),
                &info.startup,
                &mut created,
            )
        };
        if ok == 0 {
            return Err(step("CreateProcessW", io::Error::last_os_error()));
        }

        // SAFETY: `created.thread` and `created.process` are handles this call
        // just produced and that nobody else owns.
        let process_handle = unsafe { OwnedHandle::from_raw_handle(created.process) };
        let thread_handle = unsafe { OwnedHandle::from_raw_handle(created.thread) };

        // The job is what makes the tree kill enforceable: every process the
        // command starts is born inside it, and closing the handle ends all of
        // them. The child is still suspended here, so it cannot start anything
        // before it is a member.
        let job = match Job::new().and_then(|job| job.assign(&process_handle).map(|()| job)) {
            Ok(job) => job,
            Err(err) => {
                // Suspended and never resumed: end it rather than leave behind a
                // process nobody can reach.
                // SAFETY: the handle is owned here and the process is this call's.
                unsafe {
                    TerminateProcess(process_handle.as_raw_handle(), 1);
                }
                return Err(step("job object", err));
            }
        };

        // SAFETY: the thread handle is owned here and still suspended.
        let resumed = unsafe { ResumeThread(thread_handle.as_raw_handle()) };
        if resumed == u32::MAX {
            // SAFETY: as above.
            unsafe {
                TerminateProcess(process_handle.as_raw_handle(), 1);
            }
            return Err(step("ResumeThread", io::Error::last_os_error()));
        }
        drop(thread_handle);

        // Drop our copies of the write ends so the pipes report end-of-file once
        // the command exits.
        drop(out_write);
        drop(err_write);
        drop(stdin);

        Ok(Process::from_windows(
            job,
            process_handle,
            created.process_id,
            Some(File::from(out_read)),
            Some(File::from(err_read)),
        ))
    }
}

impl Drop for AppContainer {
    fn drop(&mut self) {
        // SAFETY: `name` is the buffer this struct owns and still keeps alive.
        // A failure here cannot be reported from a destructor; the profile name
        // is unique per run, so a leak is inert rather than wrong.
        unsafe {
            DeleteAppContainerProfile(self.name.as_ptr());
        }
    }
}

/// A SID this process allocated, freed when it goes out of scope.
struct OwnedSid {
    raw: *mut c_void,
}

impl OwnedSid {
    fn as_ptr(&self) -> *mut c_void {
        self.raw
    }
}

impl Drop for OwnedSid {
    fn drop(&mut self) {
        // SAFETY: `raw` came from `DeriveAppContainerSidFromAppContainerName`,
        // which documents `FreeSid` as the way to release it.
        unsafe {
            FreeSid(self.raw);
        }
    }
}

/// The `STARTUPINFOEX` attribute list, sized by the API itself.
struct AttributeList {
    // `usize` and not `u8`: the list must be pointer-aligned, and a `Vec<u8>`
    // only guarantees byte alignment even where the allocator happens to do
    // better.
    words: Vec<usize>,
}

impl AttributeList {
    fn new() -> io::Result<Self> {
        let mut size = 0usize;
        // SAFETY: the documented way to ask for the size; a null list is
        // expected to fail with ERROR_INSUFFICIENT_BUFFER.
        unsafe {
            InitializeProcThreadAttributeList(ptr::null_mut(), 1, 0, &mut size);
        }
        if size == 0 {
            return Err(io::Error::last_os_error());
        }
        let mut words = vec![0usize; size.div_ceil(std::mem::size_of::<usize>())];
        // SAFETY: `words` is at least `size` bytes, and `size` is passed back
        // exactly as the first call produced it.
        let ok = unsafe {
            InitializeProcThreadAttributeList(words.as_mut_ptr() as *mut c_void, 1, 0, &mut size)
        };
        if ok == 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(Self { words })
    }

    fn as_ptr(&mut self) -> *mut c_void {
        self.words.as_mut_ptr() as *mut c_void
    }

    /// Names the identity the child runs under. No capabilities are requested:
    /// an AppContainer with none has no network, which is what `allow_network`
    /// being false asks for, and what it being true cannot get without a
    /// capability the user would have to be granted.
    fn set_security_capabilities(&mut self, capabilities: &SecurityCapabilities) -> io::Result<()> {
        // SAFETY: the list was initialised above, the caller keeps `capabilities`
        // alive until `CreateProcessW` returns, and its size is the one the API
        // expects for this attribute.
        let ok = unsafe {
            UpdateProcThreadAttribute(
                self.words.as_mut_ptr() as *mut c_void,
                0,
                PROC_THREAD_ATTRIBUTE_SECURITY_CAPABILITIES,
                capabilities as *const SecurityCapabilities as *const c_void,
                std::mem::size_of::<SecurityCapabilities>(),
                ptr::null_mut(),
                ptr::null_mut(),
            )
        };
        if ok == 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(())
    }
}

impl Drop for AttributeList {
    fn drop(&mut self) {
        // SAFETY: the buffer is this struct's and was initialised with exactly
        // one attribute; the API frees only what it put there.
        unsafe {
            DeleteProcThreadAttributeList(self.words.as_mut_ptr() as *mut c_void);
        }
    }
}

/// Builds the child's environment block from the request alone.
///
/// `LOCALAPPDATA` is the one variable this function adds, and the reason is
/// measured rather than inherited: `CreateProcessW` inside an AppContainer
/// returns `ERROR_ENVVAR_NOT_FOUND` (203) when the block does not carry it,
/// because the kernel derives the container's per-package profile from it. It
/// is a path the platform requires, not a credential: the identity cannot write
/// to it, and it grants no access. Everything else still comes from the client
/// or does not exist.
fn environment_block(env: &BTreeMap<String, String>) -> Vec<u16> {
    let mut block: Vec<u16> = Vec::with_capacity(env.len() * 32 + 64);
    for (key, value) in env {
        // A NUL inside a key or a value would end the block early and hand the
        // child a different environment than the client wrote.
        if key.is_empty() || key.contains('\0') || value.contains('\0') {
            continue;
        }
        block.extend(key.encode_utf16());
        block.push(u16::from(b'='));
        block.extend(value.encode_utf16());
        block.push(0);
    }

    let named = env
        .keys()
        .any(|key| key.eq_ignore_ascii_case("LOCALAPPDATA"));
    if !named {
        if let Some(path) = local_app_data() {
            block.extend("LOCALAPPDATA=".encode_utf16());
            block.extend(path.encode_utf16());
            block.push(0);
        }
    }

    // The block is a sequence of NUL-terminated entries ending with a NUL that
    // closes the last one and the empty entry after it.
    block.push(0);
    block
}

/// The value the launch needs, taken from the sidecar's own environment.
///
/// The sidecar reads this one variable of its own for the platform, and only
/// this one: it is a directory name, never a secret.
fn local_app_data() -> Option<String> {
    std::env::var_os("LOCALAPPDATA")
        .map(|path| path.to_string_lossy().into_owned())
        .or_else(|| {
            std::env::var_os("USERPROFILE")
                .map(|home| format!("{}\\AppData\\Local", home.to_string_lossy()))
        })
}

fn step(name: &str, err: io::Error) -> io::Error {
    io::Error::new(err.kind(), format!("{name}: {err}"))
}

fn create_pipe(attributes: &SecurityAttributes) -> io::Result<(OwnedHandle, OwnedHandle)> {
    let mut read: *mut c_void = ptr::null_mut();
    let mut write: *mut c_void = ptr::null_mut();
    // SAFETY: `attributes` outlives the call and both handles are out-parameters.
    let ok = unsafe { CreatePipe(&mut read, &mut write, attributes, 0) };
    if ok == 0 {
        return Err(io::Error::last_os_error());
    }
    // SAFETY: the call produced both handles and this takes ownership of them.
    Ok(unsafe {
        (
            OwnedHandle::from_raw_handle(read),
            OwnedHandle::from_raw_handle(write),
        )
    })
}

fn set_inherit(handle: &OwnedHandle, inherit: bool) -> io::Result<()> {
    // SAFETY: `handle` is owned by the caller and stays valid across the call.
    let ok = unsafe {
        SetHandleInformation(
            handle.as_raw_handle(),
            HANDLE_FLAG_INHERIT,
            if inherit { HANDLE_FLAG_INHERIT } else { 0 },
        )
    };
    if ok == 0 {
        return Err(io::Error::last_os_error());
    }
    Ok(())
}

fn open_null(attributes: &SecurityAttributes) -> io::Result<OwnedHandle> {
    let name = wide("NUL");
    // SAFETY: `name` and `attributes` outlive the call; a null template is the
    // documented value when no template file is used.
    let handle = unsafe {
        CreateFileW(
            name.as_ptr(),
            GENERIC_READ,
            FILE_SHARE_READ | FILE_SHARE_WRITE,
            attributes,
            OPEN_EXISTING,
            0,
            ptr::null_mut(),
        )
    };
    if handle == INVALID_HANDLE_VALUE {
        return Err(io::Error::last_os_error());
    }
    // SAFETY: the call produced the handle and this takes ownership of it.
    Ok(unsafe { OwnedHandle::from_raw_handle(handle) })
}

fn wide(text: &str) -> Vec<u16> {
    text.encode_utf16().chain(std::iter::once(0)).collect()
}

/// Reads a NUL-terminated UTF-16 string without taking ownership.
///
/// # Safety
///
/// `raw` must point at a NUL-terminated UTF-16 buffer that stays valid for the
/// duration of the call.
unsafe fn copy_wide_string(raw: *const u16) -> String {
    let mut len = 0usize;
    // SAFETY: the caller guarantees the buffer is NUL-terminated.
    while unsafe { *raw.add(len) } != 0 {
        len += 1;
    }
    // SAFETY: the same guarantee covers the range just measured.
    String::from_utf16_lossy(unsafe { std::slice::from_raw_parts(raw, len) })
}

/// Turns an HRESULT into the OS error it wraps when it has the `0x8007xxxx`
/// shape, and keeps it opaque otherwise.
fn hresult_to_io(hr: i32) -> io::Error {
    let code = (hr as u32) & 0xFFFF;
    let from_win32 = ((hr as u32) & 0xFFFF_0000) == 0x8007_0000;
    if from_win32 {
        io::Error::from_raw_os_error(code as i32)
    } else {
        io::Error::other(format!("HRESULT 0x{:08X}", hr as u32))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn unique_name(tag: &str) -> String {
        format!("seshat.sandboxd.test.{}.{tag}", std::process::id())
    }

    /// The environment a Windows shell needs to start at all, built from this
    /// process. It deliberately does **not** carry `LOCALAPPDATA`: that is what
    /// the launch has to synthesise.
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

    /// `cmd.exe` by absolute path, so the test measures the identity and not a
    /// `PATH` lookup.
    fn cmd() -> String {
        match std::env::var("SystemRoot") {
            Ok(root) => format!("{root}\\System32\\cmd.exe"),
            Err(_) => "cmd.exe".to_string(),
        }
    }

    /// A work directory and an unrelated directory beside it, both left behind
    /// when the test ends.
    struct Scratch {
        root: std::path::PathBuf,
    }

    impl Scratch {
        fn new(tag: &str) -> Self {
            let root =
                std::env::temp_dir().join(format!("seshat-appc-{}-{tag}", std::process::id()));
            let _ = std::fs::remove_dir_all(&root);
            std::fs::create_dir_all(root.join("work")).expect("create the work directory");
            std::fs::create_dir_all(root.join("outside")).expect("create the outside directory");
            Self { root }
        }

        fn work(&self) -> String {
            self.root.join("work").to_string_lossy().into_owned()
        }
    }

    impl Drop for Scratch {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.root);
        }
    }

    #[test]
    fn a_profile_can_be_created_and_its_sid_read_back() {
        let profile = AppContainer::create(&unique_name("sid")).expect("create the profile");
        let sid = profile.sid_string().expect("read the SID back");
        assert!(
            sid.starts_with("S-1-15-2-"),
            "an AppContainer SID is a package SID, got {sid}"
        );
    }

    #[test]
    fn the_profile_is_gone_after_the_value_is_dropped() {
        let name = unique_name("drop");
        {
            let profile = AppContainer::create(&name).expect("create the profile");
            drop(profile);
        }
        // Creating it again can only succeed if the first one was deleted.
        let again = AppContainer::create(&name).expect("recreate after drop");
        assert!(again.sid_string().is_ok());
    }

    /// Finding 1 of the toolchain measurement, as a regression test: without
    /// `LOCALAPPDATA` in the block, `CreateProcessW` refuses the launch with
    /// error 203 and no command ever runs.
    #[test]
    fn a_command_runs_when_the_client_did_not_send_local_app_data() {
        let scratch = Scratch::new("env");
        let profile = AppContainer::create(&unique_name("env")).expect("create the profile");
        profile
            .grant(Path::new(&scratch.work()))
            .expect("grant the work directory");

        let env = shell_env();
        assert!(
            !env.keys()
                .any(|key| key.eq_ignore_ascii_case("LOCALAPPDATA")),
            "the test must not send the variable it is about"
        );

        let line = format!("\"{}\" /C echo hello", cmd());
        let mut process = profile
            .launch(&line, &env, &scratch.work())
            .expect("launch inside the identity");
        let code = process.wait().expect("wait for the command");
        assert_eq!(
            code, 0,
            "the launch needs LOCALAPPDATA synthesised by the sidecar"
        );
    }

    /// The point of the whole module: a write inside the granted directory is
    /// allowed, and the same write one level above it is refused by the system.
    #[test]
    fn a_command_writes_inside_the_grant_and_nowhere_else() {
        let scratch = Scratch::new("grant");
        let profile = AppContainer::create(&unique_name("grant")).expect("create the profile");
        profile
            .grant(Path::new(&scratch.work()))
            .expect("grant the work directory");

        let inside = format!("\"{}\" /C echo inside>inside.txt", cmd());
        let mut allowed = profile
            .launch(&inside, &shell_env(), &scratch.work())
            .expect("launch the allowed write");
        assert_eq!(allowed.wait().expect("wait"), 0);
        assert!(
            scratch.root.join("work").join("inside.txt").exists(),
            "a write inside the granted directory must succeed"
        );

        let outside = format!("\"{}\" /C echo outside>..\\outside\\escaped.txt", cmd());
        let mut refused = profile
            .launch(&outside, &shell_env(), &scratch.work())
            .expect("launch the refused write");
        let code = refused.wait().expect("wait");
        assert_ne!(code, 0, "the redirect outside the grant must fail");
        assert!(
            !scratch.root.join("outside").join("escaped.txt").exists(),
            "a write outside the granted directory must not happen"
        );
    }

    /// The grant is a right the sidecar hands out and takes back, not a change
    /// to the machine: once revoked, the same write is refused.
    #[test]
    fn revoking_the_grant_takes_the_access_away() {
        let scratch = Scratch::new("revoke");
        let profile = AppContainer::create(&unique_name("revoke")).expect("create the profile");
        profile
            .grant(Path::new(&scratch.work()))
            .expect("grant the work directory");
        profile
            .revoke(Path::new(&scratch.work()))
            .expect("revoke the grant");

        let line = format!("\"{}\" /C echo nope>denied.txt", cmd());
        let mut process = profile
            .launch(&line, &shell_env(), &scratch.work())
            .expect("launch after revoking");
        let _ = process.wait().expect("wait");
        assert!(
            !scratch.root.join("work").join("denied.txt").exists(),
            "a revoked grant must refuse the write it used to allow"
        );
    }
}
