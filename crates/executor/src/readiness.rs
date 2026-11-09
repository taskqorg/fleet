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
            // ponytail: inherited env carries the connection until W4 sets per-project credentials.
            let o = Command::new("git").args(["-c", "core.hooksPath=/dev/null", "ls-remote", "--exit-code", remote, "HEAD"])
                .env("GIT_TERMINAL_PROMPT", "0").env("GIT_SSH_COMMAND", "ssh -o BatchMode=yes")
                .stdin(Stdio::null()).output();
            let failed = match o { Ok(o) if o.status.success() => None, Ok(o) => Some(format!("ls-remote-exit-{}", o.status.code().unwrap_or(-1))), Err(_) => Some("git-unavailable".into()) };
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
