// SPDX-License-Identifier: MIT
//! srad's rumqtt client and event loop, unchanged, with taps: every Sparkplug
//! message srad publishes becomes a `published` event (edge), and the
//! connection state and STATE messages are visible to the adapter.

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;

use async_trait::async_trait;
use serde_json::json;
use srad::client::MessageKind;
use srad::client::{Event, LastWill, StatePayload};
use srad::client_rumqtt as rumqtt;
use srad::types::constants::BDSEQ;
use srad::types::payload::{metric, Payload};
use srad::types::topic::{DeviceTopic, NodeTopic, StateTopic, TopicFilter};
use tokio::sync::{broadcast, mpsc, watch};

use crate::out::{diag, emit};

/// A Sparkplug message srad handed to the MQTT client.
#[derive(Clone, Debug)]
pub struct Published {
    pub kind: String,
    pub device: Option<String>,
    pub bd_seq: Option<i64>,
}

/// A Sparkplug message as srad's event loop received and decoded it.
pub struct Received {
    pub kind: String,
    pub group: String,
    pub node: String,
    pub device: Option<String>,
    pub payload: Payload,
}

/// What the taps share with the adapter.
pub struct Taps {
    /// Emit `published` events (the edge role).
    pub emit_published: bool,
    pub published: broadcast::Sender<Published>,
    /// True while connected (CONNACK seen).
    pub online: watch::Sender<bool>,
    /// STATE messages: (host ID, online).
    pub state: broadcast::Sender<(String, bool)>,
    /// Set before a clean stop, so that the disconnect is not a lost connection.
    pub stopping: AtomicBool,
    /// Every NBIRTH, NDATA, NDEATH, DBIRTH, DDATA and DDEATH received, in order.
    pub received: Option<mpsc::UnboundedSender<Received>>,
}

impl Taps {
    pub fn new(emit_published: bool, received: Option<mpsc::UnboundedSender<Received>>) -> Arc<Self> {
        Arc::new(Taps {
            received,
            emit_published,
            published: broadcast::channel(256).0,
            online: watch::channel(false).0,
            state: broadcast::channel(64).0,
            stopping: AtomicBool::new(false),
        })
    }

    fn record(&self, topic: &str, payload: &Payload) {
        let parts: Vec<&str> = topic.split('/').collect();
        let kind = parts.get(2).copied().unwrap_or("").to_string();
        let device = parts.get(4).map(|d| d.to_string());
        let birth_or_death = kind == "NBIRTH" || kind == "NDEATH";
        let bd_seq = payload
            .metrics
            .iter()
            .find(|m| m.name.as_deref() == Some(BDSEQ))
            .and_then(|m| match &m.value {
                Some(metric::Value::LongValue(v)) => Some(*v as i64),
                Some(metric::Value::IntValue(v)) => Some(*v as i64),
                _ => None,
            })
            .filter(|_| birth_or_death);
        if self.emit_published {
            emit(
                "published",
                json!({
                    "type": kind,
                    "device": device,
                    "seq": if kind == "NDEATH" { None } else { payload.seq },
                    "bdSeq": bd_seq,
                }),
            );
        }
        let _ = self.published.send(Published { kind, device, bd_seq });
    }
}

pub struct TapClient {
    pub inner: rumqtt::Client,
    pub taps: Arc<Taps>,
}

#[async_trait]
impl srad::client::Client for TapClient {
    async fn disconnect(&self) -> Result<(), ()> {
        self.inner.disconnect().await
    }

    async fn publish_state_message(&self, topic: StateTopic, payload: StatePayload) -> Result<(), ()> {
        self.inner.publish_state_message(topic, payload).await
    }

    async fn try_publish_state_message(&self, topic: StateTopic, payload: StatePayload) -> Result<(), ()> {
        self.inner.try_publish_state_message(topic, payload).await
    }

    async fn publish_node_message(&self, topic: NodeTopic, payload: Payload) -> Result<(), ()> {
        let (t, p) = (topic.topic.clone(), payload.clone());
        let r = self.inner.publish_node_message(topic, payload).await;
        if r.is_ok() {
            self.taps.record(&t, &p);
        }
        r
    }

    async fn try_publish_node_message(&self, topic: NodeTopic, payload: Payload) -> Result<(), ()> {
        let (t, p) = (topic.topic.clone(), payload.clone());
        let r = self.inner.try_publish_node_message(topic, payload).await;
        if r.is_ok() {
            self.taps.record(&t, &p);
        }
        r
    }

    async fn publish_device_message(&self, topic: DeviceTopic, payload: Payload) -> Result<(), ()> {
        let (t, p) = (topic.topic.clone(), payload.clone());
        let r = self.inner.publish_device_message(topic, payload).await;
        if r.is_ok() {
            self.taps.record(&t, &p);
        }
        r
    }

    async fn try_publish_device_message(&self, topic: DeviceTopic, payload: Payload) -> Result<(), ()> {
        let (t, p) = (topic.topic.clone(), payload.clone());
        let r = self.inner.try_publish_device_message(topic, payload).await;
        if r.is_ok() {
            self.taps.record(&t, &p);
        }
        r
    }

    async fn subscribe_many(&self, topics: Vec<TopicFilter>) -> Result<(), ()> {
        self.inner.subscribe_many(topics).await
    }
}

pub struct TapEventLoop {
    pub inner: rumqtt::EventLoop,
    pub taps: Arc<Taps>,
}

#[async_trait]
impl srad::client::EventLoop for TapEventLoop {
    async fn poll(&mut self) -> Event {
        let event = self.inner.poll().await;
        match &event {
            Event::Online => {
                let _ = self.taps.online.send(true);
            }
            Event::Offline => {
                let _ = self.taps.online.send(false);
                if !self.taps.stopping.load(Ordering::SeqCst) {
                    emit("connection-lost", json!({"detail": "the MQTT connection was closed"}));
                    std::process::exit(1);
                }
            }
            Event::State { host_id, payload } => {
                let _ = self
                    .taps
                    .state
                    .send((host_id.clone(), matches!(payload, StatePayload::Online { .. })));
            }
            Event::InvalidPublish { reason, .. } => diag(format!("invalid publish: {reason}")),
            Event::Node(m) => self.forward("N", &m.group_id, &m.node_id, None, &m.message),
            Event::Device(m) => self.forward("D", &m.group_id, &m.node_id, Some(&m.device_id), &m.message),
        }
        event
    }

    fn set_last_will(&mut self, will: LastWill) {
        self.inner.set_last_will(will)
    }
}

impl TapEventLoop {
    fn forward(&self, prefix: &str, group: &str, node: &str, device: Option<&str>, m: &srad::client::Message) {
        let Some(tx) = &self.taps.received else { return };
        let verb = match m.kind {
            MessageKind::Birth => "BIRTH",
            MessageKind::Death => "DEATH",
            MessageKind::Data => "DATA",
            _ => return,
        };
        let _ = tx.send(Received {
            kind: format!("{prefix}{verb}"),
            group: group.into(),
            node: node.into(),
            device: device.map(str::to_string),
            payload: m.payload.clone(),
        });
    }
}
