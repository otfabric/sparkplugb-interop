// SPDX-License-Identifier: MIT
package org.otfabric.sparkplugbinterop.tahu;

import java.util.HashMap;
import java.util.Map;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

import com.fasterxml.jackson.databind.JsonNode;

/** Strict "--name value" flags; every misuse is a usage error (exit 2). */
final class Flags {

	enum Kind {
		STR, INT, URL, ID, JSON
	}

	static final Pattern ID = Pattern.compile("^[^/+#]+$");
	private static final Pattern URL = Pattern.compile("^tcp://([^:/?#\\[\\]@]+)(?::([0-9]*))?/?$");

	static final class Usage extends Exception {
		private static final long serialVersionUID = 1L;

		Usage(String msg) {
			super(msg);
		}
	}

	record Broker(String host, int port) {
		String url() {
			return "tcp://" + host + ":" + port;
		}
	}

	private final Map<String, Object> values = new HashMap<>();

	private Flags() {
	}

	static Flags parse(String[] args, int from, Map<String, Kind> spec, String... required) throws Usage {
		Flags f = new Flags();
		for (int i = from; i < args.length; i += 2) {
			String a = args[i];
			if (!a.startsWith("--") || !spec.containsKey(a.substring(2))) {
				throw new Usage("unknown argument " + a);
			}
			String name = a.substring(2);
			if (f.values.containsKey(name)) {
				throw new Usage("--" + name + " given twice");
			}
			if (i + 1 >= args.length) {
				throw new Usage("--" + name + " needs a value");
			}
			f.values.put(name, convert(name, args[i + 1], spec.get(name)));
		}
		for (String r : required) {
			if (!f.values.containsKey(r)) {
				throw new Usage("--" + r + " is required");
			}
		}
		return f;
	}

	private static Object convert(String name, String v, Kind kind) throws Usage {
		switch (kind) {
			case INT:
				if (!v.matches("[0-9]+") || v.length() > 9 || Integer.parseInt(v) <= 0) {
					throw new Usage("--" + name + ": '" + v + "' is not a positive integer");
				}
				return Integer.parseInt(v);
			case URL: {
				Matcher m = URL.matcher(v);
				if (!m.matches()) {
					throw new Usage("--" + name + ": '" + v + "' is not tcp://host[:port]");
				}
				int port = 1883;
				if (m.group(2) != null && !m.group(2).isEmpty()) {
					if (m.group(2).length() > 5 || Integer.parseInt(m.group(2)) > 65535) {
						throw new Usage("--" + name + ": port out of range");
					}
					port = Integer.parseInt(m.group(2));
				}
				return new Broker(m.group(1), port);
			}
			case ID:
				if (!ID.matcher(v).matches()) {
					throw new Usage("--" + name + ": '" + v + "' is not a valid Sparkplug ID");
				}
				return v;
			case JSON: {
				JsonNode node;
				try {
					node = Out.JSON.readTree(v);
				} catch (Exception e) {
					throw new Usage("--" + name + ": " + e.getMessage());
				}
				if (node == null || !node.isObject()) {
					throw new Usage("--" + name + " must be a JSON object");
				}
				return node;
			}
			default:
				return v;
		}
	}

	boolean has(String name) {
		return values.containsKey(name);
	}

	String str(String name, String dflt) {
		return values.containsKey(name) ? (String) values.get(name) : dflt;
	}

	int integer(String name, int dflt) {
		return values.containsKey(name) ? (Integer) values.get(name) : dflt;
	}

	Broker broker() {
		return (Broker) values.get("broker");
	}

	JsonNode json(String name) {
		return (JsonNode) values.get(name);
	}
}
