// SPDX-License-Identifier: MIT
//! Fixtures (docs/FIXTURES.md): loading and the checks every adapter makes.

use std::collections::{HashMap, HashSet};

use serde_json::Value;

pub const DATATYPES: [&str; 14] = [
    "Int8", "Int16", "Int32", "Int64", "UInt8", "UInt16", "UInt32", "UInt64", "Float", "Double", "Boolean", "String",
    "DateTime", "Text",
];
pub const MAX_SAFE: i64 = (1 << 53) - 1;

pub fn fixtures_dir() -> String {
    std::env::var("SPARKPLUGB_INTEROP_FIXTURES").unwrap_or_else(|_| "/fixtures".into())
}

/// A valid Sparkplug ID: non-empty, without '/', '+' or '#'.
pub fn valid_id(s: &str) -> bool {
    !s.is_empty() && !s.contains(['/', '+', '#'])
}

#[derive(Clone, Debug)]
pub struct Metric {
    pub name: String,
    pub alias: u64,
    pub datatype: String,
    pub value: Value,
    pub writable: bool,
}

#[derive(Clone, Debug)]
pub struct Device {
    pub id: String,
    pub metrics: Vec<Metric>,
}

#[derive(Clone, Debug)]
pub struct Change {
    pub name: String,
    pub value: Value,
}

#[derive(Clone, Debug)]
pub struct Update {
    pub node: Vec<Change>,
    pub devices: HashMap<String, Vec<Change>>,
}

#[derive(Clone, Debug)]
pub struct Fixture {
    pub name: String,
    pub group: String,
    pub edge_node: String,
    pub node: Vec<Metric>,
    pub devices: Vec<Device>,
    pub updates: Vec<Update>,
}

impl Fixture {
    pub fn device(&self, id: &str) -> Option<&Device> {
        self.devices.iter().find(|d| d.id == id)
    }
}

fn range(dt: &str) -> Option<(i64, i64)> {
    Some(match dt {
        "Int8" => (i8::MIN as i64, i8::MAX as i64),
        "Int16" => (i16::MIN as i64, i16::MAX as i64),
        "Int32" => (i32::MIN as i64, i32::MAX as i64),
        "Int64" => (-MAX_SAFE, MAX_SAFE),
        "UInt8" => (0, u8::MAX as i64),
        "UInt16" => (0, u16::MAX as i64),
        "UInt32" => (0, u32::MAX as i64),
        "UInt64" | "DateTime" => (0, MAX_SAFE),
        _ => return None,
    })
}

/// Checks that v is a value of the datatype dt.
pub fn check_value(dt: &str, v: &Value) -> Result<(), String> {
    if let Some((lo, hi)) = range(dt) {
        return match v.as_i64() {
            Some(i) if v.is_i64() || v.is_u64() => {
                if lo <= i && i <= hi {
                    Ok(())
                } else {
                    Err(format!("{v} is not a {dt}"))
                }
            }
            _ => Err(format!("{v} is not a {dt}")),
        };
    }
    match dt {
        "Float" | "Double" => {
            let f = v.as_f64().ok_or_else(|| format!("{v} is not a number"))?;
            if dt == "Float" && (f as f32) as f64 != f {
                return Err(format!("{v} is not exactly a Float"));
            }
            Ok(())
        }
        "Boolean" => v.as_bool().map(|_| ()).ok_or_else(|| format!("{v} is not a boolean")),
        "String" | "Text" => v.as_str().map(|_| ()).ok_or_else(|| format!("{v} is not a string")),
        _ => Err(format!("unknown datatype {dt:?}")),
    }
}

pub fn path_of(name_or_path: &str) -> String {
    if name_or_path.contains('/') || name_or_path.ends_with(".json") {
        name_or_path.to_string()
    } else {
        format!("{}/{name_or_path}/fixture.json", fixtures_dir())
    }
}

fn s(v: &Value, k: &str) -> Result<String, String> {
    v.get(k)
        .and_then(Value::as_str)
        .map(str::to_string)
        .ok_or_else(|| format!("{k} is missing or not a string"))
}

fn arr<'a>(v: &'a Value, k: &str) -> Result<&'a Vec<Value>, String> {
    v.get(k)
        .and_then(Value::as_array)
        .ok_or_else(|| format!("{k} is missing or not an array"))
}

fn metrics(v: &Value, scope: &str, aliases: &mut HashSet<u64>) -> Result<Vec<Metric>, String> {
    let list = arr(v, "metrics")?;
    if list.is_empty() {
        return Err(format!("{scope} has no metrics"));
    }
    let mut names = HashSet::new();
    let mut out = Vec::new();
    for m in list {
        let name = s(m, "name")?;
        let at = format!("{scope}, metric {name:?}");
        if !names.insert(name.clone()) || name.starts_with("Node Control/") || name == "bdSeq" {
            return Err(format!("{at}: duplicate or reserved name"));
        }
        let alias = m
            .get("alias")
            .filter(|a| a.is_u64())
            .and_then(Value::as_u64)
            .filter(|a| *a >= 1);
        let alias = match alias {
            Some(a) if aliases.insert(a) => a,
            _ => return Err(format!("{at}: invalid or duplicate alias")),
        };
        let datatype = s(m, "datatype")?;
        let value = m.get("value").cloned().ok_or_else(|| format!("{at}: no value"))?;
        check_value(&datatype, &value).map_err(|e| format!("{at}: {e}"))?;
        let writable = match m.get("writable") {
            None => false,
            Some(Value::Bool(b)) => *b,
            Some(_) => return Err(format!("{at}: writable is not a boolean")),
        };
        out.push(Metric {
            name,
            alias,
            datatype,
            value,
            writable,
        });
    }
    Ok(out)
}

fn changes(v: &Value) -> Result<Vec<Change>, String> {
    v.as_array()
        .ok_or("changes are not an array")?
        .iter()
        .map(|c| {
            Ok(Change {
                name: s(c, "name")?,
                value: c.get("value").cloned().ok_or("change without a value")?,
            })
        })
        .collect()
}

pub fn load(name_or_path: &str) -> Result<Fixture, String> {
    let path = path_of(name_or_path);
    let text = std::fs::read_to_string(&path).map_err(|e| format!("{path}: {e}"))?;
    let fx: Value = serde_json::from_str(&text).map_err(|e| format!("{path}: {e}"))?;
    parse(&fx).map_err(|e| format!("{path}: {e}"))
}

fn parse(fx: &Value) -> Result<Fixture, String> {
    if fx.get("schemaVersion").and_then(Value::as_str) != Some("1.0") {
        return Err("schemaVersion is not 1.0".into());
    }
    let (group, edge_node) = (s(fx, "group")?, s(fx, "edgeNode")?);
    if !valid_id(&group) || !valid_id(&edge_node) {
        return Err("group or edgeNode is not a valid Sparkplug ID".into());
    }
    let mut aliases = HashSet::new();
    let node = metrics(fx.get("node").ok_or("node is missing")?, "node", &mut aliases)?;
    let mut devices = Vec::new();
    for d in arr(fx, "devices")? {
        let id = s(d, "id")?;
        if !valid_id(&id) || devices.iter().any(|x: &Device| x.id == id) {
            return Err(format!("device {id:?} is invalid or appears twice"));
        }
        let metrics = metrics(d, &format!("device {id}"), &mut aliases)?;
        devices.push(Device { id, metrics });
    }
    let type_of = |dev: Option<&str>, name: &str| -> Option<String> {
        let list = match dev {
            None => &node,
            Some(d) => &devices.iter().find(|x| x.id == d)?.metrics,
        };
        list.iter().find(|m| m.name == name).map(|m| m.datatype.clone())
    };
    let mut updates = Vec::new();
    for (i, u) in arr(fx, "updates")?.iter().enumerate() {
        let node_changes = changes(u.get("node").ok_or(format!("updates[{i}].node is missing"))?)?;
        let mut dev_changes = HashMap::new();
        let mut all: Vec<(Option<String>, Change)> = node_changes.iter().map(|c| (None, c.clone())).collect();
        for (dev, cs) in u
            .get("devices")
            .and_then(Value::as_object)
            .ok_or(format!("updates[{i}].devices is missing"))?
        {
            if !devices.iter().any(|d| &d.id == dev) {
                return Err(format!("updates[{i}]: unknown device {dev:?}"));
            }
            let cs = changes(cs)?;
            all.extend(cs.iter().map(|c| (Some(dev.clone()), c.clone())));
            dev_changes.insert(dev.clone(), cs);
        }
        if all.is_empty() {
            return Err(format!("updates[{i}] changes nothing"));
        }
        for (dev, c) in &all {
            let dt = type_of(dev.as_deref(), &c.name).ok_or_else(|| {
                format!(
                    "updates[{i}]: {} has no metric {:?}",
                    dev.as_deref().unwrap_or("node"),
                    c.name
                )
            })?;
            check_value(&dt, &c.value).map_err(|e| format!("updates[{i}], metric {:?}: {e}", c.name))?;
        }
        updates.push(Update {
            node: node_changes,
            devices: dev_changes,
        });
    }
    let name = fx.get("name").and_then(Value::as_str).unwrap_or("").to_string();
    Ok(Fixture {
        name,
        group,
        edge_node,
        node,
        devices,
        updates,
    })
}
