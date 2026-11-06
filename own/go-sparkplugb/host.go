// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"fmt"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/otfabric/go-sparkplugb/host"
	"github.com/otfabric/go-sparkplugb/mqtt"
	pb "github.com/otfabric/go-sparkplugb/proto"
)

// rebirthWarning is the format of go-sparkplugb's warning when its host asks
// an edge node for a rebirth on its own (host.App.askRebirth): the library has
// no callback for it, so its log is the rebirth-requested event.
const rebirthWarning = "%s/%s: %s — requesting rebirth"

// hostRole is the primary host application: go-sparkplugb's host.App.
type hostRole struct {
	app *host.App

	mu      sync.Mutex
	members map[string]string // template definition + "\x00" + member -> datatype, from births
}

func runHost(f flags) int {
	hostID := f.str("host-id", "")
	h := &hostRole{members: map[string]string{}}
	lost := make(chan error, 1)
	paho := mqtt.NewPahoTransport()
	paho.ConnectTimeout = time.Duration(f.int("connect-timeout-ms", 10000)) * time.Millisecond
	tr := &tap{inner: paho, onLost: func(err error) {
		select {
		case lost <- err:
		default:
		}
	}}
	app, err := host.New(tr, host.Config{
		HostID:        hostID,
		BrokerURL:     f.str("broker", ""),
		ClientID:      f.str("client-id", fmt.Sprintf("%s-host-%s", adapter, hostID)),
		Logger:        stderrLogger{onWarn: h.warned},
		OnNodeBirth:   h.nodeBirth,
		OnDeviceBirth: h.deviceBirth,
		OnNodeData:    h.nodeData,
		OnDeviceData:  h.deviceData,
		OnNodeDeath:   h.nodeDeath,
		OnDeviceDeath: h.deviceDeath,
	})
	if err != nil {
		diag("host: %v", err)
		return exitFailed
	}
	h.app = app
	if err := serveControl(f.str("control-socket", defaultSocket), map[string]handler{
		"send-command": h.sendCommand, "request-rebirth": h.requestRebirth, "nodes": h.nodes,
	}); err != nil {
		diag("control socket: %v", err)
		return exitFailed
	}
	ctx, cancel := context.WithTimeout(context.Background(), paho.ConnectTimeout+opTimeout)
	err = app.Connect(ctx)
	cancel()
	if err != nil {
		diag("connect: %v", err)
		return exitConnect
	}
	if err := writeReady(f.str("ready-file", defaultReady)); err != nil {
		diag("ready file: %v", err)
		return exitFailed
	}
	emit("ready", field{"adapter", adapter}, field{"hostId", hostID})

	stop, cancelStop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancelStop()
	select {
	case <-stop.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		if err := app.Close(ctx); err != nil {
			diag("stop: %v", err)
		}
		cancel()
		emit("stopped")
		return 0
	case err := <-lost:
		emit("connection-lost", field{"detail", err.Error()})
		return exitFailed
	}
}

func (h *hostRole) warned(format string, args []any) {
	if format != rebirthWarning || len(args) != 3 {
		return
	}
	reason := "seq-gap"
	if r, _ := args[2].(string); strings.Contains(r, "before its NBIRTH") {
		reason = "unknown-node"
	}
	emit("rebirth-requested", field{"group", args[0]}, field{"edgeNode", args[1]}, field{"reason", reason})
}

// message writes the "message" event. The handlers run one after the other,
// right after the library applied the message, so the node's state holds its
// seq, bdSeq, aliases and datatypes.
func (h *hostRole) message(typ, group, node, device string, metrics []*pb.Payload_Metric) {
	ns := h.app.GetNode(group, node)
	var seq, bdSeq, dev any
	if ns != nil && typ != "NDEATH" {
		seq = ns.LastSeq
	}
	if device != "" {
		dev = device
	}
	if ns != nil && typ == "NDEATH" {
		bdSeq = ns.BdSeq
	}
	var (
		aliases map[uint64]string
		known   map[string]*pb.Payload_Metric
	)
	if ns != nil {
		aliases, known = ns.Aliases, ns.Metrics
		if ds := ns.Devices[device]; device != "" && ds != nil {
			aliases, known = ds.Aliases, ds.Metrics
		}
	}
	birth := typ == "NBIRTH" || typ == "DBIRTH"
	docs := []map[string]any{}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, m := range metrics {
		if typ == "NBIRTH" && m.GetName() == "bdSeq" {
			bdSeq = m.GetLongValue()
			continue
		}
		name := m.GetName()
		if name == "" && m.Alias != nil {
			name = aliases[m.GetAlias()]
		}
		if m.Datatype == nil && known[name] != nil && known[name].Datatype != nil {
			c := cloneMetric(m)
			d := known[name].GetDatatype()
			c.Datatype = &d
			m = c
		}
		if birth {
			if t := m.GetTemplateValue(); t.GetIsDefinition() {
				for _, member := range t.GetMetrics() {
					h.members[name+"\x00"+member.GetName()] = datatypeName(member.GetDatatype())
				}
			}
		}
		doc := map[string]any{"name": nil, "datatype": nil, "value": nil}
		if name != "" {
			doc["name"] = name
		} else {
			doc["alias"] = m.GetAlias()
		}
		if m.Datatype != nil {
			doc["datatype"] = datatypeName(m.GetDatatype())
			v, err := jsonValue(m, h.members)
			if err != nil {
				diag("%s %s/%s/%s: metric %s: %v", typ, group, node, device, name, err)
			}
			doc["value"] = v
		}
		if props, err := jsonProperties(m); err != nil {
			diag("%s %s/%s/%s: metric %s: %v", typ, group, node, device, name, err)
		} else if props != nil {
			doc["properties"] = props
		}
		docs = append(docs, doc)
	}
	emit("message", field{"type", typ}, field{"group", group}, field{"edgeNode", node}, field{"device", dev},
		field{"seq", seq}, field{"bdSeq", bdSeq}, field{"metrics", docs})
}

func (h *hostRole) nodeBirth(g, n string, ms []*pb.Payload_Metric) { h.message("NBIRTH", g, n, "", ms) }
func (h *hostRole) nodeData(g, n string, ms []*pb.Payload_Metric)  { h.message("NDATA", g, n, "", ms) }
func (h *hostRole) nodeDeath(g, n string)                          { h.message("NDEATH", g, n, "", nil) }
func (h *hostRole) deviceBirth(g, n, d string, ms []*pb.Payload_Metric) {
	h.message("DBIRTH", g, n, d, ms)
}
func (h *hostRole) deviceData(g, n, d string, ms []*pb.Payload_Metric) {
	h.message("DDATA", g, n, d, ms)
}
func (h *hostRole) deviceDeath(g, n, d string) { h.message("DDEATH", g, n, d, nil) }

// ---- operations ----

func target(p map[string]any) (group, node, device string, err error) {
	for _, k := range []string{"group", "edgeNode"} {
		v, ok := p[k].(string)
		if !ok || !idPattern.MatchString(v) {
			return "", "", "", invalid("%s is missing or invalid", k)
		}
	}
	group, node = p["group"].(string), p["edgeNode"].(string)
	if v, present := p["device"]; present && v != nil {
		s, ok := v.(string)
		if !ok || !idPattern.MatchString(s) {
			return "", "", "", invalid("device is invalid")
		}
		device = s
	}
	return group, node, device, nil
}

func (h *hostRole) sendCommand(p map[string]any) (any, error) {
	group, node, device, err := target(p)
	if err != nil {
		return nil, err
	}
	list, ok := p["metrics"].([]any)
	if !ok || len(list) == 0 {
		return nil, invalid("metrics is missing or empty")
	}
	metrics := make([]*pb.Payload_Metric, 0, len(list))
	for _, item := range list {
		m, _ := item.(map[string]any)
		name, nameOK := m["name"].(string)
		dt, dtOK := m["datatype"].(string)
		value, valueOK := m["value"]
		if !nameOK || name == "" || !dtOK || !valueOK {
			return nil, invalid("invalid metric %v", item)
		}
		pm, err := newMetric(name, nil, dt, value)
		if err != nil {
			return nil, invalid("metric %s: %v", name, err)
		}
		metrics = append(metrics, pm)
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	if device != "" {
		if err := h.app.SendDeviceCommand(ctx, group, node, device, metrics); err != nil {
			return nil, notConnected(err)
		}
		return map[string]any{"published": []string{"DCMD/" + device}}, nil
	}
	if err := h.app.SendNodeCommand(ctx, group, node, metrics); err != nil {
		return nil, notConnected(err)
	}
	return map[string]any{"published": []string{"NCMD"}}, nil
}

func (h *hostRole) requestRebirth(p map[string]any) (any, error) {
	group, node, _, err := target(map[string]any{"group": p["group"], "edgeNode": p["edgeNode"]})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	if err := h.app.RequestRebirth(ctx, group, node); err != nil {
		return nil, notConnected(err)
	}
	return map[string]any{"published": []string{"NCMD"}}, nil
}

func (h *hostRole) nodes(map[string]any) (any, error) {
	all := h.app.Nodes()
	sort.Slice(all, func(i, j int) bool {
		if all[i].GroupID != all[j].GroupID {
			return all[i].GroupID < all[j].GroupID
		}
		return all[i].EdgeNodeID < all[j].EdgeNodeID
	})
	nodes := []map[string]any{}
	for _, n := range all {
		ids := make([]string, 0, len(n.Devices))
		for id := range n.Devices {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		devices := []map[string]any{}
		for _, id := range ids {
			devices = append(devices, map[string]any{"device": id, "online": n.Devices[id].Online})
		}
		nodes = append(nodes, map[string]any{"group": n.GroupID, "edgeNode": n.EdgeNodeID, "online": n.Online,
			"bdSeq": n.BdSeq, "devices": devices})
	}
	return map[string]any{"nodes": nodes}, nil
}
