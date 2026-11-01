// SPDX-License-Identifier: MIT
package org.otfabric.sparkplugbinterop.tahu;

import java.math.BigInteger;
import java.util.Date;
import java.util.Iterator;
import java.util.List;
import java.util.Map;

import org.eclipse.tahu.message.model.DataSet;
import org.eclipse.tahu.message.model.DataSet.DataSetBuilder;
import org.eclipse.tahu.message.model.DataSetDataType;
import org.eclipse.tahu.message.model.Metric;
import org.eclipse.tahu.message.model.Metric.MetricBuilder;
import org.eclipse.tahu.message.model.MetricDataType;
import org.eclipse.tahu.message.model.Parameter;
import org.eclipse.tahu.message.model.ParameterDataType;
import org.eclipse.tahu.message.model.PropertyDataType;
import org.eclipse.tahu.message.model.PropertySet;
import org.eclipse.tahu.message.model.PropertySet.PropertySetBuilder;
import org.eclipse.tahu.message.model.PropertyValue;
import org.eclipse.tahu.message.model.Row;
import org.eclipse.tahu.message.model.Row.RowBuilder;
import org.eclipse.tahu.message.model.Template;
import org.eclipse.tahu.message.model.Template.TemplateBuilder;
import org.eclipse.tahu.message.model.Value;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ArrayNode;
import com.fasterxml.jackson.databind.node.JsonNodeFactory;
import com.fasterxml.jackson.databind.node.ObjectNode;

/** Conversions between contract values (JSON) and Tahu metric values. */
final class Values {

	static final List<String> DATATYPES = List.of("Int8", "Int16", "Int32", "Int64", "UInt8", "UInt16", "UInt32",
			"UInt64", "Float", "Double", "Boolean", "String", "DateTime", "Text", "DataSet", "Template");

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
		if (dt.equals("DataSet")) {
			if (!v.path("columns").isArray() || !v.path("types").isArray() || !v.path("rows").isArray()) {
				throw new Invalid(where + ": not a DataSet");
			}
			for (JsonNode row : v.get("rows")) {
				if (!row.isArray() || row.size() != v.get("types").size()) {
					throw new Invalid(where + ": a row has not one cell per column");
				}
				for (int i = 0; i < row.size(); i++) {
					check(where, v.get("types").get(i).asText(), row.get(i));
				}
			}
		} else if (dt.equals("Template")) {
			if (!v.path("isDefinition").isBoolean() || !v.path("parameters").isArray() || !v.path("metrics").isArray()) {
				throw new Invalid(where + ": not a Template");
			}
			for (JsonNode p : v.get("parameters")) {
				check(where, p.path("type").asText(), p.get("value"));
			}
			for (JsonNode m : v.get("metrics")) {
				check(where, m.path("datatype").asText(), m.get("value"));
			}
		} else if (range != null) {
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

	/** Checks the properties of a fixture metric, which may have none. */
	static void checkProperties(String where, JsonNode properties) throws Invalid {
		if (properties == null) {
			return;
		}
		Iterator<Map.Entry<String, JsonNode>> it = properties.fields();
		while (it.hasNext()) {
			Map.Entry<String, JsonNode> e = it.next();
			check(where + ", property " + e.getKey(), e.getValue().path("type").asText(), e.getValue().get("value"));
		}
	}

	private static DataSet dataSet(JsonNode v) throws Exception {
		DataSetBuilder b = new DataSetBuilder(v.get("columns").size());
		for (JsonNode c : v.get("columns")) {
			b.addColumnName(c.asText());
		}
		for (JsonNode t : v.get("types")) {
			b.addType(new DataSetDataType(t.asText()));
		}
		for (JsonNode row : v.get("rows")) {
			RowBuilder r = new RowBuilder();
			for (int i = 0; i < row.size(); i++) {
				String t = v.get("types").get(i).asText();
				r.addValue(new Value<Object>(new DataSetDataType(t), toJava(t, row.get(i))));
			}
			b.addRow(r.createRow());
		}
		return b.createDataSet();
	}

	private static Template template(JsonNode v) throws Exception {
		TemplateBuilder b = new TemplateBuilder().definition(v.get("isDefinition").asBoolean());
		if (v.hasNonNull("version")) {
			b.version(v.get("version").asText());
		}
		if (v.hasNonNull("templateRef")) {
			b.templateRef(v.get("templateRef").asText());
		}
		for (JsonNode p : v.get("parameters")) {
			String t = p.get("type").asText();
			b.addParameter(new Parameter(p.get("name").asText(), new ParameterDataType(t), toJava(t, p.get("value"))));
		}
		for (JsonNode m : v.get("metrics")) {
			b.addMetric(metric(m.get("name").asText(), m.get("datatype").asText(), m.get("value")));
		}
		return b.createTemplate();
	}

	private static PropertySet properties(JsonNode properties) throws Exception {
		PropertySetBuilder b = new PropertySetBuilder();
		Iterator<Map.Entry<String, JsonNode>> it = properties.fields();
		while (it.hasNext()) {
			Map.Entry<String, JsonNode> e = it.next();
			String t = e.getValue().get("type").asText();
			b.addProperty(e.getKey(), new PropertyValue<PropertyDataType>(new PropertyDataType(t), toJava(t, e.getValue().get("value"))));
		}
		return b.createPropertySet();
	}

	/** The Java value Tahu expects for the datatype. */
	static Object toJava(String dt, JsonNode v) throws Exception {
		if (v == null || v.isNull()) {
			return null;
		}
		switch (dt) {
			case "DataSet":
				return dataSet(v);
			case "Template":
				return template(v);
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
		if (v instanceof DataSet d) {
			return json(d);
		}
		if (v instanceof Template t) {
			return json(t);
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

	private static ObjectNode json(DataSet d) {
		ObjectNode o = NODES.objectNode();
		ArrayNode columns = o.putArray("columns");
		d.getColumnNames().forEach(columns::add);
		ArrayNode types = o.putArray("types");
		d.getTypes().forEach(t -> types.add(t.getType()));
		ArrayNode rows = o.putArray("rows");
		for (Row row : d.getRows()) {
			ArrayNode r = rows.addArray();
			row.getValues().forEach(v -> r.add(toJson(v.getValue())));
		}
		return o;
	}

	private static ObjectNode json(Template t) {
		ObjectNode o = NODES.objectNode();
		if (t.getVersion() != null && !t.getVersion().isEmpty()) {
			o.put("version", t.getVersion());
		}
		if (t.getTemplateRef() != null && !t.getTemplateRef().isEmpty()) {
			o.put("templateRef", t.getTemplateRef());
		}
		o.put("isDefinition", t.isDefinition());
		ArrayNode params = o.putArray("parameters");
		for (Parameter p : t.getParameters()) {
			params.addObject().put("name", p.getName()).put("type", p.getType().getType()).set("value", toJson(p.getValue()));
		}
		ArrayNode metrics = o.putArray("metrics");
		for (Metric m : t.getMetrics()) {
			metrics.add(doc(m.getName(), m));
		}
		return o;
	}

	static Metric metric(String name, String dt, JsonNode value) throws Exception {
		return metric(name, dt, value, null);
	}

	/** A metric, with the properties of a fixture metric if it has any. */
	static Metric metric(String name, String dt, JsonNode value, JsonNode properties) throws Exception {
		Metric m = new MetricBuilder(name, type(dt), toJava(dt, value)).timestamp(new Date()).createMetric();
		if (properties != null) {
			m.setProperties(properties(properties));
		}
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
		if (m.getProperties() != null && !m.getProperties().isEmpty()) {
			ObjectNode props = o.putObject("properties");
			m.getProperties().getPropertyMap().forEach((k, p) -> {
				ObjectNode e = props.putObject(k);
				e.put("type", p.getType().getType());
				e.set("value", toJson(p.getValue()));
			});
		}
		return o;
	}
}
