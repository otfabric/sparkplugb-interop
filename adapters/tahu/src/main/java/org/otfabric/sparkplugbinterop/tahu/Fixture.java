// SPDX-License-Identifier: MIT
package org.otfabric.sparkplugbinterop.tahu;

import java.io.File;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.Iterator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;

import com.fasterxml.jackson.databind.JsonNode;

/** A fixture of docs/FIXTURES.md, validated as the contract requires (exit 4 when invalid). */
final class Fixture {

	static final String DIR = System.getenv().getOrDefault("SPARKPLUGB_INTEROP_FIXTURES", "/fixtures");
	static final String NODE = "";

	record MetricDef(String name, long alias, String datatype, JsonNode value, boolean writable) {
	}

	record Change(String name, JsonNode value) {
	}

	record Update(List<Change> node, Map<String, List<Change>> devices) {
	}

	static final class Invalid extends Exception {
		private static final long serialVersionUID = 1L;

		Invalid(String msg) {
			super(msg);
		}
	}

	final String name;
	final String group;
	final String edgeNode;
	final List<MetricDef> nodeMetrics = new ArrayList<>();
	/** Device ID to its metrics, in fixture order. */
	final Map<String, List<MetricDef>> devices = new LinkedHashMap<>();
	final List<Update> updates = new ArrayList<>();

	private Fixture(String name, String group, String edgeNode) {
		this.name = name;
		this.group = group;
		this.edgeNode = edgeNode;
	}

	static String path(String nameOrPath) {
		if (nameOrPath.contains("/") || nameOrPath.endsWith(".json")) {
			return nameOrPath;
		}
		return DIR + "/" + nameOrPath + "/fixture.json";
	}

	/** The metric definitions of a scope: NODE or a device ID. */
	List<MetricDef> scope(String scope) {
		return NODE.equals(scope) ? nodeMetrics : devices.get(scope);
	}

	MetricDef find(String scope, String metric) {
		List<MetricDef> defs = scope(scope);
		if (defs != null) {
			for (MetricDef d : defs) {
				if (d.name().equals(metric)) {
					return d;
				}
			}
		}
		return null;
	}

	static Fixture load(String nameOrPath) throws Invalid {
		String path = path(nameOrPath);
		JsonNode fx;
		try {
			fx = Out.JSON.readTree(new File(path));
		} catch (Exception e) {
			throw new Invalid(path + ": " + e.getMessage());
		}
		if (fx == null || !fx.isObject()) {
			throw new Invalid(path + ": not a JSON object");
		}
		try {
			return validate(fx);
		} catch (Invalid e) {
			throw new Invalid(path + ": " + e.getMessage());
		}
	}

	private static JsonNode need(JsonNode o, String key, String what) throws Invalid {
		JsonNode v = o == null ? null : o.get(key);
		if (v == null || v.isMissingNode()) {
			throw new Invalid("malformed fixture (no " + what + ")");
		}
		return v;
	}

	private static String text(JsonNode o, String key) throws Invalid {
		JsonNode v = need(o, key, key);
		if (!v.isTextual()) {
			throw new Invalid("malformed fixture (" + key + " is not a string)");
		}
		return v.asText();
	}

	private static JsonNode array(JsonNode o, String key) throws Invalid {
		JsonNode v = need(o, key, key);
		if (!v.isArray()) {
			throw new Invalid("malformed fixture (" + key + " is not an array)");
		}
		return v;
	}

	private static Fixture validate(JsonNode fx) throws Invalid {
		if (!"1.0".equals(fx.path("schemaVersion").asText(null)) || !fx.path("schemaVersion").isTextual()) {
			throw new Invalid("schemaVersion is not 1.0");
		}
		for (String k : new String[] {"group", "edgeNode"}) {
			JsonNode v = fx.get(k);
			if (v == null || !v.isTextual() || !Flags.ID.matcher(v.asText()).matches()) {
				throw new Invalid(k + " is not a valid Sparkplug ID");
			}
		}
		Fixture f = new Fixture(fx.path("name").asText(""), fx.get("group").asText(), fx.get("edgeNode").asText());
		Set<Long> aliases = new HashSet<>();
		Map<String, String> types = new HashMap<>();
		f.nodeMetrics.addAll(metrics("node", array(need(fx, "node", "node"), "metrics"), aliases, types, NODE));
		for (JsonNode d : array(fx, "devices")) {
			String id = text(d, "id");
			if (f.devices.containsKey(id)) {
				throw new Invalid("a device appears twice");
			}
			f.devices.put(id, null);
			f.devices.put(id, metrics("device " + id, array(d, "metrics"), aliases, types, id));
		}
		int i = 0;
		for (JsonNode u : array(fx, "updates")) {
			List<Change> node = changes(i, "node", array(u, "node"), types, NODE);
			Map<String, List<Change>> devs = new LinkedHashMap<>();
			JsonNode du = need(u, "devices", "devices");
			if (!du.isObject()) {
				throw new Invalid("malformed fixture (updates[" + i + "].devices)");
			}
			Iterator<Map.Entry<String, JsonNode>> it = du.fields();
			int count = node.size();
			while (it.hasNext()) {
				Map.Entry<String, JsonNode> e = it.next();
				if (!f.devices.containsKey(e.getKey())) {
					throw new Invalid("updates[" + i + "]: unknown device '" + e.getKey() + "'");
				}
				if (!e.getValue().isArray()) {
					throw new Invalid("malformed fixture (updates[" + i + "].devices." + e.getKey() + ")");
				}
				List<Change> cs = changes(i, "device " + e.getKey(), e.getValue(), types, e.getKey());
				devs.put(e.getKey(), cs);
				count += cs.size();
			}
			if (count == 0) {
				throw new Invalid("updates[" + i + "] changes nothing");
			}
			f.updates.add(new Update(node, devs));
			i++;
		}
		return f;
	}

	private static List<MetricDef> metrics(String scope, JsonNode list, Set<Long> aliases, Map<String, String> types,
			String key) throws Invalid {
		if (list.isEmpty()) {
			throw new Invalid(scope + " has no metrics");
		}
		List<MetricDef> out = new ArrayList<>();
		Set<String> names = new HashSet<>();
		for (JsonNode m : list) {
			String name = text(m, "name");
			String where = scope + ", metric '" + name + "'";
			if (!names.add(name) || name.startsWith("Node Control/") || name.equals("bdSeq")) {
				throw new Invalid(where + ": duplicate or reserved name");
			}
			JsonNode a = need(m, "alias", "alias");
			if (!a.isIntegralNumber() || !a.canConvertToLong() || a.asLong() < 1 || !aliases.add(a.asLong())) {
				throw new Invalid(where + ": invalid or duplicate alias");
			}
			String dt = text(m, "datatype");
			try {
				Values.check(where, dt, m.get("value"));
			} catch (Values.Invalid e) {
				throw new Invalid(e.getMessage());
			}
			JsonNode w = m.get("writable");
			if (w != null && !w.isBoolean()) {
				throw new Invalid(where + ": writable is not a boolean");
			}
			types.put(key + "\u0000" + name, dt);
			out.add(new MetricDef(name, a.asLong(), dt, m.get("value"), w != null && w.asBoolean()));
		}
		return out;
	}

	private static List<Change> changes(int i, String scope, JsonNode list, Map<String, String> types, String key)
			throws Invalid {
		List<Change> out = new ArrayList<>();
		for (JsonNode c : list) {
			String name = text(c, "name");
			String dt = types.get(key + "\u0000" + name);
			if (dt == null) {
				throw new Invalid("updates[" + i + "]: " + scope + " has no metric '" + name + "'");
			}
			try {
				Values.check("updates[" + i + "], " + scope + ", metric '" + name + "'", dt, c.get("value"));
			} catch (Values.Invalid e) {
				throw new Invalid(e.getMessage());
			}
			out.add(new Change(name, c.get("value")));
		}
		return out;
	}
}
