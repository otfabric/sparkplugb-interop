// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"sync"

	"github.com/otfabric/go-sparkplugb/mqtt"
)

// tap is the mqtt.Transport of a role: go-sparkplugb's Paho transport, with
// what the contract reports and the library's API does not tell. It sees each
// publish after it succeeded (the edge's "published" events, whoever
// published: the adapter or the library on its own, as for a rebirth on an
// NCMD or the NDEATH before a disconnect) and each message before the library
// does (the edge's commands that the library answers itself, the primary
// host's STATE). It tells a connection that ends on its own from a
// disconnect of the library.
type tap struct {
	inner mqtt.Transport

	onPublish func(topic string, payload []byte)
	onMessage func(topic string, payload []byte)
	onLost    func(err error)

	mu            sync.Mutex
	disconnecting bool
}

func (t *tap) Connect(ctx context.Context, opts mqtt.ConnectOpts) error {
	t.mu.Lock()
	t.disconnecting = false
	t.mu.Unlock()
	lost := opts.OnConnectionLost
	opts.OnConnectionLost = func(err error) {
		t.mu.Lock()
		ours := t.disconnecting
		t.mu.Unlock()
		if !ours && t.onLost != nil {
			t.onLost(err)
		}
		if lost != nil {
			lost(err)
		}
	}
	return t.inner.Connect(ctx, opts)
}

func (t *tap) Publish(ctx context.Context, topic string, qos byte, retained bool, payload []byte) error {
	if err := t.inner.Publish(ctx, topic, qos, retained, payload); err != nil {
		return err
	}
	if t.onPublish != nil {
		t.onPublish(topic, payload)
	}
	return nil
}

func (t *tap) Subscribe(ctx context.Context, topic string, qos byte, handler mqtt.MessageHandler) error {
	return t.inner.Subscribe(ctx, topic, qos, func(topic string, payload []byte) {
		if t.onMessage != nil {
			t.onMessage(topic, payload)
		}
		handler(topic, payload)
	})
}

func (t *tap) Disconnect(ctx context.Context) error {
	t.mu.Lock()
	t.disconnecting = true
	t.mu.Unlock()
	return t.inner.Disconnect(ctx)
}
