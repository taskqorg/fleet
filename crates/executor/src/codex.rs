//! Local app-server protocol; Fleet owns command durability and process cleanup.
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{io::Write, os::unix::process::CommandExt, process::Command};
pub fn command(old: &Command) -> Command {
    let mut c = Command::new(old.get_program());
    c.env_clear()
        .envs(old.get_envs().filter_map(|(k, v)| v.map(|v| (k, v))))
        .current_dir(old.get_current_dir().unwrap())
        .process_group(0)
        .arg("app-server");
    let args: Vec<_> = old.get_args().collect();
    let mut i = 0;
    while i < args.len() {
        if matches!(args[i].to_str(), Some("-c" | "--disable" | "--enable")) && i + 1 < args.len() {
            c.arg(args[i]).arg(args[i + 1]);
            i += 2;
        } else {
            i += 1;
        }
    }
    c
}
/// Persist intent bytes before sending; a successful pipe write is not native acceptance.
pub fn write_frame(
    writer: &mut impl Write,
    log: &mut std::fs::File,
    value: &Value,
) -> std::io::Result<Value> {
    let mut bytes = serde_json::to_vec(value)?;
    bytes.push(b'\n');
    log.write_all(&bytes)?;
    log.sync_data()?;
    writer.write_all(&bytes)?;
    writer.flush()?;
    Ok(
        json!({"type":"protocol.write","method":value["method"].as_str().unwrap_or("response"),"requestId":value["id"].as_u64(),"bytes":bytes.len(),"sha256":format!("{:x}",Sha256::digest(&bytes)),"delivery":"written to local transport; native acceptance unknown"}),
    )
}
pub struct Session {
    pub thread: Option<String>,
    turn: Option<String>,
    stage: u8,
    profile: Value,
    root: String,
    resume: Option<String>,
    fork: Option<String>,
    steers: Vec<(u64, String)>,
    next_id: u64,
    harness_version: Option<String>,
}
impl Session {
    pub fn new(root: String, resume: Option<String>, p: Value) -> Self {
        Self {
            thread: None,
            turn: None,
            stage: 0,
            profile: p,
            root,
            resume,
            fork: None,
            steers: Vec::new(),
            next_id: 100,
            harness_version: None,
        }
    }
    pub fn fork(mut self, source: Option<String>) -> Self {
        self.fork = source;
        self
    }
    /// T3 CodexAdapterV2 steer: `turn/steer` with `expectedTurnId` of the active turn.
    pub fn steer(&mut self, text: &str, command: &str) -> Option<Value> {
        if self.stage != 3 {
            return None;
        }
        let (thread, turn) = (self.thread.clone()?, self.turn.clone()?);
        self.next_id += 1;
        self.steers.push((self.next_id, command.to_owned()));
        Some(json!({"id":self.next_id,"method":"turn/steer","params":{"threadId":thread,"expectedTurnId":turn,"input":[{"type":"text","text":text,"text_elements":[]}]}}))
    }
    fn turn_request(&self) -> Value {
        json!({"id":3,"method":"turn/start","params":{"threadId":self.thread,"model":self.profile["model"],"effort":self.profile["effort"],"input":[{"type":"text","text":self.profile["text"],"text_elements":[]}]}})
    }
    pub fn initialize() -> Value {
        json!({"id":1,"method":"initialize","params":{"clientInfo":{"name":"fleet","version":"0.1.0"},"capabilities":{"experimentalApi":true}}})
    }
    pub fn receive(&mut self, v: &Value) -> Result<(Vec<Value>, Option<Value>, bool), String> {
        if v.get("id").is_some() && v.get("method").is_some() {
            return Ok((
                vec![
                    json!({"id":v["id"],"error":{"code":-32601,"message":"Fleet denies ungranted native request"}}),
                ],
                None,
                false,
            ));
        }
        if let Some(i) = v["id"].as_u64().and_then(|id| self.steers.iter().position(|(n, _)| *n == id)) {
            let (_, command) = self.steers.remove(i);
            return Ok((vec![], Some(json!({"type":"steer.result","commandId":command,"ok":v.get("error").is_none(),"native":v})), false));
        }
        if v.get("error").is_some() {
            return Err(format!(
                "native protocol request failed: {}",
                v["error"]["code"]
            ));
        }
        if v["id"] == 1 && self.stage == 0 {
            self.stage = 1;
            self.harness_version = v["result"]["userAgent"].as_str().map(str::to_owned);
            let p = &self.profile;
            let profile = p.get("agentConfig").unwrap_or(p);
            let mut params = json!({"cwd":self.root,"model":p["model"],"approvalPolicy":"never","sandbox":"workspace-write","developerInstructions":profile["instructions"].as_str().unwrap_or(""),"config":{"model_reasoning_effort":p["effort"],"sandbox_workspace_write.network_access":p["gitNetwork"] == true}});
            if let Some(r) = p.get("writableRoots") { params["config"]["sandbox_workspace_write.writable_roots"] = r.clone(); }
            if let Some(n) = self.resume.as_ref().or(self.fork.as_ref()) {
                params["threadId"] = json!(n);
            }
            return Ok((
                vec![
                    json!({"method":"initialized"}),
                    json!({"id":2,"method":if self.resume.is_some(){"thread/resume"}else if self.fork.is_some(){"thread/fork"}else{"thread/start"},"params":params}),
                ],
                None,
                false,
            ));
        }
        if v["id"] == 2 && self.stage == 1 {
            let thread = v["result"]["thread"]["id"]
                .as_str()
                .ok_or("missing native thread")?
                .to_owned();
            if self.resume.as_ref().is_some_and(|n| n != &thread) {
                return Err("resumed thread identity mismatch".into());
            }
            self.thread = Some(thread.clone());
            self.stage = 2;
            return Ok((
                {
                    let profile = self.profile.get("agentConfig").unwrap_or(&self.profile);
                    let instructions = profile["instructions"].as_str().unwrap_or("");
                    if (self.resume.is_some() || self.fork.is_some()) && !instructions.is_empty() {
                        self.stage = 4;
                        vec![
                            json!({"id":4,"method":"thread/inject_items","params":{"threadId":thread,"items":[{"type":"message","role":"developer","content":[{"type":"input_text","text":instructions}]}]}}),
                        ]
                    } else {
                        vec![self.turn_request()]
                    }
                },
                Some(json!({"type":"thread.started","thread_id":thread,"harnessVersion":self.harness_version,"native":v})),
                false,
            ));
        }
        if v["id"] == 4 && self.stage == 4 {
            self.stage = 2;
            return Ok((vec![self.turn_request()], None, false));
        }
        if v["id"] == 3 && self.stage == 2 {
            self.turn = v["result"]["turn"]["id"].as_str().map(str::to_owned);
            self.stage = 3;
            return Ok((vec![], None, false));
        }
        let p = &v["params"];
        if p["threadId"].as_str() != self.thread.as_deref() {
            return Ok((vec![], None, false));
        }
        if v["method"] == "turn/started" && self.turn.is_none() {
            self.turn = p["turn"]["id"].as_str().map(str::to_owned);
        }
        if p.get("turnId")
            .and_then(Value::as_str)
            .is_some_and(|t| Some(t) != self.turn.as_deref())
        {
            return Ok((vec![], None, false));
        }
        if v["method"] == "item/completed"
            && self.turn.is_some()
            && p["turnId"].as_str() == self.turn.as_deref()
        {
            let mut item = p["item"].clone();
            if item["type"] == "agentMessage" {
                item["type"] = json!("agent_message");
            }
            // Codex collab (spawn_agent etc.): child threads are accounted under this parent execution.
            let children: Vec<Value> = if item["type"] == "collabAgentToolCall" {
                item["receiverThreadIds"].as_array().into_iter().flatten().map(|c| json!({"threadId":c,"parentThreadId":self.thread,"tool":item["tool"],"binding":{"model":item.get("model").filter(|m|!m.is_null()).unwrap_or(&self.profile["model"]),"account":"parent"},"state":item["agentsStates"].get(c.as_str().unwrap_or(""))})).collect()
            } else { vec![] };
            return Ok((
                vec![],
                Some(json!({"type":"item.completed","item":item,"children":children,"native":v})),
                false,
            ));
        }
        if v["method"] == "thread/tokenUsage/updated" {
            return Ok((
                vec![],
                Some(json!({"type":"usage.observation","usage":p["tokenUsage"],"native":v})),
                false,
            ));
        }
        if v["method"] == "turn/completed"
            && p["turn"]["id"].as_str() == self.turn.as_deref()
            && self.turn.is_some()
        {
            let success = p["turn"]["status"] == "completed";
            return Ok((
                vec![],
                Some(
                    json!({"type":if success {"turn.completed"} else {"turn.failed"},"error":p["turn"]["error"],"native":v}),
                ),
                true,
            ));
        }
        Ok((vec![], None, false))
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn collab_child_recorded_on_parent() {
        let mut s = Session::new("/r".into(), None, json!({"model":"m"}));
        s.receive(&json!({"id":1,"result":{"userAgent":"codex/9"}})).unwrap();
        let (_, e, _) = s.receive(&json!({"id":2,"result":{"thread":{"id":"p"}}})).unwrap();
        assert_eq!(e.unwrap()["harnessVersion"], "codex/9");
        s.receive(&json!({"id":3,"result":{"turn":{"id":"t"}}})).unwrap();
        let (_, e, _) = s.receive(&json!({"method":"item/completed","params":{"threadId":"p","turnId":"t","item":{"type":"collabAgentToolCall","tool":"spawnAgent","receiverThreadIds":["c"],"agentsStates":{"c":{"status":"running"}}}}})).unwrap();
        let c = &e.unwrap()["children"][0];
        assert_eq!((c["threadId"].as_str(), c["parentThreadId"].as_str(), c["binding"]["model"].as_str()), (Some("c"), Some("p"), Some("m")));
    }
    #[test]
    fn fork_and_steer() {
        let mut s = Session::new("/w".into(), None, json!({"model":"gpt-6.1-sol","effort":"low","text":"t"})).fork(Some("src".into()));
        let (r, _, _) = s.receive(&json!({"id":1,"result":{}})).unwrap();
        assert_eq!(r[1]["method"], "thread/fork");
        assert_eq!(r[1]["params"]["threadId"], "src");
        s.receive(&json!({"id":2,"result":{"thread":{"id":"forked"}}})).unwrap();
        assert!(s.steer("x", "c").is_none());
        s.receive(&json!({"id":3,"result":{"turn":{"id":"turn"}}})).unwrap();
        let f = s.steer("more", "cmd").unwrap();
        assert_eq!(f["method"], "turn/steer");
        assert_eq!(f["params"]["expectedTurnId"], "turn");
        let (_, e, done) = s.receive(&json!({"id":f["id"],"error":{"code":-1}})).unwrap();
        assert_eq!(e.unwrap()["ok"], false);
        assert!(!done);
    }
    #[test]
    fn pending_and_approval_refusal() {
        let mut s = Session::new(
            "/workspace".into(),
            Some("saved".into()),
            json!({"model":"gpt-6.1-sol","effort":"low","instructions":"changed","text":"task"}),
        );
        let first = s.receive(&json!({"id":1,"result":{}})).unwrap();
        assert_eq!(first.0.len(), 2);
        assert!(
            s.receive(&json!({"id":1,"result":{}}))
                .unwrap()
                .0
                .is_empty()
        );
        assert!(
            s.receive(&json!({"id":2,"result":{"thread":{"id":"different"}}}))
                .is_err()
        );
        let (requests, _, _) = s
            .receive(&json!({"id":42,"method":"item/commandExecution/requestApproval","params":{}}))
            .unwrap();
        assert!(requests[0].get("error").is_some());
        assert!(s.receive(&json!({"id":3,"error":{"code":-1}})).is_err());
    }
    #[test]
    fn fresh_resume_roles_and_correlation() {
        for resume in [None, Some("thread".into())] {
            let mut s = Session::new(
                "/workspace".into(),
                resume.clone(),
                json!({"model":"gpt-6.1-sol","effort":"low","instructions":"current developer","text":"task"}),
            );
            let (requests, _, _) = s.receive(&json!({"id":1,"result":{}})).unwrap();
            assert_eq!(
                requests[1]["params"]["developerInstructions"],
                "current developer"
            );
            assert_eq!(
                requests[1]["method"],
                if resume.is_some() {
                    "thread/resume"
                } else {
                    "thread/start"
                }
            );
            let (next, _, _) = s
                .receive(&json!({"id":2,"result":{"thread":{"id":"thread"}}}))
                .unwrap();
            if resume.is_some() {
                assert_eq!(next[0]["method"], "thread/inject_items");
                assert_eq!(next[0]["params"]["items"][0]["role"], "developer");
                assert_eq!(
                    next[0]["params"]["items"][0]["content"][0]["text"],
                    "current developer"
                );
                assert!(
                    s.receive(&json!({"id":2,"result":{"thread":{"id":"thread"}}}))
                        .unwrap()
                        .0
                        .is_empty()
                );
                let (turn, _, _) = s.receive(&json!({"id":4,"result":{}})).unwrap();
                assert_eq!(turn[0]["method"], "turn/start");
            }
            s.receive(&json!({"id":3,"result":{"turn":{"id":"turn"}}}))
                .unwrap();
            assert!(!s.receive(&json!({"method":"turn/completed","params":{"threadId":"foreign","turn":{"id":"turn","status":"completed"}}})).unwrap().2);
            assert!(s.receive(&json!({"method":"turn/completed","params":{"threadId":"thread","turn":{"id":"turn","status":"completed"}}})).unwrap().2);
        }
    }
}
