// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/otfabric/go-sparkplugb/payload"
	pb "github.com/otfabric/go-sparkplugb/proto"
)

// The fixtures of the repository, or of the image when built there.
func init() {
	if _, err := os.Stat("../../fixtures/baseline/fixture.json"); err == nil {
		fixtureDir = "../../fixtures"
	}
}

func TestBakedFixturesLoad(t *testing.T) {
	for _, name := range fixtures {
		f, err := loadFixture(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if f.Name != name {
			t.Errorf("%s: name %q", name, f.Name)
		}
	}
}

func writeFixture(t *testing.T, change func(map[string]any)) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir, "baseline", "fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx map[string]any
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	change(fx)
	path := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(path, mustJSON(fx), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func nodeMetric(fx map[string]any, i int) map[string]any {
	return fx["node"].(map[string]any)["metrics"].([]any)[i].(map[string]any)
}

func TestInvalidFixtures(t *testing.T) {
	cases := map[string]func(map[string]any){
		"duplicate alias": func(fx map[string]any) { nodeMetric(fx, 1)["alias"] = nodeMetric(fx, 0)["alias"] },
		"unknown metric in an update": func(fx map[string]any) {
			fx["updates"].([]any)[0].(map[string]any)["node"].([]any)[0].(map[string]any)["name"] = "No/Such/Metric"
		},
		"reserved name":        func(fx map[string]any) { nodeMetric(fx, 0)["name"] = "Node Control/Reboot" },
		"value out of range":   func(fx map[string]any) { nodeMetric(fx, 4)["value"] = float64(1 << 40) },
		"not exactly a Float":  func(fx map[string]any) { nodeMetric(fx, 2)["datatype"] = "Float"; nodeMetric(fx, 2)["value"] = 0.1 },
		"unknown datatype":     func(fx map[string]any) { nodeMetric(fx, 0)["datatype"] = "Bytes" },
		"invalid edge node ID": func(fx map[string]any) { fx["edgeNode"] = "a/b" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := loadFixture(writeFixture(t, change)); err == nil {
				t.Fatal("the fixture was accepted")
			}
		})
	}
	if _, err := loadFixture("/no/such/fixture.json"); err == nil {
		t.Error("a missing fixture was accepted")
	}
}

// TestValuesRoundTrip: every birth metric of every fixture, encoded and
// decoded by go-sparkplugb, reads back as the fixture writes it.
func TestValuesRoundTrip(t *testing.T) {
	for _, name := range fixtures {
		f, err := loadFixture(name)
		if err != nil {
			t.Fatal(err)
		}
		members := map[string]string{}
		scopes := []string{""}
		for _, d := range f.Devices {
			scopes = append(scopes, d.ID)
		}
		for _, scope := range scopes {
			for _, m := range f.metrics(scope) {
				pm, err := birthMetric(m)
				if err != nil {
					t.Fatalf("%s %s: %v", name, m.Name, err)
				}
				b, err := payload.Encode(payload.NewPayload(0, pm))
				if err != nil {
					t.Fatal(err)
				}
				p, err := payload.Decode(b)
				if err != nil {
					t.Fatal(err)
				}
				got := p.GetMetrics()[0]
				if tm := got.GetTemplateValue(); tm.GetIsDefinition() {
					for _, member := range tm.GetMetrics() {
						members[m.Name+"\x00"+member.GetName()] = datatypeName(member.GetDatatype())
					}
				}
				v, err := jsonValue(got, members)
				if err != nil {
					t.Fatalf("%s %s: %v", name, m.Name, err)
				}
				if !sameJSON(t, v, m.Value) {
					t.Errorf("%s %s: got %s, want %s", name, m.Name, mustJSON(v), mustJSON(m.Value))
				}
				props, err := jsonProperties(got)
				if err != nil {
					t.Fatal(err)
				}
				if len(m.Properties) > 0 && !sameJSON(t, props, m.Properties) {
					t.Errorf("%s %s: properties %s, want %s", name, m.Name, mustJSON(props), mustJSON(m.Properties))
				}
			}
		}
	}
}

func sameJSON(t *testing.T, a, b any) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(mustJSON(a), &x); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(mustJSON(b), &y); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(x, y)
}

func TestAliasOnlyAndNullMetrics(t *testing.T) {
	alias := uint64(7)
	m, err := newMetric("", &alias, "Int16", float64(-300))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != nil || m.GetAlias() != 7 {
		t.Errorf("alias-only metric has name %v, alias %d", m.Name, m.GetAlias())
	}
	if v, err := jsonValue(m, nil); err != nil || v != int64(-300) {
		t.Errorf("value %v, %v", v, err)
	}
	null, err := newMetric("x", nil, "Double", nil)
	if err != nil || !null.GetIsNull() || null.GetDatatype() != uint32(pb.DataType_Double) {
		t.Fatalf("null metric %v, %v", null, err)
	}
	if v, err := jsonValue(null, nil); err != nil || v != nil {
		t.Errorf("null reads as %v, %v", v, err)
	}
}

func TestCommandValues(t *testing.T) {
	for _, c := range []struct {
		dt string
		v  any
		ok bool
	}{
		{"Int8", json.Number("-128"), true},
		{"Int8", json.Number("128"), false},
		{"UInt32", json.Number("4294967295"), true},
		{"UInt32", json.Number("-1"), false},
		{"Int32", json.Number("1.5"), false},
		{"Float", json.Number("1500"), true},
		{"Float", json.Number("0.1"), false},
		{"Boolean", true, true},
		{"Boolean", "true", false},
		{"String", "x", true},
		{"Frobnicate", "x", false},
	} {
		_, err := newMetric("m", nil, c.dt, c.v)
		if (err == nil) != c.ok {
			t.Errorf("%s %v: error %v, want ok %v", c.dt, c.v, err, c.ok)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"frobnicate"},
		{"edge"},
		{"edge", "--broker", "tcp://h:1883", "--frobnicate", "1"},
		{"edge", "--broker", "tcp://h:1883", "--broker", "tcp://h:1883"},
		{"edge", "--broker", "http://h"},
		{"edge", "--broker", "tcp://h:1883", "--group", "a/b"},
		{"edge", "--broker", "tcp://h:1883", "--connect-timeout-ms", "soon"},
		{"host", "--broker", "tcp://h:1883"},
		{"host", "--broker", "tcp://h:1883", "--host-id", "a#b"},
		{"call", "frobnicate"},
		{"call", "rebirth", "--payload", "[1]"},
	} {
		_, err := run(args)
		var ue usageError
		if !errors.As(err, &ue) {
			t.Errorf("%s: %v, want a usage error", strings.Join(args, " "), err)
		}
	}
}

func TestInvalidFixtureIsExit4(t *testing.T) {
	code, err := run([]string{"edge", "--broker", "tcp://nowhere:1883", "--fixture", "/no/such/fixture.json"})
	if err != nil || code != exitFixture {
		t.Errorf("exit %d, %v", code, err)
	}
}

func TestCapabilities(t *testing.T) {
	c := capabilities()
	up := c["upstream"].(map[string]any)
	if up["independentEngine"] != false || up["name"] != upstreamName {
		t.Errorf("upstream %v", up)
	}
	var doc map[string]any
	if err := json.Unmarshal(mustJSON(c), &doc); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"edge", "host"} {
		if doc["roles"].(map[string]any)[role].(map[string]any)["supported"] != true {
			t.Errorf("%s not supported", role)
		}
	}
}

func TestAnswer(t *testing.T) {
	handlers := map[string]handler{"nodes": func(map[string]any) (any, error) { return map[string]any{"nodes": []any{}}, nil }}
	var mu sync.Mutex
	if d := answer([]byte(`{"operation":"publish-data","payload":{}}`+"\n"), handlers, &mu); d.OK || d.Error.Code != "unsupported" {
		t.Errorf("unsupported: %+v", d)
	}
	if d := answer([]byte(`{"operation":"nodes","payload":[1]}`), handlers, &mu); d.OK || d.Error.Code != "invalid-request" {
		t.Errorf("invalid payload: %+v", d)
	}
	if d := answer([]byte(`{"operation":"nodes"}`), handlers, &mu); !d.OK || *d.Operation != "nodes" {
		t.Errorf("nodes: %+v", d)
	}
}
