// This host's artifact store (design §3, Phase 3 item 22). The executor registers it with the coordinator through its outbox,
// sending the coordinator role's S3 key read from the host-only key file, and reports reachability and capacity in each poll.
// Configuration: FLEET_STORE_CONFIG, a JSON file
//   {id, zone, bucket, endpoint (tunnel), localEndpoint (loopback), keyFile, capacityBytes, backingPath, requiresLogon}.
use serde_json::{json, Value};
use std::path::Path;
use std::time::Duration;

pub fn config() -> Option<Value> {
    let path = std::env::var_os("FLEET_STORE_CONFIG")?;
    serde_json::from_str(&std::fs::read_to_string(path).ok()?).ok()
}
/// Key file formats on the hosts: JSON {accessKeyId, secretAccessKey}, env lines (AWS_ACCESS_KEY_ID=, AWS_SECRET_ACCESS_KEY=),
/// or two lines (key id, secret).
pub fn read_key(path: &Path) -> Option<(String, String)> {
    let text = std::fs::read_to_string(path).ok()?;
    if let Ok(v) = serde_json::from_str::<Value>(&text) {
        return Some((v["accessKeyId"].as_str()?.to_string(), v["secretAccessKey"].as_str()?.to_string()));
    }
    let mut id = None;
    let mut secret = None;
    for line in text.lines() {
        let line = line.trim().trim_start_matches("export ");
        // `garage key info --show-secret` output: "Key ID: GK..." and "Secret key: ...".
        if let Some(v) = line.strip_prefix("Key ID:") { id = Some(v.trim().to_string()); continue }
        if let Some(v) = line.strip_prefix("Secret key:") { secret = Some(v.trim().to_string()); continue }
        if let Some((k, v)) = line.split_once('=') {
            let v = v.trim().trim_matches('\'').trim_matches('"').to_string();
            if k == "AWS_ACCESS_KEY_ID" { id = Some(v) } else if k == "AWS_SECRET_ACCESS_KEY" { secret = Some(v) }
        }
    }
    if id.is_none() {
        let lines: Vec<&str> = text.lines().map(str::trim).filter(|l| !l.is_empty()).collect();
        if lines.len() == 2 { return Some((lines[0].to_string(), lines[1].to_string())) }
    }
    Some((id?, secret?))
}
/// Free bytes of the file system behind `path` (statvfs).
pub fn free_bytes(path: &Path) -> Option<u64> {
    use std::os::unix::ffi::OsStrExt;
    let c = std::ffi::CString::new(path.as_os_str().as_bytes()).ok()?;
    let mut s: libc::statvfs = unsafe { std::mem::zeroed() };
    if unsafe { libc::statvfs(c.as_ptr(), &mut s) } != 0 { return None }
    Some(s.f_bavail as u64 * s.f_frsize as u64)
}
/// Capacity left: the configured capacity, bounded by the free space of the disk behind the store (on Windows, the system drive
/// holding the WSL image).
pub fn effective_capacity(cfg: &Value) -> Option<u64> {
    let free = cfg["backingPath"].as_str().and_then(|p| free_bytes(Path::new(p)));
    match (cfg["capacityBytes"].as_u64(), free) {
        (Some(c), Some(f)) => Some(c.min(f)),
        (c, f) => c.or(f),
    }
}
/// Any HTTP answer from the loopback endpoint means the store process is up.
pub fn reachable(cfg: &Value) -> bool {
    let Some(url) = cfg["localEndpoint"].as_str() else { return false };
    let agent = ureq::AgentBuilder::new().timeout(Duration::from_secs(2)).build();
    match agent.get(url).call() {
        Ok(_) | Err(ureq::Error::Status(_, _)) => true,
        Err(_) => false,
    }
}
pub fn registration(cfg: &Value, host: &str) -> Option<Value> {
    let (key, secret) = read_key(Path::new(cfg["keyFile"].as_str()?))?;
    Some(json!({"hostId":host,"store":{"id":cfg["id"],"zone":cfg["zone"],"bucket":cfg["bucket"],"endpoint":cfg["endpoint"],"localEndpoint":cfg["localEndpoint"],"accessKeyId":key,"secretAccessKey":secret,"capacityBytes":effective_capacity(cfg),"requiresLogon":cfg["requiresLogon"].as_bool().unwrap_or(false)}}))
}
pub fn report(cfg: &Value) -> Value {
    json!([{"id":cfg["id"],"reachable":reachable(cfg),"capacityBytes":effective_capacity(cfg)}])
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn key_formats() {
        let d = std::env::temp_dir().join(format!("fleet-keys-{}", std::process::id()));
        std::fs::create_dir_all(&d).unwrap();
        for (name, text) in [("a", r#"{"accessKeyId":"GK1","secretAccessKey":"s1"}"#), ("b", "export AWS_ACCESS_KEY_ID=GK1\nAWS_SECRET_ACCESS_KEY='s1'\n"), ("c", "GK1\ns1\n"), ("d", "==== ACCESS KEY INFORMATION ====\nKey ID:              GK1\nKey name:            fleet-coordinator\nSecret key:          s1\nCreated:             2026-11-09\n")] {
            std::fs::write(d.join(name), text).unwrap();
            assert_eq!(read_key(&d.join(name)), Some(("GK1".into(), "s1".into())), "{name}");
        }
    }
    #[test]
    fn capacity_is_bounded_by_the_disk() {
        let c = effective_capacity(&json!({"capacityBytes":u64::MAX,"backingPath":"/"})).unwrap();
        assert!(c > 0 && c < u64::MAX, "bounded by the disk's free space");
        assert_eq!(effective_capacity(&json!({"capacityBytes":10,"backingPath":"/"})), Some(10));
    }
}
