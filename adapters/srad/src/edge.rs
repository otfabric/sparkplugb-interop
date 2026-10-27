// SPDX-License-Identifier: MIT
//! The edge role: srad's EoN (Edge of Network node) with the fixture's node
//! metrics and devices.
//!
//! srad publishes the births, data, commands' answers and NDEATH itself; the
//! adapter provides the metrics (srad's NodeMetricManager and
//! DeviceMetricManager) and drives srad's NodeHandle and DeviceHandles.
//!
//! Device births: srad births every enabled device from its own task when the
//! node births, so their order (and seq) is not the fixture's. The adapter
//! enables the devices one by one after NBIRTH, which gives the fixture order
//! at start-up. A rebirth (NCMD or NodeHandle::rebirth) births all devices at
//! once, in an order srad does not define: rebirthCommand and the rebirth call
//! are therefore not declared.

use std::collections::HashMap;
use std::sync::atomic::Ordering;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use async_trait::async_trait;
use rumqttc::v5::MqttOptions;
use serde_json::{json, Map, Value};
use srad::client_rumqtt as rumqtt;
use srad::eon::{
    BirthInitializer, DeviceHandle, DeviceMetricManager, EoNBuilder, MessageMetrics, MetricManager, MetricPublisher,
    NodeHandle, NodeMetricManager, PublishMetric,
};
use srad::types::constants::NODE_CONTROL_REBIRTH;
use srad::types::MetricId;
use tokio::sync::broadcast;

use crate::control::{self, CallError, CallResult};
use crate::fixture::{check_value, Fixture, Metric};
use crate::out::{diag, emit, ADAPTER};
use crate::tap::{Published, TapClient, TapEventLoop, Taps};
use crate::values::{self, Tok};
use crate::{mark_ready, RoleFlags};

pub const CALLS: [&str; 3] = ["publish-data", "device-death", "device-birth"];

struct Shared {
    fx: Fixture,
    /// Current values by (device, or "" for the node, metric name).
    values: Mutex<HashMap<(String, String), Value>>,
    /// The tokens of the latest birth, by (device or "", metric name).
    toks: Mutex<HashMap<(String, String), Tok>>,
    online: Mutex<Vec<String>>,
    taps: Arc<Taps>,
}

impl Shared {
    fn scope(&self, dev: &str) -> &[Metric] {
        if dev.is_empty() {
            &self.fx.node
        } else {
            self.fx.device(dev).map(|d| d.metrics.as_slice()).unwrap_or(&[])
        }
    }

    fn birth(&self, dev: &str, bi: &mut BirthInitializer) {
        let values = self.values.lock().unwrap();
        let mut toks = self.toks.lock().unwrap();
        for m in self.scope(dev) {
            let key = (dev.to_string(), m.name.clone());
            match values::register(bi, &m.name, &m.datatype, m.alias, &values[&key]) {
                Ok(tok) => {
                    toks.insert(key, tok);
                }
                Err(e) => diag(format!(
                    "birth of {}: {e}",
                    if dev.is_empty() { "the node" } else { dev }
                )),
            }
        }
    }

    /// Data message metrics for (name, value) of a scope; sets the values first.
    fn changes(&self, dev: &str, changes: &[(String, Value)]) -> Vec<PublishMetric> {
        let mut values = self.values.lock().unwrap();
        for (n, v) in changes {
            values.insert((dev.to_string(), n.clone()), v.clone());
        }
        let toks = self.toks.lock().unwrap();
        changes
            .iter()
            .filter_map(|(n, v)| {
                toks.get(&(dev.to_string(), n.clone()))
                    .and_then(|t| values::publish_metric(t, v))
            })
            .collect()
    }

    /// The metrics of an NCMD or DCMD: their documents, the writes to apply
    /// and whether it asks for a rebirth.
    fn command(&self, dev: &str, metrics: MessageMetrics) -> (Vec<Value>, Vec<(String, Value)>, bool) {
        let scope = self.scope(dev);
        let (mut docs, mut writes, mut rebirth) = (Vec::new(), Vec::new(), false);
        for m in metrics {
            let name = match &m.id {
                MetricId::Name(n) => n.clone(),
                MetricId::Alias(a) => match scope.iter().find(|x| x.alias == *a) {
                    Some(x) => x.name.clone(),
                    None => format!("alias {a}"),
                },
            };
            let meta = scope.iter().find(|x| x.name == name);
            let dt = match meta {
                Some(x) => values::datatype(&x.datatype).unwrap(),
                None if name == NODE_CONTROL_REBIRTH => srad::types::payload::DataType::Boolean,
                None => values::guess_datatype(&m.value),
            };
            let value = values::to_json(dt, m.value).unwrap_or(Value::Null);
            if dev.is_empty() && name == NODE_CONTROL_REBIRTH && value == json!(true) {
                rebirth = true;
            }
            if let Some(x) = meta.filter(|x| x.writable) {
                if check_value(&x.datatype, &value).is_ok() {
                    writes.push((name.clone(), value.clone()));
                }
            }
            docs.push(json!({"name": name, "datatype": values::datatype_name(dt), "value": value}));
        }
        (docs, writes, rebirth)
    }
}

struct NodeMgr(Arc<Shared>);

impl MetricManager for NodeMgr {
    fn initialise_birth(&self, bi: &mut BirthInitializer) {
        self.0.birth("", bi);
    }
}

#[async_trait]
impl NodeMetricManager for NodeMgr {
    async fn on_ncmd(&self, node: NodeHandle, metrics: MessageMetrics) {
        let (docs, writes, rebirth) = self.0.command("", metrics);
        let mut applied = rebirth;
        if !writes.is_empty() {
            let pm = self.0.changes("", &writes);
            applied = node.publish_metrics_unsorted(pm).await.is_ok() || applied;
        }
        emit("command", json!({"device": null, "metrics": docs, "applied": applied}));
    }
}

struct DeviceMgr(Arc<Shared>, String);

impl MetricManager for DeviceMgr {
    fn initialise_birth(&self, bi: &mut BirthInitializer) {
        self.0.birth(&self.1, bi);
    }
}

#[async_trait]
impl DeviceMetricManager for DeviceMgr {
    async fn on_dcmd(&self, device: DeviceHandle, metrics: MessageMetrics) {
        let (docs, writes, _) = self.0.command(&self.1, metrics);
        let mut applied = false;
        if !writes.is_empty() {
            let pm = self.0.changes(&self.1, &writes);
            applied = device.publish_metrics_unsorted(pm).await.is_ok();
        }
        emit(
            "command",
            json!({"device": self.1, "metrics": docs, "applied": applied}),
        );
    }
}

/// Waits for srad to publish a message of kind for device (None: the node).
async fn wait_published(
    rx: &mut broadcast::Receiver<Published>,
    kind: &str,
    device: Option<&str>,
    t: Duration,
) -> Option<Published> {
    tokio::time::timeout(t, async {
        loop {
            match rx.recv().await {
                Ok(p) if p.kind == kind && p.device.as_deref() == device => return Some(p),
                Ok(_) | Err(broadcast::error::RecvError::Lagged(_)) => continue,
                Err(_) => return None,
            }
        }
    })
    .await
    .ok()
    .flatten()
}

/// Enables a device and waits for its DBIRTH. srad drops a birth requested
/// while the node's own birth is still completing, so it asks again.
async fn birth_device(h: &DeviceHandle, rx: &mut broadcast::Receiver<Published>, dev: &str) -> bool {
    for _ in 0..20 {
        h.enable();
        if wait_published(rx, "DBIRTH", Some(dev), Duration::from_millis(500))
            .await
            .is_some()
        {
            return true;
        }
    }
    false
}

pub struct EdgeFlags {
    pub role: RoleFlags,
    pub fixture: Fixture,
    pub group: String,
    pub edge_node: String,
}

pub async fn run(f: EdgeFlags) -> i32 {
    let (group, node_id) = (f.group.clone(), f.edge_node.clone());
    let client_id = f
        .role
        .client_id
        .clone()
        .unwrap_or_else(|| format!("sparkplugb-interop-{ADAPTER}-edge-{group}-{node_id}"));
    let taps = Taps::new(true, None);
    let mut values = HashMap::new();
    for m in &f.fixture.node {
        values.insert((String::new(), m.name.clone()), m.value.clone());
    }
    for d in &f.fixture.devices {
        for m in &d.metrics {
            values.insert((d.id.clone(), m.name.clone()), m.value.clone());
        }
    }
    let shared = Arc::new(Shared {
        values: Mutex::new(values),
        toks: Mutex::new(HashMap::new()),
        online: Mutex::new(Vec::new()),
        taps: taps.clone(),
        fx: f.fixture,
    });

    let opts = MqttOptions::new(client_id, f.role.host.clone(), f.role.port);
    let (el, cl) = rumqtt::EventLoop::new(opts, 256);
    let builder = EoNBuilder::new(
        TapEventLoop {
            inner: el,
            taps: taps.clone(),
        },
        TapClient {
            inner: cl,
            taps: taps.clone(),
        },
    )
    .with_group_id(group.clone())
    .with_node_id(node_id.clone())
    .with_rebirth_cmd_cooldown(Duration::ZERO)
    .with_metric_manager(NodeMgr(shared.clone()));
    let (eon, node) = match builder.build() {
        Ok(x) => x,
        Err(e) => {
            diag(format!("EoN: {e}"));
            return 1;
        }
    };
    let mut devices = HashMap::new();
    for d in &shared.fx.devices {
        match node.register_device(d.id.clone(), DeviceMgr(shared.clone(), d.id.clone())) {
            Ok(h) => {
                devices.insert(d.id.clone(), h);
            }
            Err(e) => {
                diag(format!("device {}: {e}", d.id));
                return 1;
            }
        }
    }
    let devices = Arc::new(devices);

    let mut rx = taps.published.subscribe();
    let mut online = taps.online.subscribe();
    let eon_task = tokio::spawn(eon.run());
    if tokio::time::timeout(f.role.connect_timeout, online.wait_for(|o| *o))
        .await
        .is_err()
    {
        diag("no CONNACK within the connect timeout");
        return 3;
    }
    let Some(nbirth) = wait_published(&mut rx, "NBIRTH", None, Duration::from_secs(10)).await else {
        diag("srad published no NBIRTH");
        return 1;
    };
    for d in &shared.fx.devices {
        if !birth_device(&devices[&d.id], &mut rx, &d.id).await {
            diag(format!("srad published no DBIRTH for {}", d.id));
            return 1;
        }
        shared.online.lock().unwrap().push(d.id.clone());
    }

    let handler: control::Handler = {
        let (shared, node, devices) = (shared.clone(), node.clone(), devices.clone());
        Arc::new(move |op, p| {
            let (shared, node, devices) = (shared.clone(), node.clone(), devices.clone());
            Box::pin(async move { call(&shared, &node, &devices, &op, p).await })
        })
    };
    if let Err(e) = control::serve(&f.role.control_socket, &CALLS, handler) {
        diag(format!("control socket: {e}"));
        return 1;
    }
    if let Err(e) = mark_ready(&f.role.ready_file) {
        diag(format!("ready file: {e}"));
        return 1;
    }
    emit(
        "ready",
        json!({"adapter": ADAPTER, "fixture": shared.fx.name, "group": group, "edgeNode": node_id, "bdSeq": nbirth.bd_seq}),
    );

    crate::wait_for_term().await;
    taps.stopping.store(true, Ordering::SeqCst);
    node.cancel().await;
    let _ = tokio::time::timeout(Duration::from_secs(8), eon_task).await;
    emit("stopped", json!({}));
    0
}

async fn call(
    s: &Shared,
    node: &NodeHandle,
    devices: &HashMap<String, DeviceHandle>,
    op: &str,
    p: Map<String, Value>,
) -> CallResult {
    let not_connected = |e: srad::eon::PublishError| CallError("not-connected", e.to_string());
    match op {
        "publish-data" => {
            let i = p
                .get("update")
                .filter(|v| v.is_u64())
                .and_then(Value::as_u64)
                .map(|i| i as usize);
            let u = match i.and_then(|i| s.fx.updates.get(i)) {
                Some(u) => u.clone(),
                None => {
                    return Err(CallError::invalid(format!(
                        "no update {}",
                        p.get("update").unwrap_or(&Value::Null)
                    )))
                }
            };
            for d in &s.fx.devices {
                if u.devices.get(&d.id).is_some_and(|c| !c.is_empty()) && !s.online.lock().unwrap().contains(&d.id) {
                    return Err(CallError::invalid(format!("device {} is offline", d.id)));
                }
            }
            let mut published = Vec::new();
            if !u.node.is_empty() {
                let ch: Vec<_> = u.node.iter().map(|c| (c.name.clone(), c.value.clone())).collect();
                node.publish_metrics_unsorted(s.changes("", &ch))
                    .await
                    .map_err(not_connected)?;
                published.push("NDATA".to_string());
            }
            for d in &s.fx.devices {
                let Some(cs) = u.devices.get(&d.id).filter(|c| !c.is_empty()) else {
                    continue;
                };
                let ch: Vec<_> = cs.iter().map(|c| (c.name.clone(), c.value.clone())).collect();
                devices[&d.id]
                    .publish_metrics_unsorted(s.changes(&d.id, &ch))
                    .await
                    .map_err(not_connected)?;
                published.push(format!("DDATA/{}", d.id));
            }
            Ok(json!({"published": published}))
        }
        "device-death" | "device-birth" => {
            let death = op == "device-death";
            let dev = p.get("device").and_then(Value::as_str).unwrap_or("").to_string();
            let is_online = s.online.lock().unwrap().contains(&dev);
            let Some(h) = devices.get(&dev).filter(|_| is_online == death) else {
                let state = if death { "online" } else { "offline" };
                return Err(CallError::invalid(format!("no {state} device {dev:?}")));
            };
            let mut rx = s.taps.published.subscribe();
            let ok = if death {
                h.disable();
                wait_published(&mut rx, "DDEATH", Some(&dev), Duration::from_secs(5))
                    .await
                    .is_some()
            } else {
                birth_device(h, &mut rx, &dev).await
            };
            if !ok {
                return Err(CallError(
                    "timeout",
                    format!("srad published no {}", if death { "DDEATH" } else { "DBIRTH" }),
                ));
            }
            let mut online = s.online.lock().unwrap();
            if death {
                online.retain(|d| d != &dev);
            } else {
                online.push(dev.clone());
            }
            Ok(json!({"published": [format!("{}/{dev}", if death { "DDEATH" } else { "DBIRTH" })]}))
        }
        _ => Err(CallError("unsupported", format!("{op} is not offered by this role"))),
    }
}
