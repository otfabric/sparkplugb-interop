// SPDX-License-Identifier: MIT
//! sparkplugb-interop adapter for srad (docs/CONTAINER_CONTRACT.md).
//!
//! The edge role is srad's EoN, the host role srad's AppEventLoop and
//! AppClient, both over srad's rumqtt client (MQTT 5).

mod control;
mod edge;
mod fixture;
mod host;
mod out;
mod tap;
mod values;

use std::collections::HashMap;
use std::time::Duration;

use serde_json::{json, Value};

use crate::out::{diag, ADAPTER};

const EXIT_USAGE: i32 = 2;
const EXIT_FIXTURE: i32 = 4;
const EXIT_UNSUPPORTED: i32 = 5;
const DEFAULT_READY: &str = "/run/sparkplugb-interop/ready";
const DEFAULT_SOCKET: &str = "/run/sparkplugb-interop/control.sock";
const UPSTREAM_VERSION: &str = "0.5.0";

fn adapter_version() -> String {
    std::env::var("SPARKPLUGB_INTEROP_ADAPTER_VERSION").unwrap_or_else(|_| "dev".into())
}

fn upstream_version() -> String {
    std::env::var("SPARKPLUGB_INTEROP_UPSTREAM_VERSION").unwrap_or_else(|_| UPSTREAM_VERSION.into())
}

#[derive(Clone, Copy, PartialEq)]
enum Kind {
    Str,
    Int,
    Url,
    Id,
    Json,
}

/// Parses `--name value` pairs; any misuse is a usage error.
fn parse_flags(args: &[String], spec: &[(&str, Kind)], required: &[&str]) -> Result<HashMap<String, String>, String> {
    let mut out = HashMap::new();
    let mut i = 0;
    while i < args.len() {
        let a = &args[i];
        let name = a
            .strip_prefix("--")
            .filter(|n| spec.iter().any(|(s, _)| s == n))
            .ok_or(format!("unknown argument {a}"))?;
        let kind = spec.iter().find(|(s, _)| *s == name).unwrap().1;
        if out.contains_key(name) {
            return Err(format!("--{name} given twice"));
        }
        let v = args.get(i + 1).ok_or(format!("--{name} needs a value"))?;
        match kind {
            Kind::Int if v.parse::<u64>().map_or(true, |n| n == 0) => {
                return Err(format!("--{name}: {v:?} is not a positive integer"))
            }
            Kind::Url if parse_broker(v).is_none() => return Err(format!("--{name}: {v:?} is not tcp://host[:port]")),
            Kind::Id if !fixture::valid_id(v) => return Err(format!("--{name}: {v:?} is not a valid Sparkplug ID")),
            Kind::Json if !matches!(serde_json::from_str::<Value>(v), Ok(Value::Object(_))) => {
                return Err(format!("--{name} must be a JSON object"))
            }
            _ => {}
        }
        out.insert(name.to_string(), v.clone());
        i += 2;
    }
    for r in required {
        if !out.contains_key(*r) {
            return Err(format!("--{r} is required"));
        }
    }
    Ok(out)
}

fn parse_broker(url: &str) -> Option<(String, u16)> {
    let rest = url.strip_prefix("tcp://")?;
    let (host, port) = if let Some(r) = rest.strip_prefix('[') {
        let (h, tail) = r.split_once(']')?;
        (h, tail.strip_prefix(':'))
    } else {
        match rest.rsplit_once(':') {
            Some((h, p)) => (h, Some(p)),
            None => (rest, None),
        }
    };
    if host.is_empty() || host.contains(['/', '?', '#', '@']) {
        return None;
    }
    let port = match port {
        None => 1883,
        Some(p) => p.parse::<u16>().ok().filter(|p| *p > 0)?,
    };
    Some((host.to_string(), port))
}

/// The flags both roles share.
pub struct RoleFlags {
    pub host: String,
    pub port: u16,
    pub client_id: Option<String>,
    pub connect_timeout: Duration,
    pub ready_file: String,
    pub control_socket: String,
}

impl RoleFlags {
    fn from(f: &HashMap<String, String>) -> Self {
        let (host, port) = parse_broker(&f["broker"]).unwrap();
        RoleFlags {
            host,
            port,
            client_id: f.get("client-id").cloned(),
            connect_timeout: Duration::from_millis(f.get("connect-timeout-ms").map_or(10000, |v| v.parse().unwrap())),
            ready_file: f.get("ready-file").cloned().unwrap_or_else(|| DEFAULT_READY.into()),
            control_socket: f
                .get("control-socket")
                .cloned()
                .unwrap_or_else(|| DEFAULT_SOCKET.into()),
        }
    }
}

const COMMON: [(&str, Kind); 5] = [
    ("broker", Kind::Url),
    ("client-id", Kind::Str),
    ("connect-timeout-ms", Kind::Int),
    ("ready-file", Kind::Str),
    ("control-socket", Kind::Str),
];

pub fn mark_ready(path: &str) -> std::io::Result<()> {
    if let Some(dir) = std::path::Path::new(path)
        .parent()
        .filter(|d| !d.as_os_str().is_empty())
    {
        std::fs::create_dir_all(dir)?;
    }
    std::fs::write(path, "ready\n")
}

pub async fn wait_for_term() {
    use tokio::signal::unix::{signal, SignalKind};
    let mut term = signal(SignalKind::terminate()).expect("SIGTERM handler");
    let mut int = signal(SignalKind::interrupt()).expect("SIGINT handler");
    tokio::select! {
        _ = term.recv() => {}
        _ = int.recv() => {}
    }
}

fn capabilities() -> Value {
    json!({
        "schemaVersion": "1.0", "adapter": ADAPTER, "adapterVersion": adapter_version(), "protocol": "sparkplug-b-3.0.0",
        "upstream": {"name": "srad", "version": upstream_version(), "license": "MIT OR Apache-2.0", "language": "Rust",
                     "independentEngine": true},
        "roles": {
            "edge": {"supported": true, "fixtures": ["baseline", "scalars"], "calls": edge::CALLS,
                     "features": {"aliases": true, "rebirthCommand": false, "writes": true, "primaryHost": false,
                                  "gracefulDeath": true}},
            "host": {"supported": true, "datatypes": fixture::DATATYPES, "calls": host::CALLS,
                     "features": {"aliasResolution": true, "seqCheck": true, "gracefulOffline": true}},
        },
    })
}

fn main() {
    out::init_log();
    let mut args: Vec<String> = std::env::args().skip(1).collect();
    if args.is_empty() {
        args.push("edge".into());
    }
    std::process::exit(match run(&args[0], &args[1..]) {
        Ok(code) => code,
        Err(e) => {
            diag(format!("usage: {e}"));
            EXIT_USAGE
        }
    });
}

fn run(cmd: &str, args: &[String]) -> Result<i32, String> {
    match cmd {
        "print-capabilities" => {
            parse_flags(args, &[], &[])?;
            out::print_line(&capabilities());
            Ok(0)
        }
        "version" => {
            parse_flags(args, &[], &[])?;
            println!(
                "sparkplugb-interop-{ADAPTER} {} (srad {})",
                adapter_version(),
                upstream_version()
            );
            Ok(0)
        }
        "print-fixture" => {
            if args.len() > 1 {
                return Err("print-fixture takes at most one name".into());
            }
            let name = args.first().map_or("baseline", String::as_str);
            let path = format!("{}/{name}/fixture.json", fixture::fixtures_dir());
            match std::fs::read(&path) {
                Ok(bytes) if fixture::valid_id(name) => {
                    use std::io::Write;
                    let mut o = std::io::stdout().lock();
                    let _ = o.write_all(&bytes);
                    let _ = o.flush();
                    Ok(0)
                }
                _ => {
                    diag(format!("no baked fixture {name:?}"));
                    Ok(1)
                }
            }
        }
        "call" => {
            let op = args.first().ok_or("call needs an operation")?;
            if !control::ALL_CALLS.contains(&op.as_str()) {
                return Err(format!("unknown operation {op}"));
            }
            let f = parse_flags(
                &args[1..],
                &[
                    ("payload", Kind::Json),
                    ("timeout-ms", Kind::Int),
                    ("control-socket", Kind::Str),
                ],
                &[],
            )?;
            let payload = f.get("payload").map_or(json!({}), |p| serde_json::from_str(p).unwrap());
            let timeout = f.get("timeout-ms").map_or(10000, |t| t.parse().unwrap());
            Ok(control::call(
                op,
                payload,
                timeout,
                f.get("control-socket").map_or(DEFAULT_SOCKET, String::as_str),
            ))
        }
        "edge" => {
            let mut spec = COMMON.to_vec();
            spec.extend([
                ("fixture", Kind::Str),
                ("group", Kind::Id),
                ("edge-node", Kind::Id),
                ("primary-host", Kind::Id),
            ]);
            let f = parse_flags(args, &spec, &["broker"])?;
            if f.contains_key("primary-host") {
                diag("--primary-host is not supported: srad's EoN births on connect and does not follow a primary host's STATE");
                return Ok(EXIT_UNSUPPORTED);
            }
            let fx = match fixture::load(f.get("fixture").map_or("baseline", String::as_str)) {
                Ok(fx) => fx,
                Err(e) => {
                    diag(format!("fixture: {e}"));
                    return Ok(EXIT_FIXTURE);
                }
            };
            let flags = edge::EdgeFlags {
                role: RoleFlags::from(&f),
                group: f.get("group").cloned().unwrap_or_else(|| fx.group.clone()),
                edge_node: f.get("edge-node").cloned().unwrap_or_else(|| fx.edge_node.clone()),
                fixture: fx,
            };
            Ok(runtime().block_on(edge::run(flags)))
        }
        "host" => {
            let mut spec = COMMON.to_vec();
            spec.push(("host-id", Kind::Id));
            let f = parse_flags(args, &spec, &["broker", "host-id"])?;
            Ok(runtime().block_on(host::run(RoleFlags::from(&f), f["host-id"].clone())))
        }
        other => Err(format!("unknown command {other}")),
    }
}

fn runtime() -> tokio::runtime::Runtime {
    tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .expect("tokio runtime")
}
