// SPDX-License-Identifier: MIT

// This file is not part of sparkplughost. The sparkplugb-interop image adds it
// to the package at build time (go build -overlay, see the Dockerfile); no
// upstream file is changed, and the module is still verified against go.sum.
//
// sparkplughost reports metrics, one callback each, but the container
// contract needs every Sparkplug message as the host processed it, the nodes
// it tracks and the rebirths it requests. HostApplication keeps those
// private, so this file exposes them read-only. InteropStart and InteropStop
// are Run's start-up and shutdown, with the hooks installed between
// initClients and the connection.

package sparkplughost

import (
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/EvergenEnergy/sparkplughost/protobuf"
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// InteropMessage is a Sparkplug message after the host processed it: the
// metrics the host resolved from an alias carry their name.
type InteropMessage struct {
	Type       string
	GroupID    string
	EdgeNodeID string
	DeviceID   string
	Payload    *protobuf.Payload
}

// InteropDevice and InteropNode are what the host tracks.
type InteropDevice struct {
	DeviceID string
	Online   bool
}

type InteropNode struct {
	GroupID    string
	EdgeNodeID string
	Online     bool
	BdSeq      int64
	Devices    []InteropDevice
}

// InteropHooks are called by the host's processing goroutines.
type InteropHooks struct {
	// OnMessage is called once per received message, in the order received,
	// after the host processed it (or buffered it to reorder).
	OnMessage func(InteropMessage)
	// OnRebirthRequest is called when the host requests a rebirth on its own:
	// reason "seq-gap" from the reorder timeout, "unknown-node" otherwise.
	OnRebirthRequest func(d EdgeNodeDescriptor, reason string)
}

// interopManagers holds the edge node manager of each started host: a file
// cannot add a field to HostApplication.
var interopManagers sync.Map

type interopRequester struct {
	next   rebirthRequester
	reason string
	hook   func(EdgeNodeDescriptor, string)
}

func (r interopRequester) requestNodeRebirth(d EdgeNodeDescriptor) error {
	r.hook(d, r.reason)
	return r.next.requestNodeRebirth(d)
}

type interopProcessor struct {
	next messageProcessor
	mgr  *edgeNodeManager
	hook func(InteropMessage)
}

func (p *interopProcessor) processMessage(msg sparkplugMessage) {
	p.next.processMessage(msg)
	d := msg.topic.edgeNodeDescriptor()
	p.mgr.mu.Lock()
	if m, ok := p.mgr.metrics[d]; ok {
		// A message buffered to reorder is not resolved yet: use the aliases of
		// the last birth, as the host will.
		for _, metric := range msg.payload.GetMetrics() {
			if metric.Name == nil && metric.Alias != nil {
				if name, found := m.aliasToName[metric.GetAlias()]; found {
					metric.Name = &name
				}
			}
		}
	}
	p.mgr.mu.Unlock()
	p.hook(InteropMessage{
		Type:       string(msg.topic.messageType),
		GroupID:    msg.topic.groupID,
		EdgeNodeID: msg.topic.edgeNodeID,
		DeviceID:   msg.topic.deviceID,
		Payload:    msg.payload,
	})
}

// InteropStart connects like Run, with hooks, and returns once the host
// received its own STATE birth back (so its subscriptions are active), or
// with an error when the broker cannot be reached within timeout.
func (h *HostApplication) InteropStart(hooks InteropHooks, timeout time.Duration) error {
	h.shuttingDown.Store(false)
	h.initClients()

	unknown := interopRequester{next: h.commandPublisher, reason: "unknown-node", hook: hooks.OnRebirthRequest}
	gap := interopRequester{next: h.commandPublisher, reason: "seq-gap", hook: hooks.OnRebirthRequest}
	mgr := newEdgeNodeManager(h.metricHandler, unknown, h.logger)
	var next messageProcessor = mgr
	if h.reorderTimeout > 0 {
		next = newInOrderProcessor(h.reorderTimeout, mgr, gap)
	}
	h.messageProcessor = &interopProcessor{next: next, mgr: mgr, hook: hooks.OnMessage}
	interopManagers.Store(h, mgr)

	deadline := time.Now().Add(timeout)
	for brokerURL, client := range h.mqttClients {
		online := make(chan struct{})
		var once sync.Once
		willTS := h.lastWillMessageTimestamps[brokerURL].UnixMilli()
		// A filter other than the host's own, so the library's route for its
		// STATE topic does not replace this one.
		client.AddRoute(sparkplugbNamespace+"/STATE/+", func(_ mqtt.Client, m mqtt.Message) {
			var s statusPayload
			if m.Topic() == h.stateTopic() && json.Unmarshal(m.Payload(), &s) == nil && s.Online && s.Timestamp == willTS {
				once.Do(func() { close(online) })
			}
		})
		token := client.Connect()
		if !token.WaitTimeout(time.Until(deadline)) {
			return errors.New("no CONNACK within the connect timeout")
		}
		if token.Error() != nil {
			return token.Error()
		}
		select {
		case <-online:
		case <-time.After(time.Until(deadline)):
			return errors.New("the STATE birth did not come back within the connect timeout")
		}
	}
	return nil
}

// InteropConnected reports whether every MQTT connection is up.
func (h *HostApplication) InteropConnected() bool {
	for _, client := range h.mqttClients {
		if !client.IsConnectionOpen() {
			return false
		}
	}
	return true
}

// InteropStop is Run's shutdown: STATE offline, then disconnect.
func (h *HostApplication) InteropStop() error {
	h.shuttingDown.Store(true)
	for _, token := range h.publishOnlineStatus(false, time.Now().UTC()) {
		if token.Wait() && token.Error() != nil {
			return token.Error()
		}
	}
	for _, client := range h.mqttClients {
		client.Disconnect(uint(h.disconnectTimeout.Milliseconds()))
	}
	return nil
}

// InteropNodes returns the edge nodes the host tracks, sorted.
func (h *HostApplication) InteropNodes() []InteropNode {
	v, ok := interopManagers.Load(h)
	if !ok {
		return nil
	}
	mgr := v.(*edgeNodeManager)
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	nodes := make([]InteropNode, 0, len(mgr.nodes))
	for _, n := range mgr.nodes {
		node := InteropNode{GroupID: n.descriptor.GroupID, EdgeNodeID: n.descriptor.EdgeNodeID,
			Online: n.online, BdSeq: n.birthSequenceNumber, Devices: []InteropDevice{}}
		for id, d := range n.devices {
			node.Devices = append(node.Devices, InteropDevice{DeviceID: id, Online: d.online})
		}
		sort.Slice(node.Devices, func(i, j int) bool { return node.Devices[i].DeviceID < node.Devices[j].DeviceID })
		nodes = append(nodes, node)
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].GroupID != nodes[j].GroupID {
			return nodes[i].GroupID < nodes[j].GroupID
		}
		return nodes[i].EdgeNodeID < nodes[j].EdgeNodeID
	})
	return nodes
}
