//! fleet-stack L4: newline JSON-RPC, shared durable journal; no native/model calls here.
use rusqlite::{Connection, params};
use serde_json::{Value, json};
use std::{
    fs,
    io::{BufRead, Write},
    path::Path,
    time::{Duration, Instant},
};
type Result<T> = std::result::Result<T, Box<dyn std::error::Error>>;
const PROJECT: &[&str] = &[
    "project_context",
    "catalogue",
    "pipeline_run",
    "direct_search",
    "pipeline_history",
    "pipeline_get",
    "question_ask",
    "question_history",
    "account_list",
];
const AGENT: &[&str] = &[
    "session_start",
    "session_deliver",
    "session_transition",
    "session_fork",
    "session_resume",
    "session_handoffs",
    "message_send",
    "queue_list",
    "ask",
    "reply",
    "questions",
    "worker_done",
    "execution_status",
];
const ARTIFACT: &[&str] = &["artifact_save", "artifact_list", "artifact_get"];
fn truth(v: &Value) -> bool {
    !v.is_null()
        && v.as_f64() != Some(0.0)
        && v != false
        && v != ""
        && v.as_array().is_none_or(|a| !a.is_empty())
        && v.as_object().is_none_or(|o| !o.is_empty())
}
fn text(v: Value, error: Option<bool>) -> Value {
    let mut r = json!({"content":[{"type":"text","text":v.to_string()}]});
    if let Some(e) = error {
        r["isError"] = json!(e)
    }
    r
}
fn tools(ctx: &Value) -> Value {
    let all: Vec<Value> = serde_json::from_str(include_str!("mcp-tools.json")).unwrap();
    json!({"tools":all.into_iter().filter_map(|mut t| {
        let n=t["name"].as_str().unwrap();
        if n=="request_child" { if !truth(&ctx["profiles"]) {return None} t["inputSchema"]["properties"]["profileId"]["enum"]=ctx["profiles"].clone(); }
        else if PROJECT.contains(&n) && !truth(&ctx["projectId"]) || ARTIFACT.contains(&n) && !truth(&ctx["workdir"]) {return None}
        Some(t)
    }).collect::<Vec<_>>()})
}
fn wait(
    db: &Connection,
    table: &str,
    key: &str,
    seconds: u64,
    artifact: bool,
) -> Result<Option<Value>> {
    let end = Instant::now() + Duration::from_secs(seconds);
    while Instant::now() < end {
        let sql = if table == "transfers" {
            "SELECT response,state FROM transfers WHERE id=?"
        } else {
            "SELECT response,'' FROM delegation_requests WHERE id=?"
        };
        let row = db.query_row(sql, [key], |r| {
            Ok((r.get::<_, Option<String>>(0)?, r.get::<_, String>(1)?))
        });
        match row {
            Ok((Some(raw), state)) if !raw.is_empty() => {
                let v: Value = serde_json::from_str(&raw)?;
                if table != "transfers"
                    || state == "failed"
                    || (if artifact {
                        v.get("artifactId").is_some()
                    } else {
                        state == "done"
                    })
                {
                    return Ok(Some(v));
                }
            }
            Err(rusqlite::Error::QueryReturnedNoRows) | Ok(_) => {}
            Err(e) => return Err(e.into()),
        }
        std::thread::sleep(Duration::from_millis(if table == "transfers" {
            100
        } else {
            50
        }));
    }
    Ok(None)
}
fn save(ctx: &Value, q: &Value, db: &mut Connection) -> Result<Value> {
    use std::os::{
        fd::{AsRawFd, FromRawFd},
        unix::{
            ffi::OsStrExt,
            fs::{MetadataExt, OpenOptionsExt},
        },
    };
    let path = q["path"]
        .as_str()
        .ok_or("path must be a file inside your working area")?;
    let parts: Vec<_> = path
        .split('/')
        .filter(|p| !p.is_empty() && *p != ".")
        .collect();
    if parts.is_empty() || parts.contains(&"..") {
        return Err("path must be a file inside your working area".into());
    }
    let refused = "path must be a regular file inside your working area, not a link";
    let mut dir = fs::OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_DIRECTORY)
        .open(ctx["workdir"].as_str().ok_or("workdir")?)
        .map_err(|_| refused)?;
    for (i, p) in parts.iter().enumerate() {
        let c = std::ffi::CString::new(Path::new(p).as_os_str().as_bytes()).map_err(|_| refused)?;
        let fd = unsafe {
            libc::openat(
                dir.as_raw_fd(),
                c.as_ptr(),
                libc::O_RDONLY
                    | libc::O_NOFOLLOW
                    | if i + 1 < parts.len() {
                        libc::O_DIRECTORY
                    } else {
                        0
                    },
            )
        };
        if fd < 0 {
            return Err(refused.into());
        }
        dir = unsafe { fs::File::from_raw_fd(fd) };
    }
    let meta = dir.metadata()?;
    if !meta.is_file() || meta.nlink() != 1 {
        return Err("path must be a regular file with one link".into());
    }
    let key = crate::new_id();
    let spool = Path::new(ctx["spool"].as_str().ok_or("spool")?).join(&key);
    fs::create_dir_all(spool.parent().unwrap())?;
    let mut out = fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .open(&spool)?;
    std::io::copy(&mut dir, &mut out)?;
    let name = parts.join("/");
    let body = json!({"path":spool,"spool":true,"previews":true,"name":if truth(&q["name"]) {q["name"].clone()} else {json!(name)},"mediaType":if truth(&q["media_type"]) {q["media_type"].clone()} else {json!(mime(&name))},"executionId":ctx["executionId"],"required":q["required"]==true});
    let tx = db.transaction()?;
    tx.execute(
        "INSERT INTO transfers(id,kind,body,created_at) VALUES(?,'upload',?,?)",
        params![
            key,
            body.to_string(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)?
                .as_millis() as i64
        ],
    )?;
    if body["required"] == true {
        tx.execute_batch(
            "CREATE TABLE IF NOT EXISTS outbox(route TEXT NOT NULL,body TEXT NOT NULL)",
        )?;
        tx.execute("INSERT INTO outbox(route,body) VALUES('/api/hosts/artifacts/declare',?)",[json!({"hostId":ctx["hostId"],"executionId":ctx["executionId"],"name":body["name"]}).to_string()])?;
    }
    tx.commit()?;
    Ok(wait(db, "transfers", &key, 30, true)?
        .unwrap_or(json!({"artifactId":key,"state":"pending"})))
}
fn extension(path: &str) -> &str {
    let name = path.rsplit('/').next().unwrap_or(path);
    match name.rfind('.') {
        Some(i) if name[..i].bytes().any(|b| b != b'.') => &name[i..],
        _ => "",
    }
}
fn mime(name: &str) -> String {
    // Captured Python mimetypes table keeps the former adapter's defaults without host Python.
    static TYPES: std::sync::OnceLock<Value> = std::sync::OnceLock::new();
    let maps = TYPES.get_or_init(|| serde_json::from_str(include_str!("mcp-mime.json")).unwrap());
    let mut path = name.to_string();
    loop {
        let ext = extension(&path);
        let Some(suffix) = maps["suffixes"][ext.to_ascii_lowercase()].as_str() else {
            break;
        };
        path = format!("{}{suffix}", &path[..path.len() - ext.len()]);
    }
    let mut ext = extension(&path);
    if maps["encodings"].get(ext).is_some() {
        let end = path.len() - ext.len();
        ext = extension(&path[..end])
    }
    maps["types"][ext.to_ascii_lowercase()]
        .as_str()
        .unwrap_or("application/octet-stream")
        .to_string()
}
fn artifact(ctx: &Value, name: &str, q: &Value, db: &mut Connection) -> Result<Value> {
    let spec = tools(ctx)["tools"]
        .as_array()
        .unwrap()
        .iter()
        .find(|t| t["name"] == name)
        .unwrap()["inputSchema"]
        .clone();
    if q.as_object().is_none_or(|o| {
        o.iter()
            .any(|(k, v)| spec["properties"].get(k).is_none() || !(v.is_string() || v.is_boolean()))
            || spec["required"]
                .as_array()
                .unwrap()
                .iter()
                .any(|k| !o.contains_key(k.as_str().unwrap()))
    }) {
        return Err("Invalid artifact tool arguments".into());
    }
    let result = if name == "artifact_save" {
        save(ctx, q, db)
    } else {
        let key = crate::new_id();
        let (kind, body, seconds) = if name == "artifact_list" {
            (
                "list",
                json!({"executionId":ctx["executionId"],"filter":{"executionId":q["execution_id"],"taskId":q["task_id"],"mediaType":q["media_type"]}}),
                20,
            )
        } else {
            (
                "get",
                json!({"executionId":ctx["executionId"],"artifactId":q["artifact_id"],"inputDir":ctx["inputDir"]}),
                600,
            )
        };
        db.execute(
            "INSERT INTO transfers(id,kind,body,created_at) VALUES(?,?,?,?)",
            params![
                key,
                kind,
                body.to_string(),
                std::time::SystemTime::now()
                    .duration_since(std::time::UNIX_EPOCH)?
                    .as_millis() as i64
            ],
        )?;
        Ok(match wait(db, "transfers", &key, seconds, false)? {
            Some(mut r) => {
                r["transferId"] = json!(key);
                r
            }
            None => {
                json!({"state":"pending","transferId":key,"instruction":"Still in progress on this host; list artifacts later."})
            }
        })
    };
    Ok(match result {
        Ok(v) => text(v, None),
        Err(e) if e.is::<rusqlite::Error>() => return Err(e),
        Err(e) => {
            let message = if e.is::<std::io::Error>() {
                "path must be a regular file inside your working area, not a link".into()
            } else {
                e.to_string()
            };
            text(
                json!({"error":format!("artifact tool refused: {message}")}),
                Some(true),
            )
        }
    })
}
fn delegation(req: &Value, ctx: &Value, db: &mut Connection) -> Result<Value> {
    let method = req["method"].as_str().unwrap_or("");
    let p = &req["params"];
    match method {
        "initialize" => {
            return Ok(
                json!({"protocolVersion":p.get("protocolVersion").cloned().unwrap_or(json!("2024-11-05")),"capabilities":{"tools":{}},"serverInfo":{"name":"fleet-delegation","version":"1"}}),
            );
        }
        "ping" => return Ok(json!({})),
        "tools/list" => return Ok(tools(ctx)),
        _ => {}
    }
    let name = p["name"].as_str().unwrap_or("");
    if method != "tools/call" {
        return Err("Unsupported method".into());
    }
    let q = if truth(&p["arguments"]) {
        p["arguments"].clone()
    } else {
        json!({})
    };
    if ARTIFACT.contains(&name) && truth(&ctx["workdir"]) {
        return artifact(ctx, name, &q, db);
    }
    let agent = AGENT.contains(&name);
    let (key, body) = if agent {
        let key = format!(
            "{}:agent:{}",
            ctx["commandId"].as_str().ok_or("'commandId'")?,
            crate::new_id()
        );
        (
            key.clone(),
            json!({"hostId":ctx["hostId"],"commandId":ctx["commandId"],"executionId":ctx["executionId"],"agentToolKey":key,"request":{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":name,"arguments":q}}}),
        )
    } else {
        let child = name == "request_child";
        if !child && !(truth(&ctx["projectId"]) && PROJECT.contains(&name)) {
            return Err("Unsupported method".into());
        }
        let delegation = if child {
            if q.as_object().is_none_or(|o| {
                o.len() != 3
                    || !["requestId", "profileId", "input"]
                        .iter()
                        .all(|k| o.contains_key(*k))
            }) || !ctx["profiles"]
                .as_array()
                .is_some_and(|a| a.contains(&q["profileId"]))
                || [("requestId", 200), ("input", 32000)].iter().any(|(k, n)| {
                    q[k].as_str()
                        .is_none_or(|s| s.is_empty() || s.chars().count() > *n)
                })
            {
                return Err("Invalid child request".into());
            }
            q
        } else {
            if q.as_object().is_none_or(|o| {
                o.keys().any(|k| {
                    !["requestId", "query", "methodId", "text", "runId"].contains(&k.as_str())
                })
            }) {
                return Err("Invalid project request".into());
            }
            let effect = ["pipeline_run", "direct_search", "question_ask"].contains(&name);
            if effect && !q["requestId"].is_string() {
                return Err("A stable requestId is required for effects".into());
            }
            json!({"operation":name,"arguments":q,"requestId":if effect {q["requestId"].clone()} else {json!(format!("{name}-{}",crate::new_id()))},"projectId":ctx["projectId"]})
        };
        (
            format!(
                "{}:{}",
                ctx["commandId"].as_str().ok_or("'commandId'")?,
                delegation["requestId"]
                    .as_str()
                    .ok_or("Invalid child request")?
            ),
            json!({"hostId":ctx["hostId"],"commandId":ctx["commandId"],"executionId":ctx["executionId"],"delegation":delegation}),
        )
    };
    let body = if !agent {
        if name == "request_child" {
            let receipt = crate::contracts::ChildReceipt {
                host_id: ctx["hostId"].as_str().ok_or("'hostId'")?.into(),
                command_id: ctx["commandId"].as_str().ok_or("'commandId'")?.into(),
                execution_id: ctx["executionId"].as_str().ok_or("'executionId'")?.into(),
                delegation: crate::contracts::ChildRequest {
                    request_id: body["delegation"]["requestId"].as_str().unwrap().into(),
                    profile_id: body["delegation"]["profileId"].as_str().unwrap().into(),
                    input: body["delegation"]["input"].as_str().unwrap().into(),
                },
            };
            serde_json::to_value(receipt)?
        } else {
            serde_json::to_value(crate::contracts::ProjectReceipt {
                host_id: ctx["hostId"].as_str().ok_or("'hostId'")?.into(),
                command_id: ctx["commandId"].as_str().ok_or("'commandId'")?.into(),
                execution_id: ctx["executionId"].as_str().ok_or("'executionId'")?.into(),
                delegation: crate::contracts::ProjectRequest {
                    operation: serde_json::from_value(json!(name))?,
                    arguments: serde_json::from_value(body["delegation"]["arguments"].clone())?,
                    request_id: body["delegation"]["requestId"].as_str().unwrap().into(),
                    project_id: ctx["projectId"].as_str().ok_or("'projectId'")?.into(),
                },
            })?
        }
    } else {
        body
    };
    // Acquire the writer before reading: a deferred WAL read cannot upgrade after a concurrent host reply.
    let tx = db.transaction_with_behavior(rusqlite::TransactionBehavior::Immediate)?;
    let old = tx.query_row(
        "SELECT body FROM delegation_requests WHERE id=?",
        [&key],
        |r| r.get::<_, String>(0),
    );
    match old {
        Ok(old) => {
            if serde_json::from_str::<Value>(&old)? != body {
                return Err("Request identity conflict".into());
            }
        }
        Err(rusqlite::Error::QueryReturnedNoRows) => {
            tx.execute(
                "INSERT INTO delegation_requests(id,body) VALUES(?,?)",
                params![key, body.to_string()],
            )?;
            tx.execute(
                "INSERT INTO outbox(route,body) VALUES(?,?)",
                params![
                    if agent {
                        "/api/hosts/agent-tools"
                    } else {
                        "/api/hosts/receipts"
                    },
                    body.to_string()
                ],
            )?;
        }
        Err(e) => return Err(e.into()),
    }
    tx.commit()?;
    let response = wait(
        db,
        "delegation_requests",
        &key,
        if agent { 20 } else { 8 },
        false,
    )?;
    if agent {
        return Ok(match response {Some(r) if r.get("result").is_some()=>r["result"].clone(),r=>text(r.unwrap_or(json!({"status":"pending","instruction":"Outcome unknown; inspect with execution_status before repeating effects."})),Some(true))});
    }
    let r = response.unwrap_or(Value::Null);
    let error = truth(&r) && r.get("error").is_some();
    Ok(text(
        if name == "request_child" {
            json!({"status":if truth(&r)&&r.get("childExecutionId").is_some(){"accepted"}else{"pending-or-rejected"},"response":r,"instruction":"Finish this turn. Child work runs after your completed receipt. Retry only with the same requestId; submission is not completion."})
        } else {
            r
        },
        Some(error),
    ))
}
// Preserve the Python adapter's JSON decoder diagnostics on the newline transport.
fn parse_error(raw: &str, error: &serde_json::Error) -> String {
    let detail = error.to_string();
    let prefix: usize = raw
        .split_inclusive('\n')
        .take(error.line().saturating_sub(1))
        .map(str::len)
        .sum();
    let mut offset = if error.column() == 0 {
        prefix.saturating_sub(1)
    } else {
        prefix + error.column() - 1
    }
    .min(raw.len());
    let message = if detail.starts_with("EOF while parsing a string") {
        let mut start = 0;
        let mut quoted = false;
        let mut escaped = false;
        for (i, c) in raw.char_indices() {
            if escaped {
                escaped = false;
                continue;
            }
            if c == '\\' && quoted {
                escaped = true;
                continue;
            }
            if c == '"' {
                if !quoted {
                    start = i
                }
                quoted = !quoted
            }
        }
        offset = start;
        "Unterminated string starting at"
    } else if detail.starts_with("EOF") {
        offset = raw.len();
        if raw.trim_end().ends_with(['{', ',']) && !detail.contains("list") {
            "Expecting property name enclosed in double quotes"
        } else if raw.trim_end().ends_with(['[', ':', ',']) || detail.contains("value") {
            "Expecting value"
        } else {
            "Expecting ',' delimiter"
        }
    } else if detail.starts_with("key must be a string")
        || detail.starts_with("trailing comma") && raw.as_bytes().get(offset) == Some(&b'}')
    {
        "Expecting property name enclosed in double quotes"
    } else if detail.starts_with("trailing characters") {
        "Extra data"
    } else if detail.starts_with("expected `:`") {
        "Expecting ':' delimiter"
    } else if detail.starts_with("expected `,`") {
        "Expecting ',' delimiter"
    } else if detail.starts_with("control character") {
        "Invalid control character at"
    } else if detail.starts_with("invalid escape") {
        offset = offset.saturating_sub(1);
        "Invalid \\escape"
    } else if detail.starts_with("invalid number") {
        if raw
            .as_bytes()
            .get(offset)
            .is_some_and(|b| !b.is_ascii_digit())
        {
            offset = offset.saturating_sub(1)
        }
        "Expecting ',' delimiter"
    } else {
        "Expecting value"
    };
    while !raw.is_char_boundary(offset) {
        offset = offset.saturating_sub(1)
    }
    let position = raw[..offset].chars().count();
    let line = raw[..offset].bytes().filter(|b| *b == b'\n').count() + 1;
    let column = raw[..offset]
        .rsplit('\n')
        .next()
        .unwrap_or("")
        .chars()
        .count()
        + 1;
    format!("{message}: line {line} column {column} (char {position})")
}

pub fn run() -> Result<()> {
    let args: Vec<String> = std::env::args().skip(2).collect();
    let server = args.first().ok_or("MCP server required")?;
    let (ctx, mut db) = match server.as_str() {
        "design-rules" => (
            serde_json::from_str::<Value>(args.get(1).ok_or("rules required")?)?,
            None,
        ),
        "delegation" => {
            let db = Connection::open(args.get(1).ok_or("journal required")?)?;
            db.busy_timeout(Duration::from_secs(5))?;
            db.execute_batch("PRAGMA synchronous=FULL")?;
            (
                serde_json::from_str::<Value>(args.get(2).ok_or("context required")?)?,
                Some(db),
            )
        }
        _ => return Err("unknown MCP server".into()),
    };
    let mut stdout = std::io::stdout().lock();
    let stdin = std::io::stdin();
    let mut input = stdin.lock();
    let mut raw = String::new();
    loop {
        raw.clear();
        if input.read_line(&mut raw)? == 0 {
            break;
        }
        let parsed = serde_json::from_str::<Value>(&raw);
        let req = match parsed {
            Ok(v) => v,
            Err(error) => {
                let (code, message) = if server == "design-rules" {
                    (-32700, "Invalid request".to_string())
                } else {
                    (-32602, parse_error(&raw, &error))
                };
                writeln!(
                    stdout,
                    "{}",
                    json!({"jsonrpc":"2.0","id":null,"error":{"code":code,"message":message}})
                )?;
                stdout.flush()?;
                continue;
            }
        };
        if req.get("id").is_none() {
            continue;
        }
        let result = if let Some(db) = db.as_mut() {
            delegation(&req, &ctx, db)
        } else {
            let p = &req["params"];
            match req["method"].as_str() {
                Some("initialize") => Ok(
                    json!({"protocolVersion":p.get("protocolVersion").cloned().unwrap_or(json!("2024-11-05")),"capabilities":{"tools":{}},"serverInfo":{"name":"fleet-design-rules","version":"1.0.0"}}),
                ),
                Some("ping") => Ok(json!({})),
                Some("tools/list") => Ok(
                    json!({"tools":[{"name":"get_design_rules","description":"Read project website design rules.","inputSchema":{"type":"object","properties":{},"additionalProperties":false},"annotations":{"readOnlyHint":true,"destructiveHint":false,"idempotentHint":true}}]}),
                ),
                Some("tools/call")
                    if p["name"] == "get_design_rules"
                        && p.get("arguments").is_none_or(|a| a == &json!({})) =>
                {
                    Ok(text(ctx.clone(), Some(false)))
                }
                _ => Err("Unsupported method, tool or arguments".into()),
            }
        };
        let mut reply = json!({"jsonrpc":"2.0","id":req["id"]});
        match result {
            Ok(v) => reply["result"] = v,
            Err(e) => {
                reply["error"] = json!({"code":if server=="design-rules"{-32601}else{-32602},"message":e.to_string()})
            }
        }
        writeln!(stdout, "{reply}")?;
        stdout.flush()?;
    }
    Ok(())
}
