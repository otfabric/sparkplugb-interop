// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/otfabric/go-sparkplugb/edge"
	"github.com/otfabric/go-sparkplugb/mqtt"
	"github.com/otfabric/go-sparkplugb/payload"
	pb "github.com/otfabric/go-sparkplugb/proto"
	"github.com/otfabric/go-sparkplugb/topic"
)

const opTimeout = 10 * time.Second

// edgeRole is the edge node of a fixture: go-sparkplugb's edge.Node, with the
// current values of the fixture's metrics and the devices that are offline.
type edgeRole struct {
	node        *edge.Node
	fx          *fixture
	fixtureName string
	group       string
	edgeNode    string
	primaryHost string
	readyFile   string

	mu      sync.Mutex // fx, offline
	offline map[string]bool

	online chan struct{} // the node became Online: publish the births
	lost   chan error

	hostMu     sync.Mutex
	hostOnline *bool // the last STATE of the primary host, nil before any
}

func runEdge(f flags) int {
	ref := f.str("fixture", "baseline")
	fx, err := loadFixture(ref)
	if err != nil {
		diag("fixture: %v", err)
		return exitFixture
	}
	e := &edgeRole{
		fx:          fx,
		fixtureName: fx.Name,
		group:       f.str("group", fx.Group),
		edgeNode:    f.str("edge-node", fx.EdgeNode),
		primaryHost: f.str("primary-host", ""),
		readyFile:   f.str("ready-file", defaultReady),
		offline:     map[string]bool{},
		online:      make(chan struct{}, 1),
		lost:        make(chan error, 1),
	}
	paho := mqtt.NewPahoTransport()
	paho.ConnectTimeout = time.Duration(f.int("connect-timeout-ms", 10000)) * time.Millisecond
	tr := &tap{inner: paho, onPublish: e.published, onMessage: e.received, onLost: func(err error) {
		select {
		case e.lost <- err:
		default:
		}
	}}
	node, err := edge.New(tr, edge.Config{
		GroupID:       e.group,
		EdgeNodeID:    e.edgeNode,
		BrokerURL:     f.str("broker", ""),
		ClientID:      f.str("client-id", fmt.Sprintf("%s-edge-%s-%s", adapter, e.group, e.edgeNode)),
		PrimaryHostID: e.primaryHost,
		Logger:        stderrLogger{},
		// Called with the node's lock held: the births go out from run.
		OnState: func(_, s edge.State) {
			if s == edge.Online {
				select {
				case e.online <- struct{}{}:
				default:
				}
			}
		},
		OnNodeCmd:   func(_ string, ms []*pb.Payload_Metric) { e.command("", ms) },
		OnDeviceCmd: func(d string, ms []*pb.Payload_Metric) { e.command(d, ms) },
	})
	if err != nil {
		diag("edge: %v", err)
		return exitFailed
	}
	e.node = node
	if err := serveControl(f.str("control-socket", defaultSocket), map[string]handler{
		"publish-data": e.publishData, "rebirth": e.rebirth, "device-death": e.deviceDeath, "device-birth": e.deviceBirth,
	}); err != nil {
		diag("control socket: %v", err)
		return exitFailed
	}
	ctx, cancel := context.WithTimeout(context.Background(), paho.ConnectTimeout+opTimeout)
	err = node.Connect(ctx)
	cancel()
	if err != nil {
		diag("connect: %v", err)
		return exitConnect
	}
	return e.run()
}

func (e *edgeRole) run() int {
	stop, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	ready := false
	for {
		select {
		case <-stop.Done():
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			if err := e.node.Close(ctx); err != nil {
				diag("stop: %v", err)
			}
			cancel()
			emit("stopped")
			return 0
		case err := <-e.lost:
			emit("connection-lost", field{"detail", err.Error()})
			return exitFailed
		case <-e.online:
			if err := e.births(); err != nil {
				diag("births: %v", err)
				continue
			}
			if !ready {
				ready = true
				if err := writeReady(e.readyFile); err != nil {
					diag("ready file: %v", err)
					return exitFailed
				}
				emit("ready", field{"adapter", adapter}, field{"fixture", e.fixtureName}, field{"group", e.group},
					field{"edgeNode", e.edgeNode}, field{"bdSeq", e.node.BdSeq()})
			}
		}
	}
}

// births publishes NBIRTH and the DBIRTH of every online device, in fixture
// order, with the current values.
func (e *edgeRole) births() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	ms, err := e.fx.births("")
	if err != nil {
		return err
	}
	if err := e.node.PublishBirth(ctx, ms); err != nil {
		return fmt.Errorf("NBIRTH: %w", err)
	}
	for _, d := range e.fx.Devices {
		if e.offline[d.ID] {
			continue
		}
		ms, err := e.fx.births(d.ID)
		if err != nil {
			return err
		}
		if err := e.node.PublishDeviceBirth(ctx, d.ID, ms); err != nil {
			return fmt.Errorf("DBIRTH %s: %w", d.ID, err)
		}
	}
	return nil
}

// published is the "published" event of every Sparkplug message the edge
// node published, whoever made it.
func (e *edgeRole) published(t string, data []byte) {
	tp, err := topic.Parse(t)
	if err != nil || tp.Type == topic.STATE {
		return
	}
	p, err := payload.Decode(data)
	if err != nil {
		return
	}
	var device, seq, bdSeq any
	if tp.DeviceID != "" {
		device = tp.DeviceID
	}
	if tp.Type != topic.NDEATH {
		seq = p.GetSeq()
	}
	if tp.Type == topic.NBIRTH || tp.Type == topic.NDEATH {
		for _, m := range p.GetMetrics() {
			if m.GetName() == "bdSeq" {
				bdSeq = m.GetLongValue()
			}
		}
	}
	emit("published", field{"type", tp.Type.String()}, field{"device", device}, field{"seq", seq}, field{"bdSeq", bdSeq})
}

// received sees each message before the library: the STATE of the primary
// host, and the NCMD rebirths the library answers without a callback.
func (e *edgeRole) received(t string, data []byte) {
	tp, err := topic.Parse(t)
	if err != nil {
		return
	}
	switch tp.Type {
	case topic.STATE:
		var s struct {
			Online bool `json:"online"`
		}
		if json.Unmarshal(data, &s) != nil {
			return
		}
		e.hostMu.Lock()
		changed := e.hostOnline == nil || *e.hostOnline != s.Online
		e.hostOnline = &s.Online
		e.hostMu.Unlock()
		if changed {
			emit("primary-host", field{"hostId", e.primaryHost}, field{"online", s.Online})
		}
	case topic.NCMD:
		p, err := payload.Decode(data)
		if err != nil {
			return
		}
		for _, m := range p.GetMetrics() {
			if m.GetName() == rebirthMetric && m.GetBooleanValue() {
				emit("command", field{"device", nil}, field{"metrics", e.commandDocs("", p.GetMetrics())}, field{"applied", true})
				return
			}
		}
	}
}

// commandDocs are the metrics of a command as the contract writes them.
func (e *edgeRole) commandDocs(device string, ms []*pb.Payload_Metric) []map[string]any {
	out := []map[string]any{}
	for _, m := range ms {
		name, fm, _ := e.resolve(device, m)
		doc := map[string]any{"name": nil, "datatype": nil, "value": nil}
		if name != "" {
			doc["name"] = name
		}
		if m.Datatype == nil && fm.Datatype != "" {
			m = withDatatype(m, fm.Datatype)
		}
		if m.Datatype != nil {
			doc["datatype"] = datatypeName(m.GetDatatype())
			if v, err := jsonValue(m, nil); err == nil {
				doc["value"] = v
			}
		}
		if name == "" && m.Alias != nil {
			doc["alias"] = m.GetAlias()
		}
		out = append(out, doc)
	}
	return out
}

// resolve returns the name of a command metric, by name or alias, and the
// fixture metric of that name.
func (e *edgeRole) resolve(device string, m *pb.Payload_Metric) (string, fxMetric, bool) {
	name := m.GetName()
	if name == "" && m.Alias != nil {
		name, _ = e.node.ResolveAlias(m.GetAlias())
	}
	if name == rebirthMetric {
		return name, fxMetric{Name: name, Datatype: "Boolean"}, true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	fm, ok := e.fx.metric(device, name)
	return name, fm, ok
}

func withDatatype(m *pb.Payload_Metric, dt string) *pb.Payload_Metric {
	d, err := dataType(dt)
	if err != nil {
		return m
	}
	c := cloneMetric(m)
	v := uint32(d)
	c.Datatype = &v
	return c
}

// command applies the writes of an NCMD or a DCMD to writable metrics and
// reports each new value in an NDATA or DDATA, by alias.
func (e *edgeRole) command(device string, ms []*pb.Payload_Metric) {
	docs := e.commandDocs(device, ms)
	applied := false
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	for _, m := range ms {
		name, fm, ok := e.resolve(device, m)
		if !ok || !fm.Writable || (device != "" && !e.fx.hasDevice(device)) {
			continue
		}
		if m.Datatype != nil && datatypeName(m.GetDatatype()) != fm.Datatype {
			diag("%s %q: written as %s, the metric is %s; ignored", scopeName(device), name, datatypeName(m.GetDatatype()), fm.Datatype)
			continue
		}
		v, err := jsonValue(withDatatype(m, fm.Datatype), nil)
		if err != nil {
			diag("%s %q: %v; ignored", scopeName(device), name, err)
			continue
		}
		out, err := newMetric("", fm.Alias, fm.Datatype, normalize(v))
		if err != nil {
			diag("%s %q: %v; ignored", scopeName(device), name, err)
			continue
		}
		e.mu.Lock()
		e.fx.set(device, name, normalize(v))
		e.mu.Unlock()
		if device == "" {
			err = e.node.PublishNodeData(ctx, []*pb.Payload_Metric{out})
		} else {
			err = e.node.PublishDeviceData(ctx, device, []*pb.Payload_Metric{out})
		}
		if err != nil {
			diag("report the write of %q: %v", name, err)
			continue
		}
		applied = true
	}
	var dev any
	if device != "" {
		dev = device
	}
	emit("command", field{"device", dev}, field{"metrics", docs}, field{"applied", applied})
}

// normalize turns a JSON value of jsonValue into one goValue reads.
func normalize(v any) any {
	switch x := v.(type) {
	case int64:
		return float64(x)
	case uint64:
		return float64(x)
	}
	return v
}

// ---- operations ----

func (e *edgeRole) publishData(p map[string]any) (any, error) {
	n, ok := p["update"].(json.Number)
	if !ok {
		return nil, invalid("update is missing or not a number")
	}
	i, err := strconv.Atoi(string(n))
	if err != nil || i < 0 || i >= len(e.fx.Updates) {
		return nil, invalid("no update %s in fixture %s", n, e.fixtureName)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	u := e.fx.Updates[i]
	build := func(device string, vs []fxValue) ([]*pb.Payload_Metric, error) {
		var out []*pb.Payload_Metric
		for _, v := range vs {
			fm, _ := e.fx.metric(device, v.Name)
			m, err := newMetric("", fm.Alias, fm.Datatype, v.Value)
			if err != nil {
				return nil, err
			}
			out = append(out, m)
			e.fx.set(device, v.Name, v.Value)
		}
		return out, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	published := []string{}
	if len(u.Node) > 0 {
		ms, err := build("", u.Node)
		if err != nil {
			return nil, invalid("%v", err)
		}
		if err := e.node.PublishNodeData(ctx, ms); err != nil {
			return nil, notConnected(err)
		}
		published = append(published, "NDATA")
	}
	for _, d := range e.fx.Devices {
		vs, ok := u.Devices[d.ID]
		if !ok {
			continue
		}
		ms, err := build(d.ID, vs)
		if err != nil {
			return nil, invalid("%v", err)
		}
		if e.offline[d.ID] {
			continue // its new values are in its next DBIRTH
		}
		if err := e.node.PublishDeviceData(ctx, d.ID, ms); err != nil {
			return nil, notConnected(err)
		}
		published = append(published, "DDATA/"+d.ID)
	}
	return map[string]any{"published": published}, nil
}

func (e *edgeRole) rebirth(map[string]any) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	if err := e.node.Rebirth(ctx); err != nil {
		return nil, notConnected(err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	published := []string{"NBIRTH"}
	for _, d := range e.fx.Devices {
		if !e.offline[d.ID] {
			published = append(published, "DBIRTH/"+d.ID)
		}
	}
	return map[string]any{"published": published}, nil
}

func (e *edgeRole) device(p map[string]any) (string, error) {
	d, ok := p["device"].(string)
	if !ok || !e.fx.hasDevice(d) {
		return "", invalid("device is missing or not a device of fixture %s", e.fixtureName)
	}
	return d, nil
}

func (e *edgeRole) deviceDeath(p map[string]any) (any, error) {
	d, err := e.device(p)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.offline[d] {
		return nil, invalid("device %s is offline", d)
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	if err := e.node.PublishDeviceDeath(ctx, d); err != nil {
		return nil, notConnected(err)
	}
	e.offline[d] = true
	return map[string]any{"published": []string{"DDEATH/" + d}}, nil
}

func (e *edgeRole) deviceBirth(p map[string]any) (any, error) {
	d, err := e.device(p)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.offline[d] {
		return nil, invalid("device %s is online", d)
	}
	ms, err := e.fx.births(d)
	if err != nil {
		return nil, invalid("%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	if err := e.node.PublishDeviceBirth(ctx, d, ms); err != nil {
		return nil, notConnected(err)
	}
	delete(e.offline, d)
	return map[string]any{"published": []string{"DBIRTH/" + d}}, nil
}
