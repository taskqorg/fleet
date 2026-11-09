//! Checkout sync (docs/prototype-implementation-plan.md W3): on `base-updated` fetch and fast-forward this host's clean checkouts.
//! Dirty or diverged checkouts stay untouched and are reported with a reason; no raw stderr leaves the host.
use serde_json::{Value, json};
use std::path::{Path, PathBuf};

fn git(dir: &Path, access: &[(String, String)], args: &[&str]) -> Option<String> {
    // Cleared env + hardened config (crate::git); credentials only from the project connection (W4).
    let mut c = crate::git();
    if let Some(v) = std::env::var_os("HOME") { c.env("HOME", v); }
    crate::git_access::apply(&mut c, access);
    #[cfg(test)] c.args(["-c", "protocol.file.allow=always"]);
    let o = c.arg("-C").arg(dir).args(args).output().ok()?;
    o.status.success().then(|| String::from_utf8_lossy(&o.stdout).trim().to_owned())
}

/// Same gate as worktree.rs: canonical path under a host project root, not overlapping executor/session dirs.
fn allowed(dir: &str, roots: &[PathBuf], forbidden: &[&Path]) -> Option<PathBuf> {
    let p = std::fs::canonicalize(dir).ok()?;
    if forbidden.iter().any(|f| std::fs::canonicalize(f).is_ok_and(|f| p.starts_with(&f) || f.starts_with(&p))) { return None }
    let root = roots.iter().find(|r| p.starts_with(r))?;
    let inside = |q: &Path| !forbidden.iter().any(|f| std::fs::canonicalize(f).is_ok_and(|f| q.starts_with(&f) || f.starts_with(q))) && q.starts_with(root);
    // .git may be a gitfile: toplevel must be the checkout itself, git dirs under the same root.
    for (arg, exact) in [("--show-toplevel", true), ("--git-common-dir", false), ("--absolute-git-dir", false)] {
        let o = crate::git().env("GIT_CEILING_DIRECTORIES", root).arg("-C").arg(&p).args(["rev-parse", "--path-format=absolute", arg]).output().ok()?;
        let q = std::fs::canonicalize(String::from_utf8_lossy(&o.stdout).trim()).ok().filter(|_| o.status.success())?;
        if !inside(&q) || exact && q != p { return None }
    }
    Some(p)
}

fn sync(dir: &Path, sha: &str, access: &[(String, String)]) -> Result<(), &'static str> {
    let git = |args: &[&str]| git(dir, access, args);
    if !dir.join(".git").exists() { return Err("checkout-missing") }
    if git(&["status", "--porcelain"]).ok_or("status-failed")? != "" { return Err("checkout-dirty") }
    git(&["fetch", "--quiet", "origin"]).ok_or("fetch-failed")?;
    git(&["merge", "--ff-only", "--quiet", sha]).ok_or("not-fast-forward")?;
    Ok(())
}

/// Returns per-checkout results and whether all synced.
pub fn run(p: &Value, host: &str, roots: &[PathBuf], forbidden: &[&Path]) -> (Value, bool) {
    let sha = p["sha"].as_str().unwrap_or("");
    let valid = sha.len() >= 7 && sha.bytes().all(|b| b.is_ascii_hexdigit());
    let results: Vec<Value> = p["project"]["environments"].as_array().into_iter().flatten()
        .filter(|e| e["hostId"] == host && (e["repo"].is_null() || p["repo"].is_null() || e["repo"] == p["repo"]))
        .filter_map(|e| e["checkoutPath"].as_str().map(|d| (d, &e["repo"])))
        .map(|(dir, repo)| match if !valid { Err("invalid-sha") } else {
            let access = crate::git_access::env(&p["project"], crate::git_access::repo(&p["project"], repo), crate::git_access::dir().as_deref());
            allowed(dir, roots, forbidden).ok_or("checkout-denied").and_then(|d| sync(&d, sha, &access?))
        } {
            Ok(()) => json!({"checkoutPath":dir,"status":"synced","sha":sha}),
            Err(r) => json!({"checkoutPath":dir,"status":"failed","reason":r}),
        }).collect();
    let ok = results.iter().all(|r| r["status"] == "synced");
    (json!({"hostId":host,"sha":sha,"checkouts":results}), ok)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::process::Command;
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
        let sha = git(&up, &[], &["rev-parse", "HEAD"]).unwrap();
        let env = |n: &str| json!({"hostId":"h","checkoutPath":d.join(n).to_str()});
        let (r, ok) = run(&json!({"sha":sha,"project":{"environments":[env("clean"), env("dirty"), {"hostId":"x","checkoutPath":"/none"}]}}), "h", &[d.canonicalize().unwrap()], &[]);
        assert_eq!(run(&json!({"sha":sha,"project":{"environments":[env("clean")]}}), "h", &[], &[]).0["checkouts"][0]["reason"], "checkout-denied");
        assert!(!ok);
        assert_eq!(r["checkouts"][0]["status"], "synced");
        assert_eq!(git(&d.join("clean"), &[], &["rev-parse", "HEAD"]).unwrap(), sha);
        assert_eq!(r["checkouts"][1]["reason"], "checkout-dirty");
        assert_eq!(r["checkouts"].as_array().unwrap().len(), 2);
    }
}
