package bus

import (
	"context"
	"sync"

	"github.com/stellarlinkco/myclaw/internal/logging"
)

var buslog = logging.Component("bus")

type MessageBus struct {
	Inbound  chan InboundMessage
	Outbound chan OutboundMessage

	mu   sync.RWMutex
	subs map[string][]func(OutboundMessage)
}

func NewMessageBus(bufSize int) *MessageBus {
	if bufSize <= 0 {
		bufSize = 100
	}
	return &MessageBus{
		Inbound:  make(chan InboundMessage, bufSize),
		Outbound: make(chan OutboundMessage, bufSize),
		subs:     make(map[string][]func(OutboundMessage)),
	}
}

func (b *MessageBus) SubscribeOutbound(channel string, fn func(OutboundMessage)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs[channel] = append(b.subs[channel], fn)
}

func (b *MessageBus) DispatchOutbound(ctx context.Context) {
	for {
		select {
		case msg := <-b.Outbound:
			b.mu.RLock()
			cbs := b.subs[msg.Channel]
			b.mu.RUnlock()
			for _, cb := range cbs {
				cb(msg)
			}
			if len(cbs) == 0 {
				buslog.Warn().Str("channel", msg.Channel).Msg("no subscriber, dropping message")
			}
		case <-ctx.Done():
			return
		}
	}
}
