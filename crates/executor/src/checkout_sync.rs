//! Checkout sync (docs/prototype-implementation-plan.md W3): on `base-updated` fetch and fast-forward this host's clean checkouts.
//! Dirty or diverged checkouts stay untouched and are reported with a reason; no raw stderr leaves the host.
use serde_json::{Value, json};
use std::path::Path;
use std::process::{Command, Stdio};

fn git(dir: &str, args: &[&str]) -> Option<String> {
    // ponytail: ambient host credentials until W4 sets per-project connections.
    let o = Command::new("git").args(["-c", "core.hooksPath=/dev/null", "-C", dir]).args(args)
        .env("GIT_TERMINAL_PROMPT", "0").env("GIT_SSH_COMMAND", "ssh -o BatchMode=yes")
        .stdin(Stdio::null()).output().ok()?;
    o.status.success().then(|| String::from_utf8_lossy(&o.stdout).trim().to_owned())
}

fn sync(dir: &str, sha: &str) -> Result<(), &'static str> {
    if !Path::new(dir).join(".git").exists() { return Err("checkout-missing") }
    if git(dir, &["status", "--porcelain"]).ok_or("status-failed")? != "" { return Err("checkout-dirty") }
    git(dir, &["fetch", "--quiet", "origin"]).ok_or("fetch-failed")?;
    git(dir, &["merge", "--ff-only", "--quiet", sha]).ok_or("not-fast-forward")?;
    Ok(())
}

/// Returns per-checkout results and whether all synced.
pub fn run(p: &Value, host: &str) -> (Value, bool) {
    let sha = p["sha"].as_str().unwrap_or("");
    let valid = sha.len() >= 7 && sha.bytes().all(|b| b.is_ascii_hexdigit());
    let results: Vec<Value> = p["project"]["environments"].as_array().into_iter().flatten()
        .filter(|e| e["hostId"] == host && (e["repo"].is_null() || p["repo"].is_null() || e["repo"] == p["repo"]))
        .filter_map(|e| e["checkoutPath"].as_str())
        .map(|dir| match if valid { sync(dir, sha) } else { Err("invalid-sha") } {
            Ok(()) => json!({"checkoutPath":dir,"status":"synced","sha":sha}),
            Err(r) => json!({"checkoutPath":dir,"status":"failed","reason":r}),
        }).collect();
    let ok = results.iter().all(|r| r["status"] == "synced");
    (json!({"hostId":host,"sha":sha,"checkouts":results}), ok)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn fast_forwards_clean_and_keeps_dirty() {
        let d = std::env::temp_dir().join(format!("fleet-sync-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&d);
        let s = |dir: &Path, a: &[&str]| assert!(Command::new("git").arg("-C").arg(dir).args(a).output().unwrap().status.success(), "{a:?}");
        std::fs::create_dir_all(&d).unwrap();
        s(&d, &["init", "-q", "--bare", "-b", "main", "origin.git"]);
        s(&d, &["clone", "-q", "origin.git", "up"]);
        let up = d.join("up");
        let c = |m: &str| s(&up, &["-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", m]);
        c("one"); s(&up, &["push", "-q", "origin", "HEAD:main"]);
        s(&d, &["clone", "-q", "origin.git", "clean"]);
        s(&d, &["clone", "-q", "origin.git", "dirty"]);
        std::fs::write(d.join("dirty/x"), "x").unwrap();
        c("two"); s(&up, &["push", "-q", "origin", "HEAD:main"]);
        let sha = git(up.to_str().unwrap(), &["rev-parse", "HEAD"]).unwrap();
        let env = |n: &str| json!({"hostId":"h","checkoutPath":d.join(n).to_str()});
        let (r, ok) = run(&json!({"sha":sha,"project":{"environments":[env("clean"), env("dirty"), {"hostId":"x","checkoutPath":"/none"}]}}), "h");
        assert!(!ok);
        assert_eq!(r["checkouts"][0]["status"], "synced");
        assert_eq!(git(d.join("clean").to_str().unwrap(), &["rev-parse", "HEAD"]).unwrap(), sha);
        assert_eq!(r["checkouts"][1]["reason"], "checkout-dirty");
        assert_eq!(r["checkouts"].as_array().unwrap().len(), 2);
    }
}
