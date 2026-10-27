// SPDX-License-Identifier: MIT
package org.otfabric.sparkplugbinterop.tahu;

import java.util.ArrayList;
import java.util.Comparator;
import java.util.Date;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;

import org.eclipse.paho.client.mqttv3.MqttMessage;
import org.eclipse.tahu.host.CommandPublisher;
import org.eclipse.tahu.host.HostApplication;
import org.eclipse.tahu.host.TahuHostCallback;
import org.eclipse.tahu.host.api.MultiHostApplicationEventHandler;
import org.eclipse.tahu.host.manager.EdgeNodeManager;
import org.eclipse.tahu.host.manager.SparkplugDevice;
import org.eclipse.tahu.host.manager.SparkplugEdgeNode;
import org.eclipse.tahu.host.model.HostApplicationMetricMap;
import org.eclipse.tahu.host.seq.SequenceReorderManager;
import org.eclipse.tahu.message.SparkplugBPayloadDecoder;
import org.eclipse.tahu.message.model.DeviceDescriptor;
import org.eclipse.tahu.message.model.EdgeNodeDescriptor;
import org.eclipse.tahu.message.model.Message;
import org.eclipse.tahu.message.model.MessageType;
import org.eclipse.tahu.message.model.Metric;
import org.eclipse.tahu.message.model.SparkplugBPayload;
import org.eclipse.tahu.message.model.SparkplugBPayload.SparkplugBPayloadBuilder;
import org.eclipse.tahu.message.model.SparkplugDescriptor;
import org.eclipse.tahu.message.model.SparkplugMeta;
import org.eclipse.tahu.message.model.StatePayload;
import org.eclipse.tahu.message.model.Topic;
import org.eclipse.tahu.mqtt.MqttClientId;
import org.eclipse.tahu.mqtt.MqttOperatorDefs;
import org.eclipse.tahu.mqtt.MqttServerName;
import org.eclipse.tahu.mqtt.MqttServerUrl;
import org.eclipse.tahu.mqtt.TahuClient;
import org.eclipse.tahu.util.SparkplugUtil;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ArrayNode;
import com.fasterxml.jackson.databind.node.ObjectNode;

/**
 * The host role: Tahu's HostApplication (STATE birth and will, subscriptions, the sequence reorder manager, alias
 * and datatype maps, the edge node manager and its rebirth requests). This class is its event handler; it writes
 * what Tahu reports and answers the calls from Tahu's own node tracking.
 */
final class Host implements MultiHostApplicationEventHandler {

	private final String hostId;
	private final Flags.Broker broker;
	private final String clientId;
	private final int connectTimeoutMs;
	private final String readyFile;
	private final String socketPath;
	private final String stateTopic;

	private final CountDownLatch connected = new CountDownLatch(1);
	private final CountDownLatch stateOnline = new CountDownLatch(1);
	/** Every edge node a message was reported from, for "nodes". */
	private final Set<EdgeNodeDescriptor> seen = ConcurrentHashMap.newKeySet();
	/** Edge nodes with an NBIRTH and no NDEATH since, to tell a seq gap from an unknown node. */
	private final Set<EdgeNodeDescriptor> born = ConcurrentHashMap.newKeySet();
	private volatile boolean stopping;
	private HostApplication app;

	Host(Flags f) {
		hostId = f.str("host-id", null);
		broker = f.broker();
		clientId = f.str("client-id", "sparkplugb-interop-tahu-host-" + hostId);
		connectTimeoutMs = f.integer("connect-timeout-ms", 10000);
		readyFile = f.str("ready-file", Main.DEFAULT_READY);
		socketPath = f.str("control-socket", Main.DEFAULT_SOCKET);
		stateTopic = SparkplugMeta.SPARKPLUG_TOPIC_HOST_STATE_PREFIX + "/" + hostId;
	}

	void run() throws Exception {
		SparkplugBPayloadDecoder decoder = new SparkplugBPayloadDecoder();
		// Tahu sends its own rebirth requests through this publisher: they are the rebirth-requested events.
		CommandPublisher publisher = new CommandPublisher() {
			@Override
			public void publishCommand(Topic topic, SparkplugBPayload payload) throws Exception {
				app.publishCommand(topic, payload);
			}

			@Override
			public void publishCommand(MqttServerName server, Topic topic, SparkplugBPayload payload)
					throws Exception {
				rebirthRequested(topic, payload);
				app.publishCommand(server, topic, payload);
			}
		};
		SequenceReorderManager reorder = SequenceReorderManager.getInstance();
		reorder.init(this, publisher, decoder, 5000L);
		TahuHostCallback callback = new TahuHostCallback(this, publisher, reorder, decoder, hostId, true) {
			@Override
			public void messageArrived(MqttServerName server, MqttServerUrl url, MqttClientId id, String topic,
					MqttMessage message) {
				if (stateTopic.equals(topic)) {
					try {
						StatePayload s = Out.JSON.readValue(message.getPayload(), StatePayload.class);
						if (Boolean.TRUE.equals(s.isOnline())) {
							stateOnline.countDown();
						}
					} catch (Exception e) {
						Out.diag("STATE: " + e);
					}
				}
				super.messageArrived(server, url, id, topic, message);
			}

			@Override
			public void connectionLost(MqttServerName server, MqttServerUrl url, MqttClientId id, Throwable cause) {
				if (!stopping) {
					ObjectNode f = Out.object();
					f.put("detail", "MQTT connection lost: " + (cause == null ? "unknown" : cause.getMessage()));
					Out.emit("connection-lost", f);
					Out.halt(Main.EXIT_FAILED);
				}
			}
		};
		MqttServerName name = new MqttServerName("broker");
		TahuClient client = new TahuClient(new MqttClientId(clientId, false), name, new MqttServerUrl(broker.url()),
				null, null, true, 30, callback, null, true, true, stateTopic, null, true, stateTopic, null,
				MqttOperatorDefs.QOS1, true);
		Map<MqttServerName, TahuClient> clients = new HashMap<>();
		clients.put(name, client);
		app = new HostApplication(this, hostId, List.of(SparkplugMeta.SPARKPLUG_B_TOPIC_PREFIX + "/#"), callback,
				clients, null);
		Main.onTerm(this::stop);

		app.start(true);
		if (!connected.await(connectTimeoutMs, TimeUnit.MILLISECONDS)) {
			Out.diag("no connection to " + broker.url() + " within the connect timeout");
			Out.halt(Main.EXIT_CONNECT);
		}
		if (!stateOnline.await(30, TimeUnit.SECONDS)) {
			Out.diag("the online STATE of " + hostId + " did not come back");
			Out.halt(Main.EXIT_FAILED);
		}
		Control.serve(socketPath,
				Map.of("send-command", this::sendCommand, "request-rebirth", this::requestRebirth, "nodes", this::nodes));
		Main.markReady(readyFile);
		ObjectNode ready = Out.object();
		ready.put("adapter", Main.ADAPTER);
		ready.put("hostId", hostId);
		Out.emit("ready", ready);
		Main.park();
	}

	private void stop() {
		stopping = true;
		if (app != null) {
			// Unsubscribes, then publishes the offline STATE (with the timestamp of the will) and disconnects.
			app.shutdown();
		}
		Out.emit("stopped");
		Out.halt(0);
	}

	private void rebirthRequested(Topic topic, SparkplugBPayload payload) {
		if (!topic.isType(MessageType.NCMD)) {
			return;
		}
		boolean rebirth = payload.getMetrics().stream()
				.anyMatch(m -> SparkplugMeta.METRIC_NODE_REBIRTH.equals(m.getName()) && Boolean.TRUE.equals(m.getValue()));
		if (!rebirth) {
			return;
		}
		EdgeNodeDescriptor en = topic.getEdgeNodeDescriptor();
		ObjectNode f = Out.object();
		f.put("group", en.getGroupId());
		f.put("edgeNode", en.getEdgeNodeId());
		f.put("reason", born.contains(en) ? "seq-gap" : "unknown-node");
		Out.emit("rebirth-requested", f);
	}

	// ---- events from Tahu ----

	@Override
	public void onConnect(MqttServerName serverName) {
		connected.countDown();
	}

	@Override
	public void onDisconnect(MqttServerName serverName) {
	}

	@Override
	public void onMessage(MqttServerName serverName, SparkplugDescriptor descriptor, Message message) {
		try {
			Topic t = message.getTopic();
			MessageType type = t.getType();
			SparkplugBPayload p = message.getPayload();
			EdgeNodeDescriptor en = t.getEdgeNodeDescriptor();
			seen.add(en);
			if (type == MessageType.NBIRTH) {
				born.add(en);
			} else if (type == MessageType.NDEATH) {
				born.remove(en);
			}
			boolean bd = type == MessageType.NBIRTH || type == MessageType.NDEATH;
			ObjectNode f = Out.object();
			f.put("type", type.name());
			f.put("group", t.getGroupId());
			f.put("edgeNode", t.getEdgeNodeId());
			f.put("device", t.getDeviceId());
			if (type == MessageType.NDEATH || p.getSeq() == null) {
				f.putNull("seq");
			} else {
				f.put("seq", p.getSeq());
			}
			Long bdSeq = bd ? SparkplugUtil.getBdSequenceNumber(p) : null;
			if (bdSeq == null) {
				f.putNull("bdSeq");
			} else {
				f.put("bdSeq", bdSeq);
			}
			ArrayNode metrics = f.putArray("metrics");
			for (Metric m : p.getMetrics()) {
				if (m == null) {
					continue;
				}
				String name = m.getName();
				if (name == null && m.hasAlias()) {
					// Data messages carry aliases: Tahu's map of the births resolves them.
					name = HostApplicationMetricMap.getInstance().getMetricName(en, t.getSparkplugDescriptor(),
							m.getAlias());
				}
				if (SparkplugMeta.SPARKPLUG_BD_SEQUENCE_NUMBER_KEY.equals(name)) {
					continue;
				}
				metrics.add(Values.doc(name, m));
			}
			Out.emit("message", f);
		} catch (Exception e) {
			Out.diag("message: " + e);
		}
	}

	@Override
	public void onNodeBirthArrived(MqttServerName serverName, EdgeNodeDescriptor edgeNodeDescriptor,
			Message message) {
	}

	@Override
	public void onNodeBirthComplete(MqttServerName serverName, EdgeNodeDescriptor edgeNodeDescriptor) {
	}

	@Override
	public void onNodeDataArrived(MqttServerName serverName, EdgeNodeDescriptor edgeNodeDescriptor,
			Message message) {
	}

	@Override
	public void onNodeDataComplete(MqttServerName serverName, EdgeNodeDescriptor edgeNodeDescriptor) {
	}

	@Override
	public void onNodeDeath(MqttServerName serverName, EdgeNodeDescriptor edgeNodeDescriptor, Message message) {
	}

	@Override
	public void onNodeDeathComplete(MqttServerName serverName, EdgeNodeDescriptor edgeNodeDescriptor) {
	}

	@Override
	public void onDeviceBirthArrived(MqttServerName serverName, DeviceDescriptor deviceDescriptor, Message message) {
	}

	@Override
	public void onDeviceBirthComplete(MqttServerName serverName, DeviceDescriptor deviceDescriptor) {
	}

	@Override
	public void onDeviceDataArrived(MqttServerName serverName, DeviceDescriptor deviceDescriptor, Message message) {
	}

	@Override
	public void onDeviceDataComplete(MqttServerName serverName, DeviceDescriptor deviceDescriptor) {
	}

	@Override
	public void onDeviceDeath(MqttServerName serverName, DeviceDescriptor deviceDescriptor, Message message) {
	}

	@Override
	public void onDeviceDeathComplete(MqttServerName serverName, DeviceDescriptor deviceDescriptor) {
	}

	@Override
	public void onBirthMetric(MqttServerName serverName, SparkplugDescriptor sparkplugDescriptor, Metric metric) {
	}

	@Override
	public void onDataMetric(MqttServerName serverName, SparkplugDescriptor sparkplugDescriptor, Metric metric) {
	}

	@Override
	public void onStale(MqttServerName serverName, SparkplugDescriptor sparkplugDescriptor, Metric metric) {
	}

	// ---- calls ----

	private static String id(JsonNode p, String key, boolean optional) throws Control.CallError {
		JsonNode v = p.get(key);
		if (v == null || v.isNull()) {
			if (optional) {
				return null;
			}
			throw new Control.CallError("invalid-request", key + " is missing or invalid");
		}
		if (!v.isTextual() || !Flags.ID.matcher(v.asText()).matches()) {
			throw new Control.CallError("invalid-request", key + " is missing or invalid");
		}
		return v.asText();
	}

	private void publish(String group, String node, String device, List<Metric> metrics) throws Exception {
		SparkplugBPayload payload = new SparkplugBPayloadBuilder().setTimestamp(new Date()).addMetrics(metrics)
				.createPayload();
		Topic topic = device == null
				? new Topic(SparkplugMeta.SPARKPLUG_B_TOPIC_PREFIX, group, node, MessageType.NCMD)
				: new Topic(SparkplugMeta.SPARKPLUG_B_TOPIC_PREFIX, group, node, device, MessageType.DCMD);
		try {
			app.publishCommand(topic, payload);
		} catch (Exception e) {
			throw new Control.CallError("not-connected", String.valueOf(e.getMessage()));
		}
	}

	private static JsonNode published(String what) {
		ObjectNode r = Out.object();
		r.putArray("published").add(what);
		return r;
	}

	private JsonNode sendCommand(JsonNode p) throws Exception {
		String group = id(p, "group", false);
		String node = id(p, "edgeNode", false);
		String device = id(p, "device", true);
		JsonNode metrics = p.get("metrics");
		if (metrics == null || !metrics.isArray() || metrics.isEmpty()) {
			throw new Control.CallError("invalid-request", "metrics is missing or empty");
		}
		List<Metric> out = new ArrayList<>();
		for (JsonNode m : metrics) {
			if (!m.isObject() || !m.path("name").isTextual() || !Values.DATATYPES.contains(m.path("datatype").asText())
					|| !m.has("value")) {
				throw new Control.CallError("invalid-request", "invalid metric " + m);
			}
			String dt = m.get("datatype").asText();
			try {
				Values.check(m.get("name").asText(), dt, m.get("value"));
			} catch (Values.Invalid e) {
				throw new Control.CallError("invalid-request", e.getMessage());
			}
			out.add(Values.metric(m.get("name").asText(), dt, m.get("value")));
		}
		publish(group, node, device, out);
		return published(device == null ? "NCMD" : "DCMD/" + device);
	}

	private JsonNode requestRebirth(JsonNode p) throws Exception {
		String group = id(p, "group", false);
		String node = id(p, "edgeNode", false);
		publish(group, node, null,
				List.of(Values.metric(SparkplugMeta.METRIC_NODE_REBIRTH, "Boolean", Out.JSON.getNodeFactory().booleanNode(true))));
		return published("NCMD");
	}

	private JsonNode nodes(JsonNode p) {
		List<EdgeNodeDescriptor> list = new ArrayList<>(seen);
		list.sort(Comparator.comparing(EdgeNodeDescriptor::getGroupId).thenComparing(EdgeNodeDescriptor::getEdgeNodeId));
		ObjectNode r = Out.object();
		ArrayNode nodes = r.putArray("nodes");
		for (EdgeNodeDescriptor en : list) {
			SparkplugEdgeNode n = EdgeNodeManager.getInstance().getSparkplugEdgeNode(en);
			if (n == null) {
				continue;
			}
			ObjectNode o = nodes.addObject();
			o.put("group", en.getGroupId());
			o.put("edgeNode", en.getEdgeNodeId());
			o.put("online", n.isOnline());
			if (n.getBirthBdSeqNum() == null) {
				o.putNull("bdSeq");
			} else {
				o.put("bdSeq", n.getBirthBdSeqNum());
			}
			List<SparkplugDevice> devs = new ArrayList<>(n.getSparkplugDevices().values());
			devs.sort(Comparator.comparing(SparkplugDevice::getDeviceId));
			ArrayNode da = o.putArray("devices");
			for (SparkplugDevice d : devs) {
				ObjectNode dn = da.addObject();
				dn.put("device", d.getDeviceId());
				dn.put("online", d.isOnline());
			}
		}
		return r;
	}
}
