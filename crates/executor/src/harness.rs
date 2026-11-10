//! Native protocol shapes borrowed from AgentQ registry at d0f6bfa; no source copied.
use serde_json::{Value, json};
use std::{
    os::unix::{
        fs::{MetadataExt, PermissionsExt},
        process::CommandExt,
    },
    path::Path,
    process::Command,
};
type Result<T> = std::io::Result<T>;
fn fail(s: &str) -> std::io::Error {
    std::io::Error::other(s)
}
pub fn name(p: &Value) -> &str {
    p.get("harness")
        .or_else(|| p.get("agentConfig").and_then(|v| v.get("harness")))
        .map(|v| v.as_str().unwrap_or("invalid"))
        .unwrap_or("codex")
}
fn private(p: &Path) -> Result<()> {
    let m = std::fs::symlink_metadata(p)?;
    if !p.is_absolute() || m.file_type().is_symlink() || m.permissions().mode() & 0o077 != 0 {
        return Err(fail("native configuration must use private absolute paths"));
    }
    Ok(())
}
pub fn command(dir: &Path, native: Option<&str>, p: &Value) -> Result<(Command, Option<Vec<u8>>)> {
    let h = name(p);
    if (h != "antigravity" && p.get("settings").is_some())
        || p.get("environment").is_some()
        || (h == "codex" && p.get("tools").is_some())
    {
        return Err(fail(
            "requested native settings/environment/tools mapping unsupported",
        ));
    }
    if !matches!(h, "codex" | "claude" | "antigravity") {
        return Err(fail("unknown harness"));
    }
    if h == "codex" && p.get("account").is_none() {
        return Ok((
            crate::native::command(dir, native, p["text"].as_str().ok_or(fail("missing text"))?)?,
            None,
        ));
    }
    let registry =
        std::env::var_os("FLEET_NATIVE_ACCOUNTS").ok_or(fail("FLEET_NATIVE_ACCOUNTS required"))?;
    command_registry(dir, native, p, Path::new(&registry))
}
fn command_registry(
    dir: &Path,
    native: Option<&str>,
    p: &Value,
    path: &Path,
) -> Result<(Command, Option<Vec<u8>>)> {
    let mut resolved = p.clone();
    if let Some(profile) = p.get("agentConfig") {
        crate::profile::validate(profile).map_err(fail)?;
        for key in ["harness", "model", "effort"] {
            if let (Some(requested), Some(configured)) = (p.get(key), profile.get(key)) {
                if requested != configured {
                    return Err(fail("requested configuration conflicts with profile"));
                }
            }
        }
        for key in [
            "harness",
            "model",
            "effort",
            "instructions",
            "instructionRole",
            "tools",
            "settings",
        ] {
            if let Some(v) = profile.get(key) {
                resolved[key] = v.clone();
            }
        }
    }
    let p = &resolved;
    let h = name(p);
    private(path)?;
    if std::fs::metadata(path)?.uid() != unsafe { libc::geteuid() } {
        return Err(fail("registry must be owned by executor user"));
    }
    let accounts: Value = serde_json::from_slice(&std::fs::read(path)?)?;
    let account = p["account"]
        .as_str()
        .ok_or(fail("named account required"))?;
    let a = accounts
        .get(account)
        .ok_or(fail("account not provisioned"))?;
    if a.get("readiness").is_some_and(|s| s != "ready") {
        return Err(fail(
            "named host account is unprovisioned; operator login and readiness review required",
        ));
    }
    if a["harness"] != h {
        return Err(fail("account harness mismatch"));
    }
    let home = Path::new(a["home"].as_str().ok_or(fail("home missing"))?);
    let nh = Path::new(a["nativeHome"].as_str().ok_or(fail("nativeHome missing"))?);
    private(home)?;
    private(nh)?;
    if !home.is_dir() || !nh.is_dir() {
        return Err(fail("homes must be directories"));
    }
    let credential_home = if matches!(h, "antigravity" | "claude") && a["credentialAuthority"] == "native-current" {
        let path = Path::new(
            a["credentialHome"]
                .as_str()
                .ok_or(fail("credentialHome required for native-current"))?,
        );
        let m = std::fs::symlink_metadata(path)?;
        if !path.is_absolute()
            || !m.is_dir()
            || m.file_type().is_symlink()
            || m.uid() != unsafe { libc::geteuid() }
            || m.permissions().mode() & 0o022 != 0
        {
            return Err(fail(
                "native-current credential home must be owner-controlled",
            ));
        }
        Some(path)
    } else {
        if a.get("credentialAuthority").is_some() {
            return Err(fail("unsupported credential authority"));
        }
        None
    };
    let exe = a["executable"]
        .as_str()
        .ok_or(fail("host executable missing"))?;
    if !Path::new(exe).is_absolute() {
        return Err(fail("host executable must be absolute"));
    }
    let mut binding = json!({"harness":h,"account":account,"home":home.canonicalize()?,"nativeHome":nh.canonicalize()?});
    if h == "claude" {
        claude_placement_here().map_err(|e| fail(&format!("Claude is ineligible on this host: {e}")))?;
    }
    if matches!(h, "antigravity" | "claude") {
        binding["transport"] = json!(if h == "claude" { "stream-json" } else { "acp-1.5.3" });
        binding["credentialAuthority"] = a["credentialAuthority"].clone();
        binding["credentialHome"] = credential_home
            .map(|p| p.canonicalize())
            .transpose()?
            .map(|p| json!(p))
            .unwrap_or(Value::Null);
    }
    let marker = dir.join(format!(".fleet-harness-account.{h}.{account}.json"));
    let legacy = dir.join(".fleet-harness-account.json");
    if native.is_some()
        && std::fs::read(&marker).or_else(|_| std::fs::read(&legacy))
            .ok()
            .and_then(|b| serde_json::from_slice::<Value>(&b).ok())
            .as_ref()
            != Some(&binding)
    {
        return Err(fail("native account/session binding mismatch"));
    }
    std::fs::write(marker, serde_json::to_vec(&binding)?)?;
    if h == "codex" {
        return Ok((
            crate::native::command_at(
                dir,
                native,
                p["text"].as_str().ok_or(fail("missing text"))?,
                home,
                nh,
                exe,
            )?,
            None,
        ));
    }
    if (h != "antigravity" && p.get("settings").is_some()) || p.get("environment").is_some() {
        return Err(fail("unmapped native settings/environment"));
    }
    let required_role = if h == "claude" { "system" } else { "user" };
    if p.get("instructionRole").is_some_and(|r| r != required_role) {
        return Err(fail(
            "requested instruction role unsupported by native adapter",
        ));
    }
    if h == "claude" && p.get("effort").is_some_and(|v| !v.is_null()) {
        return Err(fail("effort unsupported by selected native adapter"));
    }
    let model = p["model"]
        .as_str()
        .filter(|s| !s.is_empty() && !s.starts_with('-'))
        .ok_or(fail("explicit native model required"))?;
    let exe = a["executable"]
        .as_str()
        .ok_or(fail("host executable missing"))?;
    if !Path::new(exe).is_absolute() {
        return Err(fail("host executable must be absolute"));
    }
    let mut c = Command::new(exe);
    c.env_clear()
        .env("HOME", home)
        .current_dir(dir)
        .process_group(0);
    for k in ["PATH", "TMPDIR", "LANG", "SSL_CERT_FILE", "SSL_CERT_DIR"] {
        if let Some(v) = std::env::var_os(k) {
            c.env(k, v);
        }
    }
    let text = p["text"].as_str().ok_or(fail("missing text"))?;
    let instructions = p["instructions"].as_str().unwrap_or("");
    if h == "claude" {
        let passwd = unsafe { libc::getpwuid(libc::geteuid()) };
        if passwd.is_null() { return Err(fail("host user metadata unavailable")); }
        let user = unsafe { std::ffi::CStr::from_ptr((*passwd).pw_name) }.to_str().map_err(|_| fail("invalid host user metadata"))?;
        c.env("USER", user).env("LOGNAME", user);
        if let Some(credential_home) = credential_home { c.env("HOME", credential_home); }
        // Skills (Lab Stage 2): written as a local plugin and loaded with --plugin-dir; slash commands stay disabled otherwise.
        let skills: Vec<&Value> = p["skills"].as_array().into_iter().flatten().collect();
        if skills.is_empty() {
            c.arg("--disable-slash-commands");
        } else {
            let plugin = dir.join(".fleet-skills");
            let _ = std::fs::remove_dir_all(&plugin);
            std::fs::create_dir_all(plugin.join(".claude-plugin"))?;
            std::fs::write(plugin.join(".claude-plugin/plugin.json"), json!({"name":"fleet-skills","version":"1.0.0","description":"Skills granted by the Fleet profile"}).to_string())?;
            for s in &skills {
                let id = s["id"].as_str().filter(|id| !id.is_empty() && id.bytes().all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'-')).ok_or(fail("invalid skill id"))?;
                std::fs::create_dir_all(plugin.join("skills").join(id))?;
                std::fs::write(plugin.join("skills").join(id).join("SKILL.md"), s["text"].as_str().unwrap_or(""))?;
            }
            c.arg("--plugin-dir").arg(&plugin);
        }
        c.env("CLAUDE_CONFIG_DIR", nh).args([
            "-p",
            "--output-format",
            "stream-json",
            "--verbose",
            "--model",
            model,
            "--permission-mode",
            "dontAsk",
            "--strict-mcp-config",
            "--setting-sources",
            "",
            "--system-prompt",
            instructions,
        ]);
        if p.get("tools").is_some_and(|v| {
            v.as_array().is_none_or(|a| {
                a.iter().any(|t| {
                    !matches!(
                        t.as_str(),
                        Some("Read" | "Edit" | "Write" | "Bash" | "Glob" | "Grep")
                    )
                })
            })
        }) {
            return Err(fail("unsupported Claude tool grant"));
        }
        let mut tools = p["tools"]
            .as_array()
            .map(|a| a.iter().filter_map(Value::as_str).collect::<Vec<_>>())
            .unwrap_or_default();
        tools.extend(claude_extra_tools(p));
        c.args(["--tools", &tools.join(",")]);
        if let Some(n) = native {
            if !safe_native_id(n) { return Err(fail("invalid native session id")); }
            c.arg(format!("--resume={n}"));
        } else if let Some(src) = p["fork"]["nativeSessionId"].as_str().filter(|s| safe_native_id(s)) {
            // Claude stores sessions per cwd project; copy the source transcript into this session's project, then fork it natively.
            let file = format!("{src}.jsonl");
            let found = std::fs::read_dir(nh.join("projects"))?.flatten().map(|d| d.path().join(&file)).find(|f| f.is_file()).ok_or(fail("native fork source not found"))?;
            let enc: String = dir.canonicalize()?.to_string_lossy().chars().map(|ch| if ch.is_ascii_alphanumeric() { ch } else { '-' }).collect();
            let target = nh.join("projects").join(enc);
            std::fs::create_dir_all(&target)?;
            if found.parent() != Some(target.as_path()) { std::fs::copy(&found, target.join(&file))?; }
            c.args([format!("--resume={src}").as_str(), "--fork-session"]);
        }
        // Variadic native tool flags must never consume the user prompt.
        Ok((c, Some(format!("{text}\n").into_bytes())))
    } else {
        crate::profile::validate_antigravity_settings(p.get("settings")).map_err(fail)?;
        if p.get("effort").is_some_and(|v| !v.is_null()) {
            return Err(fail(
                "ACP effort must be selected explicitly through an advertised model ID",
            ));
        }
        if let Some(home) = credential_home {
            c.env("HOME", home);
        }
        c.env("GEMINI_HOME", nh)
            .args(["acp", "--gemini-dir"])
            .arg(nh)
            .args([
                "--app-data-dir",
                "antigravity-cli",
                "--include-default-customizations=false",
            ]);
        if let Some(sandbox) = p["settings"]["sandbox"].as_bool() {
            c.arg(format!("--sandbox={sandbox}"));
        }
        Ok((c, None))
    }
}
/// Native ids reach argv; allow only ASCII alphanumerics and '-', at most 64, never a leading '-'.
pub fn safe_native_id(n: &str) -> bool {
    !n.is_empty() && n.len() <= 64 && !n.starts_with('-') && n.chars().all(|ch| ch.is_ascii_alphanumeric() || ch == '-')
}
/// Claude placement (docs/configuration.md): the host setting FLEET_CLAUDE_PLACEMENT is `mac` or `windows-wsl`; unset means
/// Claude never runs here (PDS18). The setting must match the platform: `mac` needs macOS, `windows-wsl` needs Linux under
/// WSL, detected from the kernel release string.
pub fn claude_placement(setting: Option<&str>, os: &str, kernel_release: &str) -> std::result::Result<&'static str, &'static str> {
    let wsl = os == "linux" && kernel_release.to_ascii_lowercase().contains("microsoft");
    match setting {
        None | Some("") => Err("claude-placement-unset"),
        Some("mac") if os == "macos" => Ok("mac"),
        Some("windows-wsl") if wsl => Ok("windows-wsl"),
        Some("mac" | "windows-wsl") => Err("claude-placement-platform-mismatch"),
        Some(_) => Err("claude-placement-invalid"),
    }
}
#[cfg(test)]
thread_local! {
    /// Tests set the placement per thread instead of mutating the process environment.
    pub static TEST_PLACEMENT: std::cell::Cell<Option<&'static str>> = const { std::cell::Cell::new(None) };
}
pub fn claude_placement_here() -> std::result::Result<&'static str, &'static str> {
    #[cfg(test)]
    if let Some(v) = TEST_PLACEMENT.with(|p| p.get()) {
        return claude_placement(Some(v), std::env::consts::OS, &std::fs::read_to_string("/proc/sys/kernel/osrelease").unwrap_or_default());
    }
    let release = std::fs::read_to_string("/proc/sys/kernel/osrelease").unwrap_or_default();
    claude_placement(std::env::var("FLEET_CLAUDE_PLACEMENT").ok().as_deref(), std::env::consts::OS, &release)
}
/// Built-in Claude tools a run adds to its profile's grant: web only with `webRoute: builtin` (R3.4; the default route is the
/// granted connections), Skill when the profile grants skills.
pub fn claude_extra_tools(p: &Value) -> Vec<&'static str> {
    let mut t = vec![];
    if p["webRoute"] == "builtin" { t.extend(["WebSearch", "WebFetch"]); }
    if p["skills"].as_array().is_some_and(|s| !s.is_empty()) { t.push("Skill"); }
    t
}
pub fn claude_mcp(c: &mut Command, profile: &Value, servers: &Value, extra: &[String]) {
    let mut allowed: Vec<String> = profile["tools"]
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
        .map(str::to_owned)
        .collect();
    allowed.extend(extra.iter().cloned());
    if servers.get("design-rules").is_some() {
        allowed.push("mcp__design-rules__get_design_rules".into());
    }
    if servers.get("fleet").is_some() {
        for tool in ["session_transition","session_fork","session_resume","session_handoffs","message_send","queue_list","ask","reply","questions","worker_done","execution_status","artifact_save","artifact_list","artifact_get"] { allowed.push(format!("mcp__fleet__{tool}")); }
        if profile["delegationProfiles"].as_array().is_some_and(|p| !p.is_empty()) { allowed.push("mcp__fleet__request_child".into()); }
        let context = servers["fleet"]["args"].as_array().and_then(|a| a.last()).and_then(Value::as_str).and_then(|s| serde_json::from_str::<Value>(s).ok());
        if context.as_ref().is_some_and(|c| c["projectId"].is_string()) {
            for tool in ["project_context","catalogue","pipeline_run","direct_search","pipeline_history","pipeline_get","question_ask","question_history","account_list"] { allowed.push(format!("mcp__fleet__{tool}")); }
        }
    }
    c.args([
        "--mcp-config",
        &json!({"mcpServers":servers}).to_string(),
        "--allowedTools",
        &allowed.join(","),
    ]);
}
fn tools(h: &str, v: &Value) -> Vec<Value> {
    if h == "claude" {
        return v["message"]["content"].as_array().into_iter().flatten().filter(|b| matches!(b["type"].as_str(),Some("tool_use"|"tool_result"))).map(|b|json!({"id":b.get("id").or(b.get("tool_use_id")),"name":b.get("name"),"arguments":b.get("input"),"result":b.get("content"),"error":b.get("is_error")})).collect();
    }
    if h == "antigravity"
        && matches!(
            v["update"]["sessionUpdate"].as_str(),
            Some("tool_call" | "tool_call_update")
        )
    {
        let b = &v["update"];
        return vec![
            json!({"id":b["toolCallId"],"name":b.get("name").or(b.get("title")),"arguments":b.get("rawInput"),"result":b.get("rawOutput").filter(|v| !v.is_null() && !v.as_object().is_some_and(|o|o.is_empty())).or(b.get("content")).or(b.get("rawOutput")),"status":b["status"],"kind":b["kind"]}),
        ];
    }
    if h == "antigravity" && v["step_update"]["step_type"] == "tool" {
        let b = &v["step_update"];
        return vec![
            json!({"id":b["step_index"],"name":b["tool_name"],"arguments":b["tool_info"]["parameters"],"result":b["tool_info"]["result"],"status":b["state"]}),
        ];
    }
    if h == "antigravity" && v["type"] == "PLANNER_RESPONSE" {
        return v["tool_calls"].as_array().into_iter().flatten().map(|b|json!({"id":b["id"],"name":b["name"],"arguments":b.get("arguments").or(b.get("args")),"result":null,"status":"requested"})).collect();
    }
    if h == "antigravity" && v["type"] == "TOOL_RESPONSE" {
        return vec![
            json!({"id":v["tool_call_id"],"name":null,"arguments":null,"result":v.get("content").or(v.get("result")),"status":v["status"]}),
        ];
    }
    if h == "codex"
        && matches!(
            v["item"]["type"].as_str(),
            Some(
                "command_execution"
                    | "mcp_tool_call"
                    | "function_call"
                    | "function_call_output"
                    | "commandExecution"
                    | "mcpToolCall"
                    | "collabAgentToolCall"
            )
        )
    {
        let b = &v["item"];
        return vec![
            json!({"id":b.get("id"),"name":b.get("tool").or(b.get("name")).or(b.get("type")),"arguments":b.get("arguments").or(b.get("command")),"result":b.get("result").or(b.get("aggregatedOutput")).or(b.get("aggregated_output")).or(b.get("output")),"status":b.get("status"),"exitCode":b.get("exitCode").or(b.get("exit_code"))}),
        ];
    }
    vec![]
}
fn usage(v: &Value) -> Value {
    let Some(raw) = v.get("usage").or(v.pointer("/result/usage")) else {
        return Value::Null;
    };
    let source = raw.get("total").unwrap_or(raw);
    let mut fields = serde_json::Map::new();
    for (native, common) in [
        ("inputTokens", "input_tokens"),
        ("outputTokens", "output_tokens"),
        ("cachedInputTokens", "cached_input_tokens"),
        ("cacheWriteInputTokens", "cache_write_input_tokens"),
        ("reasoningOutputTokens", "reasoning_output_tokens"),
        ("totalTokens", "total_tokens"),
        ("cachedReadTokens", "cache_read_tokens"),
        ("cachedWriteTokens", "cache_write_tokens"),
        ("thoughtTokens", "thought_tokens"),
        ("input_tokens", "input_tokens"),
        ("output_tokens", "output_tokens"),
        ("cached_input_tokens", "cached_input_tokens"),
    ] {
        if let Some(n) = source.get(native).filter(|n| n.is_number()) {
            fields.insert(common.into(), n.clone());
        }
    }
    if fields.is_empty() {
        Value::Null
    } else {
        Value::Object(fields)
    }
}
pub fn project(h: &str, v: &Value) -> Value {
    let session = if h == "codex" {
        v.get("thread_id")
    } else if h == "claude" {
        v.get("session_id")
    } else {
        v.get("conversation_id")
            .or(v.get("conversationId"))
            .or(v.pointer("/result/conversation_id"))
            .or_else(|| {
                if matches!(
                    v["type"].as_str(),
                    Some("conversation.started" | "system" | "result")
                ) {
                    v.get("session_id")
                } else {
                    None
                }
            })
    };
    let success = match h {
        "codex" => v["type"] == "turn.completed",
        "claude" => v["type"] == "result" && v["is_error"] == false,
        _ => {
            (v["event"] == "result" && v["result"]["status"] == "SUCCESS")
                || v["type"] == "turn.completed"
        }
    };
    let mut messages = Vec::new();
    if h == "codex" && v["item"]["type"] == "agent_message" {
        if let Some(text) = v["item"]["text"].as_str() {
            messages.push(json!({"role":"assistant","text":text}));
        }
    }
    if h == "claude" {
        for b in v["message"]["content"].as_array().into_iter().flatten() {
            if b["type"] == "text" {
                if let Some(text) = b["text"].as_str() {
                    messages.push(json!({"role":v["message"]["role"].as_str().unwrap_or("assistant"),"text":text}));
                }
            }
        }
    }
    if h == "antigravity" {
        if let Some(text) = v["result"]["response"]
            .as_str()
            .or(v["step_update"]["text_delta"].as_str())
        {
            messages.push(json!({"role":"assistant","text":text}));
        }
    }
    if h == "antigravity"
        && matches!(
            v["update"]["sessionUpdate"].as_str(),
            Some("agent_message_chunk" | "user_message_chunk")
        )
    {
        if let Some(text) = v["update"]["content"]["text"].as_str() {
            messages.push(json!({"role":if v["update"]["sessionUpdate"]=="user_message_chunk" {"user"}else{"assistant"},"text":text,"delta":true}));
        }
    }
    let message = match h {
        "claude" => v.get("message"),
        "antigravity" => v.get("step_update").or(v.get("result")),
        _ => v.get("item"),
    };
    json!({"harness":h,"session":session,"completed":success,"lifecycle":v.get("type").or(v.get("event")),"error":v.get("error").or(v.pointer("/result/error")),"message":message,"messages":messages,"tools":tools(h,v),"usage":usage(v),"nativeUsage":v.get("usage").or(v.pointer("/result/usage")),"contextUsage":if v["update"]["sessionUpdate"]=="usage_update" {json!({"used":v["update"]["used"],"capacity":v["update"]["size"],"scope":"native context window; not additive billing usage"})}else{Value::Null},"usageScope":if h=="codex" && v.get("usage").is_some_and(|u|u.get("total").is_some()) {"native thread cumulative total; not additive"} else {"native report; scope unknown, may be cumulative; not additive"},"native":v})
}
/// Typed transcript blocks (docs/designs/fleet-conversations.md, "Block model"): text, reasoning and tool, the only producer of
/// reply text. State per execution: tools started and not yet ended (claude tool_use, ACP tool_call) with their start time, and
/// the open streams (ACP chunks). A chunk record carries `deltaOf` and its chunk as a block; the next record that is not a chunk
/// closes every open stream and carries the joined final blocks first, then its own, and names the closed streams in `closes`.
/// The first chunk of a stream also carries `opens: true`, so a reader finds streams without reading every chunk.
#[derive(Default)]
pub struct Blocks {
    tools: std::collections::HashMap<String, (Value, Value, u64)>,
    streams: Vec<(String, String, String)>,
    opened: u64,
}
fn text_of(v: &Value) -> String {
    match v {
        Value::String(s) => s.clone(),
        Value::Array(a) => a.iter().map(text_of).filter(|s| !s.is_empty()).collect::<Vec<_>>().join("\n"),
        Value::Object(o) => o.get("text").map(text_of).unwrap_or_default(),
        _ => String::new(),
    }
}
fn key(v: &Value) -> String {
    v.as_str().map(String::from).unwrap_or_else(|| v.to_string())
}
fn text_block(kind: &str, markdown: String) -> Option<Value> {
    if markdown.is_empty() { return None; }
    Some(if kind == "text" { json!({"kind":"text","role":"agent","markdown":markdown}) } else { json!({"kind":"reasoning","markdown":markdown}) })
}
fn tool_block(name: &Value, input: &Value, output: &Value, state: &Value, started: Value, ended: Value) -> Value {
    json!({"kind":"tool","name":if name.is_null() {json!("tool")} else {name.clone()},"input":input,"output":output,"state":if state.is_null() {json!("completed")} else {state.clone()},"startedAt":started,"endedAt":ended})
}
impl Blocks {
    /// project() plus `blocks` (and `deltaOf` / `closes` for streams); `at` is the record's time in ms.
    pub fn project(&mut self, h: &str, v: &Value, at: u64) -> Value {
        let mut p = project(h, v);
        let chunk = self.chunk(h, v);
        let mut blocks = Vec::new();
        if let Some((kind, text)) = chunk {
            let id = match self.streams.iter_mut().find(|s| s.0 == kind) {
                Some(s) => { s.2.push_str(&text); s.1.clone() }
                None => {
                    self.opened += 1;
                    let id = format!("{kind}:{}", self.opened);
                    self.streams.push((kind.clone(), id.clone(), text.clone()));
                    p["opens"] = json!(true);
                    id
                }
            };
            p["deltaOf"] = json!(id);
            p["blocks"] = json!(text_block(&kind, text).into_iter().collect::<Vec<_>>());
            return p;
        }
        let closed: Vec<_> = self.streams.drain(..).collect();
        if !closed.is_empty() { p["closes"] = json!(closed.iter().map(|s| &s.1).collect::<Vec<_>>()); }
        let streamed_text = closed.iter().any(|s| s.0 == "text");
        blocks.extend(closed.into_iter().filter_map(|(kind, _, text)| text_block(&kind, text)));
        // The step format repeats the streamed reply in result.response: the closed stream already holds it.
        blocks.extend(self.own(h, v, at).into_iter().filter(|b| !(streamed_text && v["result"]["response"].is_string() && b["kind"] == "text")));
        p["blocks"] = json!(blocks);
        p
    }
    fn chunk(&self, h: &str, v: &Value) -> Option<(String, String)> {
        if h != "antigravity" { return None; }
        let kind = match v["update"]["sessionUpdate"].as_str() { Some("agent_message_chunk") => "text", Some("agent_thought_chunk") => "reasoning", _ => {
            return v["step_update"]["text_delta"].as_str().map(|t| ("text".into(), t.into()));
        } };
        Some((kind.into(), text_of(&v["update"]["content"])))
    }
    fn own(&mut self, h: &str, v: &Value, at: u64) -> Vec<Value> {
        let t = v["type"].as_str().unwrap_or("");
        if t == "unparsed" || t.ends_with(".failed") || (h == "claude" && t == "result" && v["is_error"] == true) {
            return vec![tool_block(&json!(t), &Value::Null, v, &json!("failed"), json!(at), json!(at))];
        }
        let mut out = Vec::new();
        // A turn end settles tools that never reported an end, so a cut-off tool still shows.
        if matches!(t, "turn.completed" | "turn.failed" | "result") || v["event"] == "result" {
            let mut open: Vec<_> = self.tools.drain().collect();
            open.sort_by_key(|(_, x)| x.2);
            out.extend(open.into_iter().map(|(_, (name, input, started))| tool_block(&name, &input, &Value::Null, &json!("unfinished"), json!(started), json!(at))));
        }
        match h {
            "codex" if t == "item.completed" => {
                let i = &v["item"];
                let started = i.get("startedAtMs").filter(|x| x.is_number()).cloned().unwrap_or(json!(at));
                let ended = i.get("completedAtMs").filter(|x| x.is_number()).cloned().unwrap_or(json!(at));
                match i["type"].as_str().unwrap_or("") {
                    "agent_message" | "agentMessage" => out.extend(text_block("text", text_of(&i["text"]))),
                    "reasoning" | "plan" => {
                        let text = [text_of(&i["text"]), text_of(&i["summary"]), text_of(&i["content"])].into_iter().filter(|s| !s.is_empty()).collect::<Vec<_>>().join("\n");
                        out.extend(text_block("reasoning", text))
                    }
                    "userMessage" | "user_message" => {}
                    other => out.push(tool_block(
                        i.get("tool").or(i.get("name")).filter(|x| !x.is_null()).unwrap_or(&json!(other)),
                        i.get("arguments").or(i.get("command")).or(i.get("changes")).unwrap_or(&Value::Null),
                        i.get("result").or(i.get("aggregatedOutput")).or(i.get("aggregated_output")).or(i.get("output")).or(i.get("error")).or(i.get("message")).unwrap_or(&Value::Null),
                        &if other == "error" { json!("failed") } else { i["status"].clone() }, started, ended)),
                }
            }
            "claude" => {
                let assistant = t == "assistant";
                for b in v["message"]["content"].as_array().into_iter().flatten() {
                    match b["type"].as_str() {
                        Some("text") if assistant => out.extend(text_block("text", text_of(&b["text"]))),
                        Some("thinking") if assistant => out.extend(text_block("reasoning", text_of(&b["thinking"]))),
                        Some("tool_use") => { self.tools.insert(key(&b["id"]), (b["name"].clone(), b["input"].clone(), at)); }
                        Some("tool_result") => {
                            let (name, input, started) = self.tools.remove(&key(&b["tool_use_id"])).unwrap_or((Value::Null, Value::Null, at));
                            out.push(tool_block(&name, &input, &b["content"], &json!(if b["is_error"] == true { "failed" } else { "completed" }), json!(started), json!(at)));
                        }
                        _ => {}
                    }
                }
            }
            "antigravity" => {
                if let Some(r) = v["result"]["response"].as_str() { out.extend(text_block("text", r.into())); }
                let u = &v["update"];
                if matches!(u["sessionUpdate"].as_str(), Some("tool_call" | "tool_call_update")) {
                    let id = key(&u["toolCallId"]);
                    let entry = self.tools.entry(id.clone()).or_insert((u.get("title").or(u.get("name")).cloned().unwrap_or(Value::Null), u["rawInput"].clone(), at));
                    if entry.0.is_null() { entry.0 = u.get("title").cloned().unwrap_or(Value::Null); }
                    if entry.1.is_null() { entry.1 = u["rawInput"].clone(); }
                    if matches!(u["status"].as_str(), Some("completed" | "failed")) {
                        let (name, input, started) = self.tools.remove(&id).unwrap();
                        let output = u.get("rawOutput").filter(|x| !x.is_null() && !x.as_object().is_some_and(|o| o.is_empty())).or(u.get("content")).cloned().unwrap_or(Value::Null);
                        out.push(tool_block(&name, &input, &output, &u["status"], json!(started), json!(at)));
                    }
                }
            }
            _ => {}
        }
        out
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn native_id_allowlist() {
        assert!(safe_native_id("01a1bfde-77b9-70c0-8c63-294cab617737"));
        for bad in ["", "-x", "--help", "a b", "a;b", "a/b", &"a".repeat(65)] { assert!(!safe_native_id(bad), "{bad}"); }
    }
    #[test]
    fn claude_placement_values() {
        assert_eq!(claude_placement(Some("mac"), "macos", ""), Ok("mac"));
        assert_eq!(claude_placement(Some("windows-wsl"), "linux", "5.15.167.4-microsoft-standard-WSL2"), Ok("windows-wsl"));
        assert_eq!(claude_placement(Some("mac"), "linux", "6.8.0-generic"), Err("claude-placement-platform-mismatch"));
        assert_eq!(claude_placement(Some("windows-wsl"), "linux", "6.8.0-generic"), Err("claude-placement-platform-mismatch"));
        assert_eq!(claude_placement(Some("windows-wsl"), "macos", ""), Err("claude-placement-platform-mismatch"));
        assert_eq!(claude_placement(None, "macos", ""), Err("claude-placement-unset"));
        assert_eq!(claude_placement(Some("linux"), "linux", ""), Err("claude-placement-invalid"));
    }
    #[test]
    fn fake_launch_routing_resume_and_credentials() {
        use std::io::Write;
        use std::process::Stdio;
        // The placement this platform allows, so the Claude launch path runs on Mac and WSL; elsewhere Claude is refused.
        let release = std::fs::read_to_string("/proc/sys/kernel/osrelease").unwrap_or_default();
        let here = if cfg!(target_os = "macos") { "mac" } else if release.to_ascii_lowercase().contains("microsoft") { "windows-wsl" } else { "unset" };
        TEST_PLACEMENT.with(|p| p.set(Some(here)));
        let root = std::env::temp_dir().join(format!("fleet-multiharness-{}", std::process::id()));
        std::fs::create_dir_all(&root).unwrap();
        let fake = root.join("native");
        std::fs::write(&fake,r#"#!/usr/bin/env python3
import os,sys,json
assert not any(k in os.environ for k in ['OPENAI_API_KEY','ANTHROPIC_API_KEY','FLEET_HOST_SECRET_FILE','GOOGLE_API_KEY'])
h='claude' if 'CLAUDE_CONFIG_DIR' in os.environ else ('antigravity' if 'GEMINI_HOME' in os.environ else 'codex')
if h=='antigravity':
 assert 'acp' in sys.argv and '--include-default-customizations=false' in sys.argv
print(json.dumps({'harness':h,'home':os.environ['HOME'],'args':sys.argv[1:]}))
"#).unwrap();
        std::fs::set_permissions(&fake, std::fs::Permissions::from_mode(0o700)).unwrap();
        let mut accounts = json!({});
        for (id, h) in [
            ("codex-one", "codex"),
            ("codex-two", "codex"),
            ("claude", "claude"),
            ("antigravity", "antigravity"),
        ] {
            for d in [
                format!("{id}-home"),
                format!("{id}-native"),
                format!("{id}-work"),
            ] {
                std::fs::create_dir_all(root.join(&d)).unwrap();
                std::fs::set_permissions(root.join(d), std::fs::Permissions::from_mode(0o700))
                    .unwrap();
            }
            accounts[id] = json!({"harness":h,"home":root.join(format!("{id}-home")),"nativeHome":root.join(format!("{id}-native")),"executable":fake});
        }
        let registry = root.join("accounts.json");
        std::fs::write(&registry, accounts.to_string()).unwrap();
        std::fs::set_permissions(&registry, std::fs::Permissions::from_mode(0o600)).unwrap();
        for (id, h) in [("claude", "claude"), ("antigravity", "antigravity")] {
            let p = json!({"harness":h,"account":id,"model":"native-model","text":"hello","instructions":"explicit"});
            let work = root.join(format!("{id}-work"));
            if h == "claude" && claude_placement_here().is_err() {
                assert!(command_registry(&work, None, &p, &registry).is_err());
                continue;
            }
            for n in [None, Some("retained-id")] {
                let (mut c, input) = command_registry(&work, n, &p, &registry).unwrap();
                if h == "claude" { assert_eq!(input.as_deref(),Some(b"hello\n".as_slice())); assert!(!c.get_args().any(|v| v == "hello")); }
                c.stdin(Stdio::piped()).stdout(Stdio::piped());
                let mut child = c.spawn().unwrap();
                if let Some(input) = input {
                    child.stdin.take().unwrap().write_all(&input).unwrap();
                }
                let output = child.wait_with_output().unwrap();
                assert!(output.status.success());
                let v: Value = serde_json::from_slice(&output.stdout).unwrap();
                assert_eq!(v["harness"], h);
                assert_eq!(v["home"], accounts[id]["home"]);
                if n.is_some() && h == "claude" {
                    assert!(
                        v["args"]
                            .as_array()
                            .unwrap()
                            .contains(&json!("--resume=retained-id"))
                    );
                }
            }
            let mut wrong = p.clone();
            wrong["account"] = json!("codex-one");
            assert!(command_registry(&work, Some("retained-id"), &wrong, &registry).is_err());
        }
        for id in ["codex-one", "codex-two"] {
            let p = json!({"harness":"codex","account":id,"text":"hello"});
            let (c, _) =
                command_registry(&root.join(format!("{id}-work")), None, &p, &registry).unwrap();
            assert!(c.get_envs().any(|(k, v)| k == "CODEX_HOME"
                && v == Some(root.join(format!("{id}-native")).as_os_str())));
        }
        let p = json!({"harness":"codex","account":"codex-two","text":"hello"});
        assert!(
            command_registry(
                &root.join("codex-one-work"),
                Some("retained-id"),
                &p,
                &registry
            )
            .is_err()
        );
        let mut pending = accounts.clone();
        pending["claude"]["readiness"] = json!("unprovisioned");
        std::fs::write(&registry, pending.to_string()).unwrap();
        assert!(
            command_registry(
                &root.join("claude-work"),
                None,
                &json!({"harness":"claude","account":"claude","model":"native-model","text":"x"}),
                &registry
            )
            .unwrap_err()
            .to_string()
            .contains("unprovisioned")
        );
        std::fs::write(&registry, accounts.to_string()).unwrap();
        let p = json!({"harness":"antigravity","account":"antigravity","model":"native-model","text":"x","effort":null,"settings":{"sandbox":false,"includeDefaultCustomizations":false,"mode":"plan"}});
        let (c, _) = command_registry(&root.join("antigravity-work"), None, &p, &registry).unwrap();
        let args: Vec<_> = c
            .get_args()
            .map(|a| a.to_string_lossy().into_owned())
            .collect();
        assert!(args.contains(&"acp".into()));
        assert!(args.contains(&"--sandbox=false".into()));
        assert!(args.contains(&"--include-default-customizations=false".into()));
        std::fs::remove_dir_all(root).unwrap();
    }
    #[test]
    fn acp_diff_result_is_not_hidden_by_empty_raw_output() {
        let p = project(
            "antigravity",
            &json!({"update":{"sessionUpdate":"tool_call_update","toolCallId":"t","status":"completed","rawOutput":{},"content":[{"type":"diff","path":"output/marker.txt","newText":"AGY_NATIVE_ONE\n"}]}}),
        );
        assert_eq!(p["tools"][0]["result"][0]["type"], "diff");
    }
    #[test]
    fn acp_native_usage_categories_stay_distinct() {
        let p = project(
            "antigravity",
            &json!({"type":"turn.completed","usage":{"inputTokens":31012,"outputTokens":223,"cachedReadTokens":4046,"cachedWriteTokens":0,"thoughtTokens":0,"totalTokens":35281}}),
        );
        assert_eq!(p["usage"]["cache_read_tokens"], 4046);
        assert_eq!(p["usage"]["total_tokens"], 35281);
        assert!(p["usage"].get("cached_input_tokens").is_none());
        assert!(p["usageScope"].as_str().unwrap().contains("unknown"));
    }
    #[test]
    fn cumulative_usage_numeric_projection() {
        let p = project(
            "codex",
            &json!({"type":"usage.observation","usage":{"total":{"inputTokens":53971,"outputTokens":233,"cachedInputTokens":49536},"last":{"inputTokens":13709}}}),
        );
        assert_eq!(p["usage"]["input_tokens"], 53971);
        assert_eq!(p["usage"]["output_tokens"], 233);
        assert!(p["usageScope"].as_str().unwrap().contains("not additive"));
        assert!(project("codex", &json!({"type":"turn.completed"}))["usage"].is_null());
    }
    #[test]
    fn common_tool_fields() {
        for (h, v) in [
            (
                "codex",
                json!({"item":{"id":"t","type":"mcpToolCall","tool":"read","arguments":{"path":"x"},"result":"ok"}}),
            ),
            (
                "claude",
                json!({"message":{"content":[{"type":"tool_use","id":"t","name":"read","input":{"path":"x"}}]}}),
            ),
            (
                "antigravity",
                json!({"step_update":{"step_type":"tool","step_index":1,"tool_name":"read","tool_info":{"parameters":{"path":"x"}},"state":"ACTIVE"}}),
            ),
        ] {
            let p = project(h, &v);
            assert_eq!(p["tools"][0]["name"], "read");
            assert_eq!(p["tools"][0]["arguments"]["path"], "x");
        }
        let p = project(
            "claude",
            &json!({"message":{"content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]}}),
        );
        assert_eq!(p["tools"][0]["result"], "ok");
    }
    #[test]
    fn blocks_per_harness() {
        let mut b = Blocks::default();
        let p = b.project("codex", &json!({"type":"item.completed","item":{"type":"agent_message","text":"Hi"}}), 5);
        assert_eq!(p["blocks"], json!([{"kind":"text","role":"agent","markdown":"Hi"}]));
        let p = b.project("codex", &json!({"type":"item.completed","item":{"type":"mcpToolCall","tool":"read","arguments":{"p":1},"result":"ok","status":"completed","startedAtMs":1,"completedAtMs":4}}), 5);
        assert_eq!(p["blocks"][0]["kind"], "tool");
        assert_eq!((p["blocks"][0]["name"].clone(), p["blocks"][0]["startedAt"].clone(), p["blocks"][0]["endedAt"].clone()), (json!("read"), json!(1), json!(4)));
        assert_eq!(b.project("codex", &json!({"type":"item.completed","item":{"type":"reasoning","summary":[],"content":[]}}), 5)["blocks"], json!([]));
        assert_eq!(b.project("codex", &json!({"type":"item.completed","item":{"type":"userMessage","content":[{"text":"hi"}]}}), 5)["blocks"], json!([]));
        let mut c = Blocks::default();
        assert_eq!(c.project("claude", &json!({"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"Yes"},{"type":"tool_use","id":"t","name":"Read","input":{"f":1}}]}}), 10)["blocks"],
            json!([{"kind":"reasoning","markdown":"hm"},{"kind":"text","role":"agent","markdown":"Yes"}]));
        let p = c.project("claude", &json!({"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]}}), 30);
        assert_eq!(p["blocks"], json!([{"kind":"tool","name":"Read","input":{"f":1},"output":"ok","state":"completed","startedAt":10,"endedAt":30}]));
        let mut a = Blocks::default();
        let chunk = |t: &str| json!({"type":"acp.update","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":t}}});
        let d1 = a.project("antigravity", &chunk("Hel"), 1);
        let d2 = a.project("antigravity", &chunk("lo"), 2);
        assert_eq!((d1["deltaOf"].clone(), d2["deltaOf"].clone()), (json!("text:1"), json!("text:1")));
        assert_eq!((d1["opens"].clone(), d2.get("opens")), (json!(true), None));
        assert_eq!(d2["blocks"][0]["markdown"], "lo");
        let end = a.project("antigravity", &json!({"type":"turn.completed","stopReason":"end_turn"}), 3);
        assert!(end.get("deltaOf").is_none());
        assert_eq!(end["closes"], json!(["text:1"]));
        assert_eq!(end["blocks"], json!([{"kind":"text","role":"agent","markdown":"Hello"}]));
        a.project("antigravity", &json!({"type":"acp.update","update":{"sessionUpdate":"tool_call","toolCallId":"x","title":"Edit","rawInput":{"a":1},"status":"pending"}}), 4);
        let p = a.project("antigravity", &json!({"type":"acp.update","update":{"sessionUpdate":"tool_call_update","toolCallId":"x","status":"completed","rawOutput":"done"}}), 9);
        assert_eq!(p["blocks"], json!([{"kind":"tool","name":"Edit","input":{"a":1},"output":"done","state":"completed","startedAt":4,"endedAt":9}]));
        let mut s = Blocks::default();
        s.project("antigravity", &json!({"step_update":{"text_delta":"Hi"}}), 1);
        assert_eq!(s.project("antigravity", &json!({"event":"result","result":{"status":"SUCCESS","response":"Hi"}}), 2)["blocks"], json!([{"kind":"text","role":"agent","markdown":"Hi"}]));
        c.project("claude", &json!({"type":"assistant","message":{"content":[{"type":"tool_use","id":"u","name":"Bash","input":{}}]}}), 40);
        assert_eq!(c.project("claude", &json!({"type":"result","is_error":false}), 50)["blocks"][0]["state"], "unfinished");
        let e = b.project("codex", &json!({"type":"item.completed","item":{"type":"error","message":"stream disconnected"}}), 5);
        assert_eq!((e["blocks"][0]["state"].clone(), e["blocks"][0]["output"].clone()), (json!("failed"), json!("stream disconnected")));
        let p = a.project("codex", &json!({"type":"unparsed","raw":"x"}), 7);
        assert_eq!((p["blocks"][0]["kind"].clone(), p["blocks"][0]["name"].clone(), p["blocks"][0]["output"]["raw"].clone()), (json!("tool"), json!("unparsed"), json!("x")));
    }
    #[test]
    fn native_projection() {
        for (h, event, id) in [
            (
                "codex",
                json!({"thread_id":"c","type":"turn.completed","usage":{"input_tokens":8}}),
                "c",
            ),
            (
                "claude",
                json!({"session_id":"a","type":"result","is_error":false,"usage":{"input_tokens":9}}),
                "a",
            ),
            (
                "antigravity",
                json!({"event":"result","result":{"status":"SUCCESS","conversation_id":"g","response":"ok"}}),
                "g",
            ),
        ] {
            let p = project(h, &event);
            assert_eq!(p["session"], id);
            assert_eq!(p["completed"], true);
            assert_eq!(p["native"], event);
            assert!(p["usageScope"].as_str().unwrap().contains("not additive"));
        }
        assert_eq!(
            project("claude", &json!({"type":"result","is_error":true}))["completed"],
            false
        );
    }
    #[test]
    fn private_paths_and_refusal() {
        assert!(private(Path::new("relative")).is_err());
        assert!(command(Path::new("/tmp"), None, &json!({"harness":"fake"})).is_err());
    }
}
