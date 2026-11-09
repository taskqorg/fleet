//! Project readiness (docs/prototype-implementation-plan.md W5): read-only checks for this host's environments.
//! Each check is `ok|failed` + reason; no raw stderr or secrets leave the host.
use serde_json::{Value, json};
use std::path::Path;
use std::process::{Command, Stdio};

fn check(name: &str, target: &str, failed: Option<String>) -> Value {
    match failed {
        None => json!({"check":name,"target":target,"status":"ok"}),
        Some(r) => json!({"check":name,"target":target,"status":"failed","reason":r}),
    }
}

fn valid_remote(r: &str) -> bool {
    !r.starts_with('-') && ["https://", "ssh://", "git@"].iter().any(|p| r.starts_with(p))
}

/// Reason from git stderr without passing stderr on: network, auth or missing repository.
fn ls_remote_reason(code: i32, stderr: &str) -> String {
    let e = stderr.to_ascii_lowercase();
    let kind = if ["could not resolve host", "network is unreachable", "connection timed out", "connection refused", "operation timed out", "temporary failure in name resolution"].iter().any(|x| e.contains(x)) { "network-unreachable" }
        else if ["permission denied", "authentication failed", "could not read username", "host key verification failed", "invalid username or password"].iter().any(|x| e.contains(x)) { "auth-denied" }
        else if ["repository not found", "does not appear to be a git repository", "not found"].iter().any(|x| e.contains(x)) { "repo-missing" }
        else if code == 2 { "head-missing" } else { "unclassified" };
    format!("ls-remote-{kind}-exit-{code}")
}

/// `registry` is the FLEET_NATIVE_ACCOUNTS file (host account bindings).
pub fn run(project: &Value, host: &str, registry: Option<&Path>) -> Value {
    let mut checks = vec![];
    let repos: Vec<&Value> = project["repos"].as_array().into_iter().flatten().collect();
    for env in project["environments"].as_array().into_iter().flatten().filter(|e| e["hostId"] == host) {
        let path = env["checkoutPath"].as_str().unwrap_or("");
        let present = Path::new(path).join(".git").exists();
        checks.push(check("checkout", path, (!present).then(|| "checkout-missing".into())));
        for r in repos.iter().filter(|r| env["repo"].is_null() || env["repo"] == r["id"]) {
            let remote = r["remote"].as_str().unwrap_or("");
            let access = crate::git_access::env(project, r, crate::git_access::dir().as_deref());
            let failed = if !valid_remote(remote) { Some("invalid-remote".into()) } else if let Err(e) = access { Some(e.into()) } else {
                let mut g = crate::worktree::clean(Command::new("git"));
                crate::git_access::apply(&mut g, &access.unwrap_or_default());
                let o = g
                    .args(["-c", "core.hooksPath=/dev/null", "-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "protocol.ssh.allow=always", "ls-remote", "--exit-code", "--", remote, "HEAD"])
                    .stdin(Stdio::null()).output();
                match o { Ok(o) if o.status.success() => None, Ok(o) => Some(ls_remote_reason(o.status.code().unwrap_or(-1), &String::from_utf8_lossy(&o.stderr))), Err(_) => Some("git-unavailable".into()) }
            };
            checks.push(check("remote", r["id"].as_str().unwrap_or(remote), failed));
        }
        if let Some(s) = project["scripts"]["setup"].as_str() {
            let first = s.split_whitespace().next().unwrap_or("");
            let missing = first.contains('/') && !Path::new(path).join(first).exists();
            checks.push(check("setup", first, missing.then(|| "setup-script-missing".into())));
        }
    }
    let harnesses: Vec<&Value> = project["roles"].as_object().into_iter().flat_map(|m| m.values()).collect();
    if !harnesses.is_empty() {
        let accounts = registry.and_then(|p| std::fs::read(p).ok()).and_then(|b| serde_json::from_slice::<Value>(&b).ok());
        match accounts.as_ref().and_then(Value::as_object) {
            None => checks.push(check("accounts", host, Some("registry-unavailable".into()))),
            Some(m) => for (name, a) in m.iter().filter(|(_, a)| harnesses.contains(&&a["harness"])) {
                let ready = a.get("readiness").is_none_or(|s| s == "ready");
                checks.push(check("account", name, (!ready).then(|| "login-absent".into())));
            },
        }
    }
    let ok = checks.iter().all(|c| c["status"] == "ok");
    json!({"hostId":host,"ok":ok,"checks":checks})
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn ls_remote_reasons() {
        assert_eq!(ls_remote_reason(128, "ssh: Could not resolve hostname github.com"), "ls-remote-network-unreachable-exit-128");
        assert_eq!(ls_remote_reason(128, "git@github.com: Permission denied (publickey)."), "ls-remote-auth-denied-exit-128");
        assert_eq!(ls_remote_reason(128, "ERROR: Repository not found."), "ls-remote-repo-missing-exit-128");
        assert_eq!(ls_remote_reason(2, ""), "ls-remote-head-missing-exit-2");
    }
    #[test]
    fn rejects_option_like_remote() {
        let p = json!({"repos":[{"id":"m","remote":"--upload-pack=touch /tmp/x"}],"environments":[{"hostId":"h","checkoutPath":"/none"}]});
        assert_eq!(run(&p, "h", None)["checks"][1]["reason"], "invalid-remote");
        assert!(!valid_remote("file:///x") && valid_remote("git@github.com:a/b.git") && valid_remote("https://x/y"));
    }
    #[test]
    fn reports_each_check() {
        let d = std::env::temp_dir().join(format!("fleet-ready-{}", std::process::id()));
        std::fs::create_dir_all(d.join("co/.git")).unwrap();
        let reg = d.join("acc.json");
        std::fs::write(&reg, r#"{"a":{"harness":"codex"},"b":{"harness":"codex","readiness":"unprovisioned"},"c":{"harness":"claude"}}"#).unwrap();
        let p = json!({"repos":[{"id":"main","remote":d.join("nope").to_str()}],"environments":[{"hostId":"h","checkoutPath":d.join("co").to_str()},{"hostId":"x","checkoutPath":"/none"}],"scripts":{"setup":"./setup.sh"},"roles":{"pm":"codex"}});
        let r = run(&p, "h", Some(&reg));
        let s: Vec<(String, String)> = r["checks"].as_array().unwrap().iter().map(|c| (c["check"].as_str().unwrap().into(), c["status"].as_str().unwrap().into())).collect();
        assert_eq!(s, [("checkout", "ok"), ("remote", "failed"), ("setup", "failed"), ("account", "ok"), ("account", "failed")].map(|(a, b)| (a.into(), b.into())));
        assert_eq!(r["ok"], false);
        assert_eq!(run(&p, "h", None)["checks"][3]["reason"], "registry-unavailable");
    }
}
