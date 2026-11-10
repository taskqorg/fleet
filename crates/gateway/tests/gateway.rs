//! Gateway behavior through the real binary and a scripted provider (fleet-connections.md §2-3), adapted from the read
//! lane's tests. Risk covered: a token, key or snapshot rule that passes in isolation but fails in the running process.
use serde_json::{Value, json};
use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::sync::atomic::{AtomicU64, Ordering::SeqCst};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

const TOKEN: &str = "invented-run-token";
static NEXT: AtomicU64 = AtomicU64::new(0);

fn sha256(text: &str) -> String {
    use sha2::{Digest, Sha256};
    Sha256::digest(text.as_bytes()).iter().map(|b| format!("{b:02x}")).collect()
}

fn fp(key: &str) -> String {
    sha256(key)[..12].to_owned()
}

struct Provider {
    url: String,
    seen: Arc<Mutex<Vec<(String, String)>>>,
}

fn read_http(stream: &mut TcpStream) -> Option<(String, String)> {
    let mut buffer = Vec::new();
    let mut chunk = [0u8; 4096];
    loop {
        if let Some(end) = buffer.windows(4).position(|w| w == b"\r\n\r\n") {
            let head = String::from_utf8_lossy(&buffer[..end]).to_string();
            let length: usize =
                head.lines().find_map(|l| l.to_lowercase().strip_prefix("content-length:").map(|v| v.trim().parse().unwrap())).unwrap_or(0);
            if buffer.len() >= end + 4 + length {
                return Some((head, String::from_utf8_lossy(&buffer[end + 4..end + 4 + length]).to_string()));
            }
        }
        match stream.read(&mut chunk) {
            Ok(0) | Err(_) => return None,
            Ok(n) => buffer.extend_from_slice(&chunk[..n]),
        }
    }
}

/// `script(key)` gives (status, delay ms); status 0 cuts the connection without an answer.
fn provider(script: impl Fn(&str) -> (u16, u64) + Send + Sync + 'static) -> Provider {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let url = format!("http://{}", listener.local_addr().unwrap());
    let seen = Arc::new(Mutex::new(Vec::new()));
    let (script, log) = (Arc::new(script), seen.clone());
    std::thread::spawn(move || {
        for stream in listener.incoming() {
            let (script, log, mut stream) = (script.clone(), log.clone(), stream.unwrap());
            std::thread::spawn(move || {
                while let Some((head, body)) = read_http(&mut stream) {
                    let key = head.lines().find_map(|l| l.strip_prefix("authorization: Bearer ")).unwrap_or("").to_owned();
                    log.lock().unwrap().push((key.clone(), body));
                    let (status, delay) = script(&key);
                    std::thread::sleep(Duration::from_millis(delay));
                    if status == 0 {
                        return;
                    }
                    let reply = json!({"results": [{"title": "invented"}], "usage": {"credits": 2}}).to_string();
                    let extra = if status == 429 { "Retry-After: 7\r\n" } else { "" };
                    let text = format!("HTTP/1.1 {status} X\r\n{extra}Content-Length: {}\r\n\r\n{reply}", reply.len());
                    if stream.write_all(text.as_bytes()).is_err() {
                        return;
                    }
                }
            });
        }
    });
    Provider { url, seen }
}

struct Fixture {
    dir: PathBuf,
    port: u16,
    url: String,
    child: Option<Child>,
}

impl Drop for Fixture {
    fn drop(&mut self) {
        if let Some(mut child) = self.child.take() {
            let _ = child.kill();
            let _ = child.wait();
        }
        let _ = std::fs::remove_dir_all(&self.dir);
    }
}

fn snapshot(url: &str, keys: &[(&str, &str)], tokens: &[&str], intake: &str, submit_idempotent: bool) -> Value {
    let keys: Vec<Value> = keys.iter().map(|(k, s)| json!({"id": fp(k), "state": s})).collect();
    let tokens: Vec<Value> = tokens.iter().map(|t| json!({"sha256": sha256(t), "executionId": "exec-1", "connections": ["tavily"]})).collect();
    json!({"schema": 2, "generated_ms": 1,
        "providers": {"tavily": {"baseUrl": url, "tools": {
            "web_search": {"method": "POST", "path": "/search", "unit": "credits", "idempotent": true, "inject": {"include_usage": true}},
            "submit": {"method": "POST", "path": "/submit", "unit": "credits", "idempotent": submit_idempotent}}}},
        "connections": {"tavily": {"provider": "tavily", "tools": ["web_search", "submit"], "intake": intake, "keys": keys, "blocked": []}},
        "tokens": tokens})
}

impl Fixture {
    fn new(url: &str, keys: &[&str]) -> Self {
        let serial = NEXT.fetch_add(1, SeqCst);
        let dir = std::env::temp_dir().join(format!("fleet-gateway-{}-{serial}", std::process::id()));
        std::fs::create_dir_all(dir.join("keys/tavily")).unwrap();
        for k in keys {
            std::fs::write(dir.join("keys/tavily").join(fp(k)), format!("{k}\n")).unwrap();
        }
        // Below the ephemeral range: a freed port :0 could go to a parallel test's listener before the gateway binds it.
        let port = 21000 + (std::process::id() % 9000) as u16 + serial as u16;
        let config = json!({"listen": format!("127.0.0.1:{port}"), "snapshot": dir.join("snapshot.json"), "keys_dir": dir.join("keys"),
                            "state_dir": dir.join("state"), "request_timeout_ms": 1500});
        std::fs::write(dir.join("config.json"), config.to_string()).unwrap();
        let mut fixture = Self { dir, port, url: url.to_owned(), child: None };
        let all: Vec<(&str, &str)> = keys.iter().map(|k| (*k, "active")).collect();
        fixture.write(&snapshot(url, &all, &[TOKEN], "all", false));
        fixture.start();
        fixture
    }

    fn write(&self, v: &Value) {
        std::fs::write(self.dir.join("snapshot.tmp"), v.to_string()).unwrap();
        std::fs::rename(self.dir.join("snapshot.tmp"), self.dir.join("snapshot.json")).unwrap();
    }

    fn start(&mut self) {
        let child = Command::new(env!("CARGO_BIN_EXE_fleet-gateway"))
            .arg("serve")
            .arg(self.dir.join("config.json"))
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .unwrap();
        self.child = Some(child);
        let end = Instant::now() + Duration::from_secs(10);
        while Instant::now() < end {
            if TcpStream::connect(("127.0.0.1", self.port)).is_ok() {
                return;
            }
            std::thread::sleep(Duration::from_millis(10));
        }
        panic!("gateway did not listen");
    }

    fn stop(&mut self) {
        let mut child = self.child.take().unwrap();
        unsafe { libc::kill(child.id() as i32, libc::SIGTERM) };
        child.wait().unwrap();
    }

    fn call(&self, tool: &str, token: &str) -> (u16, String, Value) {
        call_raw(self.port, "POST", &format!("/v1/call/tavily/{tool}"), &[("Authorization", &format!("Bearer {token}"))], r#"{"query":"q","api_key":"mine"}"#)
    }

    fn health(&self) -> Value {
        call_raw(self.port, "GET", "/healthz", &[], "").2
    }

    fn spool(&self) -> Vec<Value> {
        let mut out = Vec::new();
        for entry in std::fs::read_dir(self.dir.join("state/spool")).unwrap() {
            let text = std::fs::read_to_string(entry.unwrap().path()).unwrap();
            out.extend(text.lines().map(|l| serde_json::from_str::<Value>(l).unwrap()));
        }
        out
    }
}

fn call_raw(port: u16, method: &str, path: &str, headers: &[(&str, &str)], body: &str) -> (u16, String, Value) {
    let mut stream = TcpStream::connect(("127.0.0.1", port)).unwrap();
    stream.set_read_timeout(Some(Duration::from_secs(20))).unwrap();
    let extra: String = headers.iter().map(|(k, v)| format!("{k}: {v}\r\n")).collect();
    write!(stream, "{method} {path} HTTP/1.1\r\nHost: x\r\nConnection: close\r\n{extra}Content-Length: {}\r\n\r\n{body}", body.len()).unwrap();
    let mut text = String::new();
    stream.read_to_string(&mut text).unwrap();
    let (head, payload) = text.split_once("\r\n\r\n").unwrap_or((&text, ""));
    let status = head.split_whitespace().nth(1).and_then(|s| s.parse().ok()).unwrap_or(0);
    (status, head.to_owned(), serde_json::from_str(payload).unwrap_or(Value::Null))
}

#[test]
fn a_run_token_call_injects_the_key_and_spools_one_line() {
    let upstream = provider(|_| (200, 0));
    let gw = Fixture::new(&upstream.url, &["invented-key-a"]);
    let (status, head, body) = gw.call("web_search", TOKEN);
    assert_eq!(status, 200);
    assert_eq!(body["usage"]["credits"], 2);
    assert!(head.to_lowercase().contains("x-fleet-call-id:"));
    let (key, sent) = upstream.seen.lock().unwrap()[0].clone();
    assert_eq!(key, "invented-key-a");
    let sent: Value = serde_json::from_str(&sent).unwrap();
    assert_eq!(sent["include_usage"], true);
    assert!(sent.get("api_key").is_none(), "the caller never selects a key");
    let lines = gw.spool();
    assert_eq!(lines.len(), 1);
    assert_eq!(lines[0]["executionId"], "exec-1");
    assert_eq!(lines[0]["outcome"], "ok");
    assert_eq!(lines[0]["keyFingerprint"], fp("invented-key-a"));
    assert_eq!(lines[0]["usage"], json!({"unit": "credits", "amount": 2}));
    assert!(!std::fs::read_to_string(gw.dir.join("state/spool").read_dir().unwrap().next().unwrap().unwrap().path()).unwrap().contains("invented-key-a"));
}

#[test]
fn an_unknown_token_is_refused_and_a_token_minted_after_adoption_works_at_once() {
    let upstream = provider(|_| (200, 0));
    let gw = Fixture::new(&upstream.url, &["invented-key-a"]);
    assert_eq!(gw.call("web_search", "junk").0, 401);
    // Written between two adoption ticks: the reread on an unknown token finds it.
    gw.write(&snapshot(&gw.url, &[("invented-key-a", "active")], &[TOKEN, "fresh-token"], "all", false));
    assert_eq!(gw.call("web_search", "fresh-token").0, 200);
    let lines = gw.spool();
    assert_eq!(lines[0]["errorCode"], "unauthenticated");
    assert!(lines[0]["executionId"].is_null());
}

#[test]
fn a_refused_key_is_disabled_persisted_and_the_next_one_serves() {
    let upstream = provider(|key| if key == "invented-key-a" { (401, 0) } else { (200, 0) });
    let mut gw = Fixture::new(&upstream.url, &["invented-key-a", "invented-key-b"]);
    let (status, _, body) = gw.call("web_search", TOKEN);
    assert_eq!((status, body["error"].as_str()), (502, Some("key-disabled-401")));
    assert_eq!(gw.spool()[0]["disabled"], json!({"key": fp("invented-key-a"), "status": 401}));
    gw.stop();
    gw.start();
    // Persisted across the restart: the call goes straight to key b.
    assert_eq!(gw.call("web_search", TOKEN).0, 200);
    assert_eq!(upstream.seen.lock().unwrap().last().unwrap().0, "invented-key-b");
    assert_eq!(gw.health()["keys"]["usable"], 1);
}

#[test]
fn a_403_is_recorded_on_the_key_and_never_disables_it() {
    let upstream = provider(|_| (403, 0));
    let gw = Fixture::new(&upstream.url, &["invented-key-a"]);
    assert_eq!(gw.call("web_search", TOKEN).0, 403);
    assert_eq!(gw.call("web_search", TOKEN).0, 403);
    assert_eq!(upstream.seen.lock().unwrap().len(), 2, "the key stays in use");
    let lines = gw.spool();
    assert!(lines[0]["disabled"].is_null());
    assert_eq!(lines[0]["keyProblem"]["key"], fp("invented-key-a"));
    assert!(lines[0]["keyProblem"]["message"].as_str().unwrap().contains("invented"));
    assert_eq!(gw.health()["keys"]["usable"], 1);
}

#[test]
fn quota_replies_pass_through_and_draining_keys_take_no_calls() {
    let upstream = provider(|key| if key == "invented-key-b" { (429, 0) } else { (200, 0) });
    let gw = Fixture::new(&upstream.url, &["invented-key-a", "invented-key-b"]);
    gw.write(&snapshot(&gw.url, &[("invented-key-a", "draining"), ("invented-key-b", "active")], &[TOKEN], "all", false));
    std::thread::sleep(Duration::from_millis(1200));
    let (status, head, _) = gw.call("web_search", TOKEN);
    assert_eq!(status, 429);
    assert!(head.contains("retry-after: 7") || head.contains("Retry-After: 7"));
    assert_eq!(upstream.seen.lock().unwrap().len(), 1, "no switch to another key on a quota reply");
}

#[test]
fn no_answer_fails_over_only_for_an_idempotent_tool() {
    let upstream = provider(|key| if key == "invented-key-a" { (0, 0) } else { (200, 0) });
    let gw = Fixture::new(&upstream.url, &["invented-key-a", "invented-key-b"]);
    let (status, _, body) = gw.call("submit", TOKEN);
    assert_eq!((status, body["error"].as_str()), (502, Some("provider-unreachable")));
    assert_eq!(gw.spool()[0]["outcome"], "unknown");
    assert_eq!(upstream.seen.lock().unwrap().len(), 1, "a non-idempotent call is never sent again");
    assert_eq!(gw.call("web_search", TOKEN).0, 200);
}

#[test]
fn an_invalid_snapshot_keeps_the_last_valid_one_and_paused_intake_refuses() {
    let upstream = provider(|_| (200, 0));
    let mut gw = Fixture::new(&upstream.url, &["invented-key-a"]);
    std::fs::write(gw.dir.join("snapshot.json"), "not json").unwrap();
    std::thread::sleep(Duration::from_millis(1200));
    assert_eq!(gw.call("web_search", TOKEN).0, 200);
    assert_eq!(gw.health()["snapshot"]["rejected"], 1);
    gw.stop();
    gw.start(); // starts from the retained copy
    assert_eq!(gw.call("web_search", TOKEN).0, 200);
    gw.write(&snapshot(&gw.url, &[("invented-key-a", "active")], &[TOKEN], "paused", false));
    std::thread::sleep(Duration::from_millis(1200));
    assert_eq!(gw.call("web_search", TOKEN).2["error"], "intake-paused");
    assert_eq!(call_raw(gw.port, "POST", "/v1/call/other/web_search", &[("Authorization", &format!("Bearer {TOKEN}"))], "{}").2["error"], "not-granted");
    assert!(Path::new(&gw.dir.join("state/snapshot.good.json")).exists());
}
