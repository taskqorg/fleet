//! Fleet coordinator, trial slice (docs/designs/fleet-stack.md#trial-slice-l1): credentials, sessions, messages, host poll,
//! receipts, events and the change stream over the shared Postgres schema. Environment as packages/coordinator/server.ts:
//! PORT, FLEET_DATABASE_URL, FLEET_MIGRATION_DATABASE_URL, FLEET_PEOPLE_FILE, FLEET_BROWSER_SECRET, FLEET_HOST_ID.
mod db;
mod contracts {
    //! Host protocol types generated at build time from contracts/*.schema.json (crates/executor/build.rs).
    #![allow(dead_code)]
    include!(concat!(env!("OUT_DIR"), "/contracts.rs"));
}

use db::{ApiErr, Db, GLOBAL, Mode, Scope, fail};
use hyper::body::{Body as _, Bytes, Frame, Incoming};
use hyper::{Request, Response};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::convert::Infallible;
use std::pin::Pin;
use std::sync::Arc;
use std::task::{Context, Poll};
use tokio::sync::{mpsc, watch};
use tokio_postgres::Client;

#[derive(Clone)]
struct Person {
    email: String,
    roles: Vec<String>,
    owner: bool,
    grants: Vec<(String, String)>,
    name: String,
}
struct Host {
    id: String,
    holder: String,
}
enum Who {
    Person(Person),
    Host(Host),
}

struct St {
    db: Db,
    people: HashMap<String, Person>,
    secret: String,
    default_host: String,
    /// Bumped on every NOTIFY fleet_change; each open stream reads again when it moves.
    wake: watch::Sender<u64>,
}

type Res = Result<Value, ApiErr>;

fn now_ms() -> i64 {
    std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap().as_millis() as i64
}
fn random_hex(n: usize) -> String {
    use std::io::Read;
    let mut b = vec![0u8; n];
    std::fs::File::open("/dev/urandom").and_then(|mut f| f.read_exact(&mut b)).expect("/dev/urandom");
    b.iter().map(|x| format!("{x:02x}")).collect()
}
fn uuid() -> String {
    let h = random_hex(16);
    let v = format!("4{}", &h[13..16]);
    let var = format!("{:x}{}", (u8::from_str_radix(&h[16..17], 16).unwrap() & 3) | 8, &h[17..20]);
    format!("{}-{}-{}-{}-{}", &h[0..8], &h[8..12], v, var, &h[20..32])
}
fn sha256_hex(s: &str) -> String {
    Sha256::digest(s.as_bytes()).iter().map(|x| format!("{x:02x}")).collect()
}
/// Constant-time comparison of two secrets of equal length.
fn equal(a: &str, b: &str) -> bool {
    a.len() == b.len() && a.bytes().zip(b.bytes()).fold(0u8, |acc, (x, y)| acc | (x ^ y)) == 0
}

// ---------- people ----------

fn load_people(file: &str) -> HashMap<String, Person> {
    let list: Vec<Value> = serde_json::from_str(&std::fs::read_to_string(file).expect("FLEET_PEOPLE_FILE")).expect("people.json must be a list");
    let mut out = HashMap::new();
    for p in list {
        let email = p["email"].as_str().filter(|e| e.contains('@')).expect("Each person needs an e-mail").to_lowercase();
        let roles: Vec<String> = p["roles"].as_array().expect("roles").iter().map(|r| r.as_str().unwrap().to_string()).collect();
        assert!(!roles.is_empty() && roles.iter().all(|r| r == "operator" || r == "architect"), "Roles of {email} must be operator and/or architect");
        let grants = p["grants"].as_array().map(|g| g.iter().map(|g| (g["kind"].as_str().unwrap().to_string(), g["id"].as_str().unwrap().to_string())).collect()).unwrap_or_default();
        let name = p["name"].as_str().unwrap_or(&email).to_string();
        assert!(out.insert(email.clone(), Person { email, roles, owner: p["owner"] == true, grants, name }).is_none(), "Duplicate person");
    }
    assert_eq!(out.values().filter(|p| p.owner).count(), 1, "Exactly one person must have owner: true");
    out
}
/// person and person_grant mirror the people file.
async fn mirror_people(st: &St) -> Result<(), ApiErr> {
    in_tx!(st.db, Mode::Write(GLOBAL.into()), None, |t| async {
        t.execute("DELETE FROM person", &[]).await?;
        for p in st.people.values() {
            t.execute("INSERT INTO person(email,name,roles,owner) VALUES($1,$2,$3,$4)", &[&p.email, &p.name, &p.roles, &p.owner]).await?;
            for (k, id) in &p.grants {
                t.execute("INSERT INTO person_grant(email,object_kind,object_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", &[&p.email, k, id]).await?;
            }
        }
        Ok(())
    }
    .await)
}
/// A person other than the owner works in the scope of their project grants; the architect role adds rows without a project.
fn scope_of(who: &Who) -> Option<Scope> {
    match who {
        Who::Person(p) if !p.owner => {
            let ids: Vec<&str> = p.grants.iter().filter(|g| g.0 == "project").map(|g| g.1.as_str()).collect();
            let pre = if p.roles.iter().any(|r| r == "architect") { "," } else { "" };
            Some(Scope { project_ids: format!("{pre}{}", ids.join(",")), person: p.email.clone() })
        }
        _ => None,
    }
}

// ---------- documents ----------

async fn get_doc(c: &Client, id: &str, kind: &str) -> Result<Option<Value>, ApiErr> {
    Ok(c.query_opt("SELECT data FROM document WHERE id=$1 AND kind=$2", &[&id, &kind]).await?.map(|r| r.get(0)))
}
/// An id that already exists under another kind is left unchanged.
async fn put_doc(c: &Client, kind: &str, v: &Value) -> Result<(), ApiErr> {
    let id = v["id"].as_str().unwrap();
    let project = v["projectId"].as_str();
    c.execute("INSERT INTO document(kind,id,data,project_id) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO UPDATE SET data=excluded.data,project_id=excluded.project_id,updated_at=now() WHERE document.kind=excluded.kind", &[&kind, &id, v, &project]).await?;
    Ok(())
}
async fn docs(c: &Client, sql: &str, p: &[&(dyn tokio_postgres::types::ToSql + Sync)]) -> Result<Vec<Value>, ApiErr> {
    Ok(c.query(sql, p).await?.iter().map(|r| r.get(0)).collect())
}
async fn get_execution(c: &Client, id: &str) -> Res {
    match c.query_opt("SELECT data FROM execution WHERE id=$1 AND legacy_kind='execution'", &[&id]).await? {
        Some(r) => Ok(r.get(0)),
        None => fail(404, "Not found"),
    }
}
/// Legacy execution state to the typed state; None (unknown) keeps the last state and lowers the observation.
fn typed_state(legacy: &str) -> Option<&'static str> {
    Some(match legacy {
        "queued" | "accepted" | "pending" => "preparing",
        "started" | "running" => "running",
        "waiting" => "waiting",
        "completed" => "succeeded",
        "failed" | "quota" | "unavailable" => "failed",
        "stopped" | "cancelled" => "cancelled",
        _ => return None,
    })
}
/// Upsert of a legacy execution record into its typed row (agent runs only in the slice).
async fn put_execution(c: &Client, e: &Value) -> Result<(), ApiErr> {
    let id = e["id"].as_str().unwrap();
    let prev: Option<String> = c.query_opt("SELECT state::text FROM execution WHERE id=$1", &[&id]).await?.map(|r| r.get(0));
    let (state, obs) = match typed_state(e["state"].as_str().unwrap_or("")) {
        Some(s) => (s.to_string(), "confirmed"),
        None => (prev.unwrap_or("preparing".into()), "unconfirmed"),
    };
    let config = json!({"profileId": null, "profileSha256": null, "effort": e["effort"]});
    let (started, ended) = (e["startedAt"].as_i64(), e["finishedAt"].as_i64());
    c.execute(
        "INSERT INTO execution(id,kind,state,observation,session_id,host_id,harness,model,config_ref,started_at,ended_at,legacy_kind,legacy_id,data,directed_by)
         VALUES($1,'agent_run',$2::text::execution_state,$3::text::observation,$4,$5,'codex',$6,$7,to_timestamp($8::bigint/1000.0),to_timestamp($9::bigint/1000.0),'execution',$1,$10,$11)
         ON CONFLICT(id) DO UPDATE SET state=excluded.state,observation=excluded.observation,session_id=excluded.session_id,host_id=excluded.host_id,model=excluded.model,
          config_ref=excluded.config_ref,started_at=excluded.started_at,ended_at=excluded.ended_at,data=excluded.data,directed_by=coalesce(excluded.directed_by,execution.directed_by),
          problem=CASE WHEN excluded.state='succeeded' THEN NULL ELSE execution.problem END",
        &[&id, &state, &obs, &e["sessionId"].as_str(), &e["hostId"].as_str(), &e["model"].as_str(), &config, &started, &ended, e, &e["directedBy"].as_str()],
    )
    .await?;
    Ok(())
}

// ---------- routes ----------

async fn hosts_register(c: &Client, st: &St, who: &Who, b: &Value) -> Res {
    let Who::Person(p) = who else { return fail(403, "Only the owner manages hosts") };
    if !p.owner {
        return fail(403, "Only the owner manages hosts");
    }
    let id = b["id"].as_str().unwrap_or("");
    let valid = id.len() <= 80 && id.starts_with(|c: char| c.is_ascii_lowercase()) && id.chars().all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '-');
    if !valid {
        return fail(400, "Expected host id");
    }
    if get_doc(c, &format!("host:{id}"), "host").await?.is_some() {
        return fail(409, "Host exists");
    }
    let holder = b["holder"].as_str().map(str::to_lowercase).unwrap_or(p.email.clone());
    if !st.people.contains_key(&holder) {
        return fail(400, "Holder is not a person");
    }
    let secret = random_hex(32);
    let h = json!({"id": format!("host:{id}"), "hostId": id, "name": b["name"].as_str().unwrap_or(id), "holder": holder,
        "platform": b["platform"].as_str().unwrap_or("unknown"), "roles": [], "secretHash": sha256_hex(&secret), "intake": "all",
        "harnesses": {}, "createdAt": now_ms()});
    put_doc(c, "host", &h).await?;
    let mut view = h.as_object().unwrap().clone();
    view.remove("secretHash");
    view.remove("hostId");
    view.insert("id".into(), json!(id));
    Ok(json!({"host": view, "secret": secret}))
}

async fn session_create(c: &Client, who: &Who) -> Res {
    let Who::Person(p) = who else { return fail(403, "Person required") };
    let s = json!({"id": uuid(), "createdAt": now_ms(), "directedBy": p.email});
    put_doc(c, "session", &s).await?;
    let mut out = s.as_object().unwrap().clone();
    out.insert("sessionId".into(), s["id"].clone());
    Ok(Value::Object(out))
}

async fn session_get(c: &Client, id: &str) -> Res {
    let Some(s) = get_doc(c, id, "session").await? else { return fail(404, "Not found") };
    let mut out = s.as_object().unwrap().clone();
    out.insert("sessionId".into(), json!(id));
    out.insert("messages".into(), Value::Array(docs(c, "SELECT data FROM document WHERE kind='message' AND data->>'sessionId'=$1 ORDER BY seq", &[&id]).await?));
    out.insert("executions".into(), Value::Array(docs(c, "SELECT data FROM execution WHERE legacy_kind='execution' AND session_id=$1 ORDER BY seq", &[&id]).await?));
    Ok(Value::Object(out))
}

fn text(v: &Value, max: usize) -> Result<&str, ApiErr> {
    match v.as_str() {
        Some(s) if !s.is_empty() && s.encode_utf16().count() <= max => Ok(s),
        _ => fail(400, "Invalid text"),
    }
}

/// One message queues one execution with no host; the host is chosen at a poll. The same requestId returns the same execution.
async fn message(c: &Client, id: &str, b: &Value) -> Res {
    let Some(s) = get_doc(c, id, "session").await? else { return fail(404, "Not found") };
    let ok = b.as_object().is_some_and(|o| o.keys().all(|k| ["requestId", "text", "delivery"].contains(&k.as_str())) && o.contains_key("requestId") && o.contains_key("text"));
    if !ok {
        return fail(400, "Expected requestId, text and optional delivery");
    }
    let request_id = text(&b["requestId"], 200)?;
    let input = text(&b["text"], 32000)?;
    let sent = docs(c, "SELECT data FROM document WHERE kind='message' AND data->>'sessionId'=$1 AND data @> $2 ORDER BY seq", &[&id, &json!({"requestId": request_id})]).await?;
    if let Some(m) = sent.first() {
        if m["text"] != input {
            return fail(409, "Request identity conflict");
        }
        return get_execution(c, m["executionId"].as_str().unwrap()).await;
    }
    let eid = uuid();
    let e = json!({"id": eid, "executionId": eid, "sessionId": id, "hostId": null, "state": "queued", "createdAt": now_ms(),
        "directedBy": s["directedBy"], "model": "gpt-6.1-sol", "effort": "low",
        "dispatch": {"harness": "codex", "projectId": null, "account": null, "host": null, "last": null, "input": input, "payload": null}});
    put_execution(c, &e).await?;
    put_doc(c, "message", &json!({"id": uuid(), "sessionId": id, "executionId": eid, "requestId": request_id, "text": input})).await?;
    Ok(e)
}

const INSTRUCTIONS: &str = "Follow the user request and its scope. Discuss, answer questions, or review existing results when requested; do not create or modify a website unless requested. When creating a static website, deliver output/index.html and retain all source assets. Do not spawn subagents.";

/// The execution's host must be the polling host (`owned` in app.ts).
async fn owned(c: &Client, host: &Host, b: &Value) -> Result<(), ApiErr> {
    if let Some(eid) = b.get("executionId") {
        let e = get_execution(c, eid.as_str().unwrap_or("")).await?;
        if e["hostId"].as_str() != Some(host.id.as_str()) {
            return fail(403, "Execution rejected");
        }
    }
    Ok(())
}

async fn poll(c: &Client, st: &St, host: &Host, b: &Value) -> Res {
    let req: contracts::PollRequest = serde_json::from_value(b.clone()).map_err(|e| ApiErr { status: 400, msg: e.to_string(), retry: false })?;
    let mut obs = json!({"id": host.id, "holder": host.holder, "lastSeenAt": now_ms()});
    if ["darwin", "win32", "linux"].contains(&req.platform.as_str()) {
        obs["platform"] = json!(req.platform);
    }
    obs["capabilities"] = json!(req.capabilities);
    let problems: Vec<Value> = req.problems.iter().take(20).map(|p| json!({"kind": "edge-redirect", "route": p.route.chars().take(200).collect::<String>(), "status": p.status,
        "location": p.location.as_ref().map(|l| l.chars().take(500).collect::<String>()), "detail": p.detail.chars().take(500).collect::<String>()})).collect();
    obs["problems"] = json!(problems);
    put_doc(c, "host-observation", &obs).await?;
    // Dispatch in its simplest case: no subscriptions; a poll of the default host claims every queued agent execution,
    // oldest first. ponytail: ranking over subscriptions and hosts is out of the slice.
    if host.id == st.default_host {
        for mut e in docs(c, "SELECT data FROM execution WHERE host_id IS NULL AND state='preparing' AND kind='agent_run' ORDER BY seq", &[]).await? {
            if e["state"] != "queued" || e["dispatch"].is_null() || e["stopRequested"] == true {
                continue;
            }
            e["hostId"] = json!(host.id);
            let command = json!({"id": uuid(), "executionId": e["id"], "sessionId": e["sessionId"], "hostId": host.id, "kind": "start",
                "payload": {"harness": "codex", "text": e["dispatch"]["input"], "setup": "run", "model": e["model"], "effort": e["effort"], "instructions": INSTRUCTIONS},
                "status": "pending"});
            put_execution(c, &e).await?;
            put_doc(c, "command", &command).await?;
            c.execute("INSERT INTO route_decision(execution_id,project_id,host_id,candidates,winner) VALUES($1,NULL,$2,$3,$4)",
                &[&e["id"].as_str(), &host.id, &json!([{"subscription": null, "host": host.id, "warnings": []}]), &json!({"subscription": null, "host": host.id})]).await?;
            c.execute("UPDATE execution SET wait_reason=NULL WHERE id=$1", &[&e["id"].as_str()]).await?;
        }
    }
    let pending = docs(c, "SELECT data FROM document WHERE kind='command' AND data @> $1 ORDER BY seq", &[&json!({"hostId": host.id, "status": "pending"})]).await?;
    let commands: Vec<contracts::Command> = pending.into_iter().map(serde_json::from_value).collect::<Result<_, _>>().map_err(|e| ApiErr { status: 500, msg: e.to_string(), retry: false })?;
    Ok(serde_json::to_value(contracts::PollReply { commands }).unwrap())
}

fn rank(s: &str) -> u8 {
    match s {
        "pending" => 0,
        "accepted" => 1,
        "started" | "unknown" => 2,
        _ => 3,
    }
}
/// Status receipt: the command and execution move forward only; a repeated terminal receipt changes nothing.
async fn receipt(c: &Client, host: &Host, b: &Value) -> Res {
    owned(c, host, b).await?;
    let Some(mut cmd) = get_doc(c, b["commandId"].as_str().unwrap_or(""), "command").await? else { return fail(404, "Not found") };
    if cmd["executionId"] != b["executionId"] || cmd["hostId"] != b["hostId"] {
        return fail(403, "Command rejected");
    }
    let r: contracts::Receipt = serde_json::from_value(b.clone()).map_err(|_| ApiErr { status: 400, msg: "Invalid status".into(), retry: false })?;
    let status = serde_json::to_value(r.status).unwrap().as_str().unwrap().to_string();
    let old = cmd["status"].as_str().unwrap_or("pending").to_string();
    if rank(&status) < rank(&old) || (rank(&old) == 3 && status != old) {
        return fail(409, "Receipt regression");
    }
    if rank(&old) == 3 {
        return Ok(json!({"ok": true}));
    }
    cmd["status"] = json!(status);
    put_doc(c, "command", &cmd).await?;
    let mut e = get_execution(c, &r.execution_id).await?;
    let final_ = |s: &Value| ["completed", "failed", "stopped"].contains(&s.as_str().unwrap_or(""));
    let was_final = final_(&e["state"]);
    let kind = cmd["kind"].as_str().unwrap_or("");
    if !was_final && kind != "steer" && (kind != "stop" || status == "stopped") {
        e["state"] = json!(status);
    }
    let now = now_ms();
    e["updatedAt"] = json!(now);
    if status == "started" && e["startedAt"].is_null() {
        e["startedAt"] = json!(now);
    }
    if kind != "steer" && final_(&json!(status)) && e["finishedAt"].is_null() {
        e["finishedAt"] = json!(now);
    }
    if let Some(err) = &r.error {
        e["error"] = json!(err);
    }
    put_execution(c, &e).await?;
    Ok(json!({"ok": true}))
}

/// One event batch of one execution, in that execution's scope: every event is checked before any is stored.
async fn events(c: &Client, host: &Host, b: &Value) -> Res {
    owned(c, host, b).await?;
    let batch: contracts::HostEvents = serde_json::from_value(b.clone()).map_err(|_| ApiErr { status: 400, msg: "Invalid events".into(), retry: false })?;
    if batch.events.len() > 1000 {
        return fail(400, "Invalid events");
    }
    let eid = batch.execution_id.as_str();
    let execution = get_execution(c, eid).await?;
    let raw = b["events"].as_array().unwrap();
    let mut prepared: Vec<(&'static str, Value)> = vec![];
    for (ev, rawev) in batch.events.iter().zip(raw) {
        if ev.id.is_empty() || ev.id.encode_utf16().count() > 300 || ev.r#type == "execution.ended" || ev.seq > (1u64 << 53) - 1 {
            return fail(400, "Invalid event identity");
        }
        let id = format!("event:{eid}:{}", ev.id);
        let mut v = rawev.as_object().unwrap().clone();
        v.insert("id".into(), json!(id));
        v.insert("executionId".into(), json!(eid));
        v.insert("sessionId".into(), execution["sessionId"].clone());
        let v = Value::Object(v);
        let previous = docs(c, "SELECT data FROM document WHERE id=$1 AND kind IN ('event','event-delta')", &[&id]).await?;
        if previous.first().is_some_and(|p| *p != v) {
            return fail(409, "Event conflict");
        }
        let seq = ev.seq as i64;
        let taken = c.query_opt("SELECT 1 FROM document WHERE kind IN ('event','event-delta') AND data->>'executionId'=$1 AND data->'seq'=to_jsonb($2::bigint) AND id<>$3 LIMIT 1", &[&eid, &seq, &id]).await?;
        if taken.is_some() {
            return fail(409, "Sequence conflict");
        }
        let delta = !v["data"]["fleetNormalized"]["deltaOf"].is_null();
        prepared.push((if delta { "event-delta" } else { "event" }, v));
    }
    let mut seen: HashMap<i64, &str> = HashMap::new();
    let mut ids: HashMap<&str, &Value> = HashMap::new();
    for (_, v) in &prepared {
        let id = v["id"].as_str().unwrap();
        if ids.insert(id, v).is_some_and(|o| o != v) {
            return fail(409, "Batch event conflict");
        }
        if seen.insert(v["seq"].as_i64().unwrap(), id).is_some_and(|o| o != id) {
            return fail(409, "Batch sequence conflict");
        }
    }
    for (kind, v) in &prepared {
        c.execute("INSERT INTO document(kind,id,data,project_id) VALUES($1,$2,$3,NULL) ON CONFLICT(id) DO NOTHING", &[kind, &v["id"].as_str(), v]).await?;
    }
    Ok(json!({"ok": true}))
}

async fn execution_events(c: &Client, id: &str) -> Res {
    get_execution(c, id).await?;
    let events = docs(c, "SELECT data FROM document WHERE kind='event' AND data->>'executionId'=$1 AND data->>'type' IS DISTINCT FROM 'execution.ended' ORDER BY (data->>'seq')::bigint, seq", &[&id]).await?;
    Ok(json!({"events": events}))
}

// ---------- change stream ----------

const VISIBLE: &str = "xid < pg_snapshot_xmin(pg_current_snapshot())";
async fn head(c: &Client) -> Result<i64, ApiErr> {
    Ok(c.query_opt(&format!("SELECT seq FROM change WHERE {VISIBLE} ORDER BY xid DESC, seq DESC LIMIT 1"), &[]).await?.map(|r| r.get(0)).unwrap_or(0))
}
enum Since {
    Resync(i64),
    Changes(Vec<Value>, bool),
}
/// Changes after the cursor in (xid, seq) order, only from transactions older than every running one. held: committed rows
/// wait behind an older transaction anywhere on the server; no second NOTIFY comes when it ends, so the reader looks again.
async fn since(c: &Client, cursor: i64) -> Result<Since, ApiErr> {
    let rows = if cursor > 0 {
        if c.query_opt("SELECT 1 FROM change WHERE seq=$1", &[&cursor]).await?.is_none() {
            return Ok(Since::Resync(head(c).await?));
        }
        c.query(&format!("SELECT seq, kind, id, task_id, session_id, host_id, op::text FROM change WHERE {VISIBLE} AND (xid, seq) > (SELECT xid, seq FROM change WHERE seq=$1) ORDER BY xid, seq LIMIT 1000"), &[&cursor]).await?
    } else {
        c.query(&format!("SELECT seq, kind, id, task_id, session_id, host_id, op::text FROM change WHERE {VISIBLE} ORDER BY xid, seq LIMIT 1000"), &[]).await?
    };
    let held = rows.len() < 1000 && c.query_opt(&format!("SELECT 1 FROM change WHERE NOT ({VISIBLE}) LIMIT 1"), &[]).await?.is_some();
    let changes = rows.iter().map(|r| json!({"seq": r.get::<_, i64>(0), "kind": r.get::<_, String>(1), "id": r.get::<_, String>(2), "taskId": r.get::<_, Option<String>>(3),
        "sessionId": r.get::<_, Option<String>>(4), "hostId": r.get::<_, Option<String>>(5), "op": r.get::<_, String>(6)})).collect();
    Ok(Since::Changes(changes, held))
}
fn frame(event: Option<&str>, data: &Value, id: Option<i64>) -> Bytes {
    let mut s = String::new();
    if let Some(i) = id {
        s += &format!("id: {i}\n");
    }
    if let Some(e) = event {
        s += &format!("event: {e}\n");
    }
    s += &format!("data: {data}\n\n");
    Bytes::from(s)
}
async fn stream(st: Arc<St>, scope: Option<Scope>, cursor: Option<i64>) -> Result<Response<Body>, ApiErr> {
    // Subscribe before the first read, so a NOTIFY during it is not lost.
    let mut wake = st.wake.subscribe();
    let start = match cursor {
        Some(c) => c,
        None => in_tx!(st.db, Mode::Read, scope.as_ref(), |t| head(&t).await)?,
    };
    let (tx, rx) = mpsc::channel::<Bytes>(64);
    let _ = tx.try_send(frame(Some("ready"), &json!({"cursor": start}), None));
    tokio::spawn(async move {
        let mut cur = start;
        loop {
            let mut held = false;
            loop {
                match in_tx!(st.db, Mode::Read, scope.as_ref(), |t| since(&t, cur).await) {
                    Ok(Since::Resync(h)) => {
                        cur = h;
                        if tx.send(frame(Some("resync"), &json!({"cursor": h}), None)).await.is_err() {
                            return;
                        }
                        break;
                    }
                    Ok(Since::Changes(rows, h)) => {
                        let n = rows.len();
                        for r in rows {
                            cur = r["seq"].as_i64().unwrap();
                            if tx.send(frame(None, &r, Some(cur))).await.is_err() {
                                return;
                            }
                        }
                        held = h;
                        if n < 1000 {
                            break;
                        }
                    }
                    Err(e) => {
                        if tx.send(frame(Some("error"), &json!({"message": e.msg}), None)).await.is_err() {
                            return;
                        }
                        break;
                    }
                }
            }
            tokio::select! {
                _ = tx.closed() => return,
                r = wake.changed() => if r.is_err() { return },
                _ = tokio::time::sleep(std::time::Duration::from_millis(250)), if held => {}
            }
        }
    });
    Ok(Response::builder()
        .header("content-type", "text/event-stream; charset=utf-8")
        .header("cache-control", "no-cache, no-transform")
        .header("x-accel-buffering", "no")
        .body(Body::Stream(rx))
        .unwrap())
}
/// One LISTEN connection per process wakes every open stream. The returned client must stay alive.
async fn listen(st: Arc<St>, url: &str) -> Client {
    let (client, mut conn) = db::config(url).connect(tokio_postgres::NoTls).await.expect("LISTEN connection");
    let wake = st.wake.clone();
    tokio::spawn(async move {
        // ponytail: no reconnect; a lost LISTEN connection ends live wakes until restart.
        while let Some(m) = std::future::poll_fn(|cx| conn.poll_message(cx)).await {
            match m {
                Ok(tokio_postgres::AsyncMessage::Notification(_)) => wake.send_modify(|v| *v += 1),
                Ok(_) => {}
                Err(e) => {
                    eprintln!("LISTEN connection: {e}");
                    break;
                }
            }
        }
    });
    client.batch_execute("LISTEN fleet_change").await.expect("LISTEN");
    client
}

// ---------- HTTP ----------

enum Body {
    Full(Option<Bytes>),
    Stream(mpsc::Receiver<Bytes>),
}
impl hyper::body::Body for Body {
    type Data = Bytes;
    type Error = Infallible;
    fn poll_frame(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Option<Result<Frame<Bytes>, Infallible>>> {
        match self.get_mut() {
            Body::Full(b) => Poll::Ready(b.take().map(|b| Ok(Frame::data(b)))),
            Body::Stream(rx) => rx.poll_recv(cx).map(|o| o.map(|b| Ok(Frame::data(b)))),
        }
    }
}
fn reply(status: u16, v: &Value) -> Response<Body> {
    Response::builder()
        .status(status)
        .header("content-type", "application/json; charset=utf-8")
        .header("x-content-type-options", "nosniff")
        .header("referrer-policy", "no-referrer")
        .body(Body::Full(Some(Bytes::from(v.to_string()))))
        .unwrap()
}

async fn read_body(mut b: Incoming) -> Result<Value, ApiErr> {
    let mut buf = Vec::new();
    while let Some(f) = std::future::poll_fn(|cx| Pin::new(&mut b).poll_frame(cx)).await {
        let f = f.map_err(|_| ApiErr { status: 400, msg: "Bad body".into(), retry: false })?;
        if let Some(d) = f.data_ref() {
            buf.extend_from_slice(d);
            if buf.len() > 1 << 20 {
                return fail(413, "Body too large");
            }
        }
    }
    if buf.iter().all(u8::is_ascii_whitespace) {
        return Ok(Value::Null);
    }
    serde_json::from_slice(&buf).map_err(|_| ApiErr { status: 400, msg: "Invalid JSON".into(), retry: false })
}

/// Credentials (SL1, SL2): /api/hosts/* by the host's own secret only, everything else by the Bearer browser secret and a
/// known person. The two kinds never mix.
async fn authorize(st: &St, req: &Request<Incoming>, path: &str) -> Result<Who, ApiErr> {
    let auth = req.headers().get("authorization").and_then(|v| v.to_str().ok()).unwrap_or("");
    let supplied = auth.strip_prefix("Bearer ");
    let named = req.headers().get("x-fleet-person");
    if path.starts_with("/api/hosts/") {
        let Some(secret) = supplied.filter(|s| !s.is_empty()) else { return fail(401, "Unauthorized") };
        let c = st.db.conn().await?;
        let h = c.query_opt("SELECT data FROM document WHERE kind='host' AND data->>'secretHash'=$1", &[&sha256_hex(secret)]).await?;
        let Some(h) = h.map(|r| r.get::<_, Value>(0)) else { return fail(401, "Unauthorized") };
        let holder = h["holder"].as_str().unwrap_or("").to_string();
        if !st.people.contains_key(&holder) {
            return fail(401, "Unauthorized");
        }
        if named.is_some() {
            return fail(403, "Host requests carry no person");
        }
        return Ok(Who::Host(Host { id: h["hostId"].as_str().unwrap_or("").into(), holder }));
    }
    if !supplied.is_some_and(|s| equal(s, &st.secret)) {
        return fail(401, "Unauthorized");
    }
    let Some(email) = named.and_then(|v| v.to_str().ok()).filter(|s| !s.is_empty()) else { return fail(401, "Person required") };
    match st.people.get(&email.to_lowercase()) {
        Some(p) => Ok(Who::Person(p.clone())),
        None => fail(403, "Unknown person"),
    }
}

/// `Number(raw)` as a safe non-negative integer; absent or empty is no cursor.
fn parse_cursor(query: &str) -> Result<Option<i64>, ApiErr> {
    let Some(raw) = query.split('&').filter_map(|kv| kv.split_once('=').or(Some((kv, "")))).find(|(k, _)| *k == "cursor").map(|x| x.1) else { return Ok(None) };
    if raw.is_empty() {
        return Ok(None);
    }
    match raw.trim().parse::<f64>() {
        Ok(n) if n.fract() == 0.0 && (0.0..=9007199254740991.0).contains(&n) => Ok(Some(n as i64)),
        _ => fail(400, "Invalid cursor"),
    }
}

async fn route(st: Arc<St>, req: Request<Incoming>) -> Result<Response<Body>, ApiErr> {
    let path = req.uri().path().to_string();
    let query = req.uri().query().unwrap_or("").to_string();
    let method = req.method().as_str().to_string();
    let who = authorize(&st, &req, &path).await?;
    let mut body = read_body(req.into_body()).await?;
    // The body hostId of a host request is the host its secret names: a different one is 403, a missing one is filled in.
    if let (Who::Host(h), Some(o)) = (&who, body.as_object_mut()) {
        if o.get("hostId").is_some_and(|v| v.as_str() != Some(h.id.as_str())) {
            return fail(403, "Host rejected");
        }
        o.insert("hostId".into(), json!(h.id));
    }
    let scope = scope_of(&who);
    let sc = scope.as_ref();
    let seg: Vec<&str> = path.trim_start_matches('/').split('/').collect();
    let host = || match &who {
        Who::Host(h) => Ok(h),
        _ => fail(403, "Host route"),
    };
    let w = || Mode::Write(GLOBAL.into());
    let v = match (method.as_str(), seg.as_slice()) {
        ("GET", ["api", "stream"]) => return stream(st.clone(), scope.clone(), parse_cursor(&query)?).await,
        ("POST", ["api", "ops", "hosts_register"]) => in_tx!(st.db, w(), sc, |t| hosts_register(&t, &st, &who, &body).await),
        ("POST", ["api", "sessions"]) => in_tx!(st.db, w(), sc, |t| session_create(&t, &who).await),
        ("GET", ["api", "sessions", id]) => in_tx!(st.db, Mode::Read, sc, |t| session_get(&t, id).await),
        ("POST", ["api", "sessions", id, "messages"]) => in_tx!(st.db, w(), sc, |t| message(&t, id, &body).await),
        ("GET", ["api", "executions", id, "events"]) => in_tx!(st.db, Mode::Read, sc, |t| execution_events(&t, id).await),
        ("POST", ["api", "hosts", "poll"]) => {
            let h = host()?;
            in_tx!(st.db, w(), sc, |t| poll(&t, &st, h, &body).await)
        }
        ("POST", ["api", "hosts", "receipts"]) => {
            let h = host()?;
            in_tx!(st.db, w(), sc, |t| receipt(&t, h, &body).await)
        }
        ("POST", ["api", "hosts", "events"]) => {
            let h = host()?;
            // Host ingest of one execution runs in that execution's scope, in parallel with other executions and GLOBAL writers.
            let Some(eid) = body["executionId"].as_str() else { return fail(400, "Expected executionId") };
            in_tx!(st.db, Mode::Write(format!("execution:{eid}")), sc, |t| events(&t, h, &body).await)
        }
        _ => fail(404, "Not found"),
    }?;
    Ok(reply(200, &v))
}

#[tokio::main]
async fn main() {
    let env = |k: &str| std::env::var(k).ok().filter(|v| !v.is_empty());
    let people_file = env("FLEET_PEOPLE_FILE").expect("FLEET_PEOPLE_FILE required");
    let database = env("FLEET_DATABASE_URL").expect("FLEET_DATABASE_URL required");
    let secret = env("FLEET_BROWSER_SECRET").expect("Browser credential required");
    // The schema is compiled in: the binary needs no checkout next to it.
    let schema = include_str!("../../../packages/coordinator/db/schema.sql");
    if let Err(e) = db::migrate(&env("FLEET_MIGRATION_DATABASE_URL").unwrap_or(database.clone()), schema).await {
        panic!("migration failed: {}", e.msg)
    }
    let st = Arc::new(St {
        db: Db::new(db::config(&database), 16),
        people: load_people(&people_file),
        secret,
        default_host: env("FLEET_HOST_ID").unwrap_or("owner-mac".into()),
        wake: watch::channel(0).0,
    });
    mirror_people(&st).await.unwrap_or_else(|e| panic!("people: {}", e.msg));
    let _listener = listen(st.clone(), &database).await;
    let port: u16 = env("PORT").map(|p| p.parse().expect("PORT")).unwrap_or(8787);
    let listener = tokio::net::TcpListener::bind(("127.0.0.1", port)).await.expect("bind");
    loop {
        let Ok((sock, _)) = listener.accept().await else { continue };
        let st = st.clone();
        tokio::spawn(async move {
            let svc = hyper::service::service_fn(move |req| {
                let st = st.clone();
                async move {
                    Ok::<_, Infallible>(match route(st, req).await {
                        Ok(r) => r,
                        Err(e) => reply(e.status, &json!({"error": e.msg})),
                    })
                }
            });
            let _ = hyper::server::conn::http1::Builder::new().serve_connection(hyper_util::rt::TokioIo::new(sock), svc).await;
        });
    }
}
