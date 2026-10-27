// SPDX-License-Identifier: MIT
package org.otfabric.sparkplugbinterop.tahu;

import java.math.BigInteger;
import java.util.Date;
import java.util.List;
import java.util.Map;

import org.eclipse.tahu.message.model.Metric;
import org.eclipse.tahu.message.model.Metric.MetricBuilder;
import org.eclipse.tahu.message.model.MetricDataType;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.JsonNodeFactory;
import com.fasterxml.jackson.databind.node.ObjectNode;

/** Conversions between contract values (JSON) and Tahu metric values. */
final class Values {

	static final List<String> DATATYPES = List.of("Int8", "Int16", "Int32", "Int64", "UInt8", "UInt16", "UInt32",
			"UInt64", "Float", "Double", "Boolean", "String", "DateTime", "Text");

	static final long MAX_SAFE = (1L << 53) - 1;

	private static final Map<String, long[]> RANGES = Map.of("Int8", new long[] {-128, 127}, "Int16",
			new long[] {-32768, 32767}, "Int32", new long[] {Integer.MIN_VALUE, Integer.MAX_VALUE}, "Int64",
			new long[] {-MAX_SAFE, MAX_SAFE}, "UInt8", new long[] {0, 255}, "UInt16", new long[] {0, 65535},
			"UInt32", new long[] {0, 4294967295L}, "UInt64", new long[] {0, MAX_SAFE}, "DateTime",
			new long[] {0, MAX_SAFE});

	private static final JsonNodeFactory NODES = JsonNodeFactory.instance;

	static final class Invalid extends Exception {
		private static final long serialVersionUID = 1L;

		Invalid(String msg) {
			super(msg);
		}
	}

	private Values() {
	}

	static MetricDataType type(String datatype) {
		return new MetricDataType(datatype);
	}

	/** Checks that v is a value of the datatype, as the fixture rules say. */
	static void check(String where, String dt, JsonNode v) throws Invalid {
		if (v == null) {
			throw new Invalid(where + ": no value");
		}
		long[] range = RANGES.get(dt);
		if (range != null) {
			if (!v.isIntegralNumber() || !v.canConvertToLong() || v.asLong() < range[0] || v.asLong() > range[1]) {
				throw new Invalid(where + ": " + v + " is not a " + dt);
			}
		} else if (dt.equals("Float") || dt.equals("Double")) {
			if (!v.isNumber()) {
				throw new Invalid(where + ": " + v + " is not a number");
			}
			double d = v.asDouble();
			if (dt.equals("Float") && (double) (float) d != d) {
				throw new Invalid(where + ": " + v + " is not exactly a Float");
			}
		} else if (dt.equals("Boolean")) {
			if (!v.isBoolean()) {
				throw new Invalid(where + ": " + v + " is not a boolean");
			}
		} else if (dt.equals("String") || dt.equals("Text")) {
			if (!v.isTextual()) {
				throw new Invalid(where + ": " + v + " is not a string");
			}
		} else {
			throw new Invalid(where + ": unknown datatype '" + dt + "'");
		}
	}

	/** The Java value Tahu expects for the datatype. */
	static Object toJava(String dt, JsonNode v) {
		if (v == null || v.isNull()) {
			return null;
		}
		switch (dt) {
			case "Int8":
				return (byte) v.asLong();
			case "Int16":
			case "UInt8":
				return (short) v.asLong();
			case "Int32":
			case "UInt16":
				return (int) v.asLong();
			case "Int64":
			case "UInt32":
				return v.asLong();
			case "UInt64":
				return BigInteger.valueOf(v.asLong());
			case "Float":
				return (float) v.asDouble();
			case "Double":
				return v.asDouble();
			case "Boolean":
				return v.asBoolean();
			case "DateTime":
				return new Date(v.asLong());
			default:
				return v.asText();
		}
	}

	/** The contract value of a decoded Tahu value. */
	static JsonNode toJson(Object v) {
		if (v == null) {
			return NODES.nullNode();
		}
		if (v instanceof Boolean b) {
			return NODES.booleanNode(b);
		}
		if (v instanceof Date d) {
			return NODES.numberNode(d.getTime());
		}
		if (v instanceof BigInteger b) {
			return b.bitLength() < 64 ? NODES.numberNode(b.longValue()) : NODES.numberNode(b);
		}
		if (v instanceof Float || v instanceof Double) {
			double d = ((Number) v).doubleValue();
			if (d == Math.rint(d) && Math.abs(d) <= MAX_SAFE) {
				return NODES.numberNode((long) d);
			}
			return NODES.numberNode(d);
		}
		if (v instanceof Number n) {
			return NODES.numberNode(n.longValue());
		}
		return NODES.textNode(v.toString());
	}

	static Metric metric(String name, String dt, JsonNode value) throws Exception {
		Metric m = new MetricBuilder(name, type(dt), toJava(dt, value)).timestamp(new Date()).createMetric();
		return m;
	}

	/**
	 * {"name", "datatype", "value"} of a metric; a name that is not known (an
	 * alias without a birth) is null, with the "alias".
	 */
	static ObjectNode doc(String name, Metric m) {
		ObjectNode o = NODES.objectNode();
		o.put("name", name);
		if (name == null && m.hasAlias()) {
			o.put("alias", m.getAlias());
		}
		o.put("datatype", m.getDataType() == null ? null : m.getDataType().getType());
		o.set("value", Boolean.TRUE.equals(m.isNull()) ? NODES.nullNode() : toJson(m.getValue()));
		return o;
	}
}
