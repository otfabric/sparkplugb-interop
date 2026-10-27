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

func TestRapidReconnect(t *testing.T) {
	for _, broker := range harness.DefaultBrokers() {
		t.Run(broker.Name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			tr := mqtt.NewPahoTransport()
			n, err := edge.New(tr, edge.Config{
				GroupID:    "chaosGroup",
				EdgeNodeID: fmt.Sprintf("rapid-%s", broker.Name),
				BrokerURL:  broker.URL,
			})
			if err != nil {
				t.Fatal(err)
			}

			for i := 0; i < 50; i++ {
				if err := n.Connect(ctx); err != nil {
					t.Fatalf("connect iteration %d: %v", i, err)
				}
				if err := n.Disconnect(ctx); err != nil {
					t.Fatalf("disconnect iteration %d: %v", i, err)
				}
			}

			if n.State() != edge.Disconnected {
				t.Errorf("final state = %s, want disconnected", n.State())
			}
		})
	}
}

func TestMalformedPayload(t *testing.T) {
	for _, broker := range harness.DefaultBrokers() {
		t.Run(broker.Name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			hostTr := mqtt.NewPahoTransport()
			app, err := host.New(hostTr, host.Config{
				HostID:    fmt.Sprintf("host-malformed-%s", broker.Name),
				BrokerURL: broker.URL,
			})
			if err != nil {
				t.Fatal(err)
			}

			if err := app.Connect(ctx); err != nil {
				t.Fatalf("host connect: %v", err)
			}
			defer app.Close(ctx)
			time.Sleep(200 * time.Millisecond)

			// Publish garbage on Sparkplug topics via a raw MQTT client
			rawTr := mqtt.NewPahoTransport()
			err = rawTr.Connect(ctx, mqtt.ConnectOpts{
				BrokerURL:    broker.URL,
				ClientID:     fmt.Sprintf("garbage-%s", broker.Name),
				CleanSession: true,
			})
			if err != nil {
				t.Fatalf("raw connect: %v", err)
			}
			defer rawTr.Disconnect(ctx)

			garbage := []byte{0xFF, 0xFE, 0xFD, 0xFC, 0x00, 0x01}
			topics := []string{
				"spBv1.0/chaosGroup/NBIRTH/garbage",
				"spBv1.0/chaosGroup/NDEATH/garbage",
				"spBv1.0/chaosGroup/NDATA/garbage",
				"spBv1.0/chaosGroup/DBIRTH/garbage/dev",
				"spBv1.0/chaosGroup/DDATA/garbage/dev",
				"spBv1.0/chaosGroup/DDEATH/garbage/dev",
			}

			for _, topic := range topics {
				if err := rawTr.Publish(ctx, topic, 0, false, garbage); err != nil {
					t.Fatalf("publish garbage to %s: %v", topic, err)
				}
			}

			// Give host time to process — it should not panic
			time.Sleep(500 * time.Millisecond)

			if app.State() != host.Connected {
				t.Errorf("host state = %s after garbage, want connected", app.State())
			}
		})
	}
}

func TestConcurrentEdgeNodes(t *testing.T) {
	for _, broker := range harness.DefaultBrokers() {
		t.Run(broker.Name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			const numNodes = 10
			var wg sync.WaitGroup
			errs := make(chan error, numNodes)

			for i := 0; i < numNodes; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()

					tr := mqtt.NewPahoTransport()
					n, err := edge.New(tr, edge.Config{
						GroupID:    "concurrentGroup",
						EdgeNodeID: fmt.Sprintf("node-%s-%d", broker.Name, idx),
						BrokerURL:  broker.URL,
					})
					if err != nil {
						errs <- fmt.Errorf("node %d new: %w", idx, err)
						return
					}

					if err := n.Connect(ctx); err != nil {
						errs <- fmt.Errorf("node %d connect: %w", idx, err)
						return
					}

					m, _ := payload.NewMetric("value", pb.DataType_Int32, int32(idx))
					if err := n.PublishBirth(ctx, []*pb.Payload_Metric{m}); err != nil {
						errs <- fmt.Errorf("node %d birth: %w", idx, err)
						n.Disconnect(ctx)
						return
					}

					// Publish a few data messages
					for j := 0; j < 5; j++ {
						dm, _ := payload.NewMetric("value", pb.DataType_Int32, int32(idx*100+j))
						if err := n.PublishNodeData(ctx, []*pb.Payload_Metric{dm}); err != nil {
							errs <- fmt.Errorf("node %d data %d: %w", idx, j, err)
							break
						}
					}

					if err := n.Disconnect(ctx); err != nil {
						errs <- fmt.Errorf("node %d disconnect: %w", idx, err)
					}
				}(i)
			}

			wg.Wait()
			close(errs)

			for err := range errs {
				t.Error(err)
			}
		})
	}
}

func TestConnectionDrop(t *testing.T) {
	for _, broker := range harness.DefaultBrokers() {
		t.Run(broker.Name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			tr := mqtt.NewPahoTransport()
			n, err := edge.New(tr, edge.Config{
				GroupID:    "dropGroup",
				EdgeNodeID: fmt.Sprintf("drop-%s", broker.Name),
				BrokerURL:  broker.URL,
			})
			if err != nil {
				t.Fatal(err)
			}

			if err := n.Connect(ctx); err != nil {
				t.Fatal(err)
			}

			m, _ := payload.NewMetric("x", pb.DataType_Int32, int32(0))
			if err := n.PublishBirth(ctx, []*pb.Payload_Metric{m}); err != nil {
				t.Fatal(err)
			}

			// Force disconnect the transport (simulating connection drop)
			if err := tr.Disconnect(ctx); err != nil {
				t.Fatal(err)
			}

			if n.State() != edge.Disconnected {
				t.Errorf("state after drop = %s, want disconnected", n.State())
			}

			// A new edge node instance should connect and operate normally
			tr2 := mqtt.NewPahoTransport()
			n2, err := edge.New(tr2, edge.Config{
				GroupID:    "dropGroup",
				EdgeNodeID: fmt.Sprintf("drop-%s", broker.Name),
				BrokerURL:  broker.URL,
			})
			if err != nil {
				t.Fatal(err)
			}

			if err := n2.Connect(ctx); err != nil {
				t.Fatalf("reconnect: %v", err)
			}
			defer n2.Close(ctx)

			m2, _ := payload.NewMetric("x", pb.DataType_Int32, int32(1))
			if err := n2.PublishBirth(ctx, []*pb.Payload_Metric{m2}); err != nil {
				t.Fatalf("birth after reconnect: %v", err)
			}

			if n2.State() != edge.Online {
				t.Errorf("state after reconnect = %s, want online", n2.State())
			}
		})
	}
}
