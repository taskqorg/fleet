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
        crate::lab::validate(profile).map_err(fail)?;
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
    if h == "claude" && !cfg!(target_os = "macos") { return Err(fail("Claude account is restricted to Mac/Windows; this executor target is ineligible")); }
    if matches!(h, "antigravity" | "claude") {
        binding["transport"] = json!(if h == "claude" { "stream-json" } else { "acp-1.5.3" });
        binding["credentialAuthority"] = a["credentialAuthority"].clone();
        binding["credentialHome"] = credential_home
            .map(|p| p.canonicalize())
            .transpose()?
            .map(|p| json!(p))
            .unwrap_or(Value::Null);
    }
    let marker = dir.join(".fleet-harness-account.json");
    if native.is_some()
        && std::fs::read(&marker)
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
        c.env("CLAUDE_CONFIG_DIR", nh).args([
            "--disable-slash-commands",
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
        let tools = p["tools"]
            .as_array()
            .map(|a| a.iter().filter_map(Value::as_str).collect::<Vec<_>>())
            .unwrap_or_default();
        c.args(["--tools", &tools.join(",")]);
        if let Some(n) = native {
            c.args(["--resume", n]);
        }
        c.arg(text);
        Ok((c, None))
    } else {
        crate::lab::validate_antigravity_settings(p.get("settings")).map_err(fail)?;
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
pub fn claude_mcp(c: &mut Command, profile: &Value, servers: &Value) {
    let mut allowed: Vec<String> = profile["tools"]
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
        .map(str::to_owned)
        .collect();
    if servers.get("design-rules").is_some() {
        allowed.push("mcp__design-rules__get_design_rules".into());
    }
    if servers.get("fleet").is_some() {
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
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn fake_launch_routing_resume_and_credentials() {
        use std::io::Write;
        use std::process::Stdio;
        let root = std::env::temp_dir().join(format!("fleet-multiharness-{}", std::process::id()));
        std::fs::create_dir_all(&root).unwrap();
        let fake = root.join("native");
        std::fs::write(&fake,r#"#!/usr/bin/env python3
import os,sys,json
assert not any(k in os.environ for k in ['OPENAI_API_KEY','ANTHROPIC_API_KEY','FLEET_HOST_TOKEN','GOOGLE_API_KEY'])
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
            for n in [None, Some("retained-id")] {
                let (mut c, input) = command_registry(&work, n, &p, &registry).unwrap();
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
                            .contains(&json!("retained-id"))
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
