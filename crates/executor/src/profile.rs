use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{path::Path, process::Command};
pub fn hash(v: &Value) -> String {
    format!("{:x}", Sha256::digest(serde_json::to_vec(v).unwrap()))
}
fn exact(v: &Value, keys: &[&str]) -> bool {
    v.as_object()
        .is_some_and(|o| o.len() == keys.len() && keys.iter().all(|k| o.contains_key(*k)))
}
pub fn validate(v: &Value) -> Result<(), &'static str> {
    if v["harness"].as_str().is_some_and(|h| h != "codex") {
        let h = v["harness"].as_str().unwrap();
        let role = if h == "claude" { "system" } else { "user" };
        if v["id"]
            .as_str()
            .is_none_or(|id| id.is_empty() || id.len() > 80)
            || !(h == "claude" || crate::harness::acp(h))
            || v["instructionRole"] != role
            || (h == "claude" && !v["effort"].is_null())
            || (crate::harness::acp(h) && !v["effort"].is_null())
            || v["model"]
                .as_str()
                .is_none_or(|s| s.is_empty() || s.starts_with('-'))
            || v["instructions"].as_str().is_none_or(|s| s.len() > 16384)
        {
            return Err("unsupported native profile role/settings/grants");
        }
        if v.as_object().is_none_or(|o| {
            o.keys().any(|k| {
                !matches!(
                    k.as_str(),
                    "id" | "harness"
                        | "model"
                        | "effort"
                        | "instructionRole"
                        | "instructions"
                        | "mcpServers"
                        | "tools"
                        | "delegationProfiles"
                        | "settings"
                        | "fleetTools"
                        | "collaborationTools"
                        | "skills"
                        | "connections"
                )
            })
        }) {
            return Err("unknown native profile setting");
        }
        if h == "claude"
            && v["tools"].as_array().is_none_or(|a| {
                a.iter().any(|t| {
                    !matches!(
                        t.as_str(),
                        Some("Read" | "Edit" | "Write" | "Bash" | "Glob" | "Grep")
                    )
                })
            })
        {
            return Err("unsupported Claude tool grant");
        }
        if crate::harness::acp(h) && v.get("tools").is_some() {
            return Err("Antigravity tool allowlist unsupported");
        }
        {
            let mut grants = v.clone();
            let o = grants.as_object_mut().unwrap();
            o.remove("harness");
            o.remove("instructionRole");
            o.remove("tools");
            o.remove("settings");
            grants["model"] = json!("gpt-6.1-sol");
            grants["effort"] = json!("low");
            validate(&grants)?;
            if h == "claude" && v.get("settings").is_some() {
                return Err("unmapped Claude settings");
            }
        }
        if crate::harness::acp(h) {
            validate_antigravity_settings(v.get("settings"))?;
        }
        return Ok(());
    }
    if v.get("instructionRole").is_some_and(|r| r != "developer") || v.get("tools").is_some() {
        return Err("unsupported Codex instruction role/tools");
    }
    let mut base = v.clone();
    if let Some(o) = base.as_object_mut() {
        o.remove("harness");
        o.remove("instructionRole");
        for k in ["fleetTools", "collaborationTools"] {
            if o.remove(k).is_some_and(|v| !v.is_boolean()) {
                return Err("invalid tool setting");
            }
        }
    }
    // Id lists: delegation grants, and the profile's skills and connections (unused until Lab Stage 2).
    for (key, max, err) in [
        ("delegationProfiles", 8, "invalid delegation grant"),
        ("skills", 32, "invalid profile skills"),
        ("connections", 32, "invalid profile connections"),
    ] {
        if let Some(list) = base.as_object_mut().and_then(|o| o.remove(key)) {
            let ids = list.as_array().ok_or(err)?;
            if ids.len() > max
                || ids.iter().any(|id| {
                    id.as_str().is_none_or(|s| {
                        s.is_empty()
                            || s.len() > 80
                            || !s
                                .bytes()
                                .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'-')
                    })
                })
                || ids.iter().enumerate().any(|(i, id)| ids[..i].contains(id))
            {
                return Err(err);
            }
        }
    }
    if !exact(
        &base,
        &["id", "model", "effort", "instructions", "mcpServers"],
    ) || v["id"]
        .as_str()
        .is_none_or(|s| s.is_empty() || s.len() > 80)
        || v["model"].as_str().is_none_or(|s| s.is_empty() || s.starts_with('-'))
        || v["effort"].as_str().is_none_or(|s| s.is_empty() || s.starts_with('-'))
        || v["instructions"].as_str().is_none_or(|s| s.len() > 16384)
    {
        return Err("unsupported agent profile");
    }
    let servers = v["mcpServers"]
        .as_array()
        .ok_or("mcpServers must be an array")?;
    if servers.len() > 1 {
        return Err("at most one design-rules grant");
    }
    for s in servers {
        if !exact(s, &["id", "data"])
            || s["id"] != "design-rules"
            || !s["data"].is_object()
            || serde_json::to_vec(&s["data"]).unwrap().len() > 16384
        {
            return Err("unsupported MCP grant");
        }
    }
    Ok(())
}
pub fn validate_antigravity_settings(settings: Option<&Value>) -> Result<(), &'static str> {
    let Some(v) = settings else { return Ok(()) };
    let o = v.as_object().ok_or("invalid Antigravity settings")?;
    for (k, v) in o {
        match k.as_str() {
            "sandbox" if v.is_boolean() => {}
            "includeDefaultCustomizations" if v == false => {}
            "mode" if matches!(v.as_str(), Some("plan" | "accept-edits")) => {}
            _ => return Err("unmapped Antigravity setting"),
        }
    }
    Ok(())
}
pub fn mcp_servers(v: &Value) -> Value {
    let mut servers = json!({});
    if let Some(s) = v["mcpServers"].as_array().and_then(|a| a.first()) {
        servers["design-rules"] = json!({"command":std::env::current_exe().expect("executor path"),"args":["mcp","design-rules",s["data"].to_string()]});
    }
    servers
}
/// Connection client (docs/designs/fleet-lab.md, Connections): the same file the coordinator runs for api steps.
pub const CONNECTION_CLIENT: &str = include_str!("../../../packages/coordinator/connection_call.py");
/// Skill text appended to Codex developer instructions (Fleet's Codex runs with skill and AGENTS.md discovery disabled).
pub fn with_skills(v: &Value, skills: &Value) -> Value {
    let mut out = v.clone();
    let mut text = v["instructions"].as_str().unwrap_or("").to_string();
    for s in skills.as_array().into_iter().flatten() {
        if let (Some(id), Some(body)) = (s["id"].as_str(), s["text"].as_str()) { text.push_str(&format!("\n\n# Skill: {id}\n\n{body}")); }
    }
    out["instructions"] = json!(text);
    out
}
// JSON string quoting is also valid TOML basic-string quoting for these values.
pub fn apply(c: &mut Command, v: &Value) {
    c.args([
        "-c",
        &format!("developer_instructions={}", v["instructions"]),
    ]);
    if let Some(s) = v["mcpServers"].as_array().unwrap().first() {
        let args = json!(["mcp", "design-rules", s["data"].to_string()]);
        c.args([
            "-c",
            &format!("mcp_servers.design-rules.command={}",json!(std::env::current_exe().expect("executor path"))),
            "-c",
            &format!("mcp_servers.design-rules.args={args}"),
            "-c",
            "mcp_servers.design-rules.required=true",
        ]);
    }
}
pub fn prepare(_dir: &Path, v: &Value) -> Result<Value, Box<dyn std::error::Error>> {
    validate(v)?;
    if v["harness"].as_str().is_some_and(|h| h != "codex") {
        return Ok(
            json!({"requested":v,"profileSha256":hash(v),"applied":{"harness":v["harness"],"model":v["model"],"instructionRole":v["instructionRole"],"instructions":v["instructions"],"tools":v.get("tools"),"mcpServers":v["mcpServers"],"delegationProfiles":v.get("delegationProfiles"),"settings":v.get("settings"),"nativeReadiness":"unknown","nativePermissions":"dontAsk for Claude; native for Antigravity; no sandbox parity"}}),
        );
    }
    Ok(
        json!({"instructionSources":{"profile":"explicit developer_instructions","personalConfig":"not loaded","personalHome":"not forwarded","projectInstructions":"discovery disabled","skills":"disabled","memories":"disabled","hooks":"disabled","nativeObservation":"pending rollout provenance check"},"requested":v,"profileSha256":hash(v),"instructionsSha256":format!("{:x}",Sha256::digest(v["instructions"].as_str().unwrap().as_bytes())),"grantsSha256":hash(&v["mcpServers"]),"applied":{"model":v["model"],"effort":v["effort"],"instructions":v["instructions"],"mcpServers":v["mcpServers"],"mapping":"developerInstructions on open; acknowledged developer-role thread/inject_items before resumed turn; native model-context application unqualified","availability":"unknown until native call/result","resumeCompatibility":"requested overrides supplied per invocation; actual changed settings unqualified; prior conversation context persists","mcpServerSha256":format!("{:x}",Sha256::digest(std::fs::read(std::env::current_exe()?)?))}}),
    )
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn skills_and_web_route() {
        let v = with_skills(&json!({"instructions":"base"}), &json!([{"id":"s1","text":"do it"}]));
        assert_eq!(v["instructions"], "base\n\n# Skill: s1\n\ndo it");
        assert!(CONNECTION_CLIENT.contains("def main(argv)"));
        assert!(crate::harness::claude_extra_tools(&json!({})).is_empty());
        assert_eq!(crate::harness::claude_extra_tools(&json!({"webRoute":"builtin","skills":[{"id":"s"}]})), vec!["WebSearch", "WebFetch", "Skill"]);
    }
    #[test]
    fn scoped_claude_and_antigravity_settings() {
        let mut c: Value =
            serde_json::from_str(include_str!("../../../config/agents/claude-worker.json"))
                .unwrap();
        c["mcpServers"] = json!([{"id":"design-rules","data":{"heading":"test"}}]);
        validate(&c).unwrap();
        assert_eq!(mcp_servers(&c)["design-rules"]["command"], json!(std::env::current_exe().unwrap()));
        c["mcpServers"][0]["command"] = json!("arbitrary");
        assert!(validate(&c).is_err());
        c = serde_json::from_str(include_str!("../../../config/agents/claude-worker.json"))
            .unwrap();
        c["delegationProfiles"] = json!(["../bad"]);
        assert!(validate(&c).is_err());
        c["delegationProfiles"] = json!([]);
        c["skills"] = json!(["web-research"]);
        c["connections"] = json!(["tavily"]);
        validate(&c).unwrap();
        c["connections"] = json!(["tavily", "tavily"]);
        assert!(validate(&c).is_err());
        assert!(validate_antigravity_settings(Some(&json!({"mcpServers":{}}))).is_err());
    }
    #[test]
    fn explicit_native_profiles() {
        for source in [
            include_str!("../../../config/agents/claude-worker.json"),
            include_str!("../../../config/agents/antigravity-worker.json"),
        ] {
            let mut v: Value = serde_json::from_str(source).unwrap();
            validate(&v).unwrap();
            v["instructionRole"] = json!("developer");
            assert!(validate(&v).is_err());
        }
    }
    #[test]
    fn mapping_and_refusal() {
        let v: Value =
            serde_json::from_str(include_str!("../../../config/agents/design-rules.json")).unwrap();
        validate(&v).unwrap();
        let dir =
            std::env::temp_dir().join(format!("fleet-profile-validation-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        prepare(&dir, &v).unwrap();
        prepare(&dir, &v).unwrap();
        let mut changed = v.clone();
        changed["instructions"] = json!("different");
        assert!(prepare(&dir, &changed).is_ok());
        std::fs::remove_dir_all(dir).unwrap();

        let scenario: Value =
            serde_json::from_str(include_str!("../../../config/agents/website-review.json"))
                .unwrap();
        validate(&scenario).unwrap();
        let mut invalid = scenario.clone();
        invalid["delegationProfiles"] = json!(["../bad"]);
        assert!(validate(&invalid).is_err());
        let mut bad = v.clone();
        bad["mcpServers"][0]["command"] = json!("evil");
        assert!(validate(&bad).is_err());
        bad = v.clone();
        bad["model"] = json!("gpt-6.1");
        bad["effort"] = json!("high");
        bad["collaborationTools"] = json!(false);
        bad["fleetTools"] = json!(true);
        assert!(validate(&bad).is_ok());
        bad["model"] = json!("--dangerous");
        assert!(validate(&bad).is_err());
        bad["model"] = json!("gpt-6.1");
        bad["fleetTools"] = json!("yes");
        assert!(validate(&bad).is_err());
        let mut c = Command::new("unused");
        apply(&mut c, &v);
        let args: Vec<_> = c
            .get_args()
            .map(|s| s.to_string_lossy().to_string())
            .collect();
        assert!(
            args.iter()
                .any(|s| s == "mcp_servers.design-rules.required=true")
        );
        let baseline: Value =
            serde_json::from_str(include_str!("../../../config/agents/baseline.json")).unwrap();
        let mut c = Command::new("unused");
        apply(&mut c, &baseline);
        assert!(
            !c.get_args()
                .any(|s| s.to_string_lossy().contains("mcp_servers"))
        );
    }
}

#[cfg(test)]
mod qualification {
    #[test]
    #[ignore = "one authorized subscription qualification; explicit invocation only"]
    fn native_design_rules() {
        use std::{
            fs,
            process::Stdio,
            time::{Duration, Instant},
        };
        let dir = std::env::temp_dir().join(format!("fleet-lab-native-{}", std::process::id()));
        fs::create_dir_all(&dir).unwrap();
        let baseline = std::env::var("FLEET_LAB_VARIANT")
            .expect("set FLEET_LAB_VARIANT to baseline or configured");
        assert!(matches!(baseline.as_str(), "baseline" | "configured"));
        let baseline = baseline == "baseline";
        let v: serde_json::Value = serde_json::from_str(if baseline {
            include_str!("../../../config/agents/baseline.json")
        } else {
            include_str!("../../../config/agents/design-rules.json")
        })
        .unwrap();
        let e = super::prepare(&dir, &v).unwrap();
        fs::write(
            dir.join("preparation.json"),
            serde_json::to_vec_pretty(&e).unwrap(),
        )
        .unwrap();
        let mut c = crate::native::command(
            &dir,
            None,
            if baseline { "Confirm readiness briefly without calling tools or creating files." } else { "Call the design-rules get_design_rules tool exactly once with empty arguments, then briefly confirm receipt. Do not create files, run shell commands, generate websites or call any other tools." },
        ).unwrap();
        super::apply(&mut c, &v);
        c.stdout(Stdio::from(
            fs::File::create(dir.join("native.jsonl")).unwrap(),
        ))
        .stderr(Stdio::from(
            fs::File::create(dir.join("stderr.log")).unwrap(),
        ));
        let mut child = c.spawn().unwrap();
        let identity = crate::native::identity(child.id()).unwrap();
        let start = Instant::now();
        let status = loop {
            if let Some(s) = child.try_wait().unwrap() {
                break Some(s);
            }
            if start.elapsed() > Duration::from_secs(90) {
                let _ = crate::native::signal(child.id(), &identity, libc::SIGKILL);
                let _ = child.wait();
                break None;
            }
            std::thread::sleep(Duration::from_millis(100));
        };
        println!("NATIVE_EVIDENCE={}", dir.display());
        let raw = fs::read_to_string(dir.join("native.jsonl")).unwrap();
        assert!(
            status.is_some_and(|s| s.success()),
            "native failed; inspect retained evidence"
        );
        if baseline {
            assert!(!raw.contains("\"mcp_tool_call\""));
            return;
        }
        assert!(
            raw.lines()
                .filter_map(|l| serde_json::from_str::<serde_json::Value>(l).ok())
                .any(|e| e["item"]["type"] == "mcp_tool_call"
                    && e["item"]["tool"] == "get_design_rules"
                    && e["item"]["status"] == "completed"
                    && !e["item"]["result"].is_null()),
            "missing native MCP call/result"
        );
    }
}
