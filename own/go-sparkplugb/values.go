// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"time"

	"github.com/otfabric/go-sparkplugb/payload"
	pb "github.com/otfabric/go-sparkplugb/proto"
)

// maxSafe is the largest integer every JSON parser reads exactly.
const maxSafe = 1<<53 - 1

var intRanges = map[string][2]float64{
	"Int8": {-(1 << 7), 1<<7 - 1}, "Int16": {-(1 << 15), 1<<15 - 1}, "Int32": {-(1 << 31), 1<<31 - 1},
	"Int64": {-maxSafe, maxSafe}, "UInt8": {0, 1<<8 - 1}, "UInt16": {0, 1<<16 - 1}, "UInt32": {0, 1<<32 - 1},
	"UInt64": {0, maxSafe}, "DateTime": {0, maxSafe},
}

func dataType(name string) (pb.DataType, error) {
	v, ok := pb.DataType_value[name]
	if !ok || !slices.Contains(datatypes, name) {
		return 0, fmt.Errorf("%q is not a datatype of the contract", name)
	}
	return pb.DataType(v), nil
}

// number reads a JSON number, decoded as float64 or as json.Number.
func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

// goValue checks a JSON value against its datatype and converts it to the Go
// type the payload package takes for it.
func goValue(dt string, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch dt {
	case "Boolean":
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("%v is not a boolean", v)
		}
		return b, nil
	case "String", "Text":
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%v is not a string", v)
		}
		return s, nil
	case "DataSet":
		o, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("a DataSet is an object")
		}
		return goDataSet(o)
	case "Template":
		o, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("a Template is an object")
		}
		return goTemplate(o)
	}
	f, ok := number(v)
	if !ok {
		return nil, fmt.Errorf("%v is not a number", v)
	}
	if r, isInt := intRanges[dt]; isInt {
		if f != math.Trunc(f) || f < r[0] || f > r[1] {
			return nil, fmt.Errorf("%v is not a %s", v, dt)
		}
	}
	switch dt {
	case "Int8":
		return int8(f), nil
	case "Int16":
		return int16(f), nil
	case "Int32":
		return int32(f), nil
	case "Int64":
		return int64(f), nil
	case "UInt8":
		return uint8(f), nil
	case "UInt16":
		return uint16(f), nil
	case "UInt32":
		return uint32(f), nil
	case "UInt64":
		return uint64(f), nil
	case "DateTime":
		return time.UnixMilli(int64(f)).UTC(), nil
	case "Float":
		if float64(float32(f)) != f {
			return nil, fmt.Errorf("%v is not exactly a Float", v)
		}
		return float32(f), nil
	case "Double":
		return f, nil
	}
	return nil, fmt.Errorf("unknown datatype %q", dt)
}

func stringList(v any, what string) ([]string, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s is not a list", what)
	}
	out := make([]string, 0, len(list))
	for _, x := range list {
		s, ok := x.(string)
		if !ok {
			return nil, fmt.Errorf("%s has %v, not a string", what, x)
		}
		out = append(out, s)
	}
	return out, nil
}

func goDataSet(v map[string]any) (*pb.Payload_DataSet, error) {
	columns, err := stringList(v["columns"], "columns")
	if err != nil {
		return nil, err
	}
	names, err := stringList(v["types"], "types")
	if err != nil {
		return nil, err
	}
	if len(columns) != len(names) {
		return nil, fmt.Errorf("columns and types differ in length")
	}
	types := make([]pb.DataType, 0, len(names))
	for _, n := range names {
		t, err := dataType(n)
		if err != nil {
			return nil, err
		}
		types = append(types, t)
	}
	list, ok := v["rows"].([]any)
	if !ok {
		return nil, fmt.Errorf("rows is not a list")
	}
	var rows [][]any
	for i, r := range list {
		cells, ok := r.([]any)
		if !ok || len(cells) != len(names) {
			return nil, fmt.Errorf("row %d has not one cell per column", i)
		}
		row := make([]any, 0, len(cells))
		for j, cell := range cells {
			g, err := goValue(names[j], cell)
			if err != nil {
				return nil, fmt.Errorf("row %d: %w", i, err)
			}
			row = append(row, g)
		}
		rows = append(rows, row)
	}
	m, err := payload.NewDataSet("", columns, types, rows)
	if err != nil {
		return nil, err
	}
	return m.GetDatasetValue(), nil
}

func goTemplate(v map[string]any) (*pb.Payload_Template, error) {
	def, ok := v["isDefinition"].(bool)
	if !ok {
		return nil, fmt.Errorf("a Template has isDefinition")
	}
	plist, ok1 := v["parameters"].([]any)
	mlist, ok2 := v["metrics"].([]any)
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("a Template has parameters and metrics")
	}
	var params []*pb.Payload_Template_Parameter
	for _, x := range plist {
		p, _ := x.(map[string]any)
		name, _ := p["name"].(string)
		dtName, _ := p["type"].(string)
		dt, err := dataType(dtName)
		if err != nil {
			return nil, fmt.Errorf("parameter %q: %w", name, err)
		}
		g, err := goValue(dtName, p["value"])
		if err != nil {
			return nil, fmt.Errorf("parameter %q: %w", name, err)
		}
		tp, err := payload.NewTemplateParameter(name, dt, g)
		if err != nil {
			return nil, fmt.Errorf("parameter %q: %w", name, err)
		}
		params = append(params, tp)
	}
	var members []*pb.Payload_Metric
	for _, x := range mlist {
		m, _ := x.(map[string]any)
		name, _ := m["name"].(string)
		dtName, _ := m["datatype"].(string)
		dt, err := dataType(dtName)
		if err != nil {
			return nil, fmt.Errorf("member %q: %w", name, err)
		}
		g, err := goValue(dtName, m["value"])
		if err != nil {
			return nil, fmt.Errorf("member %q: %w", name, err)
		}
		tm, err := payload.NewMetric(name, dt, g)
		if err != nil {
			return nil, fmt.Errorf("member %q: %w", name, err)
		}
		members = append(members, tm)
	}
	version, _ := v["version"].(string)
	ref, hasRef := v["templateRef"].(string)
	if def == hasRef {
		return nil, fmt.Errorf("a definition has no templateRef, an instance has one")
	}
	var (
		m   *pb.Payload_Metric
		err error
	)
	if def {
		m, err = payload.NewTemplateDefinition("", version, members, params)
	} else {
		m, err = payload.NewTemplateInstance("", ref, members, params)
	}
	if err != nil {
		return nil, err
	}
	return m.GetTemplateValue(), nil
}

// goProperties converts the properties of a fixture metric.
func goProperties(props map[string]fxProperty) (*pb.Payload_PropertySet, error) {
	if len(props) == 0 {
		return nil, nil
	}
	out := &pb.Payload_PropertySet{}
	for _, k := range slices.Sorted(maps.Keys(props)) {
		p := props[k]
		dt, err := dataType(p.Type)
		if err != nil {
			return nil, fmt.Errorf("property %q: %w", k, err)
		}
		g, err := goValue(p.Type, p.Value)
		if err != nil {
			return nil, fmt.Errorf("property %q: %w", k, err)
		}
		v, err := payload.NewPropertyValue(dt, g)
		if err != nil {
			return nil, fmt.Errorf("property %q: %w", k, err)
		}
		out.Keys = append(out.Keys, k)
		out.Values = append(out.Values, v)
	}
	return out, nil
}

// newMetric builds a metric with its name, its alias when alias is set, and
// value v of datatype dt (a JSON value).
func newMetric(name string, alias *uint64, dtName string, v any) (*pb.Payload_Metric, error) {
	dt, err := dataType(dtName)
	if err != nil {
		return nil, err
	}
	g, err := goValue(dtName, v)
	if err != nil {
		return nil, err
	}
	var m *pb.Payload_Metric
	switch {
	case g == nil:
		// A null value: is_null, with the datatype.
		d, null := uint32(dt), true
		m = &pb.Payload_Metric{Datatype: &d, IsNull: &null}
		if name != "" {
			m.Name = &name
		}
		if alias != nil {
			m.Alias = alias
		}
		return m, nil
	case name == "":
		m, err = payload.NewAliasMetric(*alias, dt, g)
	case alias != nil:
		m, err = payload.NewMetricWithAlias(name, *alias, dt, g)
	default:
		m, err = payload.NewMetric(name, dt, g)
	}
	return m, err
}

// ---- received values, as the contract writes them ----

func datatypeName(dt uint32) string {
	if name, ok := pb.DataType_name[int32(dt)]; ok {
		return name
	}
	return "Unknown"
}

// jsonNumber converts a value of the payload package to JSON.
func jsonNumber(v any) any {
	switch x := v.(type) {
	case nil, bool, string:
		return x
	case int8:
		return int64(x)
	case int16:
		return int64(x)
	case int32:
		return int64(x)
	case int64:
		return x
	case uint8:
		return uint64(x)
	case uint16:
		return uint64(x)
	case uint32:
		return uint64(x)
	case uint64:
		return x
	case float32:
		return jsonFloat(float64(x))
	case float64:
		return jsonFloat(x)
	case time.Time:
		return x.UnixMilli()
	}
	return fmt.Sprint(v)
}

// jsonFloat keeps NaN and infinities, which JSON has no number for, as null.
func jsonFloat(f float64) any {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return f
}

// jsonValue is the value of a received metric that carries its datatype.
// members gives the datatypes of the members of template definitions, by
// definition and member name, for instances whose members carry none.
func jsonValue(m *pb.Payload_Metric, members map[string]string) (any, error) {
	if m.GetIsNull() {
		return nil, nil
	}
	if t := m.GetTemplateValue(); t != nil && m.GetDatatype() == uint32(pb.DataType_Template) {
		return jsonTemplate(t, members)
	}
	if m.GetDatatype() == uint32(pb.DataType_DataSet) {
		return jsonDataSet(m)
	}
	v, err := payload.GetValue(m)
	if err != nil {
		return nil, err
	}
	return jsonNumber(v), nil
}

func jsonDataSet(m *pb.Payload_Metric) (any, error) {
	columns, types, rows, err := payload.GetDataSetValues(m)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, dt := range types {
		names = append(names, dt.String())
	}
	out := []any{}
	for _, row := range rows {
		cells := []any{}
		for _, c := range row {
			cells = append(cells, jsonNumber(c))
		}
		out = append(out, cells)
	}
	if columns == nil {
		columns = []string{}
	}
	return map[string]any{"columns": columns, "types": names, "rows": out}, nil
}

func jsonTemplate(t *pb.Payload_Template, members map[string]string) (any, error) {
	params := []any{}
	for _, p := range t.GetParameters() {
		v, err := payload.GetTemplateParameterValue(p)
		if err != nil {
			return nil, fmt.Errorf("parameter %s: %w", p.GetName(), err)
		}
		params = append(params, map[string]any{"name": p.GetName(), "type": datatypeName(p.GetType()), "value": jsonNumber(v)})
	}
	metrics := []any{}
	for _, m := range t.GetMetrics() {
		if m.Datatype == nil {
			if dt, ok := members[t.GetTemplateRef()+"\x00"+m.GetName()]; ok {
				if d, err := dataType(dt); err == nil {
					c := uint32(d)
					m = cloneMetric(m)
					m.Datatype = &c
				}
			}
		}
		v, err := jsonValue(m, members)
		if err != nil {
			return nil, fmt.Errorf("member %s: %w", m.GetName(), err)
		}
		metrics = append(metrics, map[string]any{"name": m.GetName(), "datatype": datatypeName(m.GetDatatype()), "value": v})
	}
	o := map[string]any{"isDefinition": t.GetIsDefinition(), "parameters": params, "metrics": metrics}
	if t.GetVersion() != "" {
		o["version"] = t.GetVersion()
	}
	if t.GetTemplateRef() != "" {
		o["templateRef"] = t.GetTemplateRef()
	}
	return o, nil
}

// jsonProperties is the properties object of a received metric, nil when it
// has none.
func jsonProperties(m *pb.Payload_Metric) (map[string]any, error) {
	ps := m.GetProperties()
	if len(ps.GetKeys()) == 0 {
		return nil, nil
	}
	out := map[string]any{}
	for i, k := range ps.GetKeys() {
		if i >= len(ps.GetValues()) {
			return nil, fmt.Errorf("property %s has no value", k)
		}
		p := ps.GetValues()[i]
		v, err := payload.GetPropertyValue(p)
		if err != nil {
			return nil, fmt.Errorf("property %s: %w", k, err)
		}
		out[k] = map[string]any{"type": datatypeName(p.GetType()), "value": jsonNumber(v)}
	}
	return out, nil
}
