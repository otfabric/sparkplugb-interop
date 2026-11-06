// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	pb "github.com/otfabric/go-sparkplugb/proto"
	"google.golang.org/protobuf/proto"
)

// fixture is a sparkplugb-interop fixture (docs/FIXTURES.md).
type fixture struct {
	SchemaVersion string     `json:"schemaVersion"`
	Name          string     `json:"name"`
	Group         string     `json:"group"`
	EdgeNode      string     `json:"edgeNode"`
	Node          fxNode     `json:"node"`
	Devices       []fxDevice `json:"devices"`
	Updates       []fxUpdate `json:"updates"`
}

type fxNode struct {
	Metrics []fxMetric `json:"metrics"`
}

type fxDevice struct {
	ID      string     `json:"id"`
	Metrics []fxMetric `json:"metrics"`
}

type fxMetric struct {
	Name       string                `json:"name"`
	Alias      *uint64               `json:"alias"`
	Datatype   string                `json:"datatype"`
	Value      any                   `json:"value"`
	Writable   bool                  `json:"writable"`
	Properties map[string]fxProperty `json:"properties"`
}

type fxProperty struct {
	Type  string `json:"type"`
	Value any    `json:"value"`
}

type fxValue struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
}

type fxUpdate struct {
	Node    []fxValue            `json:"node"`
	Devices map[string][]fxValue `json:"devices"`
}

// loadFixture reads a baked fixture by name, or a fixture file by path, and
// checks the rules of docs/FIXTURES.md.
func loadFixture(ref string) (*fixture, error) {
	path := ref
	if !strings.ContainsAny(ref, "/.") {
		path = filepath.Join(fixtureDir, ref, "fixture.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f := &fixture{}
	if err := json.Unmarshal(data, f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := f.check(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

func (f *fixture) check() error {
	if f.SchemaVersion != "1.0" {
		return fmt.Errorf("schemaVersion %q is not 1.0", f.SchemaVersion)
	}
	if !idPattern.MatchString(f.Group) || !idPattern.MatchString(f.EdgeNode) {
		return fmt.Errorf("group and edgeNode must be Sparkplug IDs")
	}
	aliases := map[uint64]string{}
	scopes := map[string][]fxMetric{"": f.Node.Metrics}
	order := []string{""}
	for _, d := range f.Devices {
		if !idPattern.MatchString(d.ID) {
			return fmt.Errorf("device ID %q is not a Sparkplug ID", d.ID)
		}
		if _, dup := scopes[d.ID]; dup {
			return fmt.Errorf("device %q appears twice", d.ID)
		}
		scopes[d.ID] = d.Metrics
		order = append(order, d.ID)
	}
	for _, scope := range order {
		names := map[string]bool{}
		for _, m := range scopes[scope] {
			where := fmt.Sprintf("%s metric %q", scopeName(scope), m.Name)
			if m.Name == "" || names[m.Name] {
				return fmt.Errorf("%s: the name is empty or appears twice", where)
			}
			names[m.Name] = true
			if m.Name == "bdSeq" || strings.HasPrefix(m.Name, "Node Control/") {
				return fmt.Errorf("%s: names under Node Control/ and bdSeq are the edge's own", where)
			}
			if m.Alias == nil {
				return fmt.Errorf("%s: no alias", where)
			}
			if other, dup := aliases[*m.Alias]; dup {
				return fmt.Errorf("%s: alias %d is already used by %s", where, *m.Alias, other)
			}
			aliases[*m.Alias] = where
			if _, err := birthMetric(m); err != nil {
				return fmt.Errorf("%s: %w", where, err)
			}
		}
	}
	for i, u := range f.Updates {
		if len(u.Node) == 0 && len(u.Devices) == 0 {
			return fmt.Errorf("updates[%d] changes nothing", i)
		}
		check := func(scope string, vs []fxValue) error {
			seen := map[string]bool{}
			for _, v := range vs {
				m, ok := f.metric(scope, v.Name)
				if !ok {
					return fmt.Errorf("updates[%d]: %s has no metric %q", i, scopeName(scope), v.Name)
				}
				if seen[v.Name] {
					return fmt.Errorf("updates[%d]: %s metric %q changed twice", i, scopeName(scope), v.Name)
				}
				seen[v.Name] = true
				if _, err := goValue(m.Datatype, v.Value); err != nil {
					return fmt.Errorf("updates[%d]: %s metric %q: %w", i, scopeName(scope), v.Name, err)
				}
			}
			return nil
		}
		if err := check("", u.Node); err != nil {
			return err
		}
		for dev, vs := range u.Devices {
			if _, ok := scopes[dev]; !ok || dev == "" {
				return fmt.Errorf("updates[%d]: unknown device %q", i, dev)
			}
			if err := check(dev, vs); err != nil {
				return err
			}
		}
	}
	return nil
}

func scopeName(scope string) string {
	if scope == "" {
		return "node"
	}
	return "device " + scope
}

// metrics returns the metrics of the node ("") or of a device.
func (f *fixture) metrics(device string) []fxMetric {
	if device == "" {
		return f.Node.Metrics
	}
	for _, d := range f.Devices {
		if d.ID == device {
			return d.Metrics
		}
	}
	return nil
}

func (f *fixture) hasDevice(id string) bool {
	for _, d := range f.Devices {
		if d.ID == id {
			return true
		}
	}
	return false
}

func (f *fixture) metric(device, name string) (fxMetric, bool) {
	for _, m := range f.metrics(device) {
		if m.Name == name {
			return m, true
		}
	}
	return fxMetric{}, false
}

// set changes the value of a metric of the node ("") or of a device.
func (f *fixture) set(device, name string, v any) {
	ms := f.metrics(device)
	for i := range ms {
		if ms[i].Name == name {
			ms[i].Value = v
		}
	}
}

// birthMetric is a fixture metric as a birth carries it: name, alias,
// datatype, value and properties.
func birthMetric(m fxMetric) (*pb.Payload_Metric, error) {
	out, err := newMetric(m.Name, m.Alias, m.Datatype, m.Value)
	if err != nil {
		return nil, err
	}
	props, err := goProperties(m.Properties)
	if err != nil {
		return nil, err
	}
	out.Properties = props
	return out, nil
}

// births is the metrics of the node ("") or a device for its birth.
func (f *fixture) births(device string) ([]*pb.Payload_Metric, error) {
	var out []*pb.Payload_Metric
	for _, m := range f.metrics(device) {
		pm, err := birthMetric(m)
		if err != nil {
			return nil, err
		}
		out = append(out, pm)
	}
	return out, nil
}

func cloneMetric(m *pb.Payload_Metric) *pb.Payload_Metric {
	return proto.Clone(m).(*pb.Payload_Metric)
}
