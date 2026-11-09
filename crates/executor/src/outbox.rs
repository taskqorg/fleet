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
/// A redirect is an edge condition of the whole host (an Access login page for a path without a bypass), never a bad row:
/// the row stays queued and is retried. The redirect body is drained so the pooled connection is reused.
pub enum Answer { Ok(Value), Redirect(u16, String), Reject(u16, String), Retry(String) }
pub fn classify(r: std::result::Result<ureq::Response, ureq::Error>) -> Answer {
    match r {
        Ok(r) if (300..400).contains(&r.status()) => {
            let status = r.status();
            // Only the location's origin and path: an edge login URL can carry query parameters.
            let to = r.header("Location").unwrap_or("").split(['?', '#']).next().unwrap_or("").to_string();
            let _ = r.into_string();
            Answer::Redirect(status, to)
        }
        Ok(r) => r.into_json::<Value>().map_or_else(|e| Answer::Retry(e.to_string()), Answer::Ok),
        Err(ureq::Error::Status(code, r)) => {
            let text = r.into_string().unwrap_or_default();
            if code >= 500 || code == 429 { Answer::Retry(format!("{code} {text}")) } else { Answer::Reject(code, text) }
        }
        Err(e) => Answer::Retry(e.to_string()),
    }
}
/// The poll's redirect, as an error the exchange loop recognizes.
#[derive(Debug)]
pub struct Redirected(pub u16, pub String);
impl std::fmt::Display for Redirected {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result { write!(f, "coordinator redirected with {} to {}", self.0, self.1) }
}
impl std::error::Error for Redirected {}

/// Edge redirects in progress, by route: one log line when a route starts redirecting and one when it recovers, and a host
/// problem `edge_redirect` reported with every poll until delivery works again.
static EDGE: std::sync::Mutex<std::collections::BTreeMap<String, (u16, String)>> = std::sync::Mutex::new(std::collections::BTreeMap::new());
pub fn edge_redirect(route: &str, status: u16, to: &str) {
    let mut m = EDGE.lock().unwrap_or_else(|e| e.into_inner());
    if m.insert(route.to_string(), (status, to.to_string())).is_none() {
        eprintln!("executor: the coordinator edge redirects {route} with {status} to {to}; rows stay queued and are retried");
    }
}
pub fn edge_ok(route: &str) {
    let mut m = EDGE.lock().unwrap_or_else(|e| e.into_inner());
    if m.remove(route).is_some() { eprintln!("executor: the coordinator edge delivers {route} again") }
}
pub fn edge_problems() -> Value {
    let m = EDGE.lock().unwrap_or_else(|e| e.into_inner());
    Value::Array(m.iter().map(|(route, (status, to))| json!({"kind":"edge_redirect","route":route,"status":status,"location":to})).collect())
}

/// One coordinator request on the pooled agent (keep-alive avoids a TLS handshake per row; redirects are not followed).
pub fn send(url: &str, token: &str, route: &str, v: Value) -> Answer {
    static AGENT: std::sync::OnceLock<ureq::Agent> = std::sync::OnceLock::new();
    let a = classify(AGENT
        .get_or_init(|| ureq::AgentBuilder::new().timeout(Duration::from_secs(5)).redirects(0).build())
        .post(&format!("{url}{route}"))
        .set("Authorization", &format!("Bearer {token}"))
        .send_json(v));
    match &a { Answer::Redirect(s, to) => edge_redirect(route, *s, to), Answer::Retry(_) => {}, _ => edge_ok(route) }
    a
}
pub fn post(url: &str, token: &str, route: &str, v: Value) -> Result<Value> {
    match send(url, token, route, v) {
        Answer::Ok(v) => Ok(v),
        Answer::Redirect(s, to) => Err(Box::new(Redirected(s, to))),
        Answer::Reject(code, text) => Err(format!("coordinator {code} {text}").into()),
        Answer::Retry(e) => Err(e.into()),
    }
}

pub fn queue(db: &Connection, route: &str, v: Value) -> Result<()> {
    db.execute("INSERT INTO outbox(route,body) VALUES(?1,?2)", params![route, v.to_string()])?;
    Ok(())
}

/// Delivers queued rows in order. A 4xx row is moved to `outbox_failed` and reported as an executor event; the following
/// rows keep flowing. A redirected row stays queued and stops only its own route for this flush (rows of a route keep their
/// order); other routes keep flowing. 5xx and network errors stop the flush and retry later.
pub fn flush(db: &Connection, url: &str, token: &str, host: &str) -> Result<()> {
    let mut stmt = db.prepare("SELECT rowid,route,body FROM outbox ORDER BY rowid")?;
    let rows: Vec<(i64, String, String)> = stmt
        .query_map([], |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)))?
        .collect::<std::result::Result<_, _>>()?;
    drop(stmt);
    let mut i = 0;
    let mut single = false;
    let mut held = std::collections::HashSet::new();
    while i < rows.len() {
        let (n, p, b) = rows[i].clone();
        if held.contains(&p) { i += 1; continue }
        let mut value: Value = serde_json::from_str(&b)?;
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
            Answer::Redirect(..) => { held.insert(p); i += 1; continue }
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
        let p = edge_problems();
        assert!(p.as_array().unwrap().iter().any(|x| x["route"] == "/api/hosts/stores" && x["status"] == 302 && !x["location"].as_str().unwrap().contains("token")), "{p}");
        flush(&db, &url, "t", "mac").unwrap();
        assert_eq!(count(&db, "outbox"), 0);
        assert!(!edge_problems().as_array().unwrap().iter().any(|x| x["route"] == "/api/hosts/stores"), "cleared on delivery");
        assert_eq!(server.join().unwrap().len(), 2);
    }
    #[test]
    fn a_redirect_on_one_route_does_not_block_other_routes() {
        let (url, server) = serve(3, |path, _| if path == "/api/hosts/artifacts/declare" { ("302 Found", LOGIN) } else { ("200 OK", "") });
        let db = journal();
        queue(&db, "/api/hosts/artifacts/declare", json!({"name":"a"})).unwrap();
        queue(&db, "/api/hosts/artifacts/declare", json!({"name":"b"})).unwrap();
        queue(&db, "/api/hosts/receipts", json!({"commandId":"c1","executionId":"e1"})).unwrap();
        queue(&db, "/api/hosts/events", json!({"executionId":"e1","events":[{"id":"1"}]})).unwrap();
        flush(&db, &url, "t", "mac").unwrap();
        let seen = server.join().unwrap();
        // The second declare row is not posted after the first redirect (order within a route), the other routes are.
        assert_eq!(seen.iter().map(|(p, _)| p.as_str()).collect::<Vec<_>>(), ["/api/hosts/artifacts/declare", "/api/hosts/receipts", "/api/hosts/events"]);
        let left: Vec<String> = db.prepare("SELECT route FROM outbox ORDER BY rowid").unwrap().query_map([], |r| r.get(0)).unwrap().map(|r| r.unwrap()).collect();
        assert_eq!(left, ["/api/hosts/artifacts/declare", "/api/hosts/artifacts/declare"]);
        assert_eq!(count(&db, "outbox_failed"), 0);
    }
    #[test]
    fn a_redirected_delegation_is_not_answered_as_rejected() {
        let (url, server) = serve(1, |_, _| ("302 Found", LOGIN));
        let db = journal();
        db.execute("INSERT INTO delegation_requests(id,body) VALUES('c1:r1','{}')", []).unwrap();
        queue(&db, "/api/hosts/receipts", json!({"commandId":"c1","executionId":"e1","delegation":{"requestId":"r1"}})).unwrap();
        flush(&db, &url, "t", "mac").unwrap();
        server.join().unwrap();
        let response: Option<String> = db.query_row("SELECT response FROM delegation_requests WHERE id='c1:r1'", [], |r| r.get(0)).unwrap();
        assert_eq!(response, None);
        assert_eq!(count(&db, "outbox"), 1);
    }
}
