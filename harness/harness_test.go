// SPDX-License-Identifier: MIT

package harness

import "testing"

func TestBrokerConfigDefaults(t *testing.T) {
	brokers := DefaultBrokers()
	if len(brokers) != 4 {
		t.Fatalf("expected 4 brokers, got %d", len(brokers))
	}

	expected := map[string]int{
		"mosquitto": 1883,
		"hivemq-ce": 1884,
		"emqx":      1885,
		"nanomq":    1886,
	}
	for _, b := range brokers {
		port, ok := expected[b.Name]
		if !ok {
			t.Errorf("unexpected broker %q", b.Name)
			continue
		}
		if b.Port != port {
			t.Errorf("broker %s: port = %d, want %d", b.Name, b.Port, port)
		}
		if b.URL == "" {
			t.Errorf("broker %s: URL is empty", b.Name)
		}
	}
}
