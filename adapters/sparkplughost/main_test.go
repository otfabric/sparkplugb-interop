// SPDX-License-Identifier: MIT

// The tests build where the adapter builds: with overlay/zz_interop.go in the
// vendored sparkplughost package (Dockerfile).

package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/EvergenEnergy/sparkplughost/protobuf"
	"google.golang.org/protobuf/proto"
)

func int32Property(v int32) *protobuf.Payload_PropertyValue {
	return &protobuf.Payload_PropertyValue{
		Type:  proto.Uint32(uint32(protobuf.DataType_Int32)),
		Value: &protobuf.Payload_PropertyValue_IntValue{IntValue: uint32(v)},
	}
}

func int32Cell(v int32) *protobuf.Payload_DataSet_DataSetValue {
	return &protobuf.Payload_DataSet_DataSetValue{Value: &protobuf.Payload_DataSet_DataSetValue_IntValue{IntValue: uint32(v)}}
}

func stringCell(s string) *protobuf.Payload_DataSet_DataSetValue {
	return &protobuf.Payload_DataSet_DataSetValue{Value: &protobuf.Payload_DataSet_DataSetValue_StringValue{StringValue: s}}
}

// sameJSON compares a document with the JSON text it should encode to.
func sameJSON(t *testing.T, got any, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(mustJSON(got), &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want %s: %v", want, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("got %s, want %s", mustJSON(got), want)
	}
}

// TestMetricDocProperties: each property key is reported with the value at
// its position. A key without a value is reported with a null type and value;
// a value without a key is not reported.
func TestMetricDocProperties(t *testing.T) {
	for _, c := range []struct {
		name   string
		keys   []string
		values []*protobuf.Payload_PropertyValue
		want   string
	}{
		{"one value per key", []string{"a", "b"}, []*protobuf.Payload_PropertyValue{int32Property(-1), int32Property(2)},
			`{"a": {"type": "Int32", "value": -1}, "b": {"type": "Int32", "value": 2}}`},
		{"no values", []string{"a", "b"}, nil,
			`{"a": {"type": null, "value": null}, "b": {"type": null, "value": null}}`},
		{"one value fewer than keys", []string{"a", "b"}, []*protobuf.Payload_PropertyValue{int32Property(1)},
			`{"a": {"type": "Int32", "value": 1}, "b": {"type": null, "value": null}}`},
		{"one value more than keys", []string{"a"}, []*protobuf.Payload_PropertyValue{int32Property(1), int32Property(2)},
			`{"a": {"type": "Int32", "value": 1}}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := &protobuf.Payload_Metric{
				Name:       proto.String("m"),
				Datatype:   proto.Uint32(uint32(protobuf.DataType_Int32)),
				Value:      &protobuf.Payload_Metric_IntValue{IntValue: 7},
				Properties: &protobuf.Payload_PropertySet{Keys: c.keys, Values: c.values},
			}
			doc := metricDoc(m, "Int32")
			if doc["name"] != "m" || doc["value"] != int32(7) {
				t.Errorf("metric %v", doc)
			}
			sameJSON(t, doc["properties"], c.want)
		})
	}

	// Values without any key: the metric has no properties.
	m := &protobuf.Payload_Metric{Name: proto.String("m"), Properties: &protobuf.Payload_PropertySet{
		Values: []*protobuf.Payload_PropertyValue{int32Property(1)},
	}}
	if doc := metricDoc(m, "Int32"); doc["properties"] != nil {
		t.Errorf("properties %v of a set without keys", doc["properties"])
	}
}

// TestJSONDataSet: each element of a row is read by the type at its
// position. An element past the last type is reported as null; a row with
// fewer elements than types is reported as it arrived.
func TestJSONDataSet(t *testing.T) {
	types := []uint32{uint32(protobuf.DataType_Int32), uint32(protobuf.DataType_String)}
	for _, c := range []struct {
		name  string
		types []uint32
		cells []*protobuf.Payload_DataSet_DataSetValue
		want  string
	}{
		{"one element per type", types, []*protobuf.Payload_DataSet_DataSetValue{int32Cell(-5), stringCell("x")},
			`[[-5, "x"]]`},
		{"one element more than types", types, []*protobuf.Payload_DataSet_DataSetValue{int32Cell(1), stringCell("x"), int32Cell(3)},
			`[[1, "x", null]]`},
		{"no types", nil, []*protobuf.Payload_DataSet_DataSetValue{int32Cell(1)},
			`[[null]]`},
		{"one element fewer than types", types, []*protobuf.Payload_DataSet_DataSetValue{int32Cell(1)},
			`[[1]]`},
		{"empty row", types, nil,
			`[[]]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			ds := &protobuf.Payload_DataSet{
				NumOfColumns: proto.Uint64(2),
				Columns:      []string{"n", "s"},
				Types:        c.types,
				Rows:         []*protobuf.Payload_DataSet_Row{{Elements: c.cells}},
			}
			doc, ok := jsonDataSet(ds).(map[string]any)
			if !ok {
				t.Fatalf("document %v", jsonDataSet(ds))
			}
			sameJSON(t, doc["rows"], c.want)

			// The same through the metric document of a DataSet metric.
			m := &protobuf.Payload_Metric{
				Name:     proto.String("ds"),
				Datatype: proto.Uint32(uint32(protobuf.DataType_DataSet)),
				Value:    &protobuf.Payload_Metric_DatasetValue{DatasetValue: ds},
			}
			sameJSON(t, metricDoc(m, "DataSet")["value"].(map[string]any)["rows"], c.want)
		})
	}
}
