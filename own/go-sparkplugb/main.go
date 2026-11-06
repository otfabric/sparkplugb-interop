// SPDX-License-Identifier: MIT

// Command sparkplugb-interop puts go-sparkplugb, OT Fabric's own Sparkplug B
// stack, behind the sparkplugb-interop container contract
// (docs/CONTAINER_CONTRACT.md). It is not an independent implementation: see
// README.md.
//
// The edge role is go-sparkplugb's edge.Node: its MQTT session and will,
// bdSeq, seq, births, data, device death and birth, NDEATH, rebirth on an NCMD
// and the primary host's STATE (it disconnects when the host goes offline and
// connects anew). The host role is go-sparkplugb's host.App: its STATE birth
// and will, subscriptions, node tracking, alias resolution, sequence checks,
// reordering and rebirth requests, and commands. The adapter's own part is the
// contract: flags, the fixture, events, the control socket, and the values of
// metrics as JSON.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	adapter       = "otfabric"
	upstreamName  = "go-sparkplugb"
	runDir        = "/run/sparkplugb-interop"
	defaultReady  = runDir + "/ready"
	defaultSocket = runDir + "/control.sock"
	rebirthMetric = "Node Control/Rebirth"

	exitFailed      = 1
	exitUsage       = 2
	exitConnect     = 3
	exitFixture     = 4
	exitUnsupported = 5
)

// Set at build time (Dockerfile).
var (
	adapterVersion   = "dev"
	upstreamVersion  = "dev"
	upstreamRevision = ""
)

var (
	idPattern  = regexp.MustCompile(`^[^/+#]+$`)
	datatypes  = []string{"Int8", "Int16", "Int32", "Int64", "UInt8", "UInt16", "UInt32", "UInt64", "Float", "Double", "Boolean", "String", "DateTime", "Text", "DataSet", "Template"}
	fixtures   = []string{"baseline", "scalars", "composite"}
	edgeCalls  = []string{"publish-data", "rebirth", "device-death", "device-birth"}
	hostCalls  = []string{"send-command", "request-rebirth", "nodes"}
	allCalls   = map[string]bool{"publish-data": true, "rebirth": true, "device-death": true, "device-birth": true, "send-command": true, "request-rebirth": true, "nodes": true}
	fixtureDir = envOr("SPARKPLUGB_INTEROP_FIXTURES", "/fixtures")
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
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

// stderrLogger is the sparkplugb.Logger of both roles: the library's log goes
// to stderr. onWarn, when set, sees every warning first.
type stderrLogger struct {
	onWarn func(format string, args []any)
}

func (stderrLogger) Debugf(string, ...any) {}
func (stderrLogger) Infof(format string, args ...any) {
	diag("go-sparkplugb: "+format, args...)
}
func (l stderrLogger) Warnf(format string, args ...any) {
	if l.onWarn != nil {
		l.onWarn(format, args)
	}
	diag("go-sparkplugb: warning: "+format, args...)
}
func (stderrLogger) Errorf(format string, args ...any) {
	diag("go-sparkplugb: error: "+format, args...)
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
			if v == "" {
				return nil, usagef("--%s needs a value", name)
			}
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

// ---- control socket ----

type callError struct{ code, detail string }

func (e callError) Error() string { return e.detail }

func invalid(format string, args ...any) error {
	return callError{"invalid-request", fmt.Sprintf(format, args...)}
}

func notConnected(err error) error { return callError{"not-connected", err.Error()} }

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

// serveControl answers the operations of the running role, one at a time.
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
				doc := answer(line, handlers, &mu)
				_, _ = conn.Write(append(mustJSON(doc), '\n'))
			}()
		}
	}()
	return nil
}

func answer(line []byte, handlers map[string]handler, mu *sync.Mutex) resultDoc {
	start := time.Now()
	var req struct {
		Operation string          `json:"operation"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(line, &req); err != nil {
		return newResult("", start, nil, invalid("request is not JSON: %v", err))
	}
	h, ok := handlers[req.Operation]
	if !ok {
		return newResult(req.Operation, start, nil, callError{"unsupported", req.Operation + " is not offered by this role"})
	}
	payload := map[string]any{}
	if len(req.Payload) > 0 && string(req.Payload) != "null" {
		dec := json.NewDecoder(strings.NewReader(string(req.Payload)))
		dec.UseNumber()
		payload = nil
		if err := dec.Decode(&payload); err != nil || payload == nil {
			return newResult(req.Operation, start, nil, invalid("payload is not an object"))
		}
	}
	mu.Lock()
	defer mu.Unlock()
	result, err := h(payload)
	return newResult(req.Operation, start, result, err)
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
			return newResult(op, start, nil, notConnected(err))
		}
		defer conn.Close()
		_ = conn.SetDeadline(start.Add(timeout))
		req := append(mustJSON(map[string]any{"operation": op, "payload": payload}), '\n')
		if _, err := conn.Write(req); err != nil {
			return newResult(op, start, nil, notConnected(err))
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

// writeReady creates the readiness file.
func writeReady(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("ready\n"), 0o644)
}

// ---- commands ----

func capabilities() map[string]any {
	return map[string]any{
		"schemaVersion":  "1.0",
		"adapter":        adapter,
		"adapterVersion": adapterVersion,
		"protocol":       "sparkplug-b-3.0.0",
		"upstream": map[string]any{"name": upstreamName, "version": upstreamVersion, "license": "MIT",
			"language": "Go", "independentEngine": false},
		"roles": map[string]any{
			"edge": map[string]any{"supported": true, "fixtures": fixtures, "calls": edgeCalls,
				"features": map[string]any{"aliases": true, "rebirthCommand": true, "writes": true, "primaryHost": true, "gracefulDeath": true}},
			"host": map[string]any{"supported": true, "datatypes": datatypes, "calls": hostCalls,
				"features": map[string]any{"aliasResolution": true, "seqCheck": true, "gracefulOffline": true}},
		},
	}
}

var (
	edgeSpec = map[string]string{"broker": "url", "fixture": "str", "group": "id", "edge-node": "id", "primary-host": "id",
		"client-id": "str", "connect-timeout-ms": "int", "ready-file": "str", "control-socket": "str"}
	hostSpec = map[string]string{"broker": "url", "host-id": "id", "client-id": "str",
		"connect-timeout-ms": "int", "ready-file": "str", "control-socket": "str"}
)

func run(argv []string) (int, error) {
	if len(argv) == 0 {
		argv = []string{"edge"}
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
		if !regexp.MustCompile(`^[a-z0-9-]+$`).MatchString(name) {
			diag("no baked fixture %q", name)
			return exitFailed, nil
		}
		data, err := os.ReadFile(filepath.Join(fixtureDir, name, "fixture.json"))
		if err != nil {
			diag("no baked fixture %q", name)
			return exitFailed, nil
		}
		_, _ = os.Stdout.Write(data)
		return 0, nil
	case "version":
		if _, err := parseFlags(args, nil); err != nil {
			return 0, err
		}
		rev := ""
		if upstreamRevision != "" {
			rev = ", commit " + upstreamRevision
		}
		fmt.Printf("sparkplugb-interop-%s %s (go-sparkplugb %s%s)\n", adapter, adapterVersion, upstreamVersion, rev)
		return 0, nil
	case "call":
		return call(args)
	case "edge":
		f, err := parseFlags(args, edgeSpec, "broker")
		if err != nil {
			return 0, err
		}
		return runEdge(f), nil
	case "host":
		f, err := parseFlags(args, hostSpec, "broker", "host-id")
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
