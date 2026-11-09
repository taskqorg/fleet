// Outbox delivery: rows are queued by the command loop and flushed on a separate thread,
// so polling and accepting commands never waits on coordinator latency.
use rusqlite::{Connection, params};
use serde_json::{Value, json};
use std::time::Duration;

type Result<T> = std::result::Result<T, Box<dyn std::error::Error>>;

pub fn schema(db: &Connection) -> rusqlite::Result<()> {
    db.execute_batch("CREATE TABLE IF NOT EXISTS outbox(route TEXT NOT NULL,body TEXT NOT NULL); CREATE TABLE IF NOT EXISTS outbox_failed(route TEXT NOT NULL,body TEXT NOT NULL,status INTEGER NOT NULL,reason TEXT NOT NULL,failed_at INTEGER NOT NULL);")
}

/// One classification of a coordinator answer, shared by the outbox and the transfer client so the two cannot drift.
/// `Edge` is a condition of the host's edge, never a bad row: a redirect (an Access login page for a path without a bypass)
/// or a 2xx that is not JSON (a login page served inline). The row stays queued and is retried with backoff. The body is
/// drained so the pooled connection is reused.
pub enum Answer { Ok(Value), Edge(u16, String), Reject(u16, String), Retry(String) }
pub fn classify(r: std::result::Result<ureq::Response, ureq::Error>) -> Answer {
    match r {
        Ok(r) if (300..400).contains(&r.status()) => {
            let status = r.status();
            let to = location(&r);
            let _ = r.into_string();
            Answer::Edge(status, to)
        }
        Ok(r) => {
            let status = r.status();
            let text = r.into_string().unwrap_or_default();
            serde_json::from_str::<Value>(&text).map_or_else(|_| Answer::Edge(status, "an answer that is not JSON".into()), Answer::Ok)
        }
        Err(ureq::Error::Status(code, r)) => {
            let text = r.into_string().unwrap_or_default();
            if code >= 500 || code == 429 { Answer::Retry(format!("{code} {text}")) } else { Answer::Reject(code, text) }
        }
        Err(e) => Answer::Retry(e.to_string()),
    }
}
/// A redirect's Location without its query or fragment: an edge login URL can carry parameters.
pub fn location(r: &ureq::Response) -> String {
    r.header("Location").unwrap_or("").split(['?', '#']).next().unwrap_or("").to_string()
}
/// The poll's edge answer, as an error the exchange loop recognizes.
#[derive(Debug)]
pub struct Redirected(pub u16, pub String);
impl std::fmt::Display for Redirected {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result { write!(f, "coordinator edge answered {} ({})", self.0, self.1) }
}
impl std::error::Error for Redirected {}

/// Edge conditions in progress, by route (a coordinator route, or `store <host>`): one log line when a route starts and one
/// when it recovers; a host problem `edge-redirect` reported with every poll until then; and a backoff (2 s doubling to
/// 5 min, as for transfers) before the route is asked again.
struct Edge { status: u16, detail: String, attempts: u32, next_at: i64 }
static EDGE: std::sync::Mutex<std::collections::BTreeMap<String, Edge>> = std::sync::Mutex::new(std::collections::BTreeMap::new());
fn now_ms() -> i64 { std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map_or(0, |d| d.as_millis() as i64) }
pub fn edge_redirect(route: &str, status: u16, detail: &str) {
    let mut m = EDGE.lock().unwrap_or_else(|e| e.into_inner());
    let attempts = m.get(route).map_or(0, |e| e.attempts + 1);
    if attempts == 0 { eprintln!("executor: the edge answers {route} with {status} ({detail}); rows stay queued and are retried") }
    let wait = 2000i64.saturating_mul(1 << attempts.min(8)).min(300_000);
    m.insert(route.to_string(), Edge { status, detail: detail.to_string(), attempts, next_at: now_ms() + wait });
}
pub fn edge_ok(route: &str) {
    let mut m = EDGE.lock().unwrap_or_else(|e| e.into_inner());
    if m.remove(route).is_some() { eprintln!("executor: the edge delivers {route} again") }
}
/// Clears the routes under `prefix` once nothing is left to send there (an idle transfer route is not asked again).
pub fn edge_clear(prefix: &str) {
    let mut m = EDGE.lock().unwrap_or_else(|e| e.into_inner());
    m.retain(|r, _| if r.starts_with(prefix) { eprintln!("executor: nothing left for {r}; edge signal cleared"); false } else { true });
}
/// True while a route waits for its backoff.
pub fn edge_wait(route: &str) -> bool {
    EDGE.lock().unwrap_or_else(|e| e.into_inner()).get(route).is_some_and(|e| now_ms() < e.next_at)
}
/// The host problems for a poll; the poll's own route is left out (a poll that is delivered proves it works).
pub fn edge_problems(except: &str) -> Value {
    let m = EDGE.lock().unwrap_or_else(|e| e.into_inner());
    Value::Array(m.iter().filter(|(r, _)| r.as_str() != except).map(|(route, e)| json!({"kind":"edge-redirect","route":route,"status":e.status,"location":e.detail})).collect())
}

/// One coordinator request on the pooled agent (keep-alive avoids a TLS handshake per row; redirects are not followed).
pub fn send(url: &str, token: &str, route: &str, v: Value) -> Answer {
    static AGENT: std::sync::OnceLock<ureq::Agent> = std::sync::OnceLock::new();
    let a = classify(AGENT
        .get_or_init(|| ureq::AgentBuilder::new().timeout(Duration::from_secs(5)).redirects(0).build())
        .post(&format!("{url}{route}"))
        .set("Authorization", &format!("Bearer {token}"))
        .send_json(v));
    match &a { Answer::Edge(s, d) => edge_redirect(route, *s, d), Answer::Retry(_) => {}, _ => edge_ok(route) }
    a
}
pub fn post(url: &str, token: &str, route: &str, v: Value) -> Result<Value> {
    match send(url, token, route, v) {
        Answer::Ok(v) => Ok(v),
        Answer::Edge(s, d) => Err(Box::new(Redirected(s, d))),
        Answer::Reject(code, text) => Err(format!("coordinator {code} {text}").into()),
        Answer::Retry(e) => Err(e.into()),
    }
}

pub fn queue(db: &Connection, route: &str, v: Value) -> Result<()> {
    db.execute("INSERT INTO outbox(route,body) VALUES(?1,?2)", params![route, v.to_string()])?;
    Ok(())
}

/// Delivers queued rows in order. A 4xx row is moved to `outbox_failed` and reported as an executor event; the following
/// rows keep flowing. A row with an edge answer stays queued and holds, for this flush, its route and its execution: rows of
/// one route keep their order, and rows of one execution keep theirs across routes (an agent's required declaration before
/// its completion receipt); other routes and executions keep flowing. A held route is asked again only after its backoff.
/// 5xx and network errors stop the flush and retry later.
pub fn flush(db: &Connection, url: &str, token: &str, host: &str) -> Result<()> {
    let mut stmt = db.prepare("SELECT rowid,route,body FROM outbox ORDER BY rowid")?;
    let rows: Vec<(i64, String, String)> = stmt
        .query_map([], |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)))?
        .collect::<std::result::Result<_, _>>()?;
    drop(stmt);
    let mut i = 0;
    let mut single = false;
    let (mut routes, mut executions) = (std::collections::HashSet::new(), std::collections::HashSet::new());
    while i < rows.len() {
        let (n, p, b) = rows[i].clone();
        let mut value: Value = serde_json::from_str(&b)?;
        let execution = value["executionId"].as_str().map(str::to_owned);
        let hold = |routes: &mut std::collections::HashSet<String>, executions: &mut std::collections::HashSet<String>| { routes.insert(p.clone()); if let Some(e) = &execution { executions.insert(e.clone()); } };
        if routes.contains(&p) || execution.as_ref().is_some_and(|e| executions.contains(e)) || edge_wait(&p) { hold(&mut routes, &mut executions); i += 1; continue }
        // Consecutive event rows of one execution go in one request (coordinator validates the whole batch).
        let mut batch = vec![(n, b.clone())];
        if p == "/api/hosts/events" && !single {
            while let Some((m, q, c)) = rows.get(i + batch.len()) {
                let next: Value = serde_json::from_str(c)?;
                if q != &p || next["executionId"] != value["executionId"] || value["events"].as_array().map_or(0, |a| a.len()) >= 500 { break; }
                value["events"].as_array_mut().ok_or("events missing")?.extend(next["events"].as_array().cloned().unwrap_or_default());
                batch.push((*m, c.clone()));
            }
        }
        let response = match send(url, token, &p, value.clone()) {
            Answer::Ok(response) => response,
            Answer::Retry(e) => return Err(e.into()),
            Answer::Edge(..) => { hold(&mut routes, &mut executions); i += 1; continue }
            Answer::Reject(status, text) => {
                let reason = format!("coordinator {status} {text}");
                if value.get("delegation").is_some() || value.get("agentToolKey").is_some() {
                    json!({"error":"Coordinator rejected delegation; inspect retained command and grant"})
                } else if batch.len() > 1 {
                    // Retry this batch row by row so only the poisoned row is set aside.
                    single = true;
                    continue;
                } else {
                    let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH)?.as_millis() as i64;
                    let tx = db.unchecked_transaction()?;
                    tx.execute("INSERT INTO outbox_failed(route,body,status,reason,failed_at) VALUES(?1,?2,?3,?4,?5)", params![p, b, status, reason, now])?;
                    tx.execute("DELETE FROM outbox WHERE rowid=?", [n])?;
                    let own = value["events"].as_array().is_some_and(|a| a.iter().any(|e| e["type"] == "outbox.failed"));
                    if let (Some(execution), false) = (value["executionId"].as_str(), own) {
                        // ponytail: seq offset 1e12+rowid keeps clear of harness sequences; reserve a range if the coordinator ever caps seq.
                        let seq = 1_000_000_000_000i64 + n;
                        queue(&tx, "/api/hosts/events", json!({"hostId":host,"executionId":execution,"events":[{"id":format!("outbox-failed-{n}"),"seq":seq,"type":"outbox.failed","timestamp":now,"data":{"route":p,"status":status,"reason":reason}}]}))?;
                    }
                    tx.commit()?;
                    i += 1;
                    continue;
                }
            }
        };
        i += batch.len();
        let key = value["agentToolKey"].as_str().map(str::to_owned).or_else(|| value.get("delegation").map(|q| format!("{}:{}", value["commandId"].as_str().unwrap_or(""), q["requestId"].as_str().unwrap_or(""))));
        if let Some(k) = key {
            db.execute("UPDATE delegation_requests SET response=? WHERE id=?", params![response.to_string(), k])?;
        }
        for (n, _) in batch {
            db.execute("DELETE FROM outbox WHERE rowid=?", [n])?;
        }
    }
    Ok(())
}

/// Runs delivery on its own connection and thread until the process exits.
pub fn spawn(path: std::path::PathBuf, url: String, token: String, host: String) -> Result<()> {
    let db = Connection::open(path)?;
    db.busy_timeout(Duration::from_secs(5))?;
    std::thread::spawn(move || loop {
        if flush(&db, &url, &token, &host).is_err() {
            eprintln!("executor outbox delivery failed; rows retained, retry pending");
        }
        std::thread::sleep(Duration::from_millis(100));
    });
    Ok(())
}

#[cfg(test)]
pub mod tests {
    use super::*;
    use std::io::{BufRead, BufReader, Read, Write};
    /// A coordinator stand-in: answers `n` requests with `answer(path, body)` -> (status line, extra headers).
    pub fn serve(n: usize, answer: impl Fn(&str, &Value) -> (&'static str, &'static str) + Send + 'static) -> (String, std::thread::JoinHandle<Vec<(String, Value)>>) {
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let address = format!("http://{}", listener.local_addr().unwrap());
        let server = std::thread::spawn(move || {
            let mut seen = vec![];
            for _ in 0..n {
                let (mut socket, _) = listener.accept().unwrap();
                let mut reader = BufReader::new(socket.try_clone().unwrap());
                let mut first = String::new();
                reader.read_line(&mut first).unwrap();
                let path = first.split_whitespace().nth(1).unwrap_or("").to_string();
                let mut length = 0;
                loop {
                    let mut line = String::new();
                    reader.read_line(&mut line).unwrap();
                    if line == "\r\n" { break; }
                    if line.to_lowercase().starts_with("content-length:") { length = line.split(':').nth(1).unwrap().trim().parse().unwrap(); }
                }
                let mut body = vec![0; length];
                reader.read_exact(&mut body).unwrap();
                let v: Value = serde_json::from_slice(&body).unwrap_or(json!(null));
                let (status, extra) = answer(&path, &v);
                let text = if status.starts_with('3') { "<html>login</html>" } else { "{}" };
                write!(socket, "HTTP/1.1 {status}\r\n{extra}Content-Length: {}\r\nConnection: close\r\n\r\n{text}", text.len()).unwrap();
                seen.push((path, v));
            }
            seen
        });
        (address, server)
    }
    pub const LOGIN: &str = "Location: https://login.example/cdn-cgi/access/login?token=x\r\n";
    fn journal() -> Connection {
        let db = Connection::open_in_memory().unwrap();
        schema(&db).unwrap();
        db.execute_batch("CREATE TABLE delegation_requests(id TEXT PRIMARY KEY,body TEXT,response TEXT);").unwrap();
        db
    }
    fn count(db: &Connection, t: &str) -> i64 { db.query_row(&format!("SELECT count(*) FROM {t}"), [], |r| r.get(0)).unwrap() }
    #[test]
    fn rejected_row_is_set_aside_and_does_not_block_next_rows() {
        let (url, server) = serve(3, |_, v| if v["commandId"] == "bad" { ("400 Bad Request", "") } else { ("200 OK", "") });
        let db = journal();
        queue(&db, "/api/hosts/receipts", json!({"commandId":"bad","executionId":"e1"})).unwrap();
        queue(&db, "/api/hosts/receipts", json!({"commandId":"good","executionId":"e1"})).unwrap();
        // Second flush delivers the outbox.failed event queued by the first.
        for _ in 0..2 { flush(&db, &url, "t", "mac").unwrap(); }
        let seen = server.join().unwrap();
        assert_eq!(seen[1].1["commandId"], "good");
        assert_eq!(seen[2].1["events"][0]["type"], "outbox.failed");
        assert_eq!((count(&db, "outbox"), count(&db, "outbox_failed")), (0, 1));
    }
    #[test]
    fn redirected_row_stays_queued_and_is_delivered_after_the_edge_recovers() {
        let calls = std::sync::Arc::new(std::sync::atomic::AtomicUsize::new(0));
        let c = calls.clone();
        let (url, server) = serve(2, move |_, _| if c.fetch_add(1, std::sync::atomic::Ordering::SeqCst) == 0 { ("302 Found", LOGIN) } else { ("200 OK", "") });
        let db = journal();
        queue(&db, "/api/hosts/stores", json!({"hostId":"mac"})).unwrap();
        flush(&db, &url, "t", "mac").unwrap();
        assert_eq!((count(&db, "outbox"), count(&db, "outbox_failed")), (1, 0), "kept, not set aside");
        let p = edge_problems("");
        assert!(p.as_array().unwrap().iter().any(|x| x["route"] == "/api/hosts/stores" && x["status"] == 302 && !x["location"].as_str().unwrap().contains("token")), "{p}");
        // Within the backoff the route is not asked again; after it the row is delivered.
        flush(&db, &url, "t", "mac").unwrap();
        assert_eq!(count(&db, "outbox"), 1);
        std::thread::sleep(Duration::from_millis(2100));
        flush(&db, &url, "t", "mac").unwrap();
        assert_eq!(count(&db, "outbox"), 0);
        assert!(!edge_problems("").as_array().unwrap().iter().any(|x| x["route"] == "/api/hosts/stores"), "cleared on delivery");
        assert_eq!(server.join().unwrap().len(), 2);
    }
    #[test]
    fn a_redirect_on_one_route_does_not_block_other_routes() {
        let (url, server) = serve(3, |path, _| if path == "/api/hosts/artifacts/declare" { ("302 Found", LOGIN) } else { ("200 OK", "") });
        let db = journal();
        queue(&db, "/api/hosts/artifacts/declare", json!({"name":"a","executionId":"e1"})).unwrap();
        queue(&db, "/api/hosts/artifacts/declare", json!({"name":"b","executionId":"e2"})).unwrap();
        // e1's receipt waits for e1's declaration (its required artifact must be known first), and holds the receipts route
        // behind it; e3's receipt, queued before it, and e3's events flow.
        queue(&db, "/api/hosts/receipts", json!({"commandId":"c3","executionId":"e3"})).unwrap();
        queue(&db, "/api/hosts/receipts", json!({"commandId":"c1","executionId":"e1"})).unwrap();
        queue(&db, "/api/hosts/events", json!({"executionId":"e3","events":[{"id":"1"}]})).unwrap();
        flush(&db, &url, "t", "mac").unwrap();
        let seen = server.join().unwrap();
        // The second declare row is not posted after the first redirect (order within a route), nor e1's receipt (order
        // within an execution); e3's rows on the other routes are.
        assert_eq!(seen.iter().map(|(p, v)| format!("{p} {}", v["executionId"].as_str().unwrap_or(""))).collect::<Vec<_>>(), ["/api/hosts/artifacts/declare e1", "/api/hosts/receipts e3", "/api/hosts/events e3"]);
        let left: Vec<String> = db.prepare("SELECT route FROM outbox ORDER BY rowid").unwrap().query_map([], |r| r.get(0)).unwrap().map(|r| r.unwrap()).collect();
        assert_eq!(left, ["/api/hosts/artifacts/declare", "/api/hosts/artifacts/declare", "/api/hosts/receipts"]);
        assert_eq!(count(&db, "outbox_failed"), 0);
    }
    #[test]
    fn a_redirected_delegation_is_not_answered_as_rejected() {
        let (url, server) = serve(1, |_, _| ("302 Found", LOGIN));
        let db = journal();
        db.execute("INSERT INTO delegation_requests(id,body) VALUES('c1:r1','{}')", []).unwrap();
        // Edge state is per process and shared by parallel tests: this test uses a route no other test touches.
        queue(&db, "/api/hosts/receipts-delegation-test", json!({"commandId":"c1","executionId":"e1","delegation":{"requestId":"r1"}})).unwrap();
        flush(&db, &url, "t", "mac").unwrap();
        server.join().unwrap();
        let response: Option<String> = db.query_row("SELECT response FROM delegation_requests WHERE id='c1:r1'", [], |r| r.get(0)).unwrap();
        assert_eq!(response, None);
        assert_eq!(count(&db, "outbox"), 1);
    }
}
