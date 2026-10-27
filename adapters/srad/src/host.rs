// SPDX-License-Identifier: MIT
//! The host role: srad's application event loop (AppEventLoop and AppClient).
//!
//! srad connects with the STATE will, subscribes to spBv1.0/#, publishes
//! STATE online with the will's timestamp, decodes and validates every
//! payload and publishes commands and rebirth requests. The adapter keeps what
//! the contract asks of a host on top of it: the births' aliases and
//! datatypes, node and device state and the seq checks.

use std::collections::{BTreeMap, HashMap};
use std::sync::atomic::Ordering;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use rumqttc::v5::MqttOptions;
use serde_json::{json, Map, Value};
use srad::app::{AppClient, AppEvent, AppEventLoop, PublishTopic, SubscriptionConfig};
use srad::client_rumqtt as rumqtt;
use srad::types::constants::BDSEQ;
use srad::types::payload::{metric, DataType, Metric};
use srad::types::{MetricId, MetricValue};
use tokio::sync::mpsc;

use crate::control::{self, CallError, CallResult};
use crate::fixture::{check_value, valid_id, DATATYPES};
use crate::out::{diag, emit, ADAPTER};
use crate::tap::{Received, TapClient, TapEventLoop, Taps};
use crate::values;
use crate::{mark_ready, RoleFlags};

pub const CALLS: [&str; 3] = ["send-command", "request-rebirth", "nodes"];

/// The metrics a birth defined: names by alias, datatypes by name.
#[derive(Default)]
struct Scope {
    by_alias: HashMap<u64, String>,
    types: HashMap<String, DataType>,
}

struct Node {
    online: bool,
    bd_seq: Option<u64>,
    seq: u64,
    devices: BTreeMap<String, bool>,
    /// By device, "" for the node.
    scopes: HashMap<String, Scope>,
}

#[derive(Default)]
struct State {
    nodes: BTreeMap<(String, String), Node>,
}

fn doc(name: &str, dt: DataType, m: &Metric) -> Value {
    let v = if m.is_null == Some(true) {
        None
    } else {
        m.value.clone().map(MetricValue::from)
    };
    let value = values::to_json(dt, v).unwrap_or_else(|e| {
        diag(format!("metric {name}: {e}"));
        Value::Null
    });
    json!({"name": name, "datatype": values::datatype_name(dt), "value": value})
}

/// A birth's metrics (without bdSeq) and the scope they define.
fn birth(metrics: &[Metric]) -> (Vec<Value>, Scope) {
    let mut scope = Scope::default();
    let mut docs = Vec::new();
    for m in metrics {
        let Some(name) = m.name.clone().filter(|n| n != BDSEQ) else {
            continue;
        };
        let Some(dt) = m.datatype.and_then(|d| DataType::try_from(d).ok()) else {
            diag(format!("birth metric {name} has no valid datatype"));
            continue;
        };
        if let Some(a) = m.alias {
            scope.by_alias.insert(a, name.clone());
        }
        scope.types.insert(name.clone(), dt);
        docs.push(doc(&name, dt, m));
    }
    (docs, scope)
}

/// A data message's metrics, named and typed by the birth.
fn data(scope: Option<&Scope>, metrics: &[Metric]) -> Vec<Value> {
    let mut docs = Vec::new();
    for m in metrics {
        let name = match (m.alias, &m.name, scope) {
            (_, Some(n), _) => Some(n.clone()),
            (Some(a), None, Some(s)) => s.by_alias.get(&a).cloned(),
            _ => None,
        };
        match name.and_then(|n| scope.and_then(|s| s.types.get(&n)).map(|dt| (n, *dt))) {
            Some((n, dt)) => docs.push(doc(&n, dt, m)),
            None => diag(format!(
                "cannot resolve metric {:?}/{:?}: no birth defines it",
                m.name, m.alias
            )),
        }
    }
    docs
}

fn bd_seq(metrics: &[Metric]) -> Option<u64> {
    metrics
        .iter()
        .find(|m| m.name.as_deref() == Some(BDSEQ))
        .and_then(|m| match &m.value {
            Some(metric::Value::LongValue(v)) => Some(*v),
            Some(metric::Value::IntValue(v)) => Some(*v as u64),
            _ => None,
        })
}

struct Host {
    state: Mutex<State>,
    client: AppClient,
}

impl Host {
    fn rebirth(&self, group: &str, node: &str, reason: &str) {
        emit(
            "rebirth-requested",
            json!({"group": group, "edgeNode": node, "reason": reason}),
        );
        let (c, g, n) = (self.client.clone(), group.to_string(), node.to_string());
        tokio::spawn(async move {
            if c.publish_node_rebirth(&g, &n).await.is_err() {
                diag(format!("rebirth request to {g}/{n} failed"));
            }
        });
    }

    /// Seq check for a message other than NBIRTH and NDEATH.
    fn track(st: &mut State, key: &(String, String), seq: Option<u64>) -> Option<&'static str> {
        match st.nodes.get_mut(key) {
            Some(n) if n.online => {
                let expected = (n.seq + 1) % 256;
                n.seq = seq.unwrap_or(u64::MAX);
                (seq != Some(expected)).then_some("seq-gap")
            }
            _ => Some("unknown-node"),
        }
    }

    fn on_message(&self, r: Received) {
        let key = (r.group.clone(), r.node.clone());
        let metrics = &r.payload.metrics;
        let is_bd = r.kind == "NBIRTH" || r.kind == "NDEATH";
        let mut st = self.state.lock().unwrap();
        let scope_name = r.device.clone().unwrap_or_default();
        let (docs, scope) = match r.kind.as_str() {
            "NBIRTH" | "DBIRTH" => {
                let (d, s) = birth(metrics);
                (d, Some(s))
            }
            "NDATA" | "DDATA" => (
                data(st.nodes.get(&key).and_then(|n| n.scopes.get(&scope_name)), metrics),
                None,
            ),
            _ => (vec![], None),
        };
        let bd = if is_bd { bd_seq(metrics) } else { None };
        let seq = if r.kind == "NDEATH" { None } else { r.payload.seq };
        emit(
            "message",
            json!({"type": r.kind, "group": r.group, "edgeNode": r.node, "device": r.device, "seq": seq, "bdSeq": bd, "metrics": docs}),
        );
        let reason = match r.kind.as_str() {
            "NBIRTH" => {
                let scopes = HashMap::from([(String::new(), scope.unwrap())]);
                st.nodes.insert(
                    key,
                    Node {
                        online: true,
                        bd_seq: bd,
                        seq: seq.unwrap_or(0),
                        devices: BTreeMap::new(),
                        scopes,
                    },
                );
                None
            }
            "NDEATH" => {
                if let Some(n) = st.nodes.get_mut(&key).filter(|n| n.bd_seq == bd) {
                    n.online = false;
                    n.devices.values_mut().for_each(|o| *o = false);
                }
                None
            }
            kind => {
                let reason = Self::track(&mut st, &key, seq);
                if let (Some(n), Some(dev)) = (st.nodes.get_mut(&key).filter(|n| n.online), &r.device) {
                    match kind {
                        "DBIRTH" => {
                            n.devices.insert(dev.clone(), true);
                            n.scopes.insert(dev.clone(), scope.unwrap());
                        }
                        "DDEATH" => {
                            n.devices.insert(dev.clone(), false);
                        }
                        _ => {}
                    }
                }
                reason
            }
        };
        drop(st);
        if let Some(reason) = reason {
            self.rebirth(&r.group, &r.node, reason);
        }
    }

    fn list(&self) -> Value {
        let st = self.state.lock().unwrap();
        let nodes: Vec<Value> = st
            .nodes
            .iter()
            .map(|((g, n), s)| {
                let devices: Vec<Value> = s
                    .devices
                    .iter()
                    .map(|(d, o)| json!({"device": d, "online": o}))
                    .collect();
                json!({"group": g, "edgeNode": n, "online": s.online, "bdSeq": s.bd_seq, "devices": devices})
            })
            .collect();
        json!({"nodes": nodes})
    }

    async fn call(&self, op: &str, p: Map<String, Value>) -> CallResult {
        if op == "nodes" {
            return Ok(self.list());
        }
        let id = |k: &str| match p.get(k).and_then(Value::as_str) {
            Some(v) if valid_id(v) => Ok(v.to_string()),
            _ => Err(CallError::invalid(format!("{k} is missing or invalid"))),
        };
        let (group, node) = (id("group")?, id("edgeNode")?);
        if op == "request-rebirth" {
            self.client
                .publish_node_rebirth(&group, &node)
                .await
                .map_err(|_| CallError("not-connected", "publish failed".into()))?;
            return Ok(json!({"published": ["NCMD"]}));
        }
        let device = match p.get("device") {
            None | Some(Value::Null) => None,
            Some(_) => Some(id("device")?),
        };
        let metrics = match p.get("metrics").and_then(Value::as_array) {
            Some(m) if !m.is_empty() => m,
            _ => return Err(CallError::invalid("metrics is missing or empty")),
        };
        let mut out = Vec::new();
        for m in metrics {
            let (name, dt, v) = (
                m.get("name").and_then(Value::as_str),
                m.get("datatype").and_then(Value::as_str),
                m.get("value"),
            );
            let (Some(name), Some(dt), Some(v)) = (name, dt, v) else {
                return Err(CallError::invalid(format!("invalid metric {m}")));
            };
            if !DATATYPES.contains(&dt) {
                return Err(CallError::invalid(format!("invalid metric {m}")));
            }
            check_value(dt, v).map_err(|e| CallError::invalid(format!("{name}: {e}")))?;
            // A metric the birth gave an alias is addressed by its alias.
            let alias = {
                let st = self.state.lock().unwrap();
                st.nodes
                    .get(&(group.clone(), node.clone()))
                    .and_then(|n| n.scopes.get(device.as_deref().unwrap_or("")))
                    .and_then(|s| s.by_alias.iter().find(|(_, n)| n.as_str() == name).map(|(a, _)| *a))
            };
            let mid = alias
                .map(MetricId::Alias)
                .unwrap_or_else(|| MetricId::Name(name.to_string()));
            out.push(values::app_metric(mid, dt, v).ok_or_else(|| CallError::invalid(format!("invalid metric {m}")))?);
        }
        let (topic, published) = match &device {
            Some(d) => (PublishTopic::new_device_cmd(&group, &node, d), format!("DCMD/{d}")),
            None => (PublishTopic::new_node_cmd(&group, &node), "NCMD".to_string()),
        };
        self.client
            .publish_metrics(topic, out)
            .await
            .map_err(|_| CallError("not-connected", "publish failed".into()))?;
        Ok(json!({"published": [published]}))
    }
}

pub async fn run(role: RoleFlags, host_id: String) -> i32 {
    let client_id = role
        .client_id
        .clone()
        .unwrap_or_else(|| format!("sparkplugb-interop-{ADAPTER}-host-{host_id}"));
    let (tx, mut received) = mpsc::unbounded_channel();
    let taps = Taps::new(false, Some(tx));
    let opts = MqttOptions::new(client_id, role.host.clone(), role.port);
    let (el, cl) = rumqtt::EventLoop::new(opts, 256);
    let (mut app, client) = AppEventLoop::new(
        host_id.clone(),
        SubscriptionConfig::AllGroups,
        TapEventLoop {
            inner: el,
            taps: taps.clone(),
        },
        TapClient {
            inner: cl,
            taps: taps.clone(),
        },
    );
    let host = Arc::new(Host {
        state: Mutex::new(State::default()),
        client: client.clone(),
    });

    let mut online = taps.online.subscribe();
    let mut states = taps.state.subscribe();
    let h = host.clone();
    // srad's AppEventLoop does the application's MQTT and STATE work; its
    // events carry types srad-app does not export, so the messages are read
    // from srad's client events (the tap), in the order received.
    tokio::spawn(async move {
        while let Some(r) = received.recv().await {
            h.on_message(r);
        }
    });
    let task = tokio::spawn(async move {
        loop {
            match app.poll().await {
                AppEvent::Cancelled => break,
                AppEvent::InvalidPayload(e) => diag(format!("srad rejected a payload: {e:?}")),
                _ => {}
            }
        }
    });
    if tokio::time::timeout(role.connect_timeout, online.wait_for(|o| *o))
        .await
        .is_err()
    {
        diag("no CONNACK within the connect timeout");
        return 3;
    }
    // srad subscribes, then publishes STATE online: once the broker sends it
    // back, both are done.
    let echoed = tokio::time::timeout(Duration::from_secs(10), async {
        loop {
            match states.recv().await {
                Ok((id, true)) if id == host_id => return true,
                Ok(_) | Err(tokio::sync::broadcast::error::RecvError::Lagged(_)) => continue,
                Err(_) => return false,
            }
        }
    })
    .await;
    if !matches!(echoed, Ok(true)) {
        diag("srad's STATE online did not come back from the broker");
        return 1;
    }

    let handler: control::Handler = {
        let host = host.clone();
        Arc::new(move |op, p| {
            let host = host.clone();
            Box::pin(async move { host.call(&op, p).await })
        })
    };
    if let Err(e) = control::serve(&role.control_socket, &CALLS, handler) {
        diag(format!("control socket: {e}"));
        return 1;
    }
    if let Err(e) = mark_ready(&role.ready_file) {
        diag(format!("ready file: {e}"));
        return 1;
    }
    emit("ready", json!({"adapter": ADAPTER, "hostId": host_id}));

    crate::wait_for_term().await;
    taps.stopping.store(true, Ordering::SeqCst);
    client.cancel().await;
    let _ = tokio::time::timeout(Duration::from_secs(8), task).await;
    emit("stopped", json!({}));
    0
}
