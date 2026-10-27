// SPDX-License-Identifier: MIT

package harness

import (
	"context"
	"fmt"
	"net"
	"time"
)

type BrokerConfig struct {
	Name string
	URL  string
	Port int
}

func DefaultBrokers() []BrokerConfig {
	return []BrokerConfig{
		{Name: "mosquitto", URL: "tcp://127.0.0.1:1883", Port: 1883},
		{Name: "hivemq-ce", URL: "tcp://127.0.0.1:1884", Port: 1884},
		{Name: "emqx", URL: "tcp://127.0.0.1:1885", Port: 1885},
		{Name: "nanomq", URL: "tcp://127.0.0.1:1886", Port: 1886},
	}
}

func WaitForBroker(ctx context.Context, broker BrokerConfig) error {
	addr := fmt.Sprintf("127.0.0.1:%d", broker.Port)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("broker %s at %s not ready: %w", broker.Name, addr, ctx.Err())
		case <-ticker.C:
			conn, err := net.DialTimeout("tcp", addr, time.Second)
			if err == nil {
				conn.Close()
				return nil
			}
		}
	}
}

func WaitForAllBrokers(ctx context.Context) error {
	for _, b := range DefaultBrokers() {
		if err := WaitForBroker(ctx, b); err != nil {
			return err
		}
	}
	return nil
}
