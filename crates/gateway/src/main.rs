//! fleet-gateway: the only process that uses API key values at run time (docs/designs/fleet-connections.md §3).
//! Copied from Pipe's read lane (assetq `lane/`): loopback socket, snapshot adoption every second with a retained last-valid
//! copy, disabled-key persistence, provider error mapping and a lost counter. Changed: one generic route over the provider
//! catalogue in the snapshot, per-run token auth, no fixed queue bound, a spool file instead of the coreq telemetry store.
use bytes::Bytes;
use http_body_util::{BodyExt, Full, Limited};
use hyper::body::Incoming;
use hyper::header::{AUTHORIZATION, CONTENT_TYPE, HeaderValue, RETRY_AFTER};
use hyper::service::service_fn;
use hyper::{Method, Request, Response, StatusCode};
use hyper_rustls::HttpsConnector;
use hyper_util::client::legacy::{Client, connect::HttpConnector};
use hyper_util::rt::{TokioExecutor, TokioIo};
use hyper_util::server::graceful::GracefulShutdown;
use serde_json::{Map, Value, json};
use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::io::Write;
use std::os::fd::FromRawFd;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering::SeqCst};
use std::sync::{Arc, Mutex, RwLock};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};
use tokio::net::TcpListener;
use tokio::signal::unix::{SignalKind, signal};

const VERSION: &str = env!("CARGO_PKG_VERSION");
const REQUEST_BYTES: usize = 1 << 20;
const REFUSED_BYTES: usize = 64 << 10;
const REFUSED_PATIENCE: Duration = Duration::from_secs(2);
const PROVIDER_BYTES: usize = 16 << 20;
const SYSTEMD_FIRST_FD: i32 = 3;

type Body = Full<Bytes>;
type Res<T> = Result<T, String>;

fn now_ms() -> u64 {
    SystemTime::now().duration_since(UNIX_EPOCH).map_or(0, |d| d.as_millis() as u64)
}

fn sha256(text: &str) -> String {
    Sha256::digest(text.as_bytes()).iter().map(|b| format!("{b:02x}")).collect()
}

fn is_name(text: &str) -> bool {
    !text.is_empty() && text.len() <= 64 && text.bytes().all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == b'-' || c == b'_')
}

fn write_atomic(path: &Path, bytes: &[u8]) -> std::io::Result<()> {
    let temporary = path.with_extension("tmp");
    std::fs::write(&temporary, bytes)?;
    std::fs::rename(&temporary, path)
}

/// Days since the epoch as YYYY-MM-DD (civil-from-days, UTC), for the spool file name.
fn day(ms: u64) -> String {
    let z = (ms / 86_400_000) as i64 + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    format!("{:04}-{:02}-{:02}", yoe + era * 400 + i64::from(m <= 2), m, d)
}

// ---------------------------------------------------------------- configuration

struct Config {
    listen: Option<String>,
    snapshot: PathBuf,
    keys_dir: PathBuf,
    state_dir: PathBuf,
    request_timeout: Duration,
    drain_timeout: Duration,
}

impl Config {
    fn load(path: &str) -> Res<Self> {
        let text = std::fs::read_to_string(path).map_err(|e| format!("config {path}: {e}"))?;
        let v: Value = serde_json::from_str(&text).map_err(|e| format!("config {path}: {e}"))?;
        let text_of = |name: &str| v[name].as_str().map(str::to_owned).ok_or(format!("config {name} is required"));
        let number = |name: &str, default: u64| v[name].as_u64().unwrap_or(default);
        Ok(Self {
            listen: v["listen"].as_str().map(str::to_owned),
            snapshot: text_of("snapshot")?.into(),
            keys_dir: text_of("keys_dir")?.into(),
            state_dir: text_of("state_dir")?.into(),
            request_timeout: Duration::from_millis(number("request_timeout_ms", 30_000)),
            drain_timeout: Duration::from_millis(number("drain_timeout_ms", 35_000)),
        })
    }
}

// ---------------------------------------------------------------- snapshot

struct Tool {
    method: Method,
    path: String,
    idempotent: bool,
    unit: String,
    inject: Map<String, Value>,
}

struct Provider {
    base_url: String,
    tools: HashMap<String, Tool>,
}

struct Key {
    id: String,
    state: String,
    enabled_ms: u64,
}

struct Connection {
    provider: String,
    tools: Vec<String>,
    intake: String,
    keys: Vec<Key>,
}

struct Token {
    execution_id: String,
    connections: Vec<String>,
}

struct Snapshot {
    revision: String,
    generated_ms: u64,
    providers: HashMap<String, Provider>,
    connections: HashMap<String, Connection>,
    tokens: HashMap<String, Token>,
}

fn text(v: &Value, what: &str) -> Res<String> {
    v.as_str().map(str::to_owned).ok_or(format!("{what} is missing"))
}

impl Snapshot {
    fn parse(bytes: &[u8]) -> Res<Self> {
        let v: Value = serde_json::from_slice(bytes).map_err(|e| format!("not JSON: {e}"))?;
        if v["schema"] != 2 {
            return Err("unsupported schema".into());
        }
        let generated_ms = v["generated_ms"].as_u64().ok_or("generated_ms is missing")?;
        let mut providers = HashMap::new();
        for (id, p) in v["providers"].as_object().ok_or("providers is missing")? {
            let mut tools = HashMap::new();
            for (name, t) in p["tools"].as_object().ok_or("provider tools are missing")? {
                let method = if t["method"] == "GET" { Method::GET } else { Method::POST };
                let path = text(&t["path"], "tool path")?;
                if !path.starts_with('/') {
                    return Err("tool path must start with /".into());
                }
                let inject = t["inject"].as_object().cloned().unwrap_or_default();
                let unit = t["unit"].as_str().unwrap_or("calls").to_owned();
                tools.insert(name.clone(), Tool { method, path, idempotent: t["idempotent"] == true, unit, inject });
            }
            let base_url = text(&p["baseUrl"], "provider baseUrl")?.trim_end_matches('/').to_owned();
            providers.insert(id.clone(), Provider { base_url, tools });
        }
        let mut connections = HashMap::new();
        for (name, c) in v["connections"].as_object().ok_or("connections is missing")? {
            if !is_name(name) {
                return Err("connection name is invalid".into());
            }
            let mut keys = Vec::new();
            for k in c["keys"].as_array().ok_or("connection keys are missing")? {
                let id = text(&k["id"], "key id")?;
                if id.len() != 12 || !id.bytes().all(|b| b.is_ascii_hexdigit()) {
                    return Err("key id is not a fingerprint".into());
                }
                keys.push(Key { id, state: text(&k["state"], "key state")?, enabled_ms: k["enabledAt"].as_u64().unwrap_or(0) });
            }
            let tools = c["tools"].as_array().into_iter().flatten().filter_map(Value::as_str).map(str::to_owned).collect();
            let provider = text(&c["provider"], "connection provider")?;
            if !providers.contains_key(&provider) {
                return Err(format!("connection {name} names an unknown provider"));
            }
            connections.insert(name.clone(), Connection { provider, tools, intake: c["intake"].as_str().unwrap_or("all").to_owned(), keys });
        }
        let mut tokens = HashMap::new();
        for t in v["tokens"].as_array().ok_or("tokens is missing")? {
            let hash = text(&t["sha256"], "token hash")?;
            if hash.len() != 64 || !hash.bytes().all(|c| c.is_ascii_hexdigit()) {
                return Err("token hash is not SHA-256".into());
            }
            let connections = t["connections"].as_array().into_iter().flatten().filter_map(Value::as_str).map(str::to_owned).collect();
            tokens.insert(hash, Token { execution_id: text(&t["executionId"], "token executionId")?, connections });
        }
        let revision = Sha256::digest(bytes).iter().take(6).map(|b| format!("{b:02x}")).collect();
        Ok(Self { revision, generated_ms, providers, connections, tokens })
    }
}

/// The coordinator's file when valid, else this gateway's own retained copy of the last valid one.
fn load_snapshot(config: &Config) -> Res<Snapshot> {
    let good = config.state_dir.join("snapshot.good.json");
    match std::fs::read(&config.snapshot).map_err(|e| e.to_string()).and_then(|b| Snapshot::parse(&b).map(|s| (b, s))) {
        Ok((bytes, snapshot)) => {
            write_atomic(&good, &bytes).map_err(|e| format!("retain snapshot: {e}"))?;
            Ok(snapshot)
        }
        Err(error) => {
            let bytes = std::fs::read(&good).map_err(|e| format!("no usable snapshot: coordinator: {error}; retained: {e}"))?;
            eprintln!("fleet-gateway: snapshot refused ({error}); serving the retained copy");
            Snapshot::parse(&bytes).map_err(|e| format!("no usable snapshot: coordinator: {error}; retained: {e}"))
        }
    }
}

// ---------------------------------------------------------------- spool

/// One JSON line per call in <state>/spool/<day>.jsonl; a line that cannot be written counts in the persisted lost counter.
struct Spool {
    dir: PathBuf,
    lost: AtomicU64,
    write: Mutex<()>,
}

impl Spool {
    fn open(state_dir: &Path) -> Res<Self> {
        let dir = state_dir.join("spool");
        std::fs::create_dir_all(&dir).map_err(|e| format!("spool directory: {e}"))?;
        let lost = std::fs::read_to_string(state_dir.join("spool-lost")).ok().and_then(|t| t.trim().parse().ok()).unwrap_or(0);
        Ok(Self { dir, lost: AtomicU64::new(lost), write: Mutex::new(()) })
    }

    fn append(&self, line: &Value) {
        let _one = self.write.lock().unwrap();
        let path = self.dir.join(format!("{}.jsonl", day(now_ms())));
        let written = std::fs::OpenOptions::new()
            .create(true)
            .append(true)
            .open(&path)
            .and_then(|mut f| f.write_all(format!("{line}\n").as_bytes()));
        if let Err(error) = written {
            let lost = self.lost.fetch_add(1, SeqCst) + 1;
            eprintln!("fleet-gateway: spool write failed ({error}); lost {lost}");
            let _ = write_atomic(&self.dir.with_file_name("spool-lost"), lost.to_string().as_bytes());
        }
    }
}

// ---------------------------------------------------------------- provider call

/// The outcome of one call, ready for the caller and for the spool.
struct Answer {
    status: StatusCode,
    body: Bytes,
    retry_after: Option<HeaderValue>,
    outcome: &'static str,
    error: Option<String>,
    key: Option<String>,
    usage: Option<u64>,
    disabled: Option<(String, u16)>,
}

fn refusal(status: StatusCode, outcome: &'static str, error: &str, reason: &str) -> Answer {
    Answer {
        status,
        body: json!({"error": error, "reason": reason}).to_string().into(),
        retry_after: None,
        outcome,
        error: Some(error.to_owned()),
        key: None,
        usage: None,
        disabled: None,
    }
}

// ---------------------------------------------------------------- service

struct Gateway {
    config: Config,
    client: Client<HttpsConnector<HttpConnector>, Body>,
    snapshot: RwLock<Arc<Snapshot>>,
    seen: Mutex<Option<SystemTime>>,
    reread: tokio::sync::Mutex<()>,
    rejected_snapshots: AtomicU64,
    /// Key values by fingerprint, read from <keys_dir>/<connection>/<fingerprint> when a snapshot names them.
    values: RwLock<HashMap<String, String>>,
    /// Keys the provider refused here: fingerprint → (HTTP status, when). Persisted in <state>/disabled-keys.
    disabled: Mutex<HashMap<String, (u16, u64)>>,
    current: Mutex<HashMap<String, usize>>,
    spool: Spool,
    draining: AtomicBool,
    started: Instant,
    calls: AtomicU64,
}

struct Caller {
    token: Option<String>,
    execution_id: Option<String>,
    connections: Vec<String>,
}

fn reply(status: StatusCode, body: impl Into<Bytes>) -> Response<Body> {
    Response::builder().status(status).header(CONTENT_TYPE, "application/json").body(Full::new(body.into())).expect("static response shape")
}

impl Gateway {
    fn load_values(&self, snapshot: &Snapshot) {
        let mut values = self.values.write().unwrap();
        for (name, connection) in &snapshot.connections {
            for key in &connection.keys {
                if values.contains_key(&key.id) {
                    continue;
                }
                match std::fs::read_to_string(self.config.keys_dir.join(name).join(&key.id)) {
                    // A file whose value does not match its fingerprint is never used under that name.
                    Ok(v) if sha256(v.trim())[..12] == key.id => {
                        values.insert(key.id.clone(), v.trim().to_owned());
                    }
                    Ok(_) => eprintln!("fleet-gateway: key file {name}/{} does not match its fingerprint", key.id),
                    Err(e) => eprintln!("fleet-gateway: key {name}/{}: {e}", key.id),
                }
            }
        }
    }

    /// Adopt a newer valid snapshot; refuse an invalid one and keep serving the current one.
    fn refresh_snapshot(&self, force: bool) {
        let Ok(modified) = std::fs::metadata(&self.config.snapshot).and_then(|m| m.modified()) else { return };
        {
            let mut seen = self.seen.lock().unwrap();
            if !force && *seen == Some(modified) {
                return;
            }
            *seen = Some(modified);
        }
        let good = self.config.state_dir.join("snapshot.good.json");
        match std::fs::read(&self.config.snapshot).map_err(|e| e.to_string()).and_then(|b| Snapshot::parse(&b).map(|s| (b, s))) {
            Ok((bytes, snapshot)) => {
                if let Err(error) = write_atomic(&good, &bytes) {
                    eprintln!("fleet-gateway: retain snapshot: {error}");
                }
                self.load_values(&snapshot);
                *self.snapshot.write().unwrap() = Arc::new(snapshot);
            }
            Err(error) => {
                self.rejected_snapshots.fetch_add(1, SeqCst);
                eprintln!("fleet-gateway: snapshot refused, keeping the last good one: {error}");
            }
        }
    }

    /// The token's run, else one more read of the snapshot (a token minted after the last adoption), else none.
    /// Rereads are coalesced: a caller that finds one running waits for it instead of starting another.
    async fn authenticate(&self, token: &str) -> Option<(String, Vec<String>)> {
        let hash = sha256(token);
        let find = |s: &Snapshot| s.tokens.get(&hash).map(|t| (t.execution_id.clone(), t.connections.clone()));
        if let Some(found) = find(&self.snapshot.read().unwrap()) {
            return Some(found);
        }
        match self.reread.try_lock() {
            Ok(_one) => self.refresh_snapshot(true),
            Err(_) => drop(self.reread.lock().await),
        }
        find(&self.snapshot.read().unwrap())
    }

    fn usable(&self, connection: &Connection) -> Vec<String> {
        let disabled = self.disabled.lock().unwrap();
        connection
            .keys
            .iter()
            .filter(|k| k.state == "active" && disabled.get(&k.id).is_none_or(|(_, at)| k.enabled_ms > *at))
            .map(|k| k.id.clone())
            .collect()
    }

    async fn call(&self, name: &str, connection: &Connection, provider: &Provider, tool: &Tool, body: Bytes) -> Answer {
        let keys = self.usable(connection);
        if keys.is_empty() {
            return refusal(StatusCode::SERVICE_UNAVAILABLE, "error", "no-usable-key", "no active key; the holder or the owner must act");
        }
        let start = *self.current.lock().unwrap().get(name).unwrap_or(&0);
        let mut last_failure = String::new();
        for step in 0..keys.len() {
            let index = (start + step) % keys.len();
            let id = &keys[index];
            let Some(value) = self.values.read().unwrap().get(id).cloned() else {
                last_failure = format!("key {id} has no value file");
                continue;
            };
            let request = Request::builder()
                .method(tool.method.clone())
                .uri(format!("{}{}", provider.base_url, tool.path))
                .header(AUTHORIZATION, format!("Bearer {value}"))
                .header(CONTENT_TYPE, "application/json")
                .body(Full::new(body.clone()))
                .expect("static request shape");
            let sent = tokio::time::timeout(self.config.request_timeout, self.client.request(request)).await;
            let response = match sent {
                Err(_) => {
                    let mut answer = refusal(StatusCode::GATEWAY_TIMEOUT, "unknown", "provider-timeout", "the provider did not answer in time");
                    answer.key = Some(id.clone());
                    return answer;
                }
                // No HTTP answer on this key: try the next one only when sending again cannot charge twice.
                Ok(Err(error)) => {
                    last_failure = if error.is_connect() { "connect".into() } else { "transport".into() };
                    if !tool.idempotent {
                        let mut answer =
                            refusal(StatusCode::BAD_GATEWAY, "unknown", "provider-unreachable", "no answer; the call is not sent again");
                        answer.key = Some(id.clone());
                        return answer;
                    }
                    self.current.lock().unwrap().insert(name.to_owned(), (index + 1) % keys.len());
                    continue;
                }
                Ok(Ok(response)) => response,
            };
            let status = response.status();
            let retry_after = response.headers().get(RETRY_AFTER).cloned();
            let read = tokio::time::timeout(self.config.request_timeout, Limited::new(response.into_body(), PROVIDER_BYTES).collect()).await;
            let Ok(Ok(collected)) = read else {
                let mut answer = refusal(StatusCode::BAD_GATEWAY, "unknown", "provider-body", "the provider reply was cut off, too large or too slow");
                answer.key = Some(id.clone());
                return answer;
            };
            let bytes = collected.to_bytes();
            if matches!(status.as_u16(), 401 | 402 | 403) {
                let reason = format!("the provider refused key {id} with HTTP {}; the key is disabled", status.as_u16());
                let mut answer = refusal(StatusCode::BAD_GATEWAY, "error", &format!("key-disabled-{}", status.as_u16()), &reason);
                answer.key = Some(id.clone());
                answer.disabled = Some((id.clone(), status.as_u16()));
                return answer;
            }
            // Success and every other provider reply, quota and 429 included, reach the caller unchanged.
            let usage = serde_json::from_slice::<Value>(&bytes).ok().and_then(|v| v["usage"][&tool.unit].as_u64());
            let (outcome, error) =
                if status.is_success() { ("ok", None) } else { ("error", Some(format!("provider-{}", status.as_u16()))) };
            return Answer { status, body: bytes, retry_after, outcome, error, key: Some(id.clone()), usage, disabled: None };
        }
        refusal(StatusCode::BAD_GATEWAY, "error", "provider-unreachable", &format!("no key reached the provider; last failure: {last_failure}"))
    }

    fn on_disabled(&self, id: &str, status: u16) {
        let mut disabled = self.disabled.lock().unwrap();
        disabled.insert(id.to_owned(), (status, now_ms()));
        let text: String = disabled.iter().map(|(k, (s, at))| format!("{k} {s} {at}\n")).collect();
        if let Err(error) = write_atomic(&self.config.state_dir.join("disabled-keys"), text.as_bytes()) {
            eprintln!("fleet-gateway: disabled key {id} not persisted: {error}");
        }
        eprintln!("fleet-gateway: key {id} disabled after HTTP {status}");
    }

    fn health(&self) -> Value {
        let snapshot = self.snapshot.read().unwrap().clone();
        let keys: usize = snapshot.connections.values().map(|c| c.keys.len()).sum();
        let usable: usize = snapshot.connections.values().map(|c| self.usable(c).len()).sum();
        json!({
            "status": if self.draining.load(SeqCst) { "draining" } else { "ok" }, "version": VERSION, "uptime_s": self.started.elapsed().as_secs(),
            "snapshot": {"revision": snapshot.revision, "age_s": now_ms().saturating_sub(snapshot.generated_ms) / 1000,
                         "tokens": snapshot.tokens.len(), "rejected": self.rejected_snapshots.load(SeqCst)},
            "keys": {"total": keys, "usable": usable, "disabled": self.disabled.lock().unwrap().len()},
            "spool": {"lost": self.spool.lost.load(SeqCst)},
        })
    }

    async fn handle(self: Arc<Self>, request: Request<Incoming>) -> Result<Response<Body>, std::convert::Infallible> {
        let path = request.uri().path().to_owned();
        if request.method() == Method::GET && path == "/healthz" {
            return Ok(reply(StatusCode::OK, self.health().to_string()));
        }
        let route = path.strip_prefix("/v1/call/").and_then(|r| r.split_once('/')).filter(|(c, t)| is_name(c) && is_name(t));
        let (Some((connection_name, tool_name)), &Method::POST) = (route, request.method()) else {
            return Ok(reply(StatusCode::NOT_FOUND, json!({"error": "not-found", "reason": "unknown route"}).to_string()));
        };
        let (connection_name, tool_name) = (connection_name.to_owned(), tool_name.to_owned());
        let (started_ms, begun) = (now_ms(), Instant::now());
        let token = request.headers().get(AUTHORIZATION).and_then(|v| v.to_str().ok()).and_then(|v| v.strip_prefix("Bearer ")).map(|t| t.trim().to_owned());
        let run = match &token {
            Some(t) => self.authenticate(t).await,
            None => None,
        };
        let caller = Caller {
            token: run.as_ref().map(|_| sha256(token.as_deref().unwrap_or(""))),
            execution_id: run.as_ref().map(|r| r.0.clone()),
            connections: run.map(|r| r.1).unwrap_or_default(),
        };
        // Read the body before any refusal: closing on unread bytes resets the connection and loses the caller's reason.
        // Unauthenticated callers get a small, short read, so a slow or large body cannot hold the gateway.
        let (limit, patience) =
            if caller.token.is_some() { (REQUEST_BYTES, self.config.request_timeout) } else { (REFUSED_BYTES, REFUSED_PATIENCE) };
        let collected = tokio::time::timeout(patience, Limited::new(request.into_body(), limit).collect()).await;
        let snapshot = self.snapshot.read().unwrap().clone();
        let found = snapshot.connections.get(&connection_name).and_then(|c| {
            let p = snapshot.providers.get(&c.provider)?;
            Some((c, p, p.tools.get(&tool_name).filter(|_| c.tools.contains(&tool_name))?))
        });
        let answer = if caller.token.is_none() {
            refusal(StatusCode::UNAUTHORIZED, "error", "unauthenticated", "a valid run token is required")
        } else if !caller.connections.contains(&connection_name) {
            refusal(StatusCode::FORBIDDEN, "error", "not-granted", "this run is not granted the connection")
        } else if let Some((connection, provider, tool)) = found {
            if connection.intake != "all" {
                refusal(StatusCode::FORBIDDEN, "error", "intake-paused", "the connection's intake is paused")
            } else {
                match collected.map(|read| read.map(|b| serde_json::from_slice::<Value>(&b.to_bytes()))) {
                    Ok(Ok(Ok(Value::Object(mut body)))) => {
                        // The caller never selects a key; catalogue fields (Tavily include_usage) are added.
                        body.remove("api_key");
                        for (k, v) in &tool.inject {
                            body.insert(k.clone(), v.clone());
                        }
                        self.call(&connection_name, connection, provider, tool, Value::Object(body).to_string().into()).await
                    }
                    _ => refusal(StatusCode::BAD_REQUEST, "error", "bad-request", "the body must be a JSON object of at most 1 MiB"),
                }
            }
        } else {
            refusal(StatusCode::NOT_FOUND, "error", "unknown-tool", "the connection has no such tool")
        };
        if let Some((id, status)) = &answer.disabled {
            self.on_disabled(id, *status);
        }
        let call_id = format!("{started_ms:x}-{:x}-{:x}", std::process::id(), self.calls.fetch_add(1, SeqCst));
        let unit = found.map(|f| f.2.unit.clone());
        self.spool.append(&json!({
            "callId": call_id, "executionId": caller.execution_id, "tokenSha256": caller.token, "connection": connection_name,
            "tool": tool_name, "keyFingerprint": answer.key, "status": answer.status.as_u16(), "errorCode": answer.error,
            "outcome": answer.outcome, "usage": answer.usage.map(|amount| json!({"unit": unit, "amount": amount})),
            "disabled": answer.disabled.as_ref().map(|(k, s)| json!({"key": k, "status": s})),
            "startedAt": started_ms, "ms": begun.elapsed().as_millis() as u64,
        }));
        let mut response = reply(answer.status, answer.body);
        response.headers_mut().insert("x-fleet-call-id", HeaderValue::from_str(&call_id).expect("hex call id"));
        if let Some(error) = answer.error.as_deref().and_then(|e| HeaderValue::from_str(e).ok()) {
            response.headers_mut().insert("x-fleet-error", error);
        }
        if let Some(value) = answer.retry_after {
            response.headers_mut().insert(RETRY_AFTER, value);
        }
        Ok(response)
    }
}

/// A socket-activated listener keeps the port open while a restart drains; `listen` serves tests and manual runs.
fn listener(config: &Config) -> Res<std::net::TcpListener> {
    let activated = std::env::var("LISTEN_PID").ok().and_then(|p| p.parse::<u32>().ok()) == Some(std::process::id())
        && std::env::var("LISTEN_FDS").ok().as_deref() == Some("1");
    let listener = if activated {
        // SAFETY: systemd passes the one socket at this descriptor and nothing else owns it.
        unsafe { std::net::TcpListener::from_raw_fd(SYSTEMD_FIRST_FD) }
    } else {
        let address = config.listen.as_deref().ok_or("no activated socket and no listen address")?;
        std::net::TcpListener::bind(address).map_err(|e| format!("bind {address}: {e}"))?
    };
    let address = listener.local_addr().map_err(|e| e.to_string())?;
    if !address.ip().is_loopback() {
        return Err(format!("the gateway binds only to loopback, not {address}"));
    }
    listener.set_nonblocking(true).map_err(|e| e.to_string())?;
    Ok(listener)
}

async fn serve(config: Config) -> Res<()> {
    std::fs::create_dir_all(&config.state_dir).map_err(|e| format!("state directory: {e}"))?;
    let disabled: HashMap<String, (u16, u64)> = std::fs::read_to_string(config.state_dir.join("disabled-keys"))
        .unwrap_or_default()
        .lines()
        .filter_map(|l| {
            let mut parts = l.split(' ');
            Some((parts.next()?.to_owned(), (parts.next()?.parse().ok()?, parts.next()?.parse().ok()?)))
        })
        .collect();
    let snapshot = load_snapshot(&config)?;
    let spool = Spool::open(&config.state_dir)?;
    let listener = TcpListener::from_std(listener(&config)?).map_err(|e| e.to_string())?;
    let connector = hyper_rustls::HttpsConnectorBuilder::new().with_webpki_roots().https_or_http().enable_http1().build();
    let client = Client::builder(TokioExecutor::new()).pool_idle_timeout(Duration::from_secs(90)).build(connector);
    let seen = std::fs::metadata(&config.snapshot).and_then(|m| m.modified()).ok();
    let gateway = Arc::new(Gateway {
        config,
        client,
        snapshot: RwLock::new(Arc::new(Snapshot { revision: String::new(), generated_ms: 0, providers: HashMap::new(), connections: HashMap::new(), tokens: HashMap::new() })),
        seen: Mutex::new(seen),
        reread: tokio::sync::Mutex::new(()),
        rejected_snapshots: AtomicU64::new(0),
        values: RwLock::new(HashMap::new()),
        disabled: Mutex::new(disabled),
        current: Mutex::new(HashMap::new()),
        spool,
        draining: AtomicBool::new(false),
        started: Instant::now(),
        calls: AtomicU64::new(0),
    });
    gateway.load_values(&snapshot);
    *gateway.snapshot.write().unwrap() = Arc::new(snapshot);
    let watcher = gateway.clone();
    tokio::spawn(async move {
        let mut tick = tokio::time::interval(Duration::from_secs(1));
        loop {
            tick.tick().await;
            watcher.refresh_snapshot(false);
        }
    });
    let mut terminate = signal(SignalKind::terminate()).map_err(|e| e.to_string())?;
    let graceful = GracefulShutdown::new();
    println!("fleet-gateway: serving on {}", listener.local_addr().map_err(|e| e.to_string())?);
    loop {
        tokio::select! {
            accepted = listener.accept() => {
                let Ok((stream, _)) = accepted else { continue };
                let gateway = gateway.clone();
                let connection = hyper::server::conn::http1::Builder::new()
                    .serve_connection(TokioIo::new(stream), service_fn(move |request| gateway.clone().handle(request)));
                let watched = graceful.watch(connection);
                tokio::spawn(async move { let _ = watched.await; });
            }
            _ = terminate.recv() => break,
        }
    }
    gateway.draining.store(true, SeqCst);
    drop(listener);
    if tokio::time::timeout(gateway.config.drain_timeout, graceful.shutdown()).await.is_err() {
        eprintln!("fleet-gateway: drain timed out; remaining calls are cut");
    }
    println!("fleet-gateway: drained, lost spool lines: {}", gateway.spool.lost.load(SeqCst));
    Ok(())
}

fn main() {
    let arguments: Vec<String> = std::env::args().skip(1).collect();
    let run = || -> Res<()> {
        if arguments == ["selftest"] {
            let _ = hyper_rustls::HttpsConnectorBuilder::new().with_webpki_roots().https_only().enable_http1().build();
            Snapshot::parse(br#"{"schema":2,"generated_ms":1,"providers":{},"connections":{},"tokens":[]}"#)?;
            assert_eq!(day(0), "1970-01-01");
            assert_eq!(day(1_762_732_800_000), "2025-11-10");
            println!("fleet-gateway {VERSION}: selftest ok");
            return Ok(());
        }
        let [command, path] = arguments.as_slice() else {
            return Err("usage: fleet-gateway selftest | serve <config.json>".into());
        };
        let config = Config::load(path)?;
        let runtime = tokio::runtime::Runtime::new().map_err(|e| e.to_string())?;
        match command.as_str() {
            "serve" => runtime.block_on(serve(config)),
            _ => Err("unknown command".into()),
        }
    };
    if let Err(error) = run() {
        eprintln!("fleet-gateway: {error}");
        std::process::exit(2);
    }
}
