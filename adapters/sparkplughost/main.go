// SPDX-License-Identifier: MIT

// Command sparkplugb-interop is the sparkplugb-interop adapter for Evergen's
// sparkplughost (docs/CONTAINER_CONTRACT.md).
//
// sparkplughost is a host application library only, so the image offers the
// host role and no edge. The host is sparkplughost's HostApplication: its MQTT
// session, STATE birth and will, subscriptions, alias resolution, node
// tracking, reordering and rebirth requests, and commands. Through the file
// that overlay/zz_interop.go adds to the package, this adapter sees each
// message after the library processed it and the nodes the library tracks.
// The adapter's own part is the contract: flags, events, the control socket,
// and the datatypes of data metrics, which the library does not track.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/EvergenEnergy/sparkplughost"
	"github.com/EvergenEnergy/sparkplughost/protobuf"
	"google.golang.org/protobuf/proto"
)

const (
	adapter       = "sparkplughost"
	upstreamPath  = "github.com/EvergenEnergy/sparkplughost"
	runDir        = "/run/sparkplugb-interop"
	defaultReady  = runDir + "/ready"
	defaultSocket = runDir + "/control.sock"
	rebirthMetric = "Node Control/Rebirth"
	maxSafe       = 1<<53 - 1

	exitFailed      = 1
	exitUsage       = 2
	exitConnect     = 3
	exitUnsupported = 5
)

var (
	idPattern  = regexp.MustCompile(`^[^/+#]+$`)
	datatypes  = []string{"Int8", "Int16", "Int32", "Int64", "UInt8", "UInt16", "UInt32", "UInt64", "Float", "Double", "Boolean", "String", "DateTime", "Text", "DataSet", "Template"}
	hostCalls  = []string{"send-command", "request-rebirth", "nodes"}
	allCalls   = map[string]bool{"publish-data": true, "rebirth": true, "device-death": true, "device-birth": true, "send-command": true, "request-rebirth": true, "nodes": true}
	intRanges  = map[string][2]float64{"Int8": {-(1 << 7), 1<<7 - 1}, "Int16": {-(1 << 15), 1<<15 - 1}, "Int32": {-(1 << 31), 1<<31 - 1}, "Int64": {-maxSafe, maxSafe}, "UInt8": {0, 255}, "UInt16": {0, 65535}, "UInt32": {0, 1<<32 - 1}, "UInt64": {0, maxSafe}, "DateTime": {0, maxSafe}}
	fixtureDir = envOr("SPARKPLUGB_INTEROP_FIXTURES", "/fixtures")
	adapterVer = envOr("SPARKPLUGB_INTEROP_ADAPTER_VERSION", "dev")
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func upstreamVersion() string {
	if v := os.Getenv("SPARKPLUGB_INTEROP_UPSTREAM_VERSION"); v != "" {
		return v
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == upstreamPath {
				return d.Version
			}
		}
	}
	return "unknown"
}

// ---- output ----

var outMu sync.Mutex

type field struct {
	k string
	v any
}

// emit writes one event line with "event" and "time" first.
func emit(event string, fields ...field) {
	var b strings.Builder
	b.WriteString(`{"event":`)
	b.Write(mustJSON(event))
	b.WriteString(`,"time":`)
	b.Write(mustJSON(time.Now().UTC().Format("2006-01-02T15:04:05.000Z")))
	for _, f := range fields {
		b.WriteByte(',')
		b.Write(mustJSON(f.k))
		b.WriteByte(':')
		b.Write(mustJSON(f.v))
	}
	b.WriteString("}\n")
	outMu.Lock()
	defer outMu.Unlock()
	_, _ = os.Stdout.WriteString(b.String())
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func diag(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[%s] %s\n", adapter, fmt.Sprintf(format, args...))
}

// ---- flags ----

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error { return usageError{fmt.Sprintf(format, args...)} }

type flags map[string]any

// parseFlags: spec maps a flag to its kind (str, int, url, id, json).
func parseFlags(args []string, spec map[string]string, required ...string) (flags, error) {
	out := flags{}
	for i := 0; i < len(args); i += 2 {
		a := args[i]
		name := strings.TrimPrefix(a, "--")
		kind, known := spec[name]
		if !strings.HasPrefix(a, "--") || !known {
			return nil, usagef("unknown argument %s", a)
		}
		if _, dup := out[name]; dup {
			return nil, usagef("--%s given twice", name)
		}
		if i+1 >= len(args) {
			return nil, usagef("--%s needs a value", name)
		}
		v := args[i+1]
		switch kind {
		case "int":
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 || strings.TrimLeft(v, "0123456789") != "" {
				return nil, usagef("--%s: %q is not a positive integer", name, v)
			}
			out[name] = n
		case "url":
			u, err := url.Parse(v)
			if err != nil || u.Scheme != "tcp" || u.Hostname() == "" || (u.Path != "" && u.Path != "/") {
				return nil, usagef("--%s: %q is not tcp://host[:port]", name, v)
			}
			port := u.Port()
			if port == "" {
				port = "1883"
			} else if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
				return nil, usagef("--%s: invalid port %q", name, port)
			}
			out[name] = "tcp://" + net.JoinHostPort(u.Hostname(), port)
		case "id":
			if !idPattern.MatchString(v) {
				return nil, usagef("--%s: %q is not a valid Sparkplug ID", name, v)
			}
			out[name] = v
		case "json":
			var obj map[string]any
			dec := json.NewDecoder(strings.NewReader(v))
			dec.UseNumber()
			if err := dec.Decode(&obj); err != nil || obj == nil || dec.More() {
				return nil, usagef("--%s must be a JSON object", name)
			}
			out[name] = obj
		default:
			out[name] = v
		}
	}
	for _, r := range required {
		if _, ok := out[r]; !ok {
			return nil, usagef("--%s is required", r)
		}
	}
	return out, nil
}

func (f flags) str(k, def string) string {
	if v, ok := f[k].(string); ok {
		return v
	}
	return def
}

func (f flags) int(k string, def int) int {
	if v, ok := f[k].(int); ok {
		return v
	}
	return def
}

// ---- values ----

type callError struct{ code, detail string }

func (e callError) Error() string { return e.detail }

func invalid(format string, args ...any) error {
	return callError{"invalid-request", fmt.Sprintf(format, args...)}
}

func isDatatype(dt string) bool {
	for _, d := range datatypes {
		if d == dt {
			return true
		}
	}
	return false
}

// checkValue validates a JSON value (decoded with UseNumber) for a datatype.
func checkValue(name, dt string, v any) error {
	if r, ok := intRanges[dt]; ok {
		n, isNum := v.(json.Number)
		if !isNum {
			return invalid("%s: %v is not a %s", name, v, dt)
		}
		i, err := strconv.ParseInt(string(n), 10, 64)
		if err != nil || float64(i) < r[0] || float64(i) > r[1] {
			return invalid("%s: %v is not a %s", name, v, dt)
		}
		return nil
	}
	switch dt {
	case "Float", "Double":
		n, isNum := v.(json.Number)
		if !isNum {
			return invalid("%s: %v is not a number", name, v)
		}
		f, err := n.Float64()
		if err != nil {
			return invalid("%s: %v is not a number", name, v)
		}
		if dt == "Float" && float64(float32(f)) != f {
			return invalid("%s: %v is not exactly a Float", name, v)
		}
	case "Boolean":
		if _, ok := v.(bool); !ok {
			return invalid("%s: %v is not a boolean", name, v)
		}
	case "String", "Text":
		if _, ok := v.(string); !ok {
			return invalid("%s: %v is not a string", name, v)
		}
	default:
		return invalid("%s: unknown datatype %q", name, dt)
	}
	return nil
}

// pbMetric builds a command metric; v is checked by checkValue.
func pbMetric(name, dt string, v any) *protobuf.Payload_Metric {
	m := &protobuf.Payload_Metric{
		Name:      proto.String(name),
		Timestamp: proto.Uint64(uint64(time.Now().UnixMilli())),
		Datatype:  proto.Uint32(uint32(protobuf.DataType_value[dt])),
	}
	switch dt {
	case "Int8", "Int16", "Int32":
		i, _ := strconv.ParseInt(string(v.(json.Number)), 10, 64)
		m.Value = &protobuf.Payload_Metric_IntValue{IntValue: uint32(int32(i))}
	case "UInt8", "UInt16", "UInt32":
		i, _ := strconv.ParseInt(string(v.(json.Number)), 10, 64)
		m.Value = &protobuf.Payload_Metric_IntValue{IntValue: uint32(i)}
	case "Int64", "UInt64", "DateTime":
		i, _ := strconv.ParseInt(string(v.(json.Number)), 10, 64)
		m.Value = &protobuf.Payload_Metric_LongValue{LongValue: uint64(i)}
	case "Float":
		f, _ := v.(json.Number).Float64()
		m.Value = &protobuf.Payload_Metric_FloatValue{FloatValue: float32(f)}
	case "Double":
		f, _ := v.(json.Number).Float64()
		m.Value = &protobuf.Payload_Metric_DoubleValue{DoubleValue: f}
	case "Boolean":
		m.Value = &protobuf.Payload_Metric_BooleanValue{BooleanValue: v.(bool)}
	case "String", "Text":
		m.Value = &protobuf.Payload_Metric_StringValue{StringValue: v.(string)}
	}
	return m
}

// integer returns the integer of whichever of int_value and long_value is set.
func integer(m *protobuf.Payload_Metric) (uint64, bool) {
	switch v := m.GetValue().(type) {
	case *protobuf.Payload_Metric_IntValue:
		return uint64(v.IntValue), true
	case *protobuf.Payload_Metric_LongValue:
		return v.LongValue, true
	}
	return 0, false
}

// jsonValue decodes a received metric value for its datatype.
func jsonValue(dt string, m *protobuf.Payload_Metric) any {
	if m.GetIsNull() || m.GetValue() == nil {
		return nil
	}
	switch dt {
	case "DataSet":
		return jsonDataSet(m.GetDatasetValue())
	case "Template":
		return jsonTemplate(m.GetTemplateValue())
	}
	i, isInt := integer(m)
	if v, ok := jsonInteger(dt, i); ok {
		return v
	}
	switch dt {
	case "Float":
		return jsonFloat(float64(m.GetFloatValue()))
	case "Double":
		return jsonFloat(m.GetDoubleValue())
	case "Boolean":
		return m.GetBooleanValue()
	case "String", "Text":
		return m.GetStringValue()
	}
	if isInt {
		return i
	}
	switch v := m.GetValue().(type) {
	case *protobuf.Payload_Metric_FloatValue:
		return jsonFloat(float64(v.FloatValue))
	case *protobuf.Payload_Metric_DoubleValue:
		return jsonFloat(v.DoubleValue)
	case *protobuf.Payload_Metric_BooleanValue:
		return v.BooleanValue
	case *protobuf.Payload_Metric_StringValue:
		return v.StringValue
	}
	return nil
}

// jsonInteger reports an integer of the datatype dt: the low bits of the
// datatype's width, so both encodings of a negative Int8 or Int16 read the same.
func jsonInteger(dt string, i uint64) (any, bool) {
	switch dt {
	case "Int8":
		return int8(uint8(i)), true
	case "Int16":
		return int16(uint16(i)), true
	case "Int32":
		return int32(uint32(i)), true
	case "Int64":
		return int64(i), true
	case "UInt8", "UInt16", "UInt32", "UInt64", "DateTime":
		return i, true
	}
	return nil, false
}

// jsonCell is a DataSet cell, a template parameter or a property value: the
// integer they carry (u, in whichever of int_value and long_value is set) or
// their other value.
func jsonCell(dt string, u uint64, other any) any {
	if v, ok := jsonInteger(dt, u); ok {
		return v
	}
	return other
}

// templateMembers has the datatype of each member of the template definitions
// seen in births, by definition and member name: in data messages, members
// may omit it as other metrics do. Guarded by host.typesMu, like the rest of
// what jsonValue needs.
var templateMembers = map[string]string{}

func jsonDataSet(ds *protobuf.Payload_DataSet) any {
	types := []string{}
	for _, t := range ds.GetTypes() {
		types = append(types, datatypeName(t))
	}
	rows := [][]any{}
	for _, row := range ds.GetRows() {
		cells := []any{}
		for i, c := range row.GetElements() {
			u := uint64(c.GetIntValue())
			if _, long := c.GetValue().(*protobuf.Payload_DataSet_DataSetValue_LongValue); long {
				u = c.GetLongValue()
			}
			cells = append(cells, jsonCell(types[i], u, c.GetStringValue()))
		}
		rows = append(rows, cells)
	}
	return map[string]any{"columns": ds.GetColumns(), "types": types, "rows": rows}
}

func jsonTemplate(t *protobuf.Payload_Template) any {
	params := []any{}
	for _, p := range t.GetParameters() {
		u := uint64(p.GetIntValue())
		if _, long := p.GetValue().(*protobuf.Payload_Template_Parameter_LongValue); long {
			u = p.GetLongValue()
		}
		dt := datatypeName(p.GetType())
		params = append(params, map[string]any{"name": p.GetName(), "type": dt, "value": jsonCell(dt, u, p.GetStringValue())})
	}
	metrics := []any{}
	for _, m := range t.GetMetrics() {
		dt := datatypeName(m.GetDatatype())
		if m.Datatype == nil {
			dt = templateMembers[t.GetTemplateRef()+"\x00"+m.GetName()]
		}
		metrics = append(metrics, metricDoc(m, dt))
	}
	o := map[string]any{"isDefinition": t.GetIsDefinition(), "parameters": params, "metrics": metrics}
	if t.GetVersion() != "" {
		o["version"] = t.GetVersion()
	}
	if t.GetTemplateRef() != "" {
		o["templateRef"] = t.GetTemplateRef()
	}
	return o
}

// metricDoc is the document of a metric whose datatype is dt.
func metricDoc(m *protobuf.Payload_Metric, dt string) map[string]any {
	doc := map[string]any{"name": m.GetName(), "datatype": dt, "value": jsonValue(dt, m)}
	if ps := m.GetProperties(); len(ps.GetKeys()) > 0 {
		props := map[string]any{}
		for i, k := range ps.GetKeys() {
			p := ps.GetValues()[i]
			u := uint64(p.GetIntValue())
			if _, long := p.GetValue().(*protobuf.Payload_PropertyValue_LongValue); long {
				u = p.GetLongValue()
			}
			t := datatypeName(p.GetType())
			props[k] = map[string]any{"type": t, "value": jsonCell(t, u, p.GetStringValue())}
		}
		doc["properties"] = props
	}
	return doc
}

// jsonFloat keeps NaN and infinities, which JSON has no number for, as null.
func jsonFloat(f float64) any {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return f
}

func datatypeName(dt uint32) string {
	if name, ok := protobuf.DataType_name[int32(dt)]; ok {
		return name
	}
	return "Unknown"
}

// ---- control socket ----

type handler func(payload map[string]any) (any, error)

type resultDoc struct {
	SchemaVersion string          `json:"schemaVersion"`
	Adapter       string          `json:"adapter"`
	Operation     *string         `json:"operation"`
	OK            bool            `json:"ok"`
	Error         *resultDocError `json:"error"`
	Result        any             `json:"result"`
	ElapsedMs     int64           `json:"elapsedMs"`
}

type resultDocError struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

func newResult(op string, start time.Time, result any, err error) resultDoc {
	d := resultDoc{SchemaVersion: "1.0", Adapter: adapter, OK: err == nil, Result: result, ElapsedMs: time.Since(start).Milliseconds()}
	if op != "" {
		d.Operation = &op
	}
	if err != nil {
		var ce callError
		if !errors.As(err, &ce) {
			ce = callError{"invalid-request", err.Error()}
		}
		d.Error = &resultDocError{Code: ce.code, Detail: ce.detail}
		d.Result = nil
	}
	return d
}

func serveControl(path string, handlers map[string]handler) error {
	_ = os.Remove(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	var mu sync.Mutex
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				line, err := bufio.NewReader(conn).ReadBytes('\n')
				if err != nil && len(line) == 0 {
					return
				}
				start := time.Now()
				var req struct {
					Operation string          `json:"operation"`
					Payload   json.RawMessage `json:"payload"`
				}
				var doc resultDoc
				if err := json.Unmarshal(line, &req); err != nil {
					doc = newResult("", start, nil, invalid("request is not JSON: %v", err))
				} else if h, ok := handlers[req.Operation]; !ok {
					doc = newResult(req.Operation, start, nil, callError{"unsupported", req.Operation + " is not offered by this role"})
				} else {
					var payload map[string]any
					dec := json.NewDecoder(strings.NewReader(string(req.Payload)))
					dec.UseNumber()
					if len(req.Payload) == 0 || string(req.Payload) == "null" {
						payload = map[string]any{}
					} else if err := dec.Decode(&payload); err != nil || payload == nil {
						doc = newResult(req.Operation, start, nil, invalid("payload is not an object"))
					}
					if doc.SchemaVersion == "" {
						mu.Lock()
						result, err := h(payload)
						mu.Unlock()
						doc = newResult(req.Operation, start, result, err)
					}
				}
				_, _ = conn.Write(append(mustJSON(doc), '\n'))
			}()
		}
	}()
	return nil
}

func call(args []string) (int, error) {
	if len(args) == 0 {
		return 0, usagef("call needs an operation")
	}
	op := args[0]
	if !allCalls[op] {
		return 0, usagef("unknown operation %s", op)
	}
	f, err := parseFlags(args[1:], map[string]string{"payload": "json", "timeout-ms": "int", "control-socket": "str"})
	if err != nil {
		return 0, err
	}
	payload, ok := f["payload"].(map[string]any)
	if !ok {
		payload = map[string]any{}
	}
	timeout := time.Duration(f.int("timeout-ms", 10000)) * time.Millisecond
	start := time.Now()
	doc := func() resultDoc {
		conn, err := net.DialTimeout("unix", f.str("control-socket", defaultSocket), timeout)
		if err != nil {
			return newResult(op, start, nil, callError{"not-connected", err.Error()})
		}
		defer conn.Close()
		_ = conn.SetDeadline(start.Add(timeout))
		req := append(mustJSON(map[string]any{"operation": op, "payload": payload}), '\n')
		if _, err := conn.Write(req); err != nil {
			return newResult(op, start, nil, callError{"not-connected", err.Error()})
		}
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return newResult(op, start, nil, callError{"timeout", "no answer in time"})
		}
		var d resultDoc
		if err := json.Unmarshal(line, &d); err != nil {
			return newResult(op, start, nil, callError{"not-connected", "no answer: " + err.Error()})
		}
		return d
	}()
	fmt.Println(string(mustJSON(doc)))
	switch {
	case doc.OK:
		return 0, nil
	case doc.Error != nil && doc.Error.Code == "unsupported":
		return exitUnsupported, nil
	default:
		return exitFailed, nil
	}
}

// ---- host ----

type metricKey struct{ group, node, device, name string }

type host struct {
	app       *sparkplughost.HostApplication
	hostID    string
	stopping  chan struct{}
	typesMu   sync.Mutex
	datatypes map[metricKey]string // from births: data messages may omit datatypes
}

func (h *host) onMessage(m sparkplughost.InteropMessage) {
	birth := m.Type == "NBIRTH" || m.Type == "DBIRTH"
	h.typesMu.Lock()
	if m.Type == "NBIRTH" {
		for k := range h.datatypes {
			if k.group == m.GroupID && k.node == m.EdgeNodeID {
				delete(h.datatypes, k)
			}
		}
	}
	var bdSeq any
	metrics := []map[string]any{}
	for _, pm := range m.Payload.GetMetrics() {
		if pm.Name == nil {
			diag("%s %s/%s/%s: metric with alias %d resolves to no name; not reported", m.Type, m.GroupID, m.EdgeNodeID, m.DeviceID, pm.GetAlias())
			continue
		}
		key := metricKey{m.GroupID, m.EdgeNodeID, m.DeviceID, pm.GetName()}
		dt := ""
		if pm.Datatype != nil {
			dt = datatypeName(pm.GetDatatype())
			if birth {
				h.datatypes[key] = dt
				if t := pm.GetTemplateValue(); t.GetIsDefinition() {
					for _, member := range t.GetMetrics() {
						templateMembers[pm.GetName()+"\x00"+member.GetName()] = datatypeName(member.GetDatatype())
					}
				}
			}
		} else {
			dt = h.datatypes[key]
		}
		if pm.GetName() == "bdSeq" && m.DeviceID == "" {
			if v, ok := integer(pm); ok {
				bdSeq = int64(v)
			}
			continue
		}
		metrics = append(metrics, metricDoc(pm, dt))
	}
	h.typesMu.Unlock()

	var seq any
	if m.Payload.Seq != nil && m.Type != "NDEATH" {
		seq = m.Payload.GetSeq()
	}
	var device any
	if m.DeviceID != "" {
		device = m.DeviceID
	}
	if m.Type != "NBIRTH" && m.Type != "NDEATH" {
		bdSeq = nil
	}
	emit("message", field{"type", m.Type}, field{"group", m.GroupID}, field{"edgeNode", m.EdgeNodeID},
		field{"device", device}, field{"seq", seq}, field{"bdSeq", bdSeq}, field{"metrics", metrics})
}

func target(p map[string]any) (sparkplughost.EdgeNodeDescriptor, string, error) {
	var d sparkplughost.EdgeNodeDescriptor
	for _, k := range []string{"group", "edgeNode"} {
		v, ok := p[k].(string)
		if !ok || !idPattern.MatchString(v) {
			return d, "", invalid("%s is missing or invalid", k)
		}
	}
	d = sparkplughost.EdgeNodeDescriptor{GroupID: p["group"].(string), EdgeNodeID: p["edgeNode"].(string)}
	dev := ""
	if v, present := p["device"]; present && v != nil {
		s, ok := v.(string)
		if !ok || !idPattern.MatchString(s) {
			return d, "", invalid("device is invalid")
		}
		dev = s
	}
	return d, dev, nil
}

func (h *host) sendCommand(p map[string]any) (any, error) {
	d, dev, err := target(p)
	if err != nil {
		return nil, err
	}
	list, ok := p["metrics"].([]any)
	if !ok || len(list) == 0 {
		return nil, invalid("metrics is missing or empty")
	}
	metrics := make([]*protobuf.Payload_Metric, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		name, nameOK := m["name"].(string)
		dt, dtOK := m["datatype"].(string)
		value, valueOK := m["value"]
		if !ok || !nameOK || !dtOK || !isDatatype(dt) || !valueOK {
			return nil, invalid("invalid metric %v", item)
		}
		if err := checkValue(name, dt, value); err != nil {
			return nil, err
		}
		metrics = append(metrics, pbMetric(name, dt, value))
	}
	if dev != "" {
		if err := h.app.SendDeviceCommand(d, dev, metrics); err != nil {
			return nil, callError{"not-connected", err.Error()}
		}
		return map[string]any{"published": []string{"DCMD/" + dev}}, nil
	}
	if err := h.app.SendEdgeNodeCommand(d, metrics); err != nil {
		return nil, callError{"not-connected", err.Error()}
	}
	return map[string]any{"published": []string{"NCMD"}}, nil
}

func (h *host) requestRebirth(p map[string]any) (any, error) {
	q := map[string]any{"group": p["group"], "edgeNode": p["edgeNode"]}
	d, _, err := target(q)
	if err != nil {
		return nil, err
	}
	if err := h.app.SendEdgeNodeRebirthRequest(d); err != nil {
		return nil, callError{"not-connected", err.Error()}
	}
	return map[string]any{"published": []string{"NCMD"}}, nil
}

func (h *host) nodes(map[string]any) (any, error) {
	nodes := []map[string]any{}
	for _, n := range h.app.InteropNodes() {
		devices := []map[string]any{}
		for _, d := range n.Devices {
			devices = append(devices, map[string]any{"device": d.DeviceID, "online": d.Online})
		}
		nodes = append(nodes, map[string]any{"group": n.GroupID, "edgeNode": n.EdgeNodeID, "online": n.Online, "bdSeq": n.BdSeq, "devices": devices})
	}
	return map[string]any{"nodes": nodes}, nil
}

func runHost(f flags) int {
	hostID := f.str("host-id", "")
	if c, ok := f["client-id"]; ok && c != hostID {
		diag("--client-id is not supported: sparkplughost connects with the host ID as its MQTT client identifier")
		return exitUnsupported
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	h := &host{hostID: hostID, stopping: make(chan struct{}), datatypes: map[metricKey]string{}}
	app, err := sparkplughost.NewHostApplication(
		[]sparkplughost.MqttBrokerConfig{{BrokerURL: f.str("broker", "")}}, hostID, sparkplughost.WithLogger(logger))
	if err != nil {
		diag("host: %v", err)
		return exitFailed
	}
	h.app = app
	hooks := sparkplughost.InteropHooks{
		OnMessage: h.onMessage,
		OnRebirthRequest: func(d sparkplughost.EdgeNodeDescriptor, reason string) {
			emit("rebirth-requested", field{"group", d.GroupID}, field{"edgeNode", d.EdgeNodeID}, field{"reason", reason})
		},
	}
	if err := app.InteropStart(hooks, time.Duration(f.int("connect-timeout-ms", 10000))*time.Millisecond); err != nil {
		diag("connect: %v", err)
		return exitConnect
	}
	if err := serveControl(f.str("control-socket", defaultSocket), map[string]handler{
		"send-command": h.sendCommand, "request-rebirth": h.requestRebirth, "nodes": h.nodes,
	}); err != nil {
		diag("control socket: %v", err)
		return exitFailed
	}
	ready := f.str("ready-file", defaultReady)
	if err := os.MkdirAll(filepath.Dir(ready), 0o755); err == nil {
		err = os.WriteFile(ready, []byte("ready\n"), 0o644)
		if err != nil {
			diag("ready file: %v", err)
			return exitFailed
		}
	}
	emit("ready", field{"adapter", adapter}, field{"hostId", hostID})

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			if err := app.InteropStop(); err != nil {
				diag("stop: %v", err)
			}
			emit("stopped")
			return 0
		case <-tick.C:
			// sparkplughost reconnects on its own; the contract ends the role.
			if !app.InteropConnected() {
				emit("connection-lost", field{"detail", "the MQTT connection to the broker was lost"})
				return exitFailed
			}
		}
	}
}

// ---- commands ----

func capabilities() map[string]any {
	return map[string]any{
		"schemaVersion":  "1.0",
		"adapter":        adapter,
		"adapterVersion": adapterVer,
		"protocol":       "sparkplug-b-3.0.0",
		"upstream": map[string]any{"name": "sparkplughost", "version": upstreamVersion(), "license": "Apache-2.0",
			"language": "Go", "independentEngine": true},
		"roles": map[string]any{
			"edge": map[string]any{"supported": false},
			"host": map[string]any{"supported": true, "datatypes": datatypes, "calls": hostCalls,
				"features": map[string]any{"aliasResolution": true, "seqCheck": true, "gracefulOffline": true}},
		},
	}
}

func run(argv []string) (int, error) {
	if len(argv) == 0 {
		argv = []string{"host"}
	}
	cmd, args := argv[0], argv[1:]
	switch cmd {
	case "print-capabilities":
		if _, err := parseFlags(args, nil); err != nil {
			return 0, err
		}
		fmt.Println(string(mustJSON(capabilities())))
		return 0, nil
	case "print-fixture":
		if len(args) > 1 {
			return 0, usagef("print-fixture takes at most one name")
		}
		name := "baseline"
		if len(args) == 1 {
			name = args[0]
		}
		data, err := os.ReadFile(filepath.Join(fixtureDir, name, "fixture.json"))
		if !idPattern.MatchString(name) || err != nil {
			diag("no baked fixture %q", name)
			return exitFailed, nil
		}
		_, _ = os.Stdout.Write(data)
		return 0, nil
	case "version":
		if _, err := parseFlags(args, nil); err != nil {
			return 0, err
		}
		fmt.Printf("sparkplugb-interop-%s %s (sparkplughost %s)\n", adapter, adapterVer, upstreamVersion())
		return 0, nil
	case "call":
		return call(args)
	case "edge":
		diag("edge is not supported: sparkplughost is a host application library")
		return exitUnsupported, nil
	case "host":
		f, err := parseFlags(args, map[string]string{"broker": "url", "host-id": "id", "client-id": "str",
			"connect-timeout-ms": "int", "ready-file": "str", "control-socket": "str"}, "broker", "host-id")
		if err != nil {
			return 0, err
		}
		return runHost(f), nil
	}
	return 0, usagef("unknown command %s", cmd)
}

func main() {
	code, err := run(os.Args[1:])
	var ue usageError
	if errors.As(err, &ue) {
		diag("usage: %s", ue.msg)
		code = exitUsage
	}
	os.Exit(code)
}
