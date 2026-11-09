//! Worktree lifecycle (docs/prototype-implementation-plan.md W2): roots, base, branch, links, setup, teardown.
use crate::{Result, git};
use serde_json::{Value, json};
use std::{env, fs, path::{Path, PathBuf}, process::{Command, Stdio}};

/// Host-trusted roots only (FLEET_PROJECT_ROOTS, ':'-separated absolute paths); payload checkoutPath must sit under one.
pub fn project_roots() -> Vec<PathBuf> {
    env::var("FLEET_PROJECT_ROOTS").unwrap_or_default().split(':').filter(|s| s.starts_with('/')).filter_map(|s| fs::canonicalize(s).ok()).collect()
}
fn checkouts(project: &Value, host: &str) -> Vec<String> {
    project["environments"].as_array().into_iter().flatten()
        .filter(|e| e["hostId"] == host).filter_map(|e| e["checkoutPath"].as_str().map(str::to_owned)).collect()
}
/// Payload workingDirectory, else this host's first project checkout.
pub fn repo_path(p: &Value, host: &str) -> Option<String> {
    p["workingDirectory"].as_str().map(str::to_owned).or_else(|| checkouts(&p["project"], host).into_iter().next())
}
fn tail(b: &[u8]) -> String {
    let s = String::from_utf8_lossy(b);
    let n = s.len().saturating_sub(4000);
    s[s.char_indices().map(|(i, _)| i).find(|&i| i >= n).unwrap_or(0)..].to_owned()
}
/// Child env: allowlist only, never executor secrets (FLEET_HOST_TOKEN etc.).
pub fn clean(mut c: Command) -> Command {
    c.env_clear();
    for k in ["PATH", "HOME", "SSH_AUTH_SOCK", "LANG"] { if let Some(v) = env::var_os(k) { c.env(k, v); } }
    c
}
/// Fetch with hooks off, credentials from the project connection only (W4).
fn fetch(top: &str, project: &Value) -> bool {
    let Ok(access) = crate::git_access::env(project, crate::git_access::repo(project, &Value::Null), crate::git_access::dir().as_deref()) else { return false };
    let mut c = clean(Command::new("git"));
    crate::git_access::apply(&mut c, &access);
    c.args(["-c", "core.hooksPath=/dev/null", "-C", top, "fetch", "--quiet", "origin"])
        .stdin(Stdio::null()).output().is_ok_and(|o| o.status.success())
}
/// A project session whose working directory is a git repository runs in its own worktree (branch <prefix>/<session>).
/// The payload path is untrusted: it must canonicalize under a configured project root and outside executor/session dirs.
/// Returns (worktree, checkout toplevel); `created` is true when the worktree was added now.
pub fn worktree(dir: &Path, session: &str, repo: Option<&str>, roots: &[PathBuf], forbidden: &[&Path], project: &Value) -> Result<Option<(PathBuf, PathBuf, bool)>> {
    let Some(repo) = repo else { return Ok(None) };
    let allowed = |p: &Path| -> Option<PathBuf> {
        if forbidden.iter().any(|f| fs::canonicalize(f).is_ok_and(|f| p.starts_with(&f) || f.starts_with(p))) { return None; }
        roots.iter().find(|r| p.starts_with(r)).cloned()
    };
    let repo = fs::canonicalize(repo).map_err(|_| "working directory unavailable")?;
    let root = allowed(&repo).ok_or("working directory outside configured project roots")?;
    let rev = |arg: &str| -> Result<Option<PathBuf>> {
        let o = git().env("GIT_CEILING_DIRECTORIES", &root).arg("-C").arg(&repo).args(["rev-parse", "--path-format=absolute", arg]).output()?;
        if !o.status.success() { return Ok(None); }
        Ok(Some(fs::canonicalize(String::from_utf8_lossy(&o.stdout).trim())?))
    };
    let (Some(top), Some(common)) = (rev("--show-toplevel")?, rev("--git-common-dir")?) else { return Ok(None) };
    if allowed(&top).is_none() || allowed(&common).is_none() {
        return Err("repository outside configured project roots".into());
    }
    let w = dir.join("worktree");
    if w.join(".git").exists() {
        return Ok(Some((w, top, false)));
    }
    let wt = &project["worktree"];
    let branch = format!("{}/{session}", wt["branchPrefix"].as_str().unwrap_or("fleet"));
    let t = top.to_string_lossy().into_owned();
    let exists = git().env("GIT_CEILING_DIRECTORIES", &root).args(["-C", &t, "rev-parse", "--verify", "--quiet", &branch]).output()?.status.success();
    let mut c = git();
    c.env("GIT_CEILING_DIRECTORIES", &root).args(["-C", &t, "worktree", "add"]);
    if exists {
        c.arg(&w).arg(&branch);
    } else {
        c.args(["--no-track", "-b", &branch]).arg(&w);
        // Base: explicit baseRef, else fresh origin/<defaultBranch> when startFromOrigin (default), else HEAD.
        let base = wt["baseRef"].as_str().map(str::to_owned).or_else(|| {
            (wt["startFromOrigin"].as_bool().unwrap_or(false) && fetch(&t, project))
                .then(|| format!("origin/{}", project["repos"][0]["defaultBranch"].as_str().unwrap_or("main")))
        });
        if let Some(b) = base { c.arg(b); }
    }
    if !c.output()?.status.success() {
        return Err("git worktree add failed".into());
    }
    // linkPaths and sharedDirectories: symlinks into the main checkout; only existing, relative, non-escaping paths.
    for rel in ["linkPaths", "sharedDirectories"].iter().flat_map(|k| wt[*k].as_array().cloned().unwrap_or_default()) {
        let Some(rel) = rel.as_str() else { continue };
        if rel.starts_with('/') || rel.split('/').any(|s| s == "..") { continue; }
        let (src, dst) = (top.join(rel), w.join(rel));
        if src.exists() && !dst.exists() {
            let Some(d) = dst.parent() else { continue };
            fs::create_dir_all(d)?;
            // Intermediate symlinks must not move either end outside its tree.
            if !fs::canonicalize(d)?.starts_with(fs::canonicalize(&w)?) || !fs::canonicalize(&src)?.starts_with(&top) { continue; }
            std::os::unix::fs::symlink(&src, &dst)?;
        }
    }
    Ok(Some((w, top, true)))
}
/// Runs a project script in the worktree and waits; returns event data with exit code and truncated output.
pub fn script(name: &str, cmd: &str, top: &Path, w: &Path, session: &str) -> Value {
    let o = clean(Command::new("sh")).args(["-c", cmd]).current_dir(w).stdin(Stdio::null())
        .env("FLEET_ROOT_PATH", top).env("FLEET_WORKTREE_PATH", w).env("FLEET_SESSION_ID", session).output();
    match o {
        Ok(o) => json!({"script":name,"exitCode":o.status.code(),"ok":o.status.success(),"stdout":tail(&o.stdout),"stderr":tail(&o.stderr)}),
        Err(e) => json!({"script":name,"exitCode":null,"ok":false,"stderr":e.to_string()}),
    }
}
/// Session finish: teardown script, then remove worktree unless dirty or teardown failed; branch deleted only if merged (`-d`).
pub fn teardown(dir: &Path, session: &str, top: &Path, project: &Value) -> Value {
    let w = dir.join("worktree");
    if !w.join(".git").exists() { return json!({"removed":false,"reason":"no-worktree"}); }
    let t = top.to_string_lossy().into_owned();
    let mut out = json!({});
    if let Some(cmd) = project["scripts"]["teardown"].as_str() {
        out["teardown"] = script("teardown", cmd, top, &w, session);
        if out["teardown"]["ok"] != true {
            out["removed"] = json!(false); out["reason"] = json!("teardown-failed"); return out;
        }
    }
    // Fleet-made links are not the agent's changes: excluded from the dirty check, removed before `worktree remove`.
    let links: Vec<String> = ["linkPaths", "sharedDirectories"].iter().flat_map(|k| project["worktree"][*k].as_array().cloned().unwrap_or_default())
        .filter_map(|v| v.as_str().map(str::to_owned)).filter(|r| fs::symlink_metadata(w.join(r)).is_ok_and(|m| m.file_type().is_symlink())).collect();
    let dirty = git().arg("-C").arg(&w).args(["status", "--porcelain", "--", "."]).args(links.iter().map(|r| format!(":(exclude){r}"))).output().map(|o| !o.status.success() || !o.stdout.is_empty()).unwrap_or(true);
    if dirty { out["removed"] = json!(false); out["reason"] = json!("uncommitted"); return out; }
    for r in &links { let _ = fs::remove_file(w.join(r)); }
    let removed = git().args(["-C", &t, "worktree", "remove"]).arg(&w).output().is_ok_and(|o| o.status.success());
    out["removed"] = json!(removed);
    if !removed { out["reason"] = json!("worktree-remove-failed"); return out; }
    let branch = format!("{}/{session}", project["worktree"]["branchPrefix"].as_str().unwrap_or("fleet"));
    out["branchDeleted"] = json!(git().args(["-C", &t, "branch", "-d", &branch]).output().is_ok_and(|o| o.status.success()));
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    fn sh(repo: &Path, a: &[&str]) { assert!(Command::new("git").arg("-C").arg(repo).args(a).status().unwrap().success()); }
    #[test]
    fn worktree_per_session_in_repo() {
        let root = env::temp_dir().join(format!("fleet-wt-{}", std::process::id()));
        let repo = root.join("repo");
        fs::create_dir_all(&repo).unwrap();
        sh(&repo, &["init", "-q"]);
        fs::write(repo.join(".env.local"), "X=1").unwrap();
        sh(&repo, &["-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "i"]);
        let s = root.join("s1");
        fs::create_dir_all(&s).unwrap();
        let roots = vec![fs::canonicalize(&repo).unwrap()];
        let r = repo.to_str().unwrap();
        let p = json!({"worktree":{"branchPrefix":"t","linkPaths":[".env.local","../x"]},"scripts":{"teardown":"echo $FLEET_SESSION_ID > ../../td"}});
        let (w, top, created) = worktree(&s, "s1", Some(r), &roots, &[], &p).unwrap().unwrap();
        assert!(created && w.join(".git").exists() && fs::read_link(w.join(".env.local")).is_ok());
        assert_eq!(worktree(&s, "s1", Some(r), &roots, &[], &p).unwrap().unwrap().2, false);
        assert!(worktree(&s, "s1", None, &roots, &[], &p).unwrap().is_none());
        // Rejected: outside configured roots, unconfigured roots, and inside executor/session dirs.
        assert!(worktree(&s, "s2", Some(root.to_str().unwrap()), &roots, &[], &p).is_err());
        assert!(worktree(&s, "s2", Some(r), &[], &[], &p).is_err());
        assert!(worktree(&s, "s2", Some(r), &roots, &[&root], &p).is_err());
        let sub = repo.join("sub");
        fs::create_dir_all(&sub).unwrap();
        assert!(worktree(&s, "s3", Some(sub.to_str().unwrap()), &[fs::canonicalize(&sub).unwrap()], &[], &p).is_err());
        let e = script("setup", "echo $FLEET_WORKTREE_PATH; exit 3", &top, &w, "s1");
        assert_eq!(e["exitCode"], 3);
        fs::write(w.join("new"), "x").unwrap();
        assert_eq!(teardown(&s, "s1", &top, &p)["reason"], "uncommitted");
        fs::remove_file(w.join("new")).unwrap();
        let t = teardown(&s, "s1", &top, &p);
        assert_eq!(t["removed"], true, "{t}");
        assert_eq!(t["branchDeleted"], true);
        assert!(root.join("td").exists() && !w.exists());
        fs::remove_dir_all(root).unwrap();
    }
}
