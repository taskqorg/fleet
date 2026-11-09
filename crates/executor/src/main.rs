mod antigravity;
mod codex;
mod harness;
mod lab;
mod native;
use base64::Engine;
use rusqlite::{Connection, params};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{collections::HashMap, os::fd::AsRawFd, sync::mpsc};
use std::{
    env, fs,
    io::{BufRead, BufReader, Read, Write},
    path::PathBuf,
    process::{Command, Stdio},
    time::Duration,
};
static SHUTDOWN: std::sync::atomic::AtomicBool = std::sync::atomic::AtomicBool::new(false);
extern "C" fn shutdown(_: i32) {
    SHUTDOWN.store(true, std::sync::atomic::Ordering::Relaxed);
}
type Result<T> = std::result::Result<T, Box<dyn std::error::Error>>;
fn id(v: &Value, k: &str) -> Result<String> {
    let s = v[k].as_str().ok_or("missing identity")?;
    if s.is_empty()
        || !s
            .bytes()
            .all(|c| c.is_ascii_alphanumeric() || b"_-".contains(&c))
    {
        return Err("invalid identity".into());
    }
    Ok(s.into())
}
enum Protocol {
    Codex(codex::Session),
    Antigravity(antigravity::Session),
}
impl Protocol {
    fn initialize(&self) -> Value {
        match self {
            Self::Codex(_) => codex::Session::initialize(),
            Self::Antigravity(_) => antigravity::Session::initialize(),
        }
    }
    fn receive(
        &mut self,
        v: &Value,
    ) -> std::result::Result<(Vec<Value>, Option<Value>, bool), String> {
        match self {
            Self::Codex(s) => s.receive(v),
            Self::Antigravity(s) => s.receive(v),
        }
    }
}
struct Executor {
    db: Connection,
    root: PathBuf,
    url: String,
    token: String,
    host: String,
    live: HashMap<String, Live>,
}
struct Wire {
    protocol: Option<Protocol>,
    stdin: Option<std::process::ChildStdin>,
    log: Option<fs::File>,
}
struct Live {
    wire: Option<std::sync::Arc<std::sync::Mutex<Wire>>>,
    child: std::process::Child,
    command: Value,
    native: Option<String>,
    seq: u64,
    completed: bool,
    stopped: bool,
    owned: HashMap<u32, String>,
    exit: Option<std::process::ExitStatus>,
    cleanup_unknown: bool,
    rx: mpsc::Receiver<String>,
    eof: bool,
    last_sample: std::time::Instant,
}
impl Executor {
    fn post(&self, route: &str, v: Value) -> Result<Value> {
        Ok(ureq::AgentBuilder::new()
            .timeout(Duration::from_secs(5))
            .build()
            .post(&format!("{}{route}", self.url))
            .set("Authorization", &format!("Bearer {}", self.token))
            .send_json(v)?
            .into_json()?)
    }
    fn queue(&self, route: &str, v: Value) -> Result<()> {
        self.db.execute(
            "INSERT INTO outbox(route,body) VALUES(?1,?2)",
            params![route, v.to_string()],
        )?;
        Ok(())
    }
    fn flush(&self) -> Result<()> {
        let mut stmt = self
            .db
            .prepare("SELECT rowid,route,body FROM outbox ORDER BY rowid")?;
        let rows = stmt.query_map([], |r| {
            Ok((
                r.get::<_, i64>(0)?,
                r.get::<_, String>(1)?,
                r.get::<_, String>(2)?,
            ))
        })?;
        for r in rows {
            let (n, p, b) = r?;
            let value: Value = serde_json::from_str(&b)?;
            let response = match self.post(&p, value.clone()) {
                Ok(response) => response,
                Err(error) => {
                    if (value.get("delegation").is_some() || value.get("agentToolKey").is_some()) && error.downcast_ref::<ureq::Error>().is_some_and(|e| matches!(e, ureq::Error::Status(code, _) if (400..500).contains(code))) {
                        json!({"error":"Coordinator rejected delegation; inspect retained command and grant"})
                    } else { return Err(error); }
                }
            };
            if let Some(k) = value["agentToolKey"].as_str() {
                self.db.execute(
                    "UPDATE delegation_requests SET response=? WHERE id=?",
                    params![response.to_string(), k],
                )?;
            }
            if let Some(q) = value.get("delegation") {
                let key = format!(
                    "{}:{}",
                    value["commandId"].as_str().unwrap_or(""),
                    q["requestId"].as_str().unwrap_or("")
                );
                self.db.execute(
                    "UPDATE delegation_requests SET response=? WHERE id=?",
                    params![response.to_string(), key],
                )?;
            }
            self.db.execute("DELETE FROM outbox WHERE rowid=?", [n])?;
        }
        Ok(())
    }
    fn receipt(&self, c: &Value, status: &str, native: Option<&str>) -> Result<()> {
        self.queue("/api/hosts/receipts",json!({"hostId":self.host,"commandId":c["id"],"executionId":c["executionId"],"status":status,"nativeSessionId":native}))
    }
    fn run(&mut self, c: Value) -> Result<()> {
        let cid = id(&c, "id")?;
        let execution = id(&c, "executionId")?;
        let session = id(&c, "sessionId")?;
        let body = c.to_string();
        let old = self
            .db
            .query_row("SELECT body FROM commands WHERE id=?", [&cid], |r| {
                r.get::<_, String>(0)
            });
        if let Ok(old) = old {
            if old != body {
                return Err("command identity conflict".into());
            }
            return Ok(());
        }
        let tx = self.db.unchecked_transaction()?;
        tx.execute(
            "INSERT INTO commands(id,body) VALUES(?1,?2)",
            params![cid, body],
        )?;
        self.receipt(&c, "accepted", None)?;
        tx.commit()?;
        if c["kind"] == "stop" {
            let result = if let Some(l) = self.live.get_mut(&execution) {
                l.stopped = true;
                if l.cleanup().is_ok() {
                    "accepted"
                } else {
                    l.cleanup_unknown = true;
                    "unknown"
                }
            } else {
                "unknown"
            };
            self.receipt(&c, result, None)?;
            if result == "unknown" {
                self.db
                    .execute("UPDATE commands SET terminal=1 WHERE id=?", [cid])?;
            }
            return Ok(());
        }
        if c["kind"] == "steer" {
            let sent = self.live.get(&execution).and_then(|l| l.wire.clone()).and_then(|w| {
                let mut w = w.lock().ok()?;
                let Wire { protocol, stdin, log } = &mut *w;
                let Some(Protocol::Codex(s)) = protocol.as_mut() else { return None };
                let frame = s.steer(c["payload"]["text"].as_str()?, &cid)?;
                codex::write_frame(stdin.as_mut()?, log.as_mut()?, &frame).ok()
            });
            return match sent {
                Some(_) => Ok(()),
                None => self.finish(&c, "failed", None),
            };
        }
        if c["kind"] != "start" && c["kind"] != "input" {
            return self.finish(&c, "failed", None);
        }
        if self
            .live
            .values()
            .any(|l| l.command["sessionId"] == session)
        {
            return self.finish(&c, "failed", None);
        }
        let p = &c["payload"];
        if harness::name(p) == "codex" && (p["model"] != "gpt-6.1-sol" || p["effort"] != "low") {
            return self.finish(&c, "failed", None);
        }
        if p.get("agentConfig")
            .is_some_and(|v| lab::validate(v).is_err())
        {
            return self.finish(&c, "failed", None);
        }
        let dir = self.root.join("sessions").join(&session);
        fs::create_dir_all(&dir)?;
        if fs::symlink_metadata(&dir)?.file_type().is_symlink()
            || !dir.canonicalize()?.starts_with(self.root.canonicalize()?)
        {
            return self.finish(&c, "failed", None);
        }
        fs::write(
            dir.join(".fleet-instructions.tmp"),
            p["instructions"].as_str().unwrap_or(""),
        )?;
        fs::rename(dir.join(".fleet-instructions.tmp"), dir.join("AGENTS.md"))?;
        if p.get("agentConfig").is_none() && harness::name(p) == "codex" {
            self.event(&c,0,"preparation",json!({"model":"gpt-6.1-sol","effort":"low","subscription":true,"instructionsSha256":format!("{:x}",Sha256::digest(p["instructions"].as_str().unwrap_or("").as_bytes())),"tools":{"shell":"workspace-write, network disabled, approvals denied","mcp":[],"subagents":false},"skills":[],"repairs":false,"nativeConfig":"Fleet-owned HOME/CODEX_HOME required; config and execpolicy ignored; AGENTS discovery, skills, memories and hooks disabled; context observations pending"}))?;
        }
        // The coordinator owns the native id per harness:account continuation; no id means a new native session
        // (a handoff to another harness must not resume the previous harness's thread).
        let native = p["nativeSessionId"].as_str().map(str::to_owned);
        if native.as_deref().is_some_and(|n| !harness::safe_native_id(n)) {
            self.event(&c, 1, "preparation.failed", json!({"reason":"invalid native session id"}))?;
            return self.finish(&c, "failed", None);
        }
        let lab_evidence = if let Some(config) = p.get("agentConfig") {
            match lab::prepare(&dir, config) {
                Ok(e) => Some(e),
                Err(_) => return self.finish(&c, "failed", native.as_deref()),
            }
        } else {
            None
        };
        let (mut cmd, native_input) = match harness::command(&dir, native.as_deref(), p) {
            Ok(cmd) => cmd,
            Err(e) => {
                self.event(&c, 1, "preparation.failed", json!({"reason":e.to_string()}))?;
                return self.finish(&c, "failed", native.as_deref());
            }
        };
        if harness::name(p) != "codex" && lab_evidence.is_none() {
            self.event(&c,0,"preparation",json!({"requested":p,"effective":{"harness":harness::name(p),"account":p["account"],"model":p["model"],"instructionRole":if harness::name(p)=="claude" {"system"} else {"user"},"nativePermissions":"native; no sandbox parity claimed","authentication":"host-provisioned; readiness unknown"}}))?;
        }
        if let Some(mut evidence) = lab_evidence {
            evidence["selection"] = json!({"harness":harness::name(p),"account":p.get("account"),"authority":"trusted host account reference; native observation follows"});
            if harness::name(p) == "codex" {
                lab::apply(&mut cmd, &p["agentConfig"]);
            }
            self.event(&c, 0, "preparation", evidence)?;
        }
        let mut claude_servers = lab::mcp_servers(&p["agentConfig"]);
        let profiles = p["agentConfig"]["delegationProfiles"].as_array().cloned().unwrap_or_default();
        {
            let context = json!({"hostId":self.host,"commandId":c["id"],"executionId":c["executionId"],"profiles":profiles,"projectId":p["projectId"]});
            let args = json!(["-c",include_str!("delegation.py"),self.root.join("journal.sqlite"),context.to_string()]);
            if matches!(harness::name(p), "claude" | "antigravity") {
                claude_servers["fleet"] = json!({"command":"python3","args":args});
            } else {
                cmd.args(["-c","mcp_servers.fleet.command=\"python3\"","-c",&format!("mcp_servers.fleet.args={args}"),"-c","mcp_servers.fleet.required=true"]);
                let mut tools = vec!["session_transition","session_fork","session_resume","session_handoffs","message_send","queue_list","ask","reply","questions","worker_done","execution_status"];
                if !profiles.is_empty() { tools.push("request_child"); }
                if p["projectId"].is_string() { tools.extend(["project_context","catalogue","pipeline_run","direct_search","pipeline_history","pipeline_get","question_ask","question_history","account_list"]); }
                cmd.args(["-c",&format!("mcp_servers.fleet.enabled_tools={}",json!(tools))]);
                for tool in tools { cmd.args(["-c",&format!("mcp_servers.fleet.tools.{tool}.approval_mode=\"approve\"")]); }
            }
        }
        if harness::name(p) == "claude" {
            harness::claude_mcp(&mut cmd, p.get("agentConfig").unwrap_or(p), &claude_servers);
        }
        let codex_protocol = harness::name(p) == "codex";
        let acp_protocol = harness::name(p) == "antigravity";
        if codex_protocol {
            cmd = codex::command(&cmd);
        }
        let mut protocol = if codex_protocol {
            Some(Protocol::Codex(codex::Session::new(
                dir.to_string_lossy().into_owned(),
                native.clone(),
                p.clone(),
            ).fork(if native.is_none() { p["fork"]["nativeSessionId"].as_str().map(str::to_owned) } else { None })))
        } else if acp_protocol {
            Some(Protocol::Antigravity(antigravity::Session::new(
                dir.to_string_lossy().into_owned(),
                native.clone(),
                p.clone(),
                claude_servers,
            )))
        } else {
            None
        };
        cmd.stdin(
            if native_input.is_some() || codex_protocol || acp_protocol {
                Stdio::piped()
            } else {
                Stdio::null()
            },
        );
        cmd.stdout(Stdio::piped())
            .stderr(Stdio::from(fs::File::create(
                dir.join(format!("{execution}.stderr")),
            )?));
        let mut raw_log = fs::File::create(dir.join(format!("{execution}.native.jsonl")))?;
        let mut outbound_log = if codex_protocol || acp_protocol {
            use std::os::unix::fs::OpenOptionsExt;
            Some(
                fs::OpenOptions::new()
                    .write(true)
                    .create_new(true)
                    .mode(0o600)
                    .open(dir.join(format!("{execution}.protocol-out.jsonl")))?,
            )
        } else {
            None
        };
        let mut child = match cmd.spawn() {
            Ok(c) => c,
            Err(_) => return self.finish(&c, "failed", native.as_deref()),
        };
        if let Some(input) = native_input {
            use std::io::Write;
            if let Some(mut stdin) = child.stdin.take() {
                if stdin.write_all(&input).is_err() {
                    let _ = child.kill();
                    let _ = child.wait();
                    return self.finish(&c, "unknown", native.as_deref());
                }
            }
        }
        let identity = match native::identity(child.id()) {
            Ok(i) => i,
            Err(_) => {
                let _ = child.kill();
                let _ = child.wait();
                return self.finish(&c, "unknown", native.as_deref());
            }
        };
        let (tx, rx) = mpsc::channel();
        let mut protocol_stdin = if codex_protocol || acp_protocol {
            child.stdin.take()
        } else {
            None
        };
        if let (Some(stdin), Some(log)) = (protocol_stdin.as_mut(), outbound_log.as_mut()) {
            match codex::write_frame(stdin, log, &protocol.as_ref().unwrap().initialize()) {
                Ok(metadata) => {
                    let _ = tx.send(metadata.to_string());
                }
                Err(_) => {
                    let _ = child.kill();
                    let _ = child.wait();
                    return self.finish(&c, "unknown", native.as_deref());
                }
            }
        }
        let stdout = child.stdout.take().ok_or("stdout missing")?;
        let wire = (codex_protocol || acp_protocol).then(|| {
            std::sync::Arc::new(std::sync::Mutex::new(Wire {
                protocol: protocol.take(),
                stdin: protocol_stdin.take(),
                log: outbound_log.take(),
            }))
        });
        let reader_wire = wire.clone();
        std::thread::spawn(move || {
            let mut acp_complete = false;
            for line in BufReader::new(stdout).lines() {
                match line {
                    Ok(s) => {
                        if writeln!(raw_log, "{s}")
                            .and_then(|_| raw_log.sync_data())
                            .is_err()
                        {
                            break;
                        }
                        if let Some(w) = reader_wire.as_ref() {
                            let mut guard = w.lock().unwrap();
                            let Wire { protocol, stdin: protocol_stdin, log: outbound_log } = &mut *guard;
                            let (Some(protocol), Some(stdin)) = (protocol.as_mut(), protocol_stdin.as_mut()) else {
                                continue;
                            };
                            let result = serde_json::from_str::<Value>(&s)
                                .map_err(|e| e.to_string())
                                .and_then(|v| protocol.receive(&v));
                            match result {
                                Ok((requests, event, done)) => {
                                    for request in requests {
                                        match codex::write_frame(
                                            stdin,
                                            outbound_log.as_mut().unwrap(),
                                            &request,
                                        ) {
                                            Ok(metadata) => {
                                                if tx.send(metadata.to_string()).is_err() {
                                                    return;
                                                }
                                            }
                                            Err(_) => {
                                                let _=tx.send(json!({"type":"protocol.failed","error":"outbound delivery unknown; no replay"}).to_string());
                                                return;
                                            }
                                        }
                                    }
                                    if let Some(event) = event {
                                        if tx.send(event.to_string()).is_err() {
                                            return;
                                        }
                                    }
                                    if done {
                                        acp_complete = true;
                                        protocol_stdin.take();
                                    }
                                }
                                Err(_) => {
                                    let _=tx.send(json!({"type":"protocol.failed","error":"native protocol outcome unconfirmed; no replay"}).to_string());
                                    return;
                                }
                            }
                            continue;
                        }
                        if tx.send(s).is_err() {
                            break;
                        }
                    }
                    Err(_) => break,
                }
            }
            if acp_protocol && !acp_complete {
                let _=tx.send(json!({"type":"protocol.failed","error":"ACP EOF before terminal outcome; no replay"}).to_string());
            }
        });
        let child_pid = child.id();
        let identity_for_tree = identity.clone();
        self.live.insert(
            execution,
            Live {
                child,
                command: c.clone(),
                native: native.clone(),
                seq: 0,
                completed: false,
                stopped: false,
                owned: HashMap::from([(child_pid, identity_for_tree)]),
                exit: None,
                cleanup_unknown: false,
                rx,
                eof: false,
                last_sample: std::time::Instant::now(),
                wire,
            },
        );
        self.receipt(&c, "started", native.as_deref())?;
        Ok(())
    }
    fn finish(&self, c: &Value, status: &str, native: Option<&str>) -> Result<()> {
        let tx = self.db.unchecked_transaction()?;
        self.receipt(c, status, native)?;
        self.db
            .execute("UPDATE commands SET terminal=1 WHERE id=?", [id(c, "id")?])?;
        tx.commit()?;
        Ok(())
    }
    fn reconcile(&self) -> Result<()> {
        let mut stmt = self
            .db
            .prepare("SELECT body FROM commands WHERE terminal=0")?;
        let commands = stmt
            .query_map([], |r| r.get::<_, String>(0))?
            .collect::<std::result::Result<Vec<_>, _>>()?;
        for body in commands {
            self.finish(&serde_json::from_str::<Value>(&body)?, "unknown", None)?;
        }
        Ok(())
    }
    fn tick(&mut self) -> Result<()> {
        let keys: Vec<_> = self.live.keys().cloned().collect();
        for execution in keys {
            let mut l = self.live.remove(&execution).unwrap();
            let result = (|| -> Result<bool> {
                if l.observe().is_err() {
                    l.cleanup_unknown = true;
                }
                if l.exit.is_none() {
                    l.exit = l.child.try_wait()?;
                }
                if l.exit.is_some() || l.stopped {
                    if l.cleanup().is_err() {
                        l.cleanup_unknown = true;
                    }
                }

                loop {
                    match l.rx.try_recv() {
                        Ok(line) => {
                            let event: Value = serde_json::from_str(&line)
                                .unwrap_or(json!({"type":"unparsed","raw":line}));
                            let projection =
                                harness::project(harness::name(&l.command["payload"]), &event);
                            if let Some(n) = projection["session"].as_str() {
                                self.db.execute(
                                    "INSERT OR REPLACE INTO sessions(id,native) VALUES(?1,?2)",
                                    params![id(&l.command, "sessionId")?, n],
                                )?;
                                l.native = Some(n.into());
                                self.receipt(&l.command, "started", Some(n))?;
                            }
                            if event["type"] == "steer.result" {
                                if let Ok(body) = self.db.query_row("SELECT body FROM commands WHERE id=? AND terminal=0", [event["commandId"].as_str().unwrap_or("")], |r| r.get::<_, String>(0)) {
                                    self.finish(&serde_json::from_str::<Value>(&body)?, if event["ok"] == true { "completed" } else { "failed" }, None)?;
                                }
                            }
                            if event["type"] == "protocol.failed" && !l.stopped {
                                l.cleanup_unknown = true;
                                l.stopped = true;
                            }
                            if projection["completed"] == true {
                                l.completed = true;
                            }
                            l.seq += 1;
                            self.event(
                                &l.command,
                                l.seq,
                                event["type"].as_str().unwrap_or("unknown"),
                                {
                                    let mut retained = event.clone();
                                    retained["fleetNormalized"] = projection;
                                    retained
                                },
                            )?;
                        }
                        Err(mpsc::TryRecvError::Empty) => break,
                        Err(mpsc::TryRecvError::Disconnected) => {
                            l.eof = true;
                            break;
                        }
                    }
                }
                if l.last_sample.elapsed() >= Duration::from_secs(2) {
                    l.seq += 1;
                    self.event(
                        &l.command,
                        l.seq,
                        "resource.observation",
                        resources(l.child.id(), &self.root),
                    )?;
                    l.last_sample = std::time::Instant::now();
                }
                if l.exit.is_some() && l.alive_owned()? {
                    return Ok(false);
                }
                // Wait for stdout EOF before reaping to retain partial records.
                if !l.eof {
                    return Ok(false);
                }
                if let Some(status) = l.exit {
                    l.seq+=1;self.event(&l.command,l.seq,"process.closed",json!({"exitCode":status.code(),"stdoutClosed":l.eof,"observedOwnedProcessesRemaining":false,"cleanupUnknown":l.cleanup_unknown}))?;
                    let dir = self
                        .root
                        .join("sessions")
                        .join(id(&l.command, "sessionId")?);
                    let output = dir.join("output");
                    if output.is_dir() {
                        if let Err(_) = self.artifacts(&output, &output, &execution) {
                            return self
                                .finish(&l.command, "failed", l.native.as_deref())
                                .map(|_| true);
                        }
                    }
                    self.finish(
                        &l.command,
                        if l.cleanup_unknown {
                            "unknown"
                        } else if l.stopped {
                            "stopped"
                        } else if status.success() && l.completed {
                            "completed"
                        } else {
                            "failed"
                        },
                        l.native.as_deref(),
                    )?;
                    if l.stopped {
                        let mut stmt=self.db.prepare("SELECT body FROM commands WHERE json_extract(body,'$.executionId')=? AND json_extract(body,'$.kind')='stop'")?;
                        for b in stmt.query_map([&execution], |r| r.get::<_, String>(0))? {
                            self.finish(
                                &serde_json::from_str::<Value>(&b?)?,
                                if l.cleanup_unknown {
                                    "unknown"
                                } else {
                                    "stopped"
                                },
                                l.native.as_deref(),
                            )?;
                        }
                    }
                    return Ok(true);
                }
                Ok(false)
            })();
            match result {
                Ok(true) => {}
                Ok(false) => {
                    self.live.insert(execution, l);
                }
                Err(e) => {
                    self.live.insert(execution, l);
                    return Err(e);
                }
            }
        }
        Ok(())
    }
    fn event(&self, c: &Value, seq: u64, kind: &str, data: Value) -> Result<()> {
        self.queue("/api/hosts/events",json!({"hostId":self.host,"executionId":c["executionId"],"events":[{"id":format!("{}-{seq}",c["id"].as_str().unwrap()),"seq":seq,"type":kind,"timestamp":std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH)?.as_millis(),"data":data}]}))
    }
    fn artifacts(
        &self,
        root: &std::path::Path,
        dir: &std::path::Path,
        execution: &str,
    ) -> Result<()> {
        if fs::symlink_metadata(root)?.file_type().is_symlink() {
            return Err("artifact root symlink".into());
        }
        for e in fs::read_dir(dir)? {
            let e = e?;
            let t = e.file_type()?;
            if t.is_symlink() {
                continue;
            }
            if t.is_dir() {
                self.artifacts(root, &e.path(), execution)?
            } else if t.is_file() {
                let path = e.path();
                let relative = path
                    .strip_prefix(root)?
                    .to_str()
                    .ok_or("non utf8 artifact")?;
                if !relative.split('/').all(|p| {
                    !p.is_empty()
                        && p != "."
                        && p != ".."
                        && p.bytes()
                            .all(|b| b.is_ascii_alphanumeric() || b"_.-".contains(&b))
                }) {
                    return Err("artifact path invalid".into());
                }
                let media = match path.extension().and_then(|x| x.to_str()).unwrap_or("") {
                    "html" => "text/html",
                    "css" => "text/css",
                    "js" => "text/javascript",
                    "json" => "application/json",
                    "svg" => "image/svg+xml",
                    "png" => "image/png",
                    "jpg" | "jpeg" => "image/jpeg",
                    "webp" => "image/webp",
                    "ico" => "image/x-icon",
                    "txt" => "text/plain",
                    "woff2" => "font/woff2",
                    _ => return Err("unsupported static artifact type".into()),
                };
                let max = env::var("FLEET_MAX_ARTIFACT_BYTES")
                    .unwrap_or("16777216".into())
                    .parse::<u64>()?;
                if e.metadata()?.len() > max {
                    return Err("artifact too large".into());
                }
                use std::os::unix::fs::OpenOptionsExt;
                if !path.canonicalize()?.starts_with(root.canonicalize()?) {
                    return Err("artifact escaped root".into());
                }
                let file = fs::OpenOptions::new()
                    .read(true)
                    .custom_flags(libc::O_NOFOLLOW)
                    .open(&path)?;
                let mut bytes = Vec::new();
                file.take(max + 1).read_to_end(&mut bytes)?;
                if bytes.len() as u64 > max {
                    return Err("artifact grew beyond limit".into());
                }
                let sha = format!("{:x}", Sha256::digest(&bytes));
                self.queue("/api/hosts/artifacts",json!({"hostId":self.host,"executionId":execution,"path":relative,"mediaType":media,"sha256":sha,"contentBase64":base64::engine::general_purpose::STANDARD.encode(bytes)}))?;
            }
        }
        Ok(())
    }
}
fn main() -> Result<()> {
    let root = PathBuf::from(env::var("FLEET_DATA_DIR")?);
    fs::create_dir_all(&root)?;
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(&root, fs::Permissions::from_mode(0o700))?;
    let lock = fs::OpenOptions::new()
        .create(true)
        .truncate(false)
        .write(true)
        .open(root.join("executor.lock"))?;
    if unsafe { libc::flock(lock.as_raw_fd(), libc::LOCK_EX | libc::LOCK_NB) } != 0 {
        return Err("executor already running".into());
    }
    let db = Connection::open(root.join("journal.sqlite"))?;
    db.execute_batch("PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; CREATE TABLE IF NOT EXISTS commands(id TEXT PRIMARY KEY,body TEXT NOT NULL,terminal INTEGER DEFAULT 0); CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY,native TEXT NOT NULL); CREATE TABLE IF NOT EXISTS outbox(route TEXT NOT NULL,body TEXT NOT NULL); CREATE TABLE IF NOT EXISTS delegation_requests(id TEXT PRIMARY KEY,body TEXT NOT NULL,response TEXT);")?;
    let mut e = Executor {
        db,
        root,
        url: env::var("FLEET_COORDINATOR_URL")?,
        token: env::var("FLEET_HOST_TOKEN")?,
        host: env::var("FLEET_HOST_ID")?,
        live: HashMap::new(),
    };
    unsafe {
        libc::signal(libc::SIGTERM, shutdown as *const () as usize);
        libc::signal(libc::SIGINT, shutdown as *const () as usize);
    }
    e.reconcile()?;
    loop {
        if SHUTDOWN.load(std::sync::atomic::Ordering::Relaxed) {
            for l in e.live.values_mut() {
                let _ = l.cleanup();
                let _ = l.child.wait();
            }
            return Ok(());
        }
        e.tick()?;
        let result = (|| -> Result<()> {
            let _ = e.flush();
            let reply=e.post("/api/hosts/poll",json!({"hostId":e.host,"capabilities":["codex","gpt-6.1-sol","low","static-output","steer","native-fork"]}))?;
            for c in reply["commands"].as_array().ok_or("commands missing")? {
                if e.run(c.clone()).is_err()
                    && !e.live.contains_key(c["executionId"].as_str().unwrap_or(""))
                {
                    let accepted =
                        e.db.query_row(
                            "SELECT body FROM commands WHERE id=? AND terminal=0",
                            [c["id"].as_str().unwrap_or("")],
                            |r| r.get::<_, String>(0),
                        )
                        .ok();
                    if accepted.as_deref() == Some(c.to_string().as_str()) {
                        e.finish(c, "failed", None)?;
                    }
                }
                let _ = e.flush();
            }
            Ok(())
        })();
        if result.is_err() {
            eprintln!("executor exchange failed; retained journal, retry pending");
        }
        std::thread::sleep(Duration::from_millis(100));
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn refuses_browser_paths() {
        for s in ["../x", "/tmp/x", "", "a/b"] {
            assert!(id(&json!({"id":s}), "id").is_err());
        }
    }
}

#[cfg(test)]
mod journal_tests {
    use super::*;
    #[test]
    fn duplicate_and_changed_identity_never_spawn_again() {
        let db = Connection::open_in_memory().unwrap();
        db.execute_batch("CREATE TABLE commands(id TEXT PRIMARY KEY,body TEXT,terminal INTEGER DEFAULT 0); CREATE TABLE outbox(route TEXT,body TEXT);").unwrap();
        let mut e = Executor {
            db,
            root: env::temp_dir(),
            url: String::new(),
            token: String::new(),
            host: "fake".into(),
            live: HashMap::new(),
        };
        let c = json!({"id":"cmd1","executionId":"exec1","sessionId":"session1","kind":"unsupported","payload":{}});
        e.run(c.clone()).unwrap();
        e.run(c.clone()).unwrap();
        assert_eq!(
            e.db.query_row("SELECT count(*) FROM commands", [], |r| r.get::<_, i64>(0))
                .unwrap(),
            1
        );
        let mut changed = c;
        changed["kind"] = json!("start");
        assert!(e.run(changed).is_err());
        assert_eq!(
            e.db.query_row("SELECT count(*) FROM outbox", [], |r| r.get::<_, i64>(0))
                .unwrap(),
            2
        );
    }
}

fn resources(pid: u32, root: &std::path::Path) -> Value {
    let out = Command::new("/bin/ps")
        .args(["-p", &pid.to_string(), "-o", "%cpu=,rss="])
        .output()
        .ok();
    let fields: Vec<f64> = out
        .as_ref()
        .map(|o| {
            String::from_utf8_lossy(&o.stdout)
                .split_whitespace()
                .filter_map(|s| s.parse().ok())
                .collect()
        })
        .unwrap_or_default();
    let disk = Command::new("/usr/bin/du")
        .args(["-sk"])
        .arg(root)
        .output()
        .ok()
        .and_then(|o| {
            String::from_utf8_lossy(&o.stdout)
                .split_whitespace()
                .next()?
                .parse::<u64>()
                .ok()
        })
        .map(|v| v * 1024);
    json!({"cpuPercent":fields.first(),"residentBytes":fields.get(1).map(|v|v*1024.0),"diskBytes":disk,"cost":null,"ioBytes":null,"coverage":{"cpu":"native leader ps lifetime average, excludes descendants","ram":"native leader rss only","disk":"entire executor data directory allocated bytes; not per execution","cost":"unknown subscription allocation","io":"unknown"}})
}

impl Live {
    fn observe(&mut self) -> Result<()> {
        let table = native::processes()?;
        loop {
            let mut added = false;
            for &(pid, parent, _) in &table {
                if self.owned.contains_key(&pid) {
                    continue;
                }
                if let Some(expected) = self.owned.get(&parent) {
                    if native::identity(parent).ok().as_ref() != Some(expected) {
                        continue;
                    }
                    if let Ok(identity) = native::identity(pid) {
                        // Confirm parent relationship again after capturing the incarnation.
                        if native::processes()?
                            .iter()
                            .any(|(p, pp, _)| *p == pid && *pp == parent)
                            && native::identity(parent).ok().as_ref() == Some(expected)
                        {
                            self.owned.insert(pid, identity);
                            added = true;
                        }
                    }
                }
            }
            if !added {
                break;
            }
        }
        Ok(())
    }
    fn alive_owned(&self) -> Result<bool> {
        for (pid, _, state) in native::processes()? {
            if state.starts_with('Z') {
                continue;
            }
            if let Some(expected) = self.owned.get(&pid) {
                match native::identity(pid) {
                    Ok(current) => {
                        if &current == expected {
                            return Ok(true);
                        }
                    }
                    Err(_) => return Err("owned process identity unreadable".into()),
                }
            }
        }
        Ok(false)
    }
    fn cleanup(&mut self) -> Result<()> {
        let mut frozen = std::collections::HashSet::new();
        loop {
            self.observe()?;
            let table = native::processes()?;
            let mut changed = false;
            for (&pid, identity) in &self.owned {
                if frozen.contains(&pid)
                    || !table
                        .iter()
                        .any(|(p, _, s)| *p == pid && !s.starts_with('Z'))
                {
                    continue;
                }
                if native::identity(pid).ok().as_ref() == Some(identity) {
                    native::signal(pid, identity, libc::SIGSTOP)?;
                    frozen.insert(pid);
                    changed = true;
                }
            }
            if !changed {
                break;
            }
        }
        let table = native::processes()?;
        for (&pid, identity) in &self.owned {
            if table
                .iter()
                .any(|(p, _, s)| *p == pid && !s.starts_with('Z'))
                && native::identity(pid).ok().as_ref() == Some(identity)
            {
                native::signal(pid, identity, libc::SIGKILL)?;
            }
        }
        Ok(())
    }
}
impl Drop for Executor {
    fn drop(&mut self) {
        for l in self.live.values_mut() {
            let _ = l.cleanup();
            let _ = l.child.wait();
        }
    }
}

#[cfg(test)]
mod delegation_transport_tests {
    use super::*;
    #[test]
    fn durable_response_before_outbox_removal() {
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let address = listener.local_addr().unwrap();
        let server = std::thread::spawn(move || {
            let (mut socket, _) = listener.accept().unwrap();
            socket
                .set_read_timeout(Some(Duration::from_secs(5)))
                .unwrap();
            let mut reader = BufReader::new(socket.try_clone().unwrap());
            let mut first = String::new();
            reader.read_line(&mut first).unwrap();
            assert!(first.starts_with("POST /api/hosts/receipts "));
            let mut length = 0;
            loop {
                let mut line = String::new();
                reader.read_line(&mut line).unwrap();
                if line == "\r\n" {
                    break;
                }
                if line.to_lowercase().starts_with("content-length:") {
                    length = line.split(':').nth(1).unwrap().trim().parse().unwrap();
                }
            }
            let mut body = vec![0; length];
            reader.read_exact(&mut body).unwrap();
            let value: Value = serde_json::from_slice(&body).unwrap();
            assert_eq!(value["commandId"], "command");
            let response = r#"{"childExecutionId":"durable-child"}"#;
            write!(socket,"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",response.len(),response).unwrap();
        });
        let db = Connection::open_in_memory().unwrap();
        db.execute_batch("CREATE TABLE outbox(route TEXT,body TEXT);CREATE TABLE delegation_requests(id TEXT PRIMARY KEY,body TEXT,response TEXT);").unwrap();
        let value = json!({"commandId":"command","executionId":"execution","hostId":"mac","delegation":{"requestId":"one","profileId":"website-reviewer","input":"review"}});
        db.execute(
            "INSERT INTO delegation_requests VALUES('command:one',?,NULL)",
            [value.to_string()],
        )
        .unwrap();
        let executor = Executor {
            db,
            root: PathBuf::new(),
            url: format!("http://{address}"),
            token: "invented".into(),
            host: "mac".into(),
            live: HashMap::new(),
        };
        executor.queue("/api/hosts/receipts", value).unwrap();
        executor.flush().unwrap();
        let response: String = executor
            .db
            .query_row("SELECT response FROM delegation_requests", [], |r| r.get(0))
            .unwrap();
        assert!(response.contains("durable-child"));
        assert_eq!(
            executor
                .db
                .query_row("SELECT count(*) FROM outbox", [], |r| r.get::<_, i64>(0))
                .unwrap(),
            0
        );
        server.join().unwrap();
    }
}
