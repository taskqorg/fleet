// Artifact transfers (design §3, Phase 3 item 27). A worker thread works through the `transfers` journal: uploads of collected
// output and agent `artifact_save`, `artifact-copy` and `artifact-verify` commands, and `artifact_get` downloads. Bytes go
// straight between this host and a store; the coordinator only reserves, presigns, and records. Each job resumes after a
// restart from its journal row (upload id and finished part ETags live on the coordinator's copy row and in `done`).
use crate::outbox;
use rusqlite::{params, Connection};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::io::{Read, Seek, SeekFrom, Write};
use std::path::{Path, PathBuf};
use std::time::Duration;


pub fn schema(db: &Connection) -> rusqlite::Result<()> {
    db.execute_batch("CREATE TABLE IF NOT EXISTS transfers(id TEXT PRIMARY KEY,kind TEXT NOT NULL,body TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'queued',response TEXT,attempts INTEGER NOT NULL DEFAULT 0,next_at INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL);")
}
fn now_ms() -> i64 {
    std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map_or(0, |d| d.as_millis() as i64)
}
/// Queues a job; `id` is also the artifact id of an upload, so a repeated reserve after a restart is the same reservation.
pub fn enqueue(db: &Connection, id: &str, kind: &str, body: &Value) -> rusqlite::Result<()> {
    db.execute("INSERT OR IGNORE INTO transfers(id,kind,body,created_at) VALUES(?1,?2,?3,?4)", params![id, kind, body.to_string(), now_ms()])?;
    Ok(())
}

/// Cloudflare Access service token from an env file (CF_ACCESS_CLIENT_ID, CF_ACCESS_CLIENT_SECRET).
pub fn access_token(file: &Path) -> Option<(String, String)> {
    let text = std::fs::read_to_string(file).ok()?;
    let mut id = None;
    let mut secret = None;
    for line in text.lines() {
        let line = line.trim().trim_start_matches("export ");
        if let Some((k, v)) = line.split_once('=') {
            let v = v.trim().trim_matches('\'').trim_matches('"').to_string();
            if k == "CF_ACCESS_CLIENT_ID" { id = Some(v) } else if k == "CF_ACCESS_CLIENT_SECRET" { secret = Some(v) }
        }
    }
    Some((id?, secret?))
}
pub fn is_loopback(url: &str) -> bool {
    let rest = url.split_once("://").map_or(url, |(_, r)| r);
    let host = rest.split(['/', '?']).next().unwrap_or("");
    let host = if host.starts_with('[') { host.split(']').next().unwrap_or("").trim_start_matches('[') } else { host.split(':').next().unwrap_or("") };
    matches!(host, "127.0.0.1" | "localhost" | "::1")
}

#[derive(Clone)]
pub struct Client {
    pub url: String,
    pub token: String,
    pub host: String,
    pub access: Option<(String, String)>,
    coordinator: ureq::Agent,
    store: ureq::Agent,
}
impl Client {
    pub fn new(url: String, token: String, host: String, access: Option<(String, String)>) -> Self {
        Client {
            url, token, host, access,
            coordinator: ureq::AgentBuilder::new().timeout(Duration::from_secs(60)).build(),
            // Parts are 32 MiB; a slow tunnel needs more than the outbox's 5 s.
            store: ureq::AgentBuilder::new().timeout_connect(Duration::from_secs(20)).timeout_read(Duration::from_secs(300)).timeout_write(Duration::from_secs(300)).build(),
        }
    }
    pub fn post(&self, route: &str, body: Value) -> std::result::Result<Value, Failure> {
        match self.coordinator.post(&format!("{}{route}", self.url)).set("Authorization", &format!("Bearer {}", self.token)).send_json(body) {
            Ok(r) => r.into_json::<Value>().map_err(|e| Failure::Retry(e.to_string())),
            Err(ureq::Error::Status(code, r)) => {
                let text = r.into_string().unwrap_or_default();
                // 409 no_store: no listed store is reachable now (a sleeping Mac, Windows logged out): wait and retry.
                if code >= 500 || code == 409 && text.contains("No reachable store") || code == 429 { Err(Failure::Retry(format!("{code} {text}"))) } else { Err(Failure::Permanent(format!("{code} {text}"))) }
            }
            Err(e) => Err(Failure::Retry(e.to_string())),
        }
    }
    /// A request to a store URL: the Access headers are added (never signed) unless it targets loopback.
    fn store_request(&self, method: &str, url: &str) -> ureq::Request {
        let mut r = self.store.request(method, url);
        if !is_loopback(url) {
            if let Some((id, secret)) = &self.access {
                r = r.set("CF-Access-Client-Id", id).set("CF-Access-Client-Secret", secret);
            }
        }
        r
    }
    pub fn put_part(&self, url: &str, bytes: &[u8]) -> std::result::Result<String, Failure> {
        match self.store_request("PUT", url).send_bytes(bytes) {
            Ok(r) => r.header("ETag").map(str::to_owned).ok_or_else(|| Failure::Retry("part without ETag".into())),
            Err(ureq::Error::Status(code, r)) if code < 500 && code != 429 => Err(Failure::Permanent(format!("store {code} {}", r.into_string().unwrap_or_default()))),
            Err(e) => Err(Failure::Retry(e.to_string())),
        }
    }
    pub fn get(&self, url: &str) -> std::result::Result<Box<dyn Read + Send + Sync>, Failure> {
        match self.store_request("GET", url).call() {
            Ok(r) => Ok(r.into_reader()),
            Err(ureq::Error::Status(code, _)) if code < 500 && code != 429 => Err(Failure::Permanent(format!("store GET {code}"))),
            Err(e) => Err(Failure::Retry(e.to_string())),
        }
    }
}
#[derive(Debug)]
pub enum Failure {
    Retry(String),
    Permanent(String),
}
/// Network-shaped I/O errors (a reset or timeout while streaming a body, a body cut short) are retried; others (a missing or
/// unreadable local file) are permanent.
impl From<std::io::Error> for Failure {
    fn from(e: std::io::Error) -> Self {
        use std::io::ErrorKind::*;
        match e.kind() {
            ConnectionReset | ConnectionAborted | ConnectionRefused | NotConnected | BrokenPipe | TimedOut | UnexpectedEof | Interrupted | WouldBlock => Failure::Retry(e.to_string()),
            _ => Failure::Permanent(e.to_string()),
        }
    }
}

/// Streaming SHA-256 and size of a file.
pub fn hash_file(path: &Path) -> std::io::Result<(String, u64)> {
    let mut f = std::fs::File::open(path)?;
    let mut h = Sha256::new();
    let mut buf = vec![0u8; 1 << 20];
    let mut size = 0u64;
    loop {
        let n = f.read(&mut buf)?;
        if n == 0 { break }
        h.update(&buf[..n]);
        size += n as u64;
    }
    Ok((format!("{:x}", h.finalize()), size))
}
fn hash_reader(mut r: impl Read) -> std::io::Result<String> {
    let mut h = Sha256::new();
    let mut buf = vec![0u8; 1 << 20];
    loop {
        let n = r.read(&mut buf)?;
        if n == 0 { break }
        h.update(&buf[..n]);
    }
    Ok(format!("{:x}", h.finalize()))
}

/// Uploads the file parts to the reservation `artifact_id` on `store_id` (an upload, or the target side of a copy), commits,
/// and, when the coordinator answers with a verify URL (this host owns the store), re-hashes over loopback (P3-D1).
fn send_parts(c: &Client, db: &Connection, job_id: &str, file: &Path, artifact_id: &str, store_id: Option<&str>, size: u64, part_size: u64) -> std::result::Result<Value, Failure> {
    let count = size.div_ceil(part_size).max(1);
    let body: Value = db.query_row("SELECT body FROM transfers WHERE id=?", [job_id], |r| r.get::<_, String>(0)).ok().and_then(|b| serde_json::from_str(&b).ok()).unwrap_or(json!({}));
    let mut done: Vec<Value> = body["done"].as_array().cloned().unwrap_or_default();
    let mut f = std::fs::File::open(file)?;
    let missing: Vec<u64> = (1..=count).filter(|n| !done.iter().any(|d| d["partNumber"] == *n)).collect();
    for batch in missing.chunks(16) {
        let mut req = json!({"hostId":c.host,"artifactId":artifact_id,"partNumbers":batch});
        if let Some(s) = store_id { req["storeId"] = json!(s) }
        let urls = c.post("/api/hosts/artifacts/parts", req)?;
        for p in urls["parts"].as_array().into_iter().flatten() {
            let n = p["partNumber"].as_u64().ok_or(Failure::Permanent("part number".into()))?;
            let mut chunk = vec![0u8; (part_size.min(size.saturating_sub((n - 1) * part_size))) as usize];
            f.seek(SeekFrom::Start((n - 1) * part_size))?;
            f.read_exact(&mut chunk)?;
            let mut tries = 0;
            let etag = loop {
                match c.put_part(p["url"].as_str().unwrap_or(""), &chunk) {
                    Ok(e) => break e,
                    Err(Failure::Retry(e)) if tries < 5 => { tries += 1; std::thread::sleep(Duration::from_secs(2u64.pow(tries).min(60))); let _ = e; }
                    Err(e) => return Err(e),
                }
            };
            done.push(json!({"partNumber":n,"etag":etag}));
            let mut b = body.clone();
            b["done"] = json!(done);
            let _ = db.execute("UPDATE transfers SET body=?2 WHERE id=?1", params![job_id, b.to_string()]);
        }
    }
    let mut req = json!({"hostId":c.host,"artifactId":artifact_id,"parts":done});
    if let Some(s) = store_id { req["storeId"] = json!(s) }
    let commit = c.post("/api/hosts/artifacts/commit", req)?;
    if let Some(v) = commit.get("verify") { return verify(c, v["artifactId"].as_str().unwrap_or(artifact_id), v["storeId"].as_str().unwrap_or(""), v["url"].as_str().unwrap_or("")) }
    Ok(commit)
}
/// Re-hash of a staged object on this host's store (over loopback), reported to the coordinator.
pub fn verify(c: &Client, artifact_id: &str, store_id: &str, url: &str) -> std::result::Result<Value, Failure> {
    let digest = hash_reader(c.get(url)?)?;
    c.post("/api/hosts/artifacts/verified", json!({"hostId":c.host,"artifactId":artifact_id,"storeId":store_id,"sha256":digest}))
}

/// One job. Returns the response stored in the journal (and, for agent requests, read back by the tool adapter).
fn run_job(c: &Client, db: &Connection, id: &str, kind: &str, body: &Value) -> std::result::Result<Value, Failure> {
    match kind {
        "upload" => {
            let path = PathBuf::from(body["path"].as_str().unwrap_or(""));
            let (sha, size) = hash_file(&path)?;
            let mut req = json!({"hostId":c.host,"executionId":body["executionId"],"artifactId":id,"name":body["name"],"mediaType":body["mediaType"],"size":size,"sha256":sha,"required":body["required"]});
            for k in ["derivedFrom", "role"] { if !body[k].is_null() { req[k] = body[k].clone() } }
            let r = c.post("/api/hosts/artifacts/reserve", req)?;
            let _ = db.execute("UPDATE transfers SET response=?2 WHERE id=?1", params![id, json!({"artifactId":r["artifactId"],"state":r["state"]}).to_string()]);
            if r["state"] == "stored" { return Ok(json!({"artifactId":r["artifactId"],"state":"stored"})) }
            if r["state"] == "failed" { return Err(Failure::Permanent("artifact failed".into())) }
            let out = send_parts(c, db, id, &path, r["artifactId"].as_str().unwrap_or(id), None, size, r["partSize"].as_u64().unwrap_or(32 << 20))?;
            Ok(json!({"artifactId":r["artifactId"],"state":out["state"]}))
        }
        "verify" => verify(c, body["artifactId"].as_str().unwrap_or(""), body["storeId"].as_str().unwrap_or(""), body["url"].as_str().unwrap_or("")),
        "copy" => {
            // Download the source object to a temporary file next to the journal, then upload it to this host's store.
            let tmp = PathBuf::from(body["spool"].as_str().unwrap_or("")).join(format!("{id}.part"));
            if !tmp.exists() || std::fs::metadata(&tmp)?.len() != body["size"].as_u64().unwrap_or(u64::MAX) {
                let mut src = c.get(body["source"]["url"].as_str().unwrap_or(""))?;
                let mut out = std::fs::File::create(&tmp)?;
                std::io::copy(&mut src, &mut out)?;
                out.flush()?;
            }
            let size = std::fs::metadata(&tmp)?.len();
            let out = send_parts(c, db, id, &tmp, body["artifactId"].as_str().unwrap_or(""), body["storeId"].as_str(), size, body["partSize"].as_u64().unwrap_or(32 << 20));
            let _ = std::fs::remove_file(&tmp);
            out
        }
        "get" => {
            // artifact_get: download into the execution's input folder (inside its working area).
            let r = c.post("/api/hosts/artifacts/get", json!({"hostId":c.host,"executionId":body["executionId"],"artifactId":body["artifactId"]}))?;
            let url = r["url"].as_str().ok_or(Failure::Permanent(format!("artifact {}", r["availability"].as_str().unwrap_or("unavailable"))))?;
            // The input folder must be a real directory (never a symlink); the file is created new, without following links.
            let name: String = r["name"].as_str().unwrap_or("artifact").rsplit('/').next().unwrap_or("artifact").chars().map(|ch| if ch.is_ascii_alphanumeric() || "._-".contains(ch) { ch } else { '_' }).collect();
            let dir = PathBuf::from(body["inputDir"].as_str().unwrap_or(""));
            std::fs::create_dir_all(&dir)?;
            let tag: String = r["id"].as_str().unwrap_or("x").chars().take(8).collect();
            // The folder is opened once without following a link, and the file is created relative to that descriptor.
            use std::os::unix::ffi::OsStrExt;
            use std::os::fd::FromRawFd;
            let cdir = std::ffi::CString::new(dir.as_os_str().as_bytes()).map_err(|_| Failure::Permanent("input folder".into()))?;
            let dfd = unsafe { libc::open(cdir.as_ptr(), libc::O_RDONLY | libc::O_DIRECTORY | libc::O_NOFOLLOW | libc::O_CLOEXEC) };
            if dfd < 0 { return Err(Failure::Permanent("input folder is not a directory".into())) }
            let created = (0..1000).find_map(|n| {
                let file = if n == 0 { format!("{tag}-{name}") } else { format!("{tag}-{n}-{name}") };
                let c = std::ffi::CString::new(file.clone()).ok()?;
                let fd = unsafe { libc::openat(dfd, c.as_ptr(), libc::O_WRONLY | libc::O_CREAT | libc::O_EXCL | libc::O_NOFOLLOW | libc::O_CLOEXEC, 0o600) };
                (fd >= 0).then(|| (dir.join(file), unsafe { std::fs::File::from_raw_fd(fd) }))
            });
            unsafe { libc::close(dfd) };
            let (target, mut out) = created.ok_or(Failure::Permanent("cannot create the input file".into()))?;
            std::io::copy(&mut c.get(url)?, &mut out)?;
            Ok(json!({"artifactId":r["id"],"path":target,"mediaType":r["mediaType"],"size":r["size"]}))
        }
        "list" => c.post("/api/hosts/artifacts/list", json!({"hostId":c.host,"executionId":body["executionId"],"filter":body["filter"]})),
        _ => Err(Failure::Permanent(format!("unknown transfer kind {kind}"))),
    }
}

/// The artifact a queued job holds a coordinator lease on, with its store when known.
fn leased(kind: &str, id: &str, body: &Value) -> Option<(String, Option<String>)> {
    match kind {
        "upload" => Some((id.to_string(), None)),
        "verify" | "copy" => Some((body["artifactId"].as_str()?.to_string(), body["storeId"].as_str().map(str::to_owned))),
        _ => None,
    }
}
/// The workers: two lanes, so a verification never waits behind a long upload or copy: `verify` jobs on their own thread,
/// every other kind on another; each takes one job at a time, oldest first. Retryable failures back off (2 s doubling to
/// 5 min) and stay queued. A job that carries a host command reports its receipt when it ends. A third thread renews the
/// coordinator lease of every queued or running job every 10 minutes, so queue time never expires a good upload.
pub fn spawn(journal: PathBuf, client: Client, after: impl Fn(&Client, &Connection, &Value, &Value) + Send + Sync + 'static) -> std::io::Result<()> {
    let after = std::sync::Arc::new(after);
    for (name, lane) in [("fleet-transfers", "kind<>'verify'"), ("fleet-verify", "kind='verify'")] {
        let (journal, client, after) = (journal.clone(), client.clone(), after.clone());
        std::thread::Builder::new().name(name.into()).spawn(move || work(journal, client, lane, &*after))?;
    }
    std::thread::Builder::new().name("fleet-leases".into()).spawn(move || {
        let Ok(db) = Connection::open(&journal) else { return };
        let _ = db.busy_timeout(Duration::from_secs(5));
        loop {
            std::thread::sleep(Duration::from_secs(600));
            let jobs: Vec<(String, String, String)> = db.prepare("SELECT id,kind,body FROM transfers WHERE state IN ('queued','running')").and_then(|mut s| s.query_map([], |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)))?.collect()).unwrap_or_default();
            for (id, kind, body) in jobs {
                let body: Value = serde_json::from_str(&body).unwrap_or(json!({}));
                if let Some((artifact, store)) = leased(&kind, &id, &body) {
                    let mut req = json!({"hostId":client.host,"artifactId":artifact});
                    if let Some(s) = store { req["storeId"] = json!(s) }
                    let _ = client.post("/api/hosts/artifacts/renew", req);
                }
            }
        }
    })?;
    Ok(())
}
fn work(journal: PathBuf, client: Client, lane: &str, after: &(dyn Fn(&Client, &Connection, &Value, &Value) + Send + Sync)) {
        let Ok(db) = Connection::open(&journal) else { return };
        let _ = db.busy_timeout(Duration::from_secs(5));
        loop {
            let job: Option<(String, String, String, i64)> = db.query_row(&format!("SELECT id,kind,body,attempts FROM transfers WHERE state IN ('queued','running') AND {lane} AND next_at<=? ORDER BY created_at LIMIT 1"), [now_ms()], |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?))).ok();
            let Some((id, kind, body, attempts)) = job else { std::thread::sleep(Duration::from_millis(250)); continue };
            let _ = db.execute("UPDATE transfers SET state='running' WHERE id=?", [&id]);
            let body: Value = serde_json::from_str(&body).unwrap_or(json!({}));
            match run_job(&client, &db, &id, &kind, &body) {
                Ok(v) => {
                    let _ = db.execute("UPDATE transfers SET state='done',response=?2 WHERE id=?1", params![id, v.to_string()]);
                    if let Some(cmd) = body.get("command") { let _ = outbox::queue(&db, "/api/hosts/receipts", json!({"hostId":client.host,"commandId":cmd["id"],"executionId":cmd["executionId"],"status":"completed"})); }
                    after(&client, &db, &body, &v);
                }
                Err(Failure::Retry(e)) => {
                    let wait = 2000i64.saturating_mul(1 << attempts.min(8)).min(300_000);
                    let _ = db.execute("UPDATE transfers SET attempts=attempts+1,next_at=?2,response=?3 WHERE id=?1", params![id, now_ms() + wait, json!({"state":"retrying","error":e}).to_string()]);
                }
                Err(Failure::Permanent(e)) => {
                    let _ = db.execute("UPDATE transfers SET state='failed',response=?2 WHERE id=?1", params![id, json!({"state":"failed","error":e}).to_string()]);
                    if let Some(cmd) = body.get("command") { let _ = outbox::queue(&db, "/api/hosts/receipts", json!({"hostId":client.host,"commandId":cmd["id"],"executionId":cmd["executionId"],"status":"failed","error":e})); }
                }
            }
        }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn loopback_detection() {
        for u in ["http://127.0.0.1:3900/fleet/k?x=1", "http://localhost:3900/", "http://[::1]:3900/k"] { assert!(is_loopback(u), "{u}") }
        for u in ["https://fleet-store-mac.ufobe.com/fleet/k", "http://10.0.0.1/k"] { assert!(!is_loopback(u), "{u}") }
    }
    #[test]
    fn access_token_from_env_file() {
        let dir = std::env::temp_dir().join(format!("fleet-access-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let f = dir.join("cf.env");
        std::fs::write(&f, "export CF_ACCESS_CLIENT_ID='abc.access'\nCF_ACCESS_CLIENT_SECRET=\"s3cr3t\"\n").unwrap();
        assert_eq!(access_token(&f), Some(("abc.access".into(), "s3cr3t".into())));
    }
    #[test]
    fn network_io_errors_retry_local_ones_do_not() {
        use std::io::{Error, ErrorKind};
        for k in [ErrorKind::ConnectionReset, ErrorKind::TimedOut, ErrorKind::UnexpectedEof, ErrorKind::BrokenPipe] { assert!(matches!(Failure::from(Error::from(k)), Failure::Retry(_)), "{k:?}") }
        for k in [ErrorKind::NotFound, ErrorKind::PermissionDenied] { assert!(matches!(Failure::from(Error::from(k)), Failure::Permanent(_)), "{k:?}") }
    }
    #[test]
    fn leases_cover_uploads_copies_and_verifications() {
        assert_eq!(leased("upload", "a1", &json!({})), Some(("a1".into(), None)));
        assert_eq!(leased("verify", "j", &json!({"artifactId":"a2","storeId":"s"})), Some(("a2".into(), Some("s".into()))));
        assert_eq!(leased("get", "j", &json!({"artifactId":"a3"})), None);
    }
    #[test]
    fn hash_streams() {
        let f = std::env::temp_dir().join(format!("fleet-hash-{}", std::process::id()));
        std::fs::write(&f, b"abc").unwrap();
        assert_eq!(hash_file(&f).unwrap(), ("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad".into(), 3));
    }
}
