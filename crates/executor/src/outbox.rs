// Outbox delivery: rows are queued by the command loop and flushed on a separate thread,
// so polling and accepting commands never waits on coordinator latency.
use rusqlite::{Connection, params};
use serde_json::{Value, json};
use std::time::Duration;

type Result<T> = std::result::Result<T, Box<dyn std::error::Error>>;

pub fn schema(db: &Connection) -> rusqlite::Result<()> {
    db.execute_batch("CREATE TABLE IF NOT EXISTS outbox(route TEXT NOT NULL,body TEXT NOT NULL); CREATE TABLE IF NOT EXISTS outbox_failed(route TEXT NOT NULL,body TEXT NOT NULL,status INTEGER NOT NULL,reason TEXT NOT NULL,failed_at INTEGER NOT NULL);")
}

/// A coordinator answer that redirects (an edge login page, a misrouted path): the coordinator never redirects machine
/// routes, so the row is set aside like a 4xx instead of being retried forever ahead of every later row.
#[derive(Debug)]
pub struct Redirected(pub u16, pub String);
impl std::fmt::Display for Redirected {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result { write!(f, "coordinator redirected with {} to {}", self.0, self.1) }
}
impl std::error::Error for Redirected {}

pub fn post(url: &str, token: &str, route: &str, v: Value) -> Result<Value> {
    // One pooled agent: keep-alive avoids a TLS handshake per outbox row. Redirects are not followed.
    static AGENT: std::sync::OnceLock<ureq::Agent> = std::sync::OnceLock::new();
    let r = AGENT
        .get_or_init(|| ureq::AgentBuilder::new().timeout(Duration::from_secs(5)).redirects(0).build())
        .post(&format!("{url}{route}"))
        .set("Authorization", &format!("Bearer {token}"))
        .send_json(v)?;
    if (300..400).contains(&r.status()) {
        // Only the location's origin and path: an edge login URL can carry query parameters.
        let to = r.header("Location").unwrap_or("").split(['?', '#']).next().unwrap_or("").to_string();
        return Err(Box::new(Redirected(r.status(), to)));
    }
    Ok(r.into_json()?)
}

pub fn queue(db: &Connection, route: &str, v: Value) -> Result<()> {
    db.execute("INSERT INTO outbox(route,body) VALUES(?1,?2)", params![route, v.to_string()])?;
    Ok(())
}

fn rejected(error: &(dyn std::error::Error + 'static)) -> Option<(u16, String)> {
    if let Some(r) = error.downcast_ref::<Redirected>() { return Some((r.0, r.to_string())) }
    match error.downcast_ref::<ureq::Error>() {
        Some(ureq::Error::Status(code, _)) if (400..500).contains(code) => Some((*code, error.to_string())),
        _ => None,
    }
}

/// Delivers queued rows in order. A 4xx row is moved to `outbox_failed` and reported as an
/// executor event; the following rows keep flowing. 5xx/network errors stop the flush and retry later.
pub fn flush(db: &Connection, url: &str, token: &str, host: &str) -> Result<()> {
    let mut stmt = db.prepare("SELECT rowid,route,body FROM outbox ORDER BY rowid")?;
    let rows: Vec<(i64, String, String)> = stmt
        .query_map([], |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)))?
        .collect::<std::result::Result<_, _>>()?;
    drop(stmt);
    let mut i = 0;
    let mut single = false;
    while i < rows.len() {
        let (n, p, b) = rows[i].clone();
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
        let response = match post(url, token, &p, value.clone()) {
            Ok(response) => response,
            Err(error) => {
                let Some((status, reason)) = rejected(error.as_ref()) else { return Err(error) };
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
mod tests {
    use super::*;
    use std::io::{BufRead, BufReader, Read, Write};
    #[test]
    fn poisoned_row_does_not_block_next_rows() { poisoned("bad", 400) }
    #[test]
    fn redirected_row_is_set_aside_and_does_not_block_next_rows() { poisoned("redirected", 302) }
    fn poisoned(first: &'static str, code: i64) {
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let address = listener.local_addr().unwrap();
        let server = std::thread::spawn(move || {
            let mut seen = vec![];
            for _ in 0..3 {
                let (mut socket, _) = listener.accept().unwrap();
                let mut reader = BufReader::new(socket.try_clone().unwrap());
                let mut first = String::new();
                reader.read_line(&mut first).unwrap();
                let mut length = 0;
                loop {
                    let mut line = String::new();
                    reader.read_line(&mut line).unwrap();
                    if line == "\r\n" { break; }
                    if line.to_lowercase().starts_with("content-length:") { length = line.split(':').nth(1).unwrap().trim().parse().unwrap(); }
                }
                let mut body = vec![0; length];
                reader.read_exact(&mut body).unwrap();
                let v: Value = serde_json::from_slice(&body).unwrap();
                let bad = v["commandId"] == "bad";
                let redirected = v["commandId"] == "redirected";
                seen.push(v);
                let (status, text) = if bad { ("400 Bad Request", "{}") } else { ("200 OK", "{}") };
                // A redirect (an edge login page) is set aside like a 4xx.
                let extra = if redirected { "Location: https://login.example/cdn-cgi/access/login?token=x\r\n" } else { "" };
                let status = if redirected { "302 Found" } else { status };
                write!(socket, "HTTP/1.1 {status}\r\n{extra}Content-Length: {}\r\nConnection: close\r\n\r\n{text}", text.len()).unwrap();
            }
            seen
        });
        let db = Connection::open_in_memory().unwrap();
        schema(&db).unwrap();
        db.execute_batch("CREATE TABLE delegation_requests(id TEXT PRIMARY KEY,body TEXT,response TEXT);").unwrap();
        queue(&db, "/api/hosts/receipts", json!({"commandId":first,"executionId":"e1"})).unwrap();
        queue(&db, "/api/hosts/receipts", json!({"commandId":"good","executionId":"e1"})).unwrap();
        // Second flush delivers the outbox.failed event queued by the first.
        for _ in 0..2 { flush(&db, &format!("http://{address}"), "t", "mac").unwrap(); }
        let seen = server.join().unwrap();
        assert_eq!(seen[1]["commandId"], "good");
        assert_eq!(seen[2]["events"][0]["type"], "outbox.failed");
        let count = |t: &str| db.query_row(&format!("SELECT count(*) FROM {t}"), [], |r| r.get::<_, i64>(0)).unwrap();
        assert_eq!((count("outbox"), count("outbox_failed")), (0, 1));
        let (status, reason): (i64, String) = db.query_row("SELECT status,reason FROM outbox_failed", [], |r| Ok((r.get(0)?, r.get(1)?))).unwrap();
        assert_eq!(status, code);
        assert!(!reason.contains("token=x"), "{reason}");
        assert_eq!(seen[2]["events"][0]["data"]["status"], code);
    }
}
