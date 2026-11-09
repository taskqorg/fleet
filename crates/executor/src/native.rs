//! Mapping informed by AgentQ 7c48127 registry/codex.rs; Fleet owns policy.
use std::os::unix::process::CommandExt;
use std::{env, path::Path, process::Command};
pub fn command(dir: &Path, native: Option<&str>, text: &str) -> std::io::Result<Command> {
    let home = env::var_os("FLEET_NATIVE_HOME")
        .ok_or(std::io::Error::other("FLEET_NATIVE_HOME required"))?;
    let account = env::var_os("FLEET_CODEX_HOME")
        .ok_or(std::io::Error::other("FLEET_CODEX_HOME required"))?;
    command_in(dir, native, text, Path::new(&home), Path::new(&account))
}
pub(crate) fn command_in(
    dir: &Path,
    native: Option<&str>,
    text: &str,
    home: &Path,
    account: &Path,
) -> std::io::Result<Command> {
    command_at(
        dir,
        native,
        text,
        home,
        account,
        &env::var("FLEET_CODEX_BIN").unwrap_or("codex".into()),
    )
}
pub(crate) fn command_at(
    dir: &Path,
    native: Option<&str>,
    text: &str,
    home: &Path,
    account: &Path,
    executable: &str,
) -> std::io::Result<Command> {
    use std::os::unix::fs::PermissionsExt;
    for p in [home, account] {
        if !p.is_absolute()
            || !p.is_dir()
            || std::fs::symlink_metadata(p)?.file_type().is_symlink()
            || std::fs::metadata(p)?.permissions().mode() & 0o077 != 0
        {
            return Err(std::io::Error::other(
                "Fleet native homes must be absolute private directories",
            ));
        }
    }
    let marker = dir.join(".fleet-native-home.json");
    let binding = serde_json::to_vec(
        &serde_json::json!({"home":home.canonicalize()?,"codexHome":account.canonicalize()?}),
    )?;
    if native.is_some() && (!marker.exists() || std::fs::read(&marker)? != binding) {
        return Err(std::io::Error::other(
            "native session home mismatch; legacy history retained without migration",
        ));
    }
    std::fs::write(marker, binding)?;
    let mut c = Command::new(executable);
    c.env_clear();
    for key in [
        "PATH",
        "USER",
        "LOGNAME",
        "TMPDIR",
        "LANG",
        "LC_ALL",
        "SSL_CERT_FILE",
        "SSL_CERT_DIR",
    ] {
        if let Some(v) = env::var_os(key) {
            c.env(key, v);
        }
    }
    c.env("HOME", home).env("CODEX_HOME", account);
    c.current_dir(dir).process_group(0).args([
        "exec",
        "--ignore-user-config",
        "--ignore-rules",
        "--skip-git-repo-check",
        "--json",
        "-c",
        "project_doc_max_bytes=0",
        "-c",
        "skills.enabled=false",
        "-c",
        "skills.include_instructions=false",
        "-c",
        "mcp_servers={}",
        "--disable",
        "memories",
        "--disable",
        "hooks",
        "--enable",
        "skip_host_skill_discovery",
        "-m",
        "gpt-6.1-sol",
        "-c",
        "model_reasoning_effort=low",
        "-c",
        "forced_login_method=\"chatgpt\"",
        "-c",
        "approval_policy=\"never\"",
        "-c",
        "sandbox_mode=\"workspace-write\"",
        "-c",
        "sandbox_workspace_write.network_access=false",
        "--disable",
        "multi_agent",
        "--disable",
        "apps",
        "--disable",
        "plugins",
        "--disable",
        "browser_use",
        "--disable",
        "computer_use",
        "--disable",
        "image_generation",
    ]);
    if let Some(n) = native {
        c.args(["resume", n]);
    }
    c.arg(text);
    Ok(c)
}
#[cfg(target_os = "macos")]
pub fn identity(pid: u32) -> std::io::Result<String> {
    let mut info = std::mem::MaybeUninit::<libc::proc_bsdinfo>::zeroed();
    let size = std::mem::size_of::<libc::proc_bsdinfo>() as i32;
    let n = unsafe {
        libc::proc_pidinfo(
            pid as i32,
            libc::PROC_PIDTBSDINFO,
            0,
            info.as_mut_ptr().cast(),
            size,
        )
    };
    if n != size {
        return Err(std::io::Error::last_os_error());
    }
    let i = unsafe { info.assume_init() };
    if i.pbi_pid != pid || i.pbi_start_tvsec == 0 {
        return Err(std::io::Error::other("invalid creation identity"));
    }
    Ok(format!("{}-{}", i.pbi_start_tvsec, i.pbi_start_tvusec))
}
#[cfg(not(target_os = "macos"))]
pub fn identity(pid: u32) -> std::io::Result<String> {
    let s = std::fs::read_to_string(format!("/proc/{pid}/stat"))?;
    Ok(s.rsplit_once(')')
        .ok_or(std::io::Error::other("invalid stat"))?
        .1
        .split_whitespace()
        .nth(19)
        .ok_or(std::io::Error::other("start missing"))?
        .into())
}

// CoreQ process.rs identity/version fence, adapted to signal owned descendants.
#[cfg(target_os = "macos")]
pub fn signal(pid: u32, expected: &str, sig: i32) -> std::io::Result<()> {
    #[repr(C)]
    #[derive(Default)]
    struct Unique {
        uuid: [u8; 16],
        id: u64,
        parent: u64,
        version: i32,
        parent_version: i32,
        reserved: [u64; 2],
    }
    let mut info = Unique::default();
    let n = unsafe { libc::proc_pidinfo(pid as i32, 17, 0, (&mut info as *mut Unique).cast(), 56) };
    if n != 56 || info.id == 0 {
        return Err(std::io::Error::other("process version unavailable"));
    }
    if identity(pid)?.as_str() != expected {
        return Err(std::io::Error::other("process incarnation changed"));
    }
    let symbol =
        unsafe { libc::dlsym(libc::RTLD_DEFAULT, c"proc_signal_with_audittoken".as_ptr()) };
    if symbol.is_null() {
        return Err(std::io::Error::other("identity-bound signal unsupported"));
    }
    let call: unsafe extern "C" fn(*mut [u32; 8], i32) -> i32 =
        unsafe { std::mem::transmute(symbol) };
    let mut token = [0; 8];
    token[5] = pid;
    token[7] = info.version as u32;
    let error = unsafe { call(&mut token, sig) };
    if error == 0 {
        Ok(())
    } else {
        Err(std::io::Error::from_raw_os_error(error))
    }
}
#[cfg(not(target_os = "macos"))]
pub fn signal(pid: u32, expected: &str, sig: i32) -> std::io::Result<()> {
    use std::os::fd::{AsRawFd, FromRawFd, OwnedFd};
    let fd = unsafe { libc::syscall(libc::SYS_pidfd_open, pid, 0) };
    if fd < 0 {
        return Err(std::io::Error::last_os_error());
    }
    let fd = unsafe { OwnedFd::from_raw_fd(fd as i32) };
    if identity(pid)?.as_str() != expected {
        return Err(std::io::Error::other("process incarnation changed"));
    }
    if unsafe {
        libc::syscall(
            libc::SYS_pidfd_send_signal,
            fd.as_raw_fd(),
            sig,
            std::ptr::null::<libc::siginfo_t>(),
            0,
        )
    } == 0
    {
        Ok(())
    } else {
        Err(std::io::Error::last_os_error())
    }
}
pub fn processes() -> std::io::Result<Vec<(u32, u32, String)>> {
    let o = Command::new("/bin/ps")
        .args(["-axo", "pid=,ppid=,stat="])
        .output()?;
    if !o.status.success() {
        return Err(std::io::Error::other("process snapshot failed"));
    }
    Ok(String::from_utf8_lossy(&o.stdout)
        .lines()
        .filter_map(|l| {
            let mut f = l.split_whitespace();
            Some((
                f.next()?.parse().ok()?,
                f.next()?.parse().ok()?,
                f.next()?.into(),
            ))
        })
        .collect())
}

#[cfg(test)]
mod isolation_tests {
    use super::*;
    #[test]
    fn clean_homes_and_resume_binding() {
        use std::os::unix::fs::PermissionsExt;
        let root = std::env::temp_dir().join(format!("fleet-homes-{}", std::process::id()));
        for d in ["home", "account", "work"] {
            std::fs::create_dir_all(root.join(d)).unwrap();
            std::fs::set_permissions(root.join(d), std::fs::Permissions::from_mode(0o700)).unwrap();
        }
        let c = command_in(
            &root.join("work"),
            None,
            "test",
            &root.join("home"),
            &root.join("account"),
        )
        .unwrap();
        let vars: std::collections::HashMap<_, _> = c.get_envs().collect();
        assert_eq!(
            vars.get(std::ffi::OsStr::new("HOME")).unwrap().unwrap(),
            root.join("home")
        );
        let args: Vec<_> = c.get_args().map(|v| v.to_string_lossy()).collect();
        assert!(args.contains(&"mcp_servers={}".into()));
        assert!(args.contains(&"project_doc_max_bytes=0".into()));
        assert!(
            command_in(
                &root.join("work"),
                Some("native"),
                "test",
                &root.join("home"),
                &root.join("account")
            )
            .is_ok()
        );
        assert!(
            command_in(
                &root.join("work"),
                Some("native"),
                "test",
                &root.join("home"),
                &root.join("home")
            )
            .is_err()
        );
        std::fs::remove_file(root.join("work/.fleet-native-home.json")).unwrap();
        assert!(
            command_in(
                &root.join("work"),
                Some("legacy"),
                "test",
                &root.join("home"),
                &root.join("account")
            )
            .is_err()
        );
        std::fs::remove_dir_all(root).unwrap();
    }
}
