// SPDX-License-Identifier: MIT

// Not part of sparkplughost: the image adds it to the vendored package with
// zz_interop.go and runs it before the build (Dockerfile).

package sparkplughost

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/EvergenEnergy/sparkplughost/protobuf"
)

type countingProcessor struct{ n int }

func (p *countingProcessor) processMessage(sparkplugMessage) { p.n++ }

type nopRequester struct{}

func (nopRequester) requestNodeRebirth(EdgeNodeDescriptor) error { return nil }

// TestInteropHookThatDoesNotReturn: a hook that does not return normally is
// logged, and the next message is processed and reported.
func TestInteropHookThatDoesNotReturn(t *testing.T) {
	var log bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&log, nil))
	next := &countingProcessor{}
	var reported []string
	p := &interopProcessor{
		next:   next,
		mgr:    newEdgeNodeManager(func(HostMetric) {}, nopRequester{}, logger),
		logger: logger,
		hook: func(m InteropMessage) {
			reported = append(reported, m.EdgeNodeID)
			if m.EdgeNodeID == "first" {
				panic("hook stops")
			}
		},
	}
	for _, node := range []string{"first", "second"} {
		p.processMessage(sparkplugMessage{
			topic:   topic{namespace: sparkplugbNamespace, messageType: messageTypeNDATA, groupID: "g", edgeNodeID: node},
			payload: &protobuf.Payload{},
		})
	}
	if next.n != 2 || strings.Join(reported, ",") != "first,second" {
		t.Errorf("processed %d, reported %v; want 2 and [first second]", next.n, reported)
	}
	if !strings.Contains(log.String(), "did not return normally") || !strings.Contains(log.String(), "edge_node=first") {
		t.Errorf("log %q", log.String())
	}
}
