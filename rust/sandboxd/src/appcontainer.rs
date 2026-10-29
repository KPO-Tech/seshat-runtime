//! The Windows security identity a sandboxed command will run under.
//!
//! An AppContainer is the one identity Windows hands to a normal user: no
//! elevation, no local account created, and a package SID that no ACL grants
//! anything to until we say so. That is what makes a *path* rule enforceable
//! without administrator rights — a deny ACE has to name an identity, and the
//! only identity available for free is this one. See
//! [`docs/spike-windows-confinement.md`](../../../docs/spike-windows-confinement.md)
//! for the measurements behind that claim.
//!
//! What this module does today is narrower than its purpose: it creates the
//! profile, reads back its SID and deletes it. Launching the command inside that
//! identity is the next step, so the sidecar keeps reporting `degraded` — a
//! capability that can be created is not a capability that is applied.

// Windows exposes these APIs only as C functions; the standard library has no
// wrapper. Every `unsafe` block below states what it relies on, and the
// workspace keeps `unsafe_code = deny` everywhere else.
#![allow(unsafe_code)]

use std::ffi::c_void;
use std::io;
use std::ptr;

/// Returned by `CreateAppContainerProfile` when the profile already exists.
const HRESULT_ALREADY_EXISTS: i32 = 0x8007_00B7u32 as i32;

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
}

#[link(name = "kernel32")]
extern "system" {
    fn LocalFree(handle: *mut c_void) -> *mut c_void;
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

    /// The SID string, e.g. `S-1-15-2-2797...`. Used in ACL grants and in
    /// diagnostics; never parsed by us.
    pub fn sid_string(&self) -> io::Result<String> {
        let mut sid: *mut c_void = ptr::null_mut();

        // SAFETY: `name` is the NUL-terminated buffer kept by `create`, and
        // `sid` is an out-parameter owned by the caller after the call.
        let hr = unsafe { DeriveAppContainerSidFromAppContainerName(self.name.as_ptr(), &mut sid) };
        if hr != 0 {
            return Err(hresult_to_io(hr));
        }

        let mut raw: *mut u16 = ptr::null_mut();
        // SAFETY: `sid` is a valid SID from the call above; the string it
        // returns is NUL-terminated and must be freed with `LocalFree`.
        let ok = unsafe { ConvertSidToStringSidW(sid, &mut raw) };
        unsafe {
            LocalFree(sid);
        }
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

/// Proves the host can hand out an identity, without keeping one.
///
/// Called once at startup so the sidecar's diagnostics say whether Windows can
/// host a confined run at all, instead of failing later on a command.
pub fn probe() -> Result<String, String> {
    let name = format!("seshat.sandboxd.probe.{}", std::process::id());
    let profile = match AppContainer::create(&name) {
        Ok(profile) => profile,
        // A name left behind by a previous crash is not a capability problem:
        // the profile we would create already exists.
        Err(err) if err.raw_os_error() == Some(HRESULT_ALREADY_EXISTS & 0xFFFF) => {
            return Ok("profile already present".to_string());
        }
        Err(err) => return Err(err.to_string()),
    };
    let sid = profile.sid_string().map_err(|err| err.to_string())?;
    drop(profile);
    Ok(sid)
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
}
