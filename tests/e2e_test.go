//go:build e2e

// SPDX-License-Identifier: MIT

package tests

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-sparkplugb/edge"
	"github.com/otfabric/go-sparkplugb/host"
	"github.com/otfabric/go-sparkplugb/mqtt"
	"github.com/otfabric/go-sparkplugb/payload"
	pb "github.com/otfabric/go-sparkplugb/proto"
	"github.com/otfabric/sparkplugb-interop/harness"
)

func TestEdgeLifecycle(t *testing.T) {
	for _, broker := range harness.DefaultBrokers() {
		t.Run(broker.Name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			tr := mqtt.NewPahoTransport()
			n, err := edge.New(tr, edge.Config{
				GroupID:    "interopGroup",
				EdgeNodeID: fmt.Sprintf("lifecycle-%s", broker.Name),
				BrokerURL:  broker.URL,
			})
			if err != nil {
				t.Fatal(err)
			}

			if err := n.Connect(ctx); err != nil {
				t.Fatalf("connect to %s: %v", broker.Name, err)
			}

			m, _ := payload.NewMetric("temperature", pb.DataType_Double, float64(22.5))
			if err := n.PublishBirth(ctx, []*pb.Payload_Metric{m}); err != nil {
				t.Fatalf("publish birth: %v", err)
			}

			dm, _ := payload.NewMetric("pressure", pb.DataType_Float, float32(101.3))
			if err := n.PublishDeviceBirth(ctx, "sensor1", []*pb.Payload_Metric{dm}); err != nil {
				t.Fatalf("publish device birth: %v", err)
			}

			updated, _ := payload.NewMetric("temperature", pb.DataType_Double, float64(23.1))
			if err := n.PublishNodeData(ctx, []*pb.Payload_Metric{updated}); err != nil {
				t.Fatalf("publish node data: %v", err)
			}

			if err := n.PublishDeviceDeath(ctx, "sensor1"); err != nil {
				t.Fatalf("publish device death: %v", err)
			}

			if err := n.Disconnect(ctx); err != nil {
				t.Fatalf("disconnect: %v", err)
			}

			if n.State() != edge.Disconnected {
				t.Errorf("state = %s, want disconnected", n.State())
			}
		})
	}
}

func TestHostDiscovery(t *testing.T) {
	for _, broker := range harness.DefaultBrokers() {
		t.Run(broker.Name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			var discovered sync.WaitGroup
			discovered.Add(1)

			hostTr := mqtt.NewPahoTransport()
			app, err := host.New(hostTr, host.Config{
				HostID:    fmt.Sprintf("host-discover-%s", broker.Name),
				BrokerURL: broker.URL,
				OnNodeBirth: func(groupID, nodeID string, metrics []*pb.Payload_Metric) {
					if groupID == "discoverGroup" && nodeID == fmt.Sprintf("edge-%s", broker.Name) {
						discovered.Done()
					}
				},
			})
			if err != nil {
				t.Fatal(err)
			}

			if err := app.Connect(ctx); err != nil {
				t.Fatalf("host connect: %v", err)
			}
			defer app.Close(ctx)

			// Small delay for subscriptions to propagate
			time.Sleep(200 * time.Millisecond)

			edgeTr := mqtt.NewPahoTransport()
			n, err := edge.New(edgeTr, edge.Config{
				GroupID:    "discoverGroup",
				EdgeNodeID: fmt.Sprintf("edge-%s", broker.Name),
				BrokerURL:  broker.URL,
			})
			if err != nil {
				t.Fatal(err)
			}

			if err := n.Connect(ctx); err != nil {
				t.Fatalf("edge connect: %v", err)
			}
			defer n.Close(ctx)

			m, _ := payload.NewMetric("x", pb.DataType_Int32, int32(42))
			if err := n.PublishBirth(ctx, []*pb.Payload_Metric{m}); err != nil {
				t.Fatalf("edge birth: %v", err)
			}

			done := make(chan struct{})
			go func() {
				discovered.Wait()
				close(done)
			}()

			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("timed out waiting for host to discover edge node")
			}

			ns := app.GetNode("discoverGroup", fmt.Sprintf("edge-%s", broker.Name))
			if ns == nil {
				t.Fatal("node not found in host state")
			}
			if !ns.Online {
				t.Error("node should be online")
			}
		})
	}
}

func TestAliasRoundTrip(t *testing.T) {
	for _, broker := range harness.DefaultBrokers() {
		t.Run(broker.Name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			var dataReceived sync.WaitGroup
			dataReceived.Add(1)

			hostTr := mqtt.NewPahoTransport()
			app, err := host.New(hostTr, host.Config{
				HostID:    fmt.Sprintf("host-alias-%s", broker.Name),
				BrokerURL: broker.URL,
				OnNodeData: func(groupID, nodeID string, metrics []*pb.Payload_Metric) {
					if groupID == "aliasGroup" {
						dataReceived.Done()
					}
				},
			})
			if err != nil {
				t.Fatal(err)
			}

			if err := app.Connect(ctx); err != nil {
				t.Fatalf("host connect: %v", err)
			}
			defer app.Close(ctx)
			time.Sleep(200 * time.Millisecond)

			edgeTr := mqtt.NewPahoTransport()
			n, err := edge.New(edgeTr, edge.Config{
				GroupID:    "aliasGroup",
				EdgeNodeID: fmt.Sprintf("edge-alias-%s", broker.Name),
				BrokerURL:  broker.URL,
			})
			if err != nil {
				t.Fatal(err)
			}

			if err := n.Connect(ctx); err != nil {
				t.Fatalf("edge connect: %v", err)
			}
			defer n.Close(ctx)

			// Birth with aliases
			m1, _ := payload.NewMetricWithAlias("temperature", 1, pb.DataType_Double, float64(22.5))
			m2, _ := payload.NewMetricWithAlias("humidity", 2, pb.DataType_Float, float32(55.0))
			if err := n.PublishBirth(ctx, []*pb.Payload_Metric{m1, m2}); err != nil {
				t.Fatal(err)
			}

			time.Sleep(100 * time.Millisecond)

			// Data with alias-only metrics
			a1, _ := payload.NewAliasMetric(1, pb.DataType_Double, float64(23.1))
			a2, _ := payload.NewAliasMetric(2, pb.DataType_Float, float32(56.2))
			if err := n.PublishNodeData(ctx, []*pb.Payload_Metric{a1, a2}); err != nil {
				t.Fatal(err)
			}

			done := make(chan struct{})
			go func() {
				dataReceived.Wait()
				close(done)
			}()

			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("timed out waiting for alias data")
			}

			// Verify host resolved aliases
			edgeID := fmt.Sprintf("edge-alias-%s", broker.Name)
			name, ok := app.ResolveAlias("aliasGroup", edgeID, 1)
			if !ok || name != "temperature" {
				t.Errorf("ResolveAlias(1) = %q, %v; want temperature", name, ok)
			}
			name, ok = app.ResolveAlias("aliasGroup", edgeID, 2)
			if !ok || name != "humidity" {
				t.Errorf("ResolveAlias(2) = %q, %v; want humidity", name, ok)
			}
		})
	}
}

func TestRebirthCycle(t *testing.T) {
	for _, broker := range harness.DefaultBrokers() {
		t.Run(broker.Name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			birthCount := 0
			var mu sync.Mutex
			var secondBirth sync.WaitGroup
			secondBirth.Add(1)

			hostTr := mqtt.NewPahoTransport()
			app, err := host.New(hostTr, host.Config{
				HostID:    fmt.Sprintf("host-rebirth-%s", broker.Name),
				BrokerURL: broker.URL,
				OnNodeBirth: func(groupID, nodeID string, metrics []*pb.Payload_Metric) {
					if groupID == "rebirthGroup" {
						mu.Lock()
						birthCount++
						if birthCount == 2 {
							secondBirth.Done()
						}
						mu.Unlock()
					}
				},
			})
			if err != nil {
				t.Fatal(err)
			}

			if err := app.Connect(ctx); err != nil {
				t.Fatalf("host connect: %v", err)
			}
			defer app.Close(ctx)
			time.Sleep(200 * time.Millisecond)

			edgeTr := mqtt.NewPahoTransport()
			n, err := edge.New(edgeTr, edge.Config{
				GroupID:    "rebirthGroup",
				EdgeNodeID: fmt.Sprintf("edge-rebirth-%s", broker.Name),
				BrokerURL:  broker.URL,
			})
			if err != nil {
				t.Fatal(err)
			}

			if err := n.Connect(ctx); err != nil {
				t.Fatalf("edge connect: %v", err)
			}
			defer n.Close(ctx)

			m, _ := payload.NewMetric("x", pb.DataType_Int32, int32(0))
			if err := n.PublishBirth(ctx, []*pb.Payload_Metric{m}); err != nil {
				t.Fatal(err)
			}

			time.Sleep(200 * time.Millisecond)

			// Host requests rebirth
			edgeID := fmt.Sprintf("edge-rebirth-%s", broker.Name)
			if err := app.RequestRebirth(ctx, "rebirthGroup", edgeID); err != nil {
				t.Fatalf("request rebirth: %v", err)
			}

			done := make(chan struct{})
			go func() {
				secondBirth.Wait()
				close(done)
			}()

			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("timed out waiting for rebirth")
			}

			mu.Lock()
			if birthCount < 2 {
				t.Errorf("birthCount = %d, want >= 2", birthCount)
			}
			mu.Unlock()
		})
	}
}
