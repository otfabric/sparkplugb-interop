// SPDX-License-Identifier: MIT
package org.otfabric.sparkplugbinterop.tahu;

import java.util.ArrayList;
import java.util.Date;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;

import org.eclipse.paho.client.mqttv3.MqttMessage;
import org.eclipse.tahu.edge.EdgeClient;
import org.eclipse.tahu.edge.api.MetricHandler;
import org.eclipse.tahu.message.DefaultBdSeqManager;
import org.eclipse.tahu.message.SparkplugBPayloadDecoder;
import org.eclipse.tahu.message.SparkplugBPayloadEncoder;
import org.eclipse.tahu.message.model.EdgeNodeDescriptor;
import org.eclipse.tahu.message.model.MessageType;
import org.eclipse.tahu.message.model.Metric;
import org.eclipse.tahu.message.model.Metric.MetricBuilder;
import org.eclipse.tahu.message.model.MetricDataType;
import org.eclipse.tahu.message.model.SparkplugBPayload;
import org.eclipse.tahu.message.model.SparkplugBPayload.SparkplugBPayloadBuilder;
import org.eclipse.tahu.message.model.SparkplugBPayloadMap;
import org.eclipse.tahu.message.model.SparkplugBPayloadMap.SparkplugBPayloadMapBuilder;
import org.eclipse.tahu.message.model.SparkplugDescriptor;
import org.eclipse.tahu.message.model.SparkplugMeta;
import org.eclipse.tahu.message.model.StatePayload;
import org.eclipse.tahu.message.model.Topic;
import org.eclipse.tahu.model.MetricDataTypeMap;
import org.eclipse.tahu.model.MqttServerDefinition;
import org.eclipse.tahu.mqtt.ClientCallback;
import org.eclipse.tahu.mqtt.MqttClientId;
import org.eclipse.tahu.mqtt.MqttServerName;
import org.eclipse.tahu.mqtt.MqttServerUrl;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ArrayNode;
import com.fasterxml.jackson.databind.node.ObjectNode;

/**
 * The edge role: Tahu's EdgeClient runs the session (connect, will, NCMD/DCMD subscriptions, primary host STATE,
 * seq numbers and aliases); this class is its MetricHandler and ClientCallback and holds the fixture's values.
 *
 * Locking: every entry point of this class takes {@code lock} before calling into the EdgeClient (which then takes
 * its own lock). The one path in the other order, the EdgeClient thread publishing the first births, runs before
 * any call or command is accepted.
 */
final class Edge implements MetricHandler, ClientCallback {

	static final String REBIRTH = SparkplugMeta.METRIC_NODE_REBIRTH;

	private final Fixture fx;
	private final String group;
	private final String nodeId;
	private final Flags.Broker broker;
	private final String clientId;
	private final String primaryHost;
	private final int connectTimeoutMs;
	private final String readyFile;
	private final String socketPath;
	private final EdgeNodeDescriptor desc;

	private final Object lock = new Object();
	private final Object bdLock = new Object();
	/** Scope (Fixture.NODE or a device ID) to metric name to current value. */
	private final Map<String, Map<String, JsonNode>> values = new HashMap<>();
	private final Set<String> online = new LinkedHashSet<>();
	private final Map<String, MetricDataTypeMap> commandTypes = new HashMap<>();
	/**
	 * The aliases EdgeClient gave the metrics of the current births (it numbers them itself, unique per edge node),
	 * read back from the birth payloads: alias to metric name. Hosts may command a metric by alias only.
	 */
	private final Map<Long, String> aliasNames = new ConcurrentHashMap<>();
	private final CountDownLatch connected = new CountDownLatch(1);
	private final CountDownLatch born = new CountDownLatch(1);

	private DefaultBdSeqManager bdSeqManager;
	private long birthBdSeq;
	private long deathBdSeq;
	private EdgeClient client;
	private volatile boolean stopping;
	private long lastSeq = -1;
	private List<String> births;
	private Boolean primaryOnline;

	Edge(Flags f) throws Fixture.Invalid {
		fx = Fixture.load(f.str("fixture", "baseline"));
		group = f.str("group", fx.group);
		nodeId = f.str("edge-node", fx.edgeNode);
		broker = f.broker();
		clientId = f.str("client-id", "sparkplugb-interop-tahu-edge-" + group + "-" + nodeId);
		primaryHost = f.str("primary-host", null);
		connectTimeoutMs = f.integer("connect-timeout-ms", 10000);
		readyFile = f.str("ready-file", Main.DEFAULT_READY);
		socketPath = f.str("control-socket", Main.DEFAULT_SOCKET);
		desc = new EdgeNodeDescriptor(group, nodeId);

		Map<String, JsonNode> node = new LinkedHashMap<>();
		MetricDataTypeMap nodeTypes = new MetricDataTypeMap();
		nodeTypes.addMetricDataType(REBIRTH, MetricDataType.Boolean);
		for (Fixture.MetricDef d : fx.nodeMetrics) {
			node.put(d.name(), d.value());
			nodeTypes.addMetricDataType(d.name(), Values.type(d.datatype()));
		}
		values.put(Fixture.NODE, node);
		commandTypes.put(Fixture.NODE, nodeTypes);
		for (Map.Entry<String, List<Fixture.MetricDef>> e : fx.devices.entrySet()) {
			Map<String, JsonNode> dev = new LinkedHashMap<>();
			MetricDataTypeMap devTypes = new MetricDataTypeMap();
			for (Fixture.MetricDef d : e.getValue()) {
				dev.put(d.name(), d.value());
				devTypes.addMetricDataType(d.name(), Values.type(d.datatype()));
			}
			values.put(e.getKey(), dev);
			commandTypes.put(e.getKey(), devTypes);
			online.add(e.getKey());
		}
	}

	void run() throws Exception {
		bdSeqManager = new DefaultBdSeqManager("sparkplugb-interop-" + group + "-" + nodeId);
		deathBdSeq = bdSeqManager.getNextDeathBdSeqNum();
		birthBdSeq = deathBdSeq;

		MqttServerDefinition server = new MqttServerDefinition(new MqttServerName("broker"),
				new MqttClientId(clientId, false), new MqttServerUrl(broker.url()), null, null, 30, getDeathTopic());
		client = new EdgeClient(this, desc, new ArrayList<>(fx.devices.keySet()), primaryHost, true, 0L,
				List.of(server), this, null);
		Main.onTerm(this::stop);
		Thread t = new Thread(client, "tahu-edge-client");
		t.setDaemon(true);
		t.start();

		if (!connected.await(connectTimeoutMs, TimeUnit.MILLISECONDS)) {
			Out.diag("no connection to " + broker.url() + " within the connect timeout");
			Out.halt(Main.EXIT_CONNECT);
		}
		born.await();

		Control.serve(socketPath, Map.of("publish-data", this::publishData, "rebirth", this::rebirth, "device-death",
				this::deviceDeath, "device-birth", this::deviceBirth));
		Main.markReady(readyFile);
		ObjectNode ready = Out.object();
		ready.put("adapter", Main.ADAPTER);
		ready.put("fixture", fx.name);
		ready.put("group", group);
		ready.put("edgeNode", nodeId);
		synchronized (lock) {
			ready.put("bdSeq", birthBdSeq);
		}
		Out.emit("ready", ready);
		Main.park();
	}

	private void stop() {
		stopping = true;
		if (client != null) {
			synchronized (lock) {
				boolean wasConnected = client.isConnected();
				client.shutdown();
				if (wasConnected) {
					// EdgeClient.disconnect(true) publishes a DDEATH for every device it knows, then the will.
					publishedDeviceDeaths();
					published("NDEATH", null, null, birthBdSeq);
				}
			}
		}
		Out.emit("stopped");
		Out.halt(0);
	}

	// ---- events ----

	private void published(String type, String device, Long seq, Long bdSeq) {
		if (seq != null) {
			lastSeq = seq;
		}
		ObjectNode f = Out.object();
		f.put("type", type);
		f.put("device", device);
		if (seq == null) {
			f.putNull("seq");
		} else {
			f.put("seq", seq);
		}
		if (bdSeq == null) {
			f.putNull("bdSeq");
		} else {
			f.put("bdSeq", bdSeq);
		}
		Out.emit("published", f);
	}

	/** The seq EdgeClient gives the next message it builds itself (a DDEATH). */
	private long nextSeq() {
		return (lastSeq + 1) % 256;
	}

	/** The DDEATHs of EdgeClient.disconnect(true), in the order of its device map. */
	private void publishedDeviceDeaths() {
		Map<String, Boolean> order = new ConcurrentHashMap<>();
		for (String d : fx.devices.keySet()) {
			order.put(d, Boolean.TRUE);
		}
		for (String d : order.keySet()) {
			published("DDEATH", d, nextSeq(), null);
		}
	}

	// ---- MetricHandler ----

	@Override
	public Topic getDeathTopic() {
		return new Topic(SparkplugMeta.SPARKPLUG_B_TOPIC_PREFIX, desc, MessageType.NDEATH);
	}

	@Override
	public byte[] getDeathPayloadBytes() throws Exception {
		if (stopping) {
			// EdgeClient may try one more connect after shutdown(); this stops it.
			throw new IllegalStateException("stopping");
		}
		synchronized (bdLock) {
			if (deathBdSeq == 256) {
				deathBdSeq = 0;
			}
			SparkplugBPayload p = new SparkplugBPayloadBuilder().setTimestamp(new Date())
					.addMetric(new MetricBuilder(SparkplugMeta.SPARKPLUG_BD_SEQUENCE_NUMBER_KEY, MetricDataType.Int64,
							deathBdSeq).createMetric())
					.createPayload();
			birthBdSeq = deathBdSeq;
			deathBdSeq++;
			bdSeqManager.storeNextDeathBdSeqNum(deathBdSeq);
			return new SparkplugBPayloadEncoder().getBytes(p, false);
		}
	}

	@Override
	public void publishBirthSequence() {
		synchronized (lock) {
			try {
				List<String> out = new ArrayList<>();
				long bd;
				synchronized (bdLock) {
					bd = birthBdSeq;
				}
				// Every metric of a birth carries a timestamp (Sparkplug chapter 6).
				Date now = new Date();
				SparkplugBPayloadMap nb = new SparkplugBPayloadMapBuilder().setTimestamp(now).createPayload();
				nb.addMetric(new MetricBuilder(SparkplugMeta.SPARKPLUG_BD_SEQUENCE_NUMBER_KEY, MetricDataType.Int64, bd)
						.timestamp(now).createMetric());
				nb.addMetric(new MetricBuilder(REBIRTH, MetricDataType.Boolean, false).timestamp(now).createMetric());
				for (Fixture.MetricDef d : fx.nodeMetrics) {
					nb.addMetric(Values.metric(d.name(), d.datatype(), values.get(Fixture.NODE).get(d.name()), d.properties()));
				}
				client.publishNodeBirth(nb);
				aliasNames.clear();
				learnAliases(Fixture.NODE, nb.getMetrics());
				published("NBIRTH", null, nb.getSeq(), bd);
				out.add("NBIRTH");
				for (String dev : fx.devices.keySet()) {
					if (online.contains(dev)) {
						deviceBirthMessage(dev);
						out.add("DBIRTH/" + dev);
					}
				}
				births = out;
				born.countDown();
			} catch (Exception e) {
				throw new IllegalStateException("births: " + e, e);
			}
		}
	}

	private void deviceBirthMessage(String dev) throws Exception {
		SparkplugBPayload p = new SparkplugBPayloadBuilder().setTimestamp(new Date()).createPayload();
		for (Fixture.MetricDef d : fx.devices.get(dev)) {
			p.addMetric(Values.metric(d.name(), d.datatype(), values.get(dev).get(d.name()), d.properties()));
		}
		client.publishDeviceBirth(dev, p);
		learnAliases(dev, p.getMetrics());
		published("DBIRTH", dev, p.getSeq(), null);
	}

	private void learnAliases(String scope, List<Metric> metrics) {
		for (Metric m : metrics) {
			if (m.hasAlias() && m.getName() != null) {
				aliasNames.put(m.getAlias(), m.getName());
				commandTypes.get(scope).addMetricDataType(m.getAlias(), m.getDataType());
			}
		}
	}

	@Override
	public boolean hasMetric(SparkplugDescriptor sparkplugDescriptor, String metricName) {
		String scope = sparkplugDescriptor.isDeviceDescriptor()
				? ((org.eclipse.tahu.message.model.DeviceDescriptor) sparkplugDescriptor).getDeviceId()
				: Fixture.NODE;
		return fx.find(scope, metricName) != null || (Fixture.NODE.equals(scope) && REBIRTH.equals(metricName));
	}

	// ---- ClientCallback ----

	@Override
	public void shutdown() {
	}

	@Override
	public void connectComplete(boolean reconnect, MqttServerName mqttServerName, MqttServerUrl mqttServerUrl,
			MqttClientId clientId) {
		connected.countDown();
	}

	@Override
	public void connectionLost(MqttServerName mqttServerName, MqttServerUrl mqttServerUrl, MqttClientId clientId,
			Throwable cause) {
		if (!stopping) {
			ObjectNode f = Out.object();
			f.put("detail", "MQTT connection lost: " + (cause == null ? "unknown" : cause.getMessage()));
			Out.emit("connection-lost", f);
			Out.halt(Main.EXIT_FAILED);
		}
	}

	@Override
	public void messageArrived(MqttServerName mqttServerName, MqttServerUrl mqttServerUrl, MqttClientId clientId,
			String topic, MqttMessage message) {
		if (stopping) {
			return;
		}
		try {
			if (topic.startsWith(SparkplugMeta.SPARKPLUG_TOPIC_HOST_STATE_PREFIX + "/")) {
				onState(topic.substring(SparkplugMeta.SPARKPLUG_TOPIC_HOST_STATE_PREFIX.length() + 1), message);
				return;
			}
			Topic t = Topic.parseTopic(topic);
			if (t.isType(MessageType.NCMD)) {
				onCommand(Fixture.NODE, null, message);
			} else if (t.isType(MessageType.DCMD)) {
				onCommand(t.getDeviceId(), t.getDeviceId(), message);
			}
		} catch (Exception e) {
			Out.diag("message on " + topic + ": " + e);
		}
	}

	private void onState(String hostId, MqttMessage message) throws Exception {
		if (primaryHost == null || !primaryHost.equals(hostId)) {
			return;
		}
		StatePayload state = Out.JSON.readValue(message.getPayload(), StatePayload.class);
		boolean nowOnline = Boolean.TRUE.equals(state.isOnline());
		synchronized (lock) {
			boolean wasBorn = client.isConnectedToPrimaryHost();
			if (primaryOnline == null || primaryOnline != nowOnline) {
				primaryOnline = nowOnline;
				ObjectNode f = Out.object();
				f.put("hostId", hostId);
				f.put("online", nowOnline);
				Out.emit("primary-host", f);
			}
			long bd;
			synchronized (bdLock) {
				bd = birthBdSeq;
			}
			// Births, when the host comes online, are published (and reported) by publishBirthSequence.
			client.handleStateMessage(hostId, state);
			if (!nowOnline && wasBorn) {
				publishedDeviceDeaths();
				published("NDEATH", null, null, bd);
			}
		}
	}

	private void onCommand(String scope, String device, MqttMessage message) throws Exception {
		if (born.getCount() > 0) {
			Out.diag("command before the births, ignored");
			return;
		}
		SparkplugBPayload payload = new SparkplugBPayloadDecoder().buildFromByteArray(message.getPayload(),
				commandTypes.get(scope));
		synchronized (lock) {
			ArrayNode docs = Out.JSON.createArrayNode();
			boolean rebirth = false;
			List<Fixture.Change> writes = new ArrayList<>();
			for (Metric m : payload.getMetrics()) {
				if (m == null) {
					continue;
				}
				if (m.getName() == null && m.hasAlias()) {
					m.setName(aliasNames.get(m.getAlias()));
				}
				if (m.getName() == null) {
					continue;
				}
				ObjectNode doc = Values.doc(m.getName(), m);
				docs.add(doc);
				if (device == null && REBIRTH.equals(m.getName())) {
					rebirth |= Boolean.TRUE.equals(m.getValue());
					continue;
				}
				Fixture.MetricDef def = fx.find(scope, m.getName());
				if (def == null || !def.writable() || !def.datatype().equals(doc.path("datatype").asText())) {
					continue;
				}
				try {
					Values.check(def.name(), def.datatype(), doc.get("value"));
				} catch (Values.Invalid e) {
					continue;
				}
				writes.add(new Fixture.Change(def.name(), doc.get("value")));
			}
			boolean applied = false;
			if (!writes.isEmpty() && (device == null || online.contains(device))) {
				publishValues(scope, writes);
				applied = true;
			}
			if (rebirth) {
				births = null;
				client.handleRebirthRequest(true);
				applied |= births != null;
			}
			ObjectNode f = Out.object();
			f.put("device", device);
			f.set("metrics", docs);
			f.put("applied", applied);
			Out.emit("command", f);
		}
	}

	/** Stores the changes and publishes them as one NDATA or DDATA. Called with the lock held. */
	private void publishValues(String scope, List<Fixture.Change> changes) throws Exception {
		SparkplugBPayload p = new SparkplugBPayloadBuilder().setTimestamp(new Date()).createPayload();
		for (Fixture.Change c : changes) {
			values.get(scope).put(c.name(), c.value());
			p.addMetric(Values.metric(c.name(), fx.find(scope, c.name()).datatype(), c.value()));
		}
		if (Fixture.NODE.equals(scope)) {
			client.publishNodeData(p);
			published("NDATA", null, p.getSeq(), null);
		} else {
			client.publishDeviceData(scope, p);
			published("DDATA", scope, p.getSeq(), null);
		}
	}

	// ---- calls ----

	private void requireBorn() throws Control.CallError {
		if (!client.isConnected() || !client.isConnectedToPrimaryHost()) {
			throw new Control.CallError("not-connected", "the edge node is not online");
		}
	}

	private static JsonNode publishedResult(List<String> published) {
		ObjectNode r = Out.object();
		ArrayNode a = r.putArray("published");
		published.forEach(a::add);
		return r;
	}

	private JsonNode publishData(JsonNode p) throws Exception {
		synchronized (lock) {
			JsonNode u = p.get("update");
			if (u == null || !u.isIntegralNumber() || u.asLong() < 0 || u.asLong() >= fx.updates.size()) {
				throw new Control.CallError("invalid-request", "no update " + u);
			}
			requireBorn();
			Fixture.Update update = fx.updates.get(u.asInt());
			List<String> out = new ArrayList<>();
			if (!update.node().isEmpty()) {
				publishValues(Fixture.NODE, update.node());
				out.add("NDATA");
			}
			for (String dev : fx.devices.keySet()) {
				List<Fixture.Change> cs = update.devices().get(dev);
				if (cs == null || cs.isEmpty()) {
					continue;
				}
				if (!online.contains(dev)) {
					throw new Control.CallError("invalid-request", "device " + dev + " is offline");
				}
				publishValues(dev, cs);
				out.add("DDATA/" + dev);
			}
			return publishedResult(out);
		}
	}

	private JsonNode rebirth(JsonNode p) throws Exception {
		synchronized (lock) {
			requireBorn();
			births = null;
			client.handleRebirthRequest(true);
			if (births == null) {
				throw new Control.CallError("invalid-request", "Tahu ignored the rebirth (debounce)");
			}
			return publishedResult(births);
		}
	}

	private JsonNode deviceDeath(JsonNode p) throws Exception {
		synchronized (lock) {
			String dev = p.path("device").isTextual() ? p.get("device").asText() : null;
			if (dev == null || !online.contains(dev)) {
				throw new Control.CallError("invalid-request", "no online device " + p.get("device"));
			}
			requireBorn();
			client.publishDeviceDeath(dev);
			online.remove(dev);
			published("DDEATH", dev, nextSeq(), null);
			return publishedResult(List.of("DDEATH/" + dev));
		}
	}

	private JsonNode deviceBirth(JsonNode p) throws Exception {
		synchronized (lock) {
			String dev = p.path("device").isTextual() ? p.get("device").asText() : null;
			if (dev == null || !fx.devices.containsKey(dev) || online.contains(dev)) {
				throw new Control.CallError("invalid-request", "no offline device " + p.get("device"));
			}
			requireBorn();
			online.add(dev);
			deviceBirthMessage(dev);
			return publishedResult(List.of("DBIRTH/" + dev));
		}
	}
}
