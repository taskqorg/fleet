// Previews (design §3, Phase 3 item 28b, P3-D4): after an image or video artifact is stored, `ffmpeg` builds a 512 px
// thumbnail (and a poster frame for a video) at the lowest CPU priority, without network access, within the operator settings
// FLEET_PREVIEW_TIMEOUT_SECONDS (default 120) and FLEET_PREVIEW_MEMORY_BYTES (default 1 GiB). The results are uploaded as
// artifacts that point to their original. A failure is logged and never fails the execution.
// Until the isolation boundary (Phase 5) adds account users, ffmpeg runs as the user that runs the agents on this host.
use crate::upload;
use rusqlite::Connection;
use serde_json::{json, Value};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::OnceLock;
use std::time::{Duration, Instant};

pub fn ffmpeg_version() -> Option<String> {
    static V: OnceLock<Option<String>> = OnceLock::new();
    V.get_or_init(|| {
        let out = Command::new("ffmpeg").arg("-version").stdin(Stdio::null()).stderr(Stdio::null()).output().ok()?;
        let line = String::from_utf8_lossy(&out.stdout).lines().next()?.to_string();
        line.split_whitespace().nth(2).map(str::to_owned)
    }).clone()
}
fn setting(name: &str, default: u64) -> u64 {
    std::env::var(name).ok().and_then(|v| v.parse().ok()).unwrap_or(default)
}
/// The ffmpeg command line for one preview, wrapped for low priority and no network.
pub fn command(src: &Path, out: &Path, poster: bool) -> Command {
    let mut args: Vec<String> = vec!["-y".into(), "-hide_banner".into(), "-loglevel".into(), "error".into(), "-i".into(), src.display().to_string()];
    if !poster { args.extend(["-vf".into(), "scale='min(512,iw)':-2".into()]) }
    args.extend(["-frames:v".into(), "1".into(), out.display().to_string()]);
    let mut c = if cfg!(target_os = "macos") {
        let mut c = Command::new("nice");
        c.args(["-n", "19", "sandbox-exec", "-p", "(version 1)(allow default)(deny network*)", "ffmpeg"]);
        c
    } else if Path::new("/usr/bin/unshare").exists() {
        let mut c = Command::new("nice");
        c.args(["-n", "19", "unshare", "--user", "--map-root-user", "--net", "ffmpeg"]);
        c
    } else {
        let mut c = Command::new("nice");
        c.args(["-n", "19", "ffmpeg"]);
        c
    };
    c.args(args).stdin(Stdio::null()).stdout(Stdio::null()).stderr(Stdio::piped());
    let memory = setting("FLEET_PREVIEW_MEMORY_BYTES", 1 << 30);
    use std::os::unix::process::CommandExt;
    unsafe {
        c.pre_exec(move || {
            let lim = libc::rlimit { rlim_cur: memory as libc::rlim_t, rlim_max: memory as libc::rlim_t };
            // RLIMIT_AS is not enforced on macOS; there the time bound and idle priority remain.
            let _ = libc::setrlimit(libc::RLIMIT_AS, &lim);
            Ok(())
        });
    }
    c
}
fn run(mut c: Command) -> Result<(), String> {
    let mut child = c.spawn().map_err(|e| e.to_string())?;
    let deadline = Instant::now() + Duration::from_secs(setting("FLEET_PREVIEW_TIMEOUT_SECONDS", 120));
    loop {
        if let Some(s) = child.try_wait().map_err(|e| e.to_string())? {
            return if s.success() { Ok(()) } else { Err(format!("ffmpeg exit {:?}", s.code())) };
        }
        if Instant::now() >= deadline {
            let _ = child.kill();
            let _ = child.wait();
            return Err("ffmpeg time limit".into());
        }
        std::thread::sleep(Duration::from_millis(100));
    }
}
/// Builds the previews of a stored upload and queues them for upload.
pub fn build(db: &Connection, body: &Value, response: &Value) {
    let media = body["mediaType"].as_str().unwrap_or("");
    let video = media.starts_with("video/");
    if !(video || media.starts_with("image/") && media != "image/svg+xml" && media != "image/x-icon") || ffmpeg_version().is_none() { return }
    let Some(src) = body["path"].as_str().map(PathBuf::from) else { return };
    let spool = src.parent().map(Path::to_path_buf).unwrap_or_default();
    let name = body["name"].as_str().unwrap_or("artifact");
    for (role, poster) in if video { vec![("poster", true), ("thumbnail", false)] } else { vec![("thumbnail", false)] } {
        let id = crate::new_id();
        let out = spool.join(format!("{id}.jpg"));
        match run(command(&src, &out, poster)) {
            Ok(()) => { let _ = upload::enqueue(db, &id, "upload", &json!({"path":out,"spool":true,"previews":false,"name":format!("{name}.{role}.jpg"),"mediaType":"image/jpeg","executionId":body["executionId"],"required":false,"derivedFrom":response["artifactId"],"role":role})); }
            Err(e) => eprintln!("preview error for {name}: {e}"),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn command_runs_at_low_priority_without_network() {
        let c = command(Path::new("/tmp/in.png"), Path::new("/tmp/out.jpg"), false);
        let args: Vec<String> = std::iter::once(c.get_program().to_string_lossy().into_owned()).chain(c.get_args().map(|a| a.to_string_lossy().into_owned())).collect();
        assert_eq!(&args[..3], ["nice", "-n", "19"]);
        if cfg!(target_os = "macos") { assert!(args.iter().any(|a| a.contains("deny network"))) }
        assert!(args.iter().any(|a| a.starts_with("scale=")));
    }
    #[test]
    fn builds_a_thumbnail_when_ffmpeg_is_present() {
        if ffmpeg_version().is_none() { return }
        let d = std::env::temp_dir().join(format!("fleet-preview-{}", std::process::id()));
        std::fs::create_dir_all(&d).unwrap();
        let src = d.join("in.png");
        assert!(Command::new("ffmpeg").args(["-y", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=red:s=1024x768", "-frames:v", "1"]).arg(&src).status().unwrap().success());
        run(command(&src, &d.join("out.jpg"), false)).unwrap();
        assert!(std::fs::metadata(d.join("out.jpg")).unwrap().len() > 0);
    }
}
