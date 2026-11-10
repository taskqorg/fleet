mod antigravity;
mod codex;
mod contracts;
mod harness;
mod profile;
mod native;
mod outbox;
mod readiness;
mod checkout_sync;
mod git_access;
mod worktree;
mod upload;
mod store;
mod preview;
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
    sessions: PathBuf,
    harness_cache: HashMap<String, Value>,
    // This host's artifact store (FLEET_STORE_CONFIG) and its last poll report, refreshed every 15 s.
    store_cfg: Option<Value>,
    declared: std::cell::RefCell<Vec<String>>,
    store_report: std::cell::RefCell<(Option<std::time::Instant>, Value)>,
}
/// Reads the host secret file: not a symlink, no group or other access, one line.
fn host_secret(path: PathBuf) -> Result<String> {
    use std::os::unix::fs::MetadataExt;
    let m = fs::symlink_metadata(&path)?;
    if m.file_type().is_symlink() || m.mode() & 0o077 != 0 {
        return Err(format!("{}: host secret file must be a private file (0600)", path.display()).into());
    }
    let s = fs::read_to_string(&path)?.trim().to_string();
    if s.len() < 32 { return Err("host secret too short".into()); }
    Ok(s)
}

/// Host platform for the coordinator. A WSL host whose Claude placement is `windows-wsl` counts as Windows (`win32`), the
/// rule the coordinator's Claude gates (accounts.ts) and delivery_worker.py apply; any other Linux host stays `linux`.
fn platform() -> &'static str {
    if harness::claude_placement_here() == Ok("windows-wsl") {
        return "win32";
    }
    match env::consts::OS {
        "macos" => "darwin",
        "windows" => "win32",
        o => o,
    }
}
fn git() -> Command {
    let mut c = Command::new("git");
    c.env_clear().env("GIT_CONFIG_NOSYSTEM", "1").env("GIT_CONFIG_GLOBAL", "/dev/null")
        .args(["-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "protocol.file.allow=never"])
        .stdin(Stdio::null());
    if let Some(v) = env::var_os("PATH") { c.env("PATH", v); }
    c
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
    /// Script commands: the process group stop and timeout kill, the deadline, and the trial worktree (toplevel, path).
    group: Option<i32>,
    deadline: Option<std::time::Instant>,
    timed_out: bool,
    trial: Option<(PathBuf, PathBuf)>,
    /// Transcript block state of this execution (open tools and streams).
    blocks: harness::Blocks,
}
impl Executor {
    /// Existing sessions keep their directory; new ones go under FLEET_SESSIONS_ROOT.
    fn session_dir(&self, session: &str) -> PathBuf {
        let legacy = self.root.join("sessions").join(session);
        if legacy.exists() { legacy } else { self.sessions.join(session) }
    }
    /// Harness version and model-visible built-in tools, cached per executable and argument set.
    fn observe_harness(&mut self, cmd: &Command, harness: &str) -> Value {
        let exe = cmd.get_program().to_string_lossy().into_owned();
        let args: Vec<String> = cmd.get_args().map(|a| a.to_string_lossy().into_owned()).collect();
        let flags: Vec<&str> = args.windows(2).filter(|w| matches!(w[0].as_str(), "-c" | "--enable" | "--disable")).flat_map(|w| [w[0].as_str(), w[1].as_str()]).collect();
        let key = format!("{harness}\0{exe}\0{}", if harness == "codex" { flags.join("\0") } else { String::new() });
        if let Some(v) = self.harness_cache.get(&key) {
            return v.clone();
        }
        let version = Command::new(&exe).arg("--version").stdin(Stdio::null()).output().ok()
            .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_owned());
        let mut v = json!({"harness":harness,"executable":exe,"version":version});
        if harness == "codex" {
            // Same env, cwd and -c/--enable/--disable flags as the run; the prompt text is not sent anywhere.
            let mut probe = Command::new(&exe);
            probe.env_clear().envs(cmd.get_envs().filter_map(|(k, v)| v.map(|v| (k, v)))).stdin(Stdio::null()).stderr(Stdio::null());
            if let Some(d) = cmd.get_current_dir() { probe.current_dir(d); }
            probe.args(["debug", "prompt-input"]);
            let mut i = 0;
            while i + 1 < args.len() {
                if matches!(args[i].as_str(), "-c" | "--enable" | "--disable") { probe.arg(&args[i]).arg(&args[i + 1]); i += 2; } else { i += 1; }
            }
            probe.arg("tools");
            let tools = probe.output().ok().and_then(|o| serde_json::from_slice::<Value>(&o.stdout).ok()).map(|items| {
                fn walk(v: &Value, out: &mut Vec<String>) {
                    if let Some(a) = v["tools"].as_array() {
                        for t in a {
                            if t["tools"].is_array() { walk(t, out) } else if let Some(n) = t["name"].as_str() { out.push(n.into()) }
                        }
                    }
                }
                let mut out = vec![];
                for item in items.as_array().into_iter().flatten() { walk(item, &mut out) }
                out
            });
            v["builtinTools"] = json!(tools);
        } else {
            v["builtinTools"] = json!(if harness == "claude" { "reported in native system/init event" } else { "not exposed by ACP" });
        }
        self.harness_cache.insert(key, v.clone());
        v
    }
    fn post(&self, route: &str, v: Value) -> Result<Value> {
        outbox::post(&self.url, &self.token, route, v)
    }
    fn queue(&self, route: &str, v: Value) -> Result<()> {
        outbox::queue(&self.db, route, v)
    }
    #[cfg(test)]
    fn flush(&self) -> Result<()> {
        outbox::flush(&self.db, &self.url, &self.token, &self.host)
    }
    fn receipt(&self, c: &Value, status: &str, native: Option<&str>) -> Result<()> {
        let declared = std::mem::take(&mut *self.declared.borrow_mut());
        let r = contracts::Receipt { host_id: self.host.clone(), command_id: id(c, "id")?.into(), execution_id: id(c, "executionId")?.into(), status: serde_json::from_value(json!(status))?, native_session_id: native.map(Into::into), artifacts: Some(declared), error: None };
        self.queue("/api/hosts/receipts", serde_json::to_value(r)?)
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
        if c["kind"] == "readiness" {
            let reg = std::env::var_os("FLEET_NATIVE_ACCOUNTS");
            let data = readiness::run(&c["payload"]["project"], &self.host, reg.as_deref().map(std::path::Path::new));
            let ok = data["ok"] == true;
            self.event(&c, 0, "readiness", data)?;
            if !ok {
                self.event(&c, 1, "readiness.failed", json!({"reason":"readiness-check-failed"}))?;
            }
            return self.finish(&c, if ok { "completed" } else { "failed" }, None);
        }
        if c["kind"] == "base-updated" {
            let (data, ok) = checkout_sync::run(&c["payload"], &self.host, &worktree::project_roots(), &[&self.root, &self.sessions]);
            self.event(&c, 0, if ok { "checkout.synced" } else { "sync-failed" }, data)?;
            return self.finish(&c, if ok { "completed" } else { "failed" }, None);
        }
        if c["kind"] == "teardown" {
            // Session finish: teardown script, keep on dirty/teardown failure, safe branch delete.
            let p = &c["payload"];
            let dir = self.session_dir(&session);
            let repo = worktree::repo_path(p, &self.host);
            // Lookup only: never create a worktree; session dir must be a real dir under executor/session roots.
            let safe = fs::symlink_metadata(&dir).is_ok_and(|m| m.is_dir()) && dir.canonicalize().is_ok_and(|d| self.root.canonicalize().is_ok_and(|r| d.starts_with(r)) || self.sessions.canonicalize().is_ok_and(|r| d.starts_with(r)));
            let data = if !safe || !dir.join("worktree").join(".git").exists() { json!({"removed":false,"reason":"no-worktree"}) } else { match worktree::worktree(&dir, &session, repo.as_deref(), &worktree::project_roots(), &[&self.root, &self.sessions], &p["project"]) {
                Ok(Some((_, top, false))) => worktree::teardown(&dir, &session, &top, &p["project"]),
                Ok(_) => json!({"removed":false,"reason":"no-worktree"}),
                Err(e) => json!({"removed":false,"reason":e.to_string()}),
            } };
            let ok = data["removed"] == true || data["reason"] == "uncommitted" || data["reason"] == "no-worktree";
            // The teardown command reuses the last execution, whose seq 0.. are taken by its turn: use a fresh seq.
            let seq = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map_or(1 << 40, |d| d.as_millis() as u64);
            self.event(&c, seq, "teardown", data)?;
            return self.finish(&c, if ok { "completed" } else { "failed" }, None);
        }
        if c["kind"] == "artifact-copy" || c["kind"] == "artifact-verify" {
            // Long transfers run on the transfer worker, which sends the final receipt; the journal row resumes them.
            let mut body = c["payload"].clone();
            body["command"] = json!({"id":c["id"],"executionId":c["executionId"]});
            body["spool"] = json!(self.root.join("spool"));
            upload::enqueue(&self.db, &cid, if c["kind"] == "artifact-copy" { "copy" } else { "verify" }, &body)?;
            self.db.execute("UPDATE commands SET terminal=1 WHERE id=?", [&cid])?;
            return Ok(());
        }
        if c["kind"] == "script" {
            return self.script(&c, &execution, &session);
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
        if p.get("agentConfig")
            .is_some_and(|v| profile::validate(v).is_err())
        {
            return self.finish(&c, "failed", None);
        }
        let dir = self.session_dir(&session);
        fs::create_dir_all(&dir)?;
        if fs::symlink_metadata(&dir)?.file_type().is_symlink()
            || !(dir.canonicalize()?.starts_with(self.root.canonicalize()?)
                || dir.canonicalize()?.starts_with(self.sessions.canonicalize()?))
        {
            return self.finish(&c, "failed", None);
        }
        fs::write(
            dir.join(".fleet-instructions.tmp"),
            p["instructions"].as_str().unwrap_or(""),
        )?;
        fs::rename(dir.join(".fleet-instructions.tmp"), dir.join("AGENTS.md"))?;
        if p.get("agentConfig").is_none() && harness::name(p) == "codex" {
            self.event(&c,0,"preparation",json!({"model":p["model"],"effort":p["effort"],"subscription":true,"instructionsSha256":format!("{:x}",Sha256::digest(p["instructions"].as_str().unwrap_or("").as_bytes())),"tools":{"shell":if p["gitNetwork"] == true {"workspace-write, git network enabled, checkout .git writable, approvals denied"} else {"workspace-write, network disabled, approvals denied"},"mcp":[],"collaborationTools":p["agentConfig"]["collaborationTools"].as_bool().unwrap_or(true)},"skills":[],"repairs":false,"nativeConfig":"Fleet-owned HOME/CODEX_HOME required; config and execpolicy ignored; AGENTS discovery, skills, memories and hooks disabled; context observations pending"}))?;
        }
        // The coordinator owns the native id per harness:account continuation; no id means a new native session
        // (a handoff to another harness must not resume the previous harness's thread).
        let native = p["nativeSessionId"].as_str().map(str::to_owned);
        if native.as_deref().is_some_and(|n| !harness::safe_native_id(n)) {
            self.event(&c, 1, "preparation.failed", json!({"reason":"invalid native session id"}))?;
            return self.finish(&c, "failed", None);
        }
        let lab_evidence = if let Some(config) = p.get("agentConfig") {
            match profile::prepare(&dir, config) {
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
                profile::apply(&mut cmd, &profile::with_skills(&p["agentConfig"], &p["skills"]));
            }
            self.event(&c, 0, "preparation", evidence)?;
        }
        let fleet_tools = p["agentConfig"]["fleetTools"].as_bool().unwrap_or(true);
        let collaboration = p["agentConfig"]["collaborationTools"].as_bool().unwrap_or(true);
        if harness::name(p) == "codex" && !collaboration {
            cmd.args(["--disable", "multi_agent", "--disable", "multi_agent_v2"]);
        }
        let repo = worktree::repo_path(p, &self.host);
        let mut checkout_top = None;
        let workdir = match worktree::worktree(&dir, &session, repo.as_deref(), &worktree::project_roots(), &[&self.root, &self.sessions], &p["project"]) {
            Ok(Some((w, top, created))) => {
                checkout_top = Some(top.clone());
                if let (true, Some(script), false) = (created, p["project"]["scripts"]["setup"].as_str(), p["setup"] == "skip") {
                    let e = worktree::script("setup", script, &top, &w, &session);
                    let ok = e["ok"] == true;
                    self.event(&c, 1, "setup", e)?;
                    if !ok {
                        self.event(&c, 2, "preparation.failed", json!({"reason":"setup-failed"}))?;
                        return self.finish(&c, "failed", native.as_deref());
                    }
                }
                Some(w)
            }
            Ok(None) => None,
            Err(e) => {
                self.event(&c, 1, "preparation.failed", json!({"reason":e.to_string()}))?;
                return self.finish(&c, "failed", native.as_deref());
            }
        };
        if let Some(w) = &workdir {
            cmd.current_dir(w);
        }
        // Output collected when the run ends, and the folder artifact_get downloads into (design §8).
        let output_dir = workdir.as_ref().map(|w| w.join("output")).unwrap_or_else(|| dir.join("output"));
        let input_dir = dir.join("input");
        let _ = fs::create_dir_all(&output_dir);
        let _ = fs::create_dir_all(&input_dir);
        cmd.env("FLEET_OUTPUT_DIR", &output_dir).env("FLEET_INPUT_DIR", &input_dir);
        if let Err(e) = self.stage_inputs(p, &input_dir, &c["executionId"]) {
            self.event(&c, 1, "preparation.failed", json!({"reason":e}))?;
            return self.finish(&c, "failed", native.as_deref());
        }
        if p["project"].is_object() {
            // Repo of this host's environment (env.repo), else the project's single/first repo.
            let env_repo = p["project"]["environments"].as_array().into_iter().flatten().find(|e| e["hostId"] == self.host.as_str()).map_or(Value::Null, |e| e["repo"].clone());
            match git_access::env(&p["project"], git_access::repo(&p["project"], &env_repo), git_access::dir().as_deref()) {
                Ok(v) => git_access::apply(&mut cmd, &v),
                Err(e) => {
                    self.event(&c, 1, "preparation.failed", json!({"reason":e}))?;
                    return self.finish(&c, "failed", native.as_deref());
                }
            }
        }
        let observed = self.observe_harness(&cmd, harness::name(p));
        let mut claude_servers = profile::mcp_servers(&p["agentConfig"]);
        let profiles = p["agentConfig"]["delegationProfiles"].as_array().cloned().unwrap_or_default();
        let mut harness_observation = None;
        let mut fleet_mcp: Vec<&str> = vec![];
        if fleet_tools {
            let context = json!({"hostId":self.host,"commandId":c["id"],"executionId":c["executionId"],"profiles":profiles,"projectId":p["projectId"],"workdir":workdir.clone().unwrap_or_else(|| dir.clone()),"inputDir":input_dir,"spool":self.root.join("spool")});
            let args = json!(["-c",include_str!("delegation.py"),self.root.join("journal.sqlite"),context.to_string()]);
            if matches!(harness::name(p), "claude" | "antigravity") {
                claude_servers["fleet"] = json!({"command":"python3","args":args});
            } else {
                cmd.args(["-c","mcp_servers.fleet.command=\"python3\"","-c",&format!("mcp_servers.fleet.args={args}"),"-c","mcp_servers.fleet.required=true"]);
                let mut tools = vec!["session_start","session_deliver","session_transition","session_fork","session_resume","session_handoffs","message_send","queue_list","ask","reply","questions","worker_done","execution_status","artifact_save","artifact_list","artifact_get"];
                if !profiles.is_empty() { tools.push("request_child"); }
                if p["projectId"].is_string() { tools.extend(["project_context","catalogue","pipeline_run","direct_search","pipeline_history","pipeline_get","question_ask","question_history","account_list"]); }
                cmd.args(["-c",&format!("mcp_servers.fleet.enabled_tools={}",json!(tools))]);
                fleet_mcp = tools.clone();
                for tool in tools { cmd.args(["-c",&format!("mcp_servers.fleet.tools.{tool}.approval_mode=\"approve\"")]); }
            }
        }
        {
            let mut o = observed;
            o["fleetTools"] = json!(fleet_tools);
            o["collaborationTools"] = json!(collaboration);
            o["fleetMcpTools"] = if harness::name(p) == "codex" { json!(fleet_mcp) } else { json!(claude_servers.as_object().map(|m| m.keys().cloned().collect::<Vec<_>>())) };
            o["cwd"] = json!(workdir.as_ref().unwrap_or(&dir));
            o["worktree"] = json!(workdir.is_some());
            o["platform"] = json!(platform());
            harness_observation = Some(o);
        }
        // Granted connections (Lab Stage 2): one connection_call.py MCP server each, labelled with the pipeline step (or this
        // execution); calls are recorded in FLEET_OUTPUT_DIR/.connection-calls.jsonl, uploaded with the other outputs.
        let mut connection_tools: Vec<String> = vec![];
        let label = p["connectionLabel"].as_str().or(c["executionId"].as_str()).unwrap_or("").to_string();
        for conn in p["agentConfig"]["connections"].as_array().into_iter().flatten().filter_map(Value::as_str) {
            let dir = env::var_os("FLEET_CONNECTIONS_DIR").map(PathBuf::from).unwrap_or_default();
            let (record, tools) = match profile::connection_record(&dir, conn) {
                Ok(r) => r,
                Err(e) => {
                    self.event(&c, 1, "preparation.failed", json!({"reason":e}))?;
                    return self.finish(&c, "failed", native.as_deref());
                }
            };
            // -I: isolated, so nothing in the agent's working directory or environment is imported by the client.
            let args = json!(["-I", "-c", profile::CONNECTION_CLIENT, "mcp", record, label, output_dir]);
            if harness::name(p) == "codex" {
                cmd.args(["-c",&format!("mcp_servers.{conn}.command=\"python3\""),"-c",&format!("mcp_servers.{conn}.args={args}"),"-c",&format!("mcp_servers.{conn}.required=true"),"-c",&format!("mcp_servers.{conn}.enabled_tools={}",json!(tools))]);
                for t in &tools { cmd.args(["-c",&format!("mcp_servers.{conn}.tools.{t}.approval_mode=\"approve\"")]); }
            } else {
                claude_servers[conn] = json!({"command":"python3","args":args});
            }
            connection_tools.extend(tools.iter().map(|t| format!("mcp__{conn}__{t}")));
        }
        // Built-in web (R3.4) for pipeline agent steps: off unless the step's run sets webRoute builtin. Other sessions keep
        // the harness default.
        if harness::name(p) == "codex" && p["webRoute"].is_string() {
            cmd.args(["-c", if p["webRoute"] == "builtin" { "web_search=\"live\"" } else { "web_search=\"disabled\"" }]);
        }
        if harness::name(p) == "claude" {
            let mut extra = connection_tools.clone();
            extra.extend(harness::claude_extra_tools(p).into_iter().map(String::from));
            harness::claude_mcp(&mut cmd, p.get("agentConfig").unwrap_or(p), &claude_servers, &extra);
        }
        let codex_protocol = harness::name(p) == "codex";
        let acp_protocol = harness::name(p) == "antigravity";
        if codex_protocol {
            cmd = codex::command(&cmd);
        }
        let mut protocol = if codex_protocol {
            // gitNetwork: sandbox network on and the checkout's common .git writable (fetch/commit/push from the worktree).
            let mut sp = p.clone();
            if p["gitNetwork"] == true {
                if let Some(t) = &checkout_top { sp["writableRoots"] = json!([t.join(".git")]); }
            }
            Some(Protocol::Codex(codex::Session::new(
                workdir.as_ref().unwrap_or(&dir).to_string_lossy().into_owned(),
                native.clone(),
                sp,
            ).fork(if native.is_none() { p["fork"]["nativeSessionId"].as_str().map(str::to_owned) } else { None })))
        } else if acp_protocol {
            Some(Protocol::Antigravity(antigravity::Session::new(
                workdir.as_ref().unwrap_or(&dir).to_string_lossy().into_owned(),
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
                seq: 2,
                completed: false,
                stopped: false,
                owned: HashMap::from([(child_pid, identity_for_tree)]),
                exit: None,
                cleanup_unknown: false,
                rx,
                eof: false,
                last_sample: std::time::Instant::now(),
                wire,
                group: None,
                deadline: None,
                timed_out: false,
                trial: None,
                blocks: Default::default(),
            },
        );
        if let (Some(o), Some(l)) = (harness_observation, self.live.get_mut(&id(&c, "executionId")?)) {
            // Live sequence numbers start after the preparation events; reuse them so no sequence collides.
            l.seq += 1;
            let seq = l.seq;
            self.event(&c, seq, "harness", o)?;
        }
        self.receipt(&c, "started", native.as_deref())?;
        Ok(())
    }
    /// Lab script step (docs/designs/fleet-lab.md "Pipeline format version 2"): `sh -c <command>` in its own process group, in the trial's
    /// own worktree at the pinned commit (or the session dir when the payload names no repository). Payload: command,
    /// optional commit with workingDirectory/project, optional timeout_ms. Stop and timeout kill the whole group. The full
    /// stdout and stderr (output/script.stdout, output/script.stderr) and everything in FLEET_OUTPUT_DIR become artifacts.
    fn script(&mut self, c: &Value, execution: &str, session: &str) -> Result<()> {
        use std::os::unix::process::CommandExt;
        let p = &c["payload"];
        let dir = self.session_dir(session);
        fs::create_dir_all(&dir)?;
        if fs::symlink_metadata(&dir)?.file_type().is_symlink()
            || !(dir.canonicalize()?.starts_with(self.root.canonicalize()?)
                || dir.canonicalize()?.starts_with(self.sessions.canonicalize()?))
        {
            return self.finish(c, "failed", None);
        }
        let refuse = |reason: &str| -> Result<()> {
            self.event(c, 1, "preparation.failed", json!({"reason":reason}))?;
            self.finish(c, "failed", None)
        };
        let Some(command) = p["command"].as_str().filter(|s| !s.is_empty()) else { return refuse("script command missing") };
        let timeout = match &p["timeout_ms"] {
            Value::Null => None,
            v => match v.as_u64().filter(|ms| *ms > 0) {
                Some(ms) => Some(Duration::from_millis(ms)),
                None => return refuse("timeout_ms must be a positive integer"),
            },
        };
        let (output, input) = (dir.join("output"), dir.join("input"));
        fs::create_dir_all(&output)?;
        fs::create_dir_all(&input)?;
        if let Err(e) = self.stage_inputs(p, &input, &c["executionId"]) { return refuse(&e) }
        let (stdout, stderr) = (fs::File::create(output.join("script.stdout"))?, fs::File::create(output.join("script.stderr"))?);
        let (work, trial) = match (worktree::repo_path(p, &self.host), p["commit"].as_str()) {
            (Some(repo), Some(commit)) => match worktree::trial(&dir, &repo, commit, &worktree::project_roots(), &[&self.root, &self.sessions]) {
                Ok((w, top)) => (w.clone(), Some((top, w))),
                Err(e) => return refuse(&e.to_string()),
            },
            (None, None) => (dir.clone(), None),
            _ => return refuse("script needs both a repository and a commit, or neither"),
        };
        let drop_trial = || if let Some((top, w)) = &trial { worktree::remove_trial(top, w); };
        let mut cmd = worktree::clean(Command::new("sh"));
        cmd.args(["-c", command])
            .current_dir(&work)
            .process_group(0)
            .stdin(Stdio::null())
            .stdout(stdout)
            .stderr(stderr)
            .env("FLEET_INPUT_DIR", &input)
            .env("FLEET_OUTPUT_DIR", &output)
            .env("FLEET_SESSION_ID", session);
        let mut child = match cmd.spawn() {
            Ok(child) => child,
            Err(e) => {
                drop_trial();
                return refuse(&format!("script spawn failed: {e}"));
            }
        };
        let pid = child.id();
        let identity = match native::identity(pid) {
            Ok(i) => i,
            Err(_) => {
                unsafe { libc::killpg(pid as i32, libc::SIGKILL) };
                let _ = child.wait();
                drop_trial();
                return self.finish(c, "unknown", None);
            }
        };
        // No stdout stream to read: the closed channel marks EOF at once.
        let (_, rx) = mpsc::channel();
        self.live.insert(
            execution.to_string(),
            Live {
                child,
                command: c.clone(),
                native: None,
                seq: 2,
                completed: true,
                stopped: false,
                owned: HashMap::from([(pid, identity)]),
                exit: None,
                cleanup_unknown: false,
                rx,
                eof: false,
                last_sample: std::time::Instant::now(),
                wire: None,
                group: Some(pid as i32),
                deadline: timeout.map(|t| std::time::Instant::now() + t),
                timed_out: false,
                trial,
                blocks: Default::default(),
            },
        );
        self.event(c, 1, "script.started", json!({"commit":p["commit"],"timeoutMs":p["timeout_ms"],"trialWorktree":work}))?;
        self.receipt(c, "started", None)
    }
    /// Pipeline step inputs (Lab Stage 2): payload `inputs` [{artifactId, path}] are downloaded to FLEET_INPUT_DIR/<path>
    /// before the step starts. Paths are relative and never leave the input folder.
    fn stage_inputs(&self, p: &Value, input: &std::path::Path, execution: &Value) -> std::result::Result<(), String> {
        let list = p["inputs"].as_array().cloned().unwrap_or_default();
        if list.is_empty() { return Ok(()) }
        let access = env::var_os("FLEET_CF_ACCESS_FILE").map(PathBuf::from).or_else(|| env::var_os("HOME").map(|h| PathBuf::from(h).join(".config/fleet/cf-access-machine.env"))).and_then(|f| upload::access_token(&f));
        let client = upload::Client::new(self.url.clone(), self.token.clone(), self.host.clone(), access);
        for i in list {
            let (Some(id), Some(rel)) = (i["artifactId"].as_str(), i["path"].as_str()) else { return Err("invalid input".into()) };
            if rel.starts_with('/') || rel.split('/').any(|x| x.is_empty() || x == "." || x == "..") { return Err(format!("invalid input path {rel}")) }
            let r = client.post("/api/hosts/artifacts/get", json!({"hostId":self.host,"executionId":execution,"artifactId":id})).map_err(|e| format!("input {rel}: {e:?}"))?;
            let url = r["url"].as_str().ok_or(format!("input {rel} unavailable"))?;
            let target = input.join(rel);
            fs::create_dir_all(target.parent().unwrap()).map_err(|e| e.to_string())?;
            if !target.parent().unwrap().canonicalize().map_err(|e| e.to_string())?.starts_with(input.canonicalize().map_err(|e| e.to_string())?) { return Err(format!("input {rel} escapes the input folder")) }
            use std::os::unix::fs::OpenOptionsExt;
            let mut out = fs::OpenOptions::new().write(true).create_new(true).custom_flags(libc::O_NOFOLLOW).open(&target).map_err(|e| format!("input {rel}: {e}"))?;
            std::io::copy(&mut client.get(url).map_err(|e| format!("input {rel}: {e:?}"))?, &mut out).map_err(|e| e.to_string())?;
        }
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
                if l.exit.is_none() && !l.stopped && l.deadline.is_some_and(|d| std::time::Instant::now() >= d) {
                    l.timed_out = true;
                    l.stopped = true;
                    l.seq += 1;
                    self.event(&l.command, l.seq, "script.timeout", json!({"timeoutMs":l.command["payload"]["timeout_ms"]}))?;
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
                            let at = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH)?.as_millis() as u64;
                            let projection = l.blocks.project(harness::name(&l.command["payload"]), &event, at);
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
                            let mut retained: contracts::EventData = serde_json::from_value(event.clone())?;
                            retained.fleet_normalized = Some(serde_json::from_value(projection)?);
                            self.typed_event(&l.command, l.seq, event["type"].as_str().unwrap_or("unknown"), retained)?;
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
                    let dir = self.session_dir(&id(&l.command, "sessionId")?);
                    // Project sessions run in their worktree, so the agent writes output/ there.
                    let output = [dir.join("worktree").join("output"), dir.join("output")].into_iter().find(|o| o.is_dir()).unwrap_or_else(|| dir.join("output"));
                    let mut declared = vec![];
                    if output.is_dir() {
                        if let Err(_) = self.artifacts(&output, &output, &execution, &mut declared) {
                            if let Some((top, w)) = &l.trial {
                                worktree::remove_trial(top, w);
                            }
                            return self
                                .finish(&l.command, "failed", l.native.as_deref())
                                .map(|_| true);
                        }
                    }
                    // The coordinator holds a completed run with required artifacts still uploading as waiting (P3-D2).
                    *self.declared.borrow_mut() = declared;
                    if let Some((top, w)) = &l.trial {
                        l.seq += 1;
                        self.event(&l.command, l.seq, "trial.removed", json!({"removed":worktree::remove_trial(top, w)}))?;
                    }
                    self.finish(
                        &l.command,
                        if l.cleanup_unknown {
                            "unknown"
                        } else if l.timed_out {
                            "failed"
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
        self.typed_event(c, seq, kind, serde_json::from_value(data)?)
    }
    fn typed_event(&self, c: &Value, seq: u64, kind: &str, data: contracts::EventData) -> Result<()> {
        let event = contracts::HostEvent { id: format!("{}-{seq}", id(c, "id")?), seq, r#type: kind.into(), timestamp: std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH)?.as_millis() as u64, data };
        self.queue("/api/hosts/events", serde_json::to_value(contracts::HostEvents { host_id: self.host.clone(), execution_id: id(c, "executionId")?.into(), events: vec![event] })?)
    }
    /// Collects the output folder: each regular file (symlinks skipped, nothing outside the root) is copied into the spool, so
    /// later edits and worktree cleanup cannot change what is uploaded, and queued for upload with its relative path as name.
    fn artifacts(
        &self,
        root: &std::path::Path,
        dir: &std::path::Path,
        execution: &str,
        declared: &mut Vec<String>,
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
                self.artifacts(root, &e.path(), execution, declared)?
            } else if t.is_file() {
                let path = e.path();
                let relative = path.strip_prefix(root)?.to_str().ok_or("non utf8 artifact")?.to_string();
                if relative.split('/').any(|p| p.is_empty() || p == "." || p == "..") {
                    return Err("artifact path invalid".into());
                }
                use std::os::unix::fs::OpenOptionsExt;
                if !path.canonicalize()?.starts_with(root.canonicalize()?) {
                    return Err("artifact escaped root".into());
                }
                let mut file = fs::OpenOptions::new().read(true).custom_flags(libc::O_NOFOLLOW).open(&path)?;
                let id = new_id();
                let spool = self.root.join("spool").join(&id);
                std::io::copy(&mut file, &mut fs::File::create(&spool)?)?;
                upload::enqueue(&self.db, &id, "upload", &json!({"path":spool,"spool":true,"previews":true,"name":relative,"mediaType":media_type(&relative),"executionId":execution,"required":false}))?;
                declared.push(relative);
            }
        }
        Ok(())
    }
    fn stores(&self) -> Value {
        let Some(cfg) = &self.store_cfg else { return json!([]) };
        let mut r = self.store_report.borrow_mut();
        if r.0.is_none_or(|t| t.elapsed() >= Duration::from_secs(15)) { *r = (Some(std::time::Instant::now()), store::report(cfg)); }
        r.1.clone()
    }
}
fn capabilities() -> Vec<String> {
    let mut c: Vec<String> = ["codex","gpt-6.1-sol","low","static-output","steer","native-fork","artifact-store","script"].map(String::from).to_vec();
    if let Some(v) = preview::ffmpeg_version() { c.push(format!("ffmpeg:{v}")); }
    c
}
/// Media type from the file extension; unknown types are stored as octet-stream.
pub fn media_type(name: &str) -> &'static str {
    match name.rsplit('.').next().unwrap_or("").to_ascii_lowercase().as_str() {
        "html" | "htm" => "text/html", "css" => "text/css", "js" | "mjs" => "text/javascript", "json" => "application/json",
        "txt" | "md" | "log" => "text/plain", "csv" => "text/csv", "xml" => "application/xml", "pdf" => "application/pdf",
        "svg" => "image/svg+xml", "png" => "image/png", "jpg" | "jpeg" => "image/jpeg", "webp" => "image/webp", "gif" => "image/gif", "ico" => "image/x-icon",
        "mp4" => "video/mp4", "mov" => "video/quicktime", "webm" => "video/webm", "mkv" => "video/x-matroska",
        "glb" => "model/gltf-binary", "gltf" => "model/gltf+json", "obj" => "model/obj", "stl" => "model/stl", "usdz" => "model/vnd.usdz+zip",
        "woff2" => "font/woff2", "zip" => "application/zip", "jsonl" | "ndjson" => "application/x-ndjson",
        _ => "application/octet-stream",
    }
}
pub fn new_id() -> String {
    let mut b = [0u8; 16];
    if let Ok(mut f) = fs::File::open("/dev/urandom") { let _ = f.read_exact(&mut b); }
    b[6] = (b[6] & 0x0f) | 0x40;
    b[8] = (b[8] & 0x3f) | 0x80;
    let h: String = b.iter().map(|x| format!("{x:02x}")).collect();
    format!("{}-{}-{}-{}-{}", &h[0..8], &h[8..12], &h[12..16], &h[16..20], &h[20..32])
}
/// `fleet-executor reproject`: one JSON line per stored record on stdin, `{"executionId","harness","timestamp","record"}`,
/// in seq order; one projection per line on stdout. Block state is kept per execution. Needs no data dir, lock or journal,
/// so it runs next to a live executor (docs/designs/fleet-conversations.md, Stage 1 item 2).
fn reproject() -> Result<()> {
    use std::io::{BufRead, Write};
    let mut state: HashMap<String, harness::Blocks> = HashMap::new();
    let mut out = std::io::BufWriter::new(std::io::stdout().lock());
    for line in std::io::stdin().lock().lines() {
        let v: Value = serde_json::from_str(&line?)?;
        let b = state.entry(v["executionId"].as_str().unwrap_or("").to_string()).or_default();
        let p = b.project(v["harness"].as_str().unwrap_or("codex"), &v["record"], v["timestamp"].as_u64().unwrap_or(0));
        writeln!(out, "{p}")?;
    }
    Ok(out.flush()?)
}
fn main() -> Result<()> {
    if env::args().nth(1).as_deref() == Some("reproject") {
        return reproject();
    }
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
    db.execute_batch("PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; CREATE TABLE IF NOT EXISTS commands(id TEXT PRIMARY KEY,body TEXT NOT NULL,terminal INTEGER DEFAULT 0); CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY,native TEXT NOT NULL); CREATE TABLE IF NOT EXISTS delegation_requests(id TEXT PRIMARY KEY,body TEXT NOT NULL,response TEXT);")?;
    let mut e = Executor {
        db,
        root: root.clone(),
        url: env::var("FLEET_COORDINATOR_URL")?,
        // The host's own secret (people-access §3), from a private file; the coordinator derives the host from it.
        token: host_secret(env::var_os("FLEET_HOST_SECRET_FILE").ok_or("FLEET_HOST_SECRET_FILE required")?.into())?,
        host: env::var("FLEET_HOST_ID")?,
        live: HashMap::new(),
        sessions: env::var_os("FLEET_SESSIONS_ROOT").map(PathBuf::from).unwrap_or_else(|| root.join("sessions")),
        harness_cache: HashMap::new(),
        store_cfg: store::config(),
        declared: Default::default(),
        store_report: std::cell::RefCell::new((None, json!([]))),
    };
    outbox::schema(&e.db)?;
    upload::schema(&e.db)?;
    fs::create_dir_all(root.join("spool"))?;
    // Artifact transfers run on their own thread; Access token for store tunnels from FLEET_CF_ACCESS_FILE.
    let access = env::var_os("FLEET_CF_ACCESS_FILE").map(PathBuf::from).or_else(|| env::var_os("HOME").map(|h| PathBuf::from(h).join(".config/fleet/cf-access-machine.env"))).and_then(|f| upload::access_token(&f));
    upload::spawn(root.join("journal.sqlite"), upload::Client::new(e.url.clone(), e.token.clone(), e.host.clone(), access), |_, db, body, response| {
        if body["previews"] == true && response["state"] == "stored" {
            preview::build(db, body, response);
        }
        if body["spool"] == true {
            if let Some(p) = body["path"].as_str() { let _ = fs::remove_file(p); }
        }
    })?;
    if let Some(r) = e.store_cfg.as_ref().and_then(|c| store::registration(c, &e.host)) {
        outbox::queue(&e.db, "/api/hosts/stores", r)?;
    }
    e.db.busy_timeout(Duration::from_secs(5))?;
    outbox::spawn(root.join("journal.sqlite"), e.url.clone(), e.token.clone(), e.host.clone())?;
    fs::create_dir_all(&e.sessions)?;
    fs::set_permissions(&e.sessions, fs::Permissions::from_mode(0o700))?;
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
            // A poll the edge answered is asked again every 2 s (outbox::edge_redirect), not every 100 ms.
            if outbox::edge_wait(outbox::POLL) { return Ok(()) }
            let poll = contracts::PollRequest { host_id: e.host.clone(), platform: platform().into(), capabilities: capabilities(), stores: serde_json::from_value(e.stores())?, problems: outbox::edge_problems(outbox::POLL) };
            let reply: contracts::PollReply = serde_json::from_value(e.post("/api/hosts/poll", serde_json::to_value(poll)?)?)?;
            // ponytail: commands go on as JSON values; command payloads get schema types when each kind is next changed.
            for c in &reply.commands.into_iter().map(serde_json::to_value).collect::<std::result::Result<Vec<_>, _>>()? {
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
            }
            Ok(())
        })();
        // A redirected poll is already signalled once by the edge state (outbox::edge_redirect); other failures log here.
        if let Err(err) = &result {
            if err.downcast_ref::<outbox::Redirected>().is_none() { eprintln!("executor exchange failed; retained journal, retry pending") }
        }
        std::thread::sleep(Duration::from_millis(100));
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn script_executor(root: &std::path::Path) -> Executor {
        let db = Connection::open_in_memory().unwrap();
        db.execute_batch("CREATE TABLE commands(id TEXT PRIMARY KEY,body TEXT,terminal INTEGER DEFAULT 0); CREATE TABLE outbox(route TEXT,body TEXT);").unwrap();
        upload::schema(&db).unwrap();
        fs::create_dir_all(root.join("spool")).unwrap();
        Executor { db, root: root.into(), url: String::new(), token: String::new(), host: "fake".into(), live: HashMap::new(), sessions: root.join("sessions"), harness_cache: HashMap::new(), store_cfg: None, declared: Default::default(), store_report: std::cell::RefCell::new((None, json!([]))) }
    }
    fn receipts(e: &Executor, cid: &str) -> Vec<String> {
        let mut st = e.db.prepare("SELECT body FROM outbox WHERE route='/api/hosts/receipts'").unwrap();
        st.query_map([], |r| r.get::<_, String>(0)).unwrap().map(|b| serde_json::from_str::<Value>(&b.unwrap()).unwrap()).filter(|v| v["commandId"] == cid).map(|v| v["status"].as_str().unwrap().to_string()).collect()
    }
    fn settle(e: &mut Executor, execution: &str) {
        for _ in 0..200 { e.tick().unwrap(); if !e.live.contains_key(execution) { return; } std::thread::sleep(Duration::from_millis(25)); }
        panic!("script did not settle");
    }
    fn group_alive(marker: &std::path::Path) -> bool {
        let pid: i32 = fs::read_to_string(marker).unwrap().trim().parse().unwrap();
        unsafe { libc::kill(pid, 0) == 0 }
    }
    #[test]
    fn script_output_timeout_and_group_kill() {
        let root = env::temp_dir().join(format!("fleet-script-{}", std::process::id()));
        let _ = fs::remove_dir_all(&root);
        let mut e = script_executor(&root);
        // Completes: full stdout and the output folder are artifacts, inputs are visible.
        let ok = json!({"id":"c1","executionId":"x1","sessionId":"s1","kind":"script","payload":{"command":"echo hello; echo result > \"$FLEET_OUTPUT_DIR/r.txt\"; test -d \"$FLEET_INPUT_DIR\""}});
        e.run(ok).unwrap();
        settle(&mut e, "x1");
        assert_eq!(receipts(&e, "c1").last().unwrap(), "completed");
        assert_eq!(fs::read_to_string(root.join("sessions/s1/output/script.stdout")).unwrap(), "hello\n");
        let names: Vec<String> = { let mut st = e.db.prepare("SELECT body FROM transfers").unwrap(); st.query_map([], |r| r.get::<_, String>(0)).unwrap().map(|b| serde_json::from_str::<Value>(&b.unwrap()).unwrap()["name"].as_str().unwrap().to_string()).collect() };
        for n in ["r.txt", "script.stdout", "script.stderr"] { assert!(names.contains(&n.to_string()), "{n} {names:?}"); }
        // Timeout: a double-forked grandchild (reparented, still in the group) is killed too; the run fails.
        let slow = json!({"id":"c2","executionId":"x2","sessionId":"s2","kind":"script","payload":{"timeout_ms":300,"command":"(sleep 30 & echo $! > \"$FLEET_OUTPUT_DIR/../pid\"); sleep 30"}});
        e.run(slow).unwrap();
        settle(&mut e, "x2");
        assert_eq!(receipts(&e, "c2").last().unwrap(), "failed");
        std::thread::sleep(Duration::from_millis(100));
        assert!(!group_alive(&root.join("sessions/s2/pid")));
        // Stop: the group is killed and the run is stopped.
        let long = json!({"id":"c3","executionId":"x3","sessionId":"s3","kind":"script","payload":{"command":"sleep 30 & echo $! > \"$FLEET_OUTPUT_DIR/../pid\"; wait"}});
        e.run(long).unwrap();
        for _ in 0..100 { if root.join("sessions/s3/pid").exists() { break; } std::thread::sleep(Duration::from_millis(20)); }
        e.run(json!({"id":"c4","executionId":"x3","sessionId":"s3","kind":"stop","payload":{}})).unwrap();
        settle(&mut e, "x3");
        assert_eq!(receipts(&e, "c3").last().unwrap(), "stopped");
        std::thread::sleep(Duration::from_millis(100));
        assert!(!group_alive(&root.join("sessions/s3/pid")));
        // Refused: bad timeout, half a trial spec.
        e.run(json!({"id":"c5","executionId":"x5","sessionId":"s5","kind":"script","payload":{"command":"true","timeout_ms":0}})).unwrap();
        e.run(json!({"id":"c6","executionId":"x6","sessionId":"s6","kind":"script","payload":{"command":"true","commit":"0000000000000000000000000000000000000000"}})).unwrap();
        assert_eq!(receipts(&e, "c5").last().unwrap(), "failed");
        assert_eq!(receipts(&e, "c6").last().unwrap(), "failed");
    }
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
            sessions: env::temp_dir(),
            harness_cache: HashMap::new(),
            store_cfg: None,
            declared: Default::default(),
            store_report: std::cell::RefCell::new((None, json!([]))),
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
        // Script commands: kill the whole group, including members that left the parent tree. A pid stays reserved while
        // its process group has members, so the id names our group until killpg reports none (ESRCH), then it is dropped.
        if let Some(g) = self.group {
            if unsafe { libc::killpg(g, libc::SIGKILL) } != 0 {
                self.group = None;
            }
        }
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
            sessions: env::temp_dir(),
            harness_cache: HashMap::new(),
            store_cfg: None,
            declared: Default::default(),
            store_report: std::cell::RefCell::new((None, json!([]))),
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
