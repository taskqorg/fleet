//! Protocol shapes: AgentQ d0f6bfa acp.rs; T3 d0f1f72 AntigravityAcpSupport/AcpSessionRuntime.
//! Behavioral reuse only; no source copied. Fleet owns admission, logs and process cleanup.
use serde_json::{Value, json};
pub struct Session {
    stage: u8,
    session: Option<String>,
    resume: Option<String>,
    root: String,
    profile: Value,
    servers: Value,
    mode: Option<String>,
}
impl Session {
    pub fn new(root: String, resume: Option<String>, profile: Value, servers: Value) -> Self {
        let config = profile.get("agentConfig").unwrap_or(&profile);
        let mode = config["settings"]["mode"].as_str().map(str::to_owned);
        Self {
            stage: 0,
            session: None,
            resume,
            root,
            profile,
            servers,
            mode,
        }
    }
    pub fn initialize() -> Value {
        json!({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{},"clientInfo":{"name":"fleet","version":"0.1.0"}}})
    }
    fn request(&self, id: u64, method: &str, params: Value) -> Value {
        json!({"jsonrpc":"2.0","id":id,"method":method,"params":params})
    }
    fn prompt(&mut self) -> Value {
        self.stage = 5;
        let c = self.profile.get("agentConfig").unwrap_or(&self.profile);
        let instructions = c["instructions"].as_str().unwrap_or("");
        let content = if instructions.is_empty() {
            self.profile["text"].as_str().unwrap_or("").to_owned()
        } else {
            format!(
                "Explicit Fleet instructions (user role):\n{instructions}\n\nTask:\n{}",
                self.profile["text"].as_str().unwrap_or("")
            )
        };
        self.request(
            5,
            "session/prompt",
            json!({"sessionId":self.session,"prompt":[{"type":"text","text":content}]}),
        )
    }
    pub fn receive(&mut self, v: &Value) -> Result<(Vec<Value>, Option<Value>, bool), String> {
        if v.get("id").is_some() && v.get("method").is_some() {
            let reply = if v["method"] == "session/request_permission" {
                json!({"jsonrpc":"2.0","id":v["id"],"result":{"outcome":{"outcome":"cancelled"}}})
            } else {
                json!({"jsonrpc":"2.0","id":v["id"],"error":{"code":-32601,"message":"Fleet does not grant client file/terminal operations"}})
            };
            return Ok((
                vec![reply],
                Some(json!({"type":"permission.refused","native":v})),
                false,
            ));
        }
        if v.get("error").is_some() {
            return Err(format!(
                "ACP request failed (code {}); no replay",
                v["error"]["code"]
            ));
        }
        if v.get("id").is_some() && !v["result"].is_object() {
            return Err("ACP response missing successful result".into());
        }
        if v["id"] == 1 && self.stage == 0 {
            let r = &v["result"];
            if r["protocolVersion"] != 1 || r["agentInfo"]["version"] != "1.5.3" {
                return Err("unqualified ACP protocol/runtime version".into());
            }
            let method = if self.resume.is_some() {
                if r["agentCapabilities"]["sessionCapabilities"]
                    .get("resume")
                    .is_some()
                {
                    "session/resume"
                } else if r["agentCapabilities"]["loadSession"] == true {
                    "session/load"
                } else {
                    return Err("native ACP resume unavailable".into());
                }
            } else {
                "session/new"
            };
            let servers:Vec<_>=self.servers.as_object().into_iter().flat_map(|s|s.iter()).map(|(name,s)|json!({"name":name,"command":s["command"],"args":s["args"],"env":[]})).collect();
            let mut params = json!({"cwd":self.root,"mcpServers":servers});
            if let Some(n) = &self.resume {
                params["sessionId"] = json!(n);
            }
            self.stage = 1;
            return Ok((
                vec![self.request(2, method, params)],
                Some(json!({"type":"capabilities","native":v})),
                false,
            ));
        }
        if v["id"] == 2 && self.stage == 1 {
            let r = &v["result"];
            let id = r["sessionId"]
                .as_str()
                .map(str::to_owned)
                .or(self.resume.clone())
                .ok_or("ACP session ID missing")?;
            if self.resume.as_ref().is_some_and(|n| n != &id) {
                return Err("ACP resumed identity mismatch".into());
            }
            let model = self.profile["model"].as_str().ok_or("model missing")?;
            if !r["models"]["availableModels"]
                .as_array()
                .is_some_and(|a| a.iter().any(|m| m["modelId"] == model))
            {
                return Err("requested ACP model unavailable; no substitute".into());
            }
            if let Some(mode) = &self.mode {
                if !r["modes"]["availableModes"]
                    .as_array()
                    .is_some_and(|a| a.iter().any(|m| m["id"] == *mode))
                {
                    return Err("requested ACP mode unavailable".into());
                }
            }
            self.session = Some(id.clone());
            self.stage = 2;
            Ok((
                vec![self.request(
                    3,
                    "session/set_model",
                    json!({"sessionId":id,"modelId":model}),
                )],
                Some(json!({"type":"session.started","conversation_id":id,"native":v})),
                false,
            ))
        } else if v["id"] == 3 && self.stage == 2 {
            self.stage = 3;
            let request = if let Some(mode) = &self.mode {
                self.request(
                    4,
                    "session/set_mode",
                    json!({"sessionId":self.session,"modeId":mode}),
                )
            } else {
                self.prompt()
            };
            Ok((
                vec![request],
                Some(
                    json!({"type":"configuration.applied","model":self.profile["model"],"native":v}),
                ),
                false,
            ))
        } else if v["id"] == 4 && self.stage == 3 {
            Ok((
                vec![self.prompt()],
                Some(json!({"type":"configuration.applied","native":v})),
                false,
            ))
        } else if v["id"] == 5 && self.stage == 5 {
            self.stage = 6;
            let stop = v["result"]["stopReason"]
                .as_str()
                .ok_or("ACP stop reason missing")?;
            Ok((
                vec![],
                Some(
                    json!({"type":if stop=="end_turn" {"turn.completed"}else{"turn.failed"},"usage":v["result"].get("usage"),"stopReason":stop,"native":v}),
                ),
                true,
            ))
        } else if v["method"] == "session/update"
            && v["params"]["sessionId"].as_str() == self.session.as_deref()
            && self.session.is_some()
        {
            Ok((
                vec![],
                Some(json!({"type":"acp.update","update":v["params"]["update"],"native":v})),
                false,
            ))
        } else {
            Ok((vec![], None, false))
        }
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn profile() -> Value {
        json!({"model":"gemini-3.8-flash-low","text":"task","agentConfig":{"instructions":"explicit user","settings":{"mode":"accept-edits"}}})
    }
    fn opened() -> Value {
        json!({"id":2,"result":{"sessionId":"s","models":{"availableModels":[{"modelId":"gemini-3.8-flash-low"}]},"modes":{"availableModes":[{"id":"accept-edits"}]}}})
    }
    #[test]
    fn advertised_resume_and_control_gate() {
        for caps in [
            json!({"loadSession":true}),
            json!({"sessionCapabilities":{"resume":{}}}),
        ] {
            let mut s = Session::new("/work".into(), Some("s".into()), profile(), json!({}));
            let (r,_,_)=s.receive(&json!({"id":1,"result":{"protocolVersion":1,"agentInfo":{"version":"1.5.3"},"agentCapabilities":caps}})).unwrap();
            assert_eq!(r[0]["params"]["sessionId"], "s");
            assert!(matches!(
                r[0]["method"].as_str(),
                Some("session/load" | "session/resume")
            ));
            let (r, _, _) = s.receive(&opened()).unwrap();
            assert_eq!(r[0]["method"], "session/set_model");
            assert!(s.receive(&opened()).unwrap().0.is_empty());
            let (r, _, _) = s.receive(&json!({"id":3,"result":{}})).unwrap();
            assert_eq!(r[0]["method"], "session/set_mode");
            let (r, _, _) = s.receive(&json!({"id":4,"result":{}})).unwrap();
            assert_eq!(r[0]["method"], "session/prompt");
            assert!(
                r[0]["params"]["prompt"][0]["text"]
                    .as_str()
                    .unwrap()
                    .contains("user role")
            );
            assert!(
                s.receive(&json!({"id":4,"result":{}}))
                    .unwrap()
                    .0
                    .is_empty()
            );
            assert!(
                !s.receive(
                    &json!({"method":"session/update","params":{"sessionId":"other","update":{}}})
                )
                .unwrap()
                .1
                .is_some()
            );
            assert!(
                s.receive(&json!({"id":5,"result":{"stopReason":"end_turn"}}))
                    .unwrap()
                    .2
            );
        }
    }
    #[test]
    fn unavailable_model_or_failed_setter_never_prompts() {
        let mut s = Session::new("/work".into(), None, profile(), json!({}));
        s.receive(&json!({"id":1,"result":{"protocolVersion":1,"agentInfo":{"version":"1.5.3"}}}))
            .unwrap();
        let mut response = opened();
        response["result"]["models"]["availableModels"] = json!([]);
        assert!(s.receive(&response).is_err());
        let mut s = Session::new("/work".into(), None, profile(), json!({}));
        s.stage = 2;
        assert!(s.receive(&json!({"id":3,"error":{"code":-1}})).is_err());
        assert!(s.receive(&json!({"id":3})).is_err());
    }
}
