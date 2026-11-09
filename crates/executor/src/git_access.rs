//! Git identity and credentials (docs/prototype-implementation-plan.md W4).
//! Per execution: GIT_AUTHOR_*/GIT_COMMITTER_* from project `identity`; SSH key of the repo `connection`,
//! resolved on this host under FLEET_CONNECTIONS_DIR (`<dir>/<connection>/id_ed25519`, `<dir>/known_hosts`).
//! Ambient ssh agent and ~/.ssh config are never used. Errors are reason codes; key contents never leave the host.
use serde_json::Value;
use std::path::{Path, PathBuf};
use std::process::Command;

pub fn dir() -> Option<PathBuf> {
    std::env::var_os("FLEET_CONNECTIONS_DIR").map(PathBuf::from).filter(|p| p.is_absolute())
}

/// Repo by id; no id means the project's first repo (single-repo projects). An unknown id resolves to nothing.
pub fn repo<'a>(project: &'a Value, id: &Value) -> &'a Value {
    project["repos"].as_array().and_then(|r| if id.is_null() { r.first() } else { r.iter().find(|r| r["id"] == *id) }).unwrap_or(&Value::Null)
}

pub fn env(project: &Value, repo: &Value, dir: Option<&Path>) -> Result<Vec<(String, String)>, &'static str> {
    let mut v = vec![];
    if let (Some(n), Some(e)) = (project["identity"]["name"].as_str(), project["identity"]["email"].as_str()) {
        for who in ["AUTHOR", "COMMITTER"] {
            v.push((format!("GIT_{who}_NAME"), n.to_owned()));
            v.push((format!("GIT_{who}_EMAIL"), e.to_owned()));
        }
    }
    // Unknown repo id never borrows another repo's key. ponytail: repos without `connection` (cs, dashq, specq)
    // keep host ssh; fail closed once all projects declare one.
    let Some(name) = repo["connection"].as_str() else { return Ok(v) };
    if name.is_empty() || !name.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_') { return Err("connection-invalid") }
    let d = dir.ok_or("connections-unconfigured")?;
    let (key, known) = (d.join(name).join("id_ed25519"), d.join("known_hosts"));
    if !key.is_file() { return Err("connection-missing") }
    if !known.is_file() { return Err("known-hosts-missing") }
    // GIT_SSH_COMMAND is shell-parsed: refuse paths that would need quoting.
    let (k, h) = (key.to_string_lossy(), known.to_string_lossy());
    if [&k, &h].iter().any(|s| s.bytes().any(|b| !(b.is_ascii_alphanumeric() || b"/._-".contains(&b)))) { return Err("connection-path-invalid") }
    v.push(("GIT_SSH_COMMAND".into(), format!("ssh -F /dev/null -i {k} -o IdentitiesOnly=yes -o IdentityAgent=none -o BatchMode=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile={h}")));
    Ok(v)
}

pub fn apply(c: &mut Command, vars: &[(String, String)]) {
    c.env_remove("SSH_AUTH_SOCK").env("GIT_TERMINAL_PROMPT", "0");
    c.envs(vars.iter().map(|(k, v)| (k, v)));
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    #[test]
    fn resolves_own_connection_only() {
        let d = std::env::temp_dir().join(format!("fleet-conn-{}", std::process::id()));
        std::fs::create_dir_all(d.join("a")).unwrap();
        std::fs::write(d.join("a/id_ed25519"), "k").unwrap();
        let p = json!({"identity":{"name":"fleet-bot","email":"b@x"},"repos":[{"id":"main","connection":"a"},{"id":"o","connection":"b"},{"id":"bad","connection":"../a"}]});
        assert_eq!(env(&p, repo(&p, &json!("main")), Some(&d)), Err("known-hosts-missing"));
        std::fs::write(d.join("known_hosts"), "h").unwrap();
        let v = env(&p, repo(&p, &Value::Null), Some(&d)).unwrap();
        assert!(v.contains(&("GIT_COMMITTER_NAME".into(), "fleet-bot".into())));
        let ssh = &v.iter().find(|(k, _)| k == "GIT_SSH_COMMAND").unwrap().1;
        assert!(ssh.contains("/a/id_ed25519 ") && ssh.contains("IdentitiesOnly=yes") && ssh.contains("StrictHostKeyChecking=yes"));
        assert_eq!(env(&p, repo(&p, &json!("o")), Some(&d)), Err("connection-missing"));
        assert_eq!(env(&p, repo(&p, &json!("bad")), Some(&d)), Err("connection-invalid"));
        assert_eq!(env(&p, repo(&p, &json!("main")), None), Err("connections-unconfigured"));
        assert_eq!(env(&p, repo(&p, &json!("nope")), Some(&d)), Ok(v[..4].to_vec()));
        assert_eq!(env(&json!({}), &json!({}), None).unwrap().len(), 0);
    }
}
