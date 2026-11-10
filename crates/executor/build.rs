// Rust types from contracts/*.schema.json (fleet-stack L3), written to OUT_DIR and included by src/contracts.rs.
// contracts/gen-ts.mjs reads the same files for TypeScript; both accept only the keyword subset in contracts/README.md and
// fail on anything else, so a schema never yields a silently weaker type. Needs only cargo (serde_json is a build dependency).
use serde_json::Value;
use std::fmt::Write;

const KEYS: [&str; 9] = ["$comment", "type", "properties", "required", "additionalProperties", "items", "enum", "$ref", "minimum"];

fn pascal(s: &str) -> String {
    s.split(|c: char| !c.is_ascii_alphanumeric()).filter(|w| !w.is_empty()).map(|w| w[..1].to_uppercase() + &w[1..]).collect()
}
fn snake(s: &str) -> String {
    let mut o = String::new();
    for c in s.chars() {
        if c.is_ascii_uppercase() { o.push('_'); o.push(c.to_ascii_lowercase()) } else if c.is_ascii_alphanumeric() { o.push(c) } else { o.push('_') }
    }
    if ["type", "ref", "move", "match", "use", "loop", "self", "struct", "enum", "fn", "mod", "impl"].contains(&o.as_str()) { format!("r#{o}") } else { o }
}
/// The Rust type of one schema, and whether `null` is one of its values.
fn ty(s: &Value, at: &str) -> (String, bool) {
    let o = s.as_object().unwrap_or_else(|| panic!("{at}: schema is not an object"));
    for k in o.keys() { assert!(KEYS.contains(&k.as_str()), "{at}: unsupported keyword {k}") }
    if let Some(r) = s["$ref"].as_str() { return (r.trim_start_matches("#/$defs/").to_string(), false) }
    assert!(s.get("enum").is_none(), "{at}: an enum must be a $defs entry");
    let types: Vec<&str> = match &s["type"] {
        Value::Null => return ("serde_json::Value".into(), false),
        Value::String(t) => vec![t],
        Value::Array(a) => a.iter().map(|t| t.as_str().unwrap()).collect(),
        t => panic!("{at}: bad type {t}"),
    };
    let null = types.contains(&"null");
    let rest: Vec<&str> = types.into_iter().filter(|t| *t != "null").collect();
    assert!(rest.len() == 1, "{at}: only one type besides null is supported");
    let t = match rest[0] {
        "string" => "String".into(),
        "boolean" => "bool".into(),
        "number" => "f64".into(),
        "integer" => if s["minimum"].as_i64().is_some_and(|m| m >= 0) { "u64".into() } else { "i64".into() },
        "array" => format!("Vec<{}>", ty(s.get("items").unwrap_or(&Value::Object(Default::default())), &format!("{at}[]")).0),
        "object" => {
            assert!(s["properties"].as_object().is_none_or(|p| p.is_empty()), "{at}: an object with properties must be a $defs entry");
            "serde_json::Map<String, serde_json::Value>".into()
        }
        t => panic!("{at}: unsupported type {t}"),
    };
    (t, null)
}
fn def(out: &mut String, name: &str, s: &Value) {
    if let Some(e) = s["enum"].as_array() {
        writeln!(out, "#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, serde::Serialize, serde::Deserialize)]\npub enum {name} {{").unwrap();
        for (i, v) in e.iter().enumerate() { let v = v.as_str().expect("string enum"); writeln!(out, "    {}#[serde(rename = {v:?})]\n    {},", if i == 0 { "#[default] " } else { "" }, pascal(v)).unwrap() }
        writeln!(out, "}}\n").unwrap();
        return;
    }
    let props = match s["properties"].as_object() { Some(p) if s["type"] == "object" => p, _ => { writeln!(out, "pub type {name} = {};\n", ty(s, name).0).unwrap(); return } };
    let required: Vec<&str> = s["required"].as_array().map(|r| r.iter().filter_map(Value::as_str).collect()).unwrap_or_default();
    writeln!(out, "#[derive(Clone, Debug, Default, serde::Serialize, serde::Deserialize)]\npub struct {name} {{").unwrap();
    for (k, v) in props {
        let (t, null) = ty(v, &format!("{name}.{k}"));
        // Required and nullable: always written (null). Optional: omitted when None.
        let attr = if required.contains(&k.as_str()) { if null { format!("Option<{t}>") } else { t } } else {
            writeln!(out, "    #[serde(default, skip_serializing_if = \"Option::is_none\")]").unwrap();
            format!("Option<{t}>")
        };
        writeln!(out, "    #[serde(rename = {k:?})]\n    pub {}: {attr},", snake(k)).unwrap();
    }
    // An open object keeps the fields the schema does not name, so a reader passes them on unchanged.
    if s["additionalProperties"] == true { writeln!(out, "    #[serde(flatten)]\n    pub rest: serde_json::Map<String, serde_json::Value>,").unwrap() }
    writeln!(out, "}}\n").unwrap();
}
fn main() {
    let dir = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../../contracts");
    println!("cargo:rerun-if-changed={}", dir.display());
    let mut out = String::new();
    let mut files: Vec<_> = std::fs::read_dir(&dir).unwrap().filter_map(|e| e.ok()).map(|e| e.path()).filter(|p| p.to_string_lossy().ends_with(".schema.json")).collect();
    files.sort();
    for f in files {
        println!("cargo:rerun-if-changed={}", f.display());
        let s: Value = serde_json::from_slice(&std::fs::read(&f).unwrap()).unwrap();
        for (name, d) in s["$defs"].as_object().expect("$defs") { def(&mut out, name, d) }
    }
    std::fs::write(std::path::Path::new(&std::env::var("OUT_DIR").unwrap()).join("contracts.rs"), out).unwrap();
}
