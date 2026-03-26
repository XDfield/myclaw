package channel

import (
	"context"
	"fmt"
	"sync"

	"github.com/stellarlinkco/myclaw/internal/bus"
	"github.com/stellarlinkco/myclaw/internal/config"
	"github.com/stellarlinkco/myclaw/internal/logging"
)

var chlog = logging.Component("channel-mgr")

type ChannelManager struct {
	channels map[string]Channel
	bus      *bus.MessageBus
}

func NewChannelManager(cfg config.ChannelsConfig, b *bus.MessageBus) (*ChannelManager, error) {
	m := &ChannelManager{
		channels: make(map[string]Channel),
		bus:      b,
	}

	if cfg.Telegram.Enabled {
		ch, err := NewTelegramChannel(cfg.Telegram, b)
		if err != nil {
			return nil, fmt.Errorf("init telegram channel: %w", err)
		}
		m.channels[ch.Name()] = ch
		b.SubscribeOutbound(ch.Name(), func(msg bus.OutboundMessage) {
			if err := ch.Send(msg); err != nil {
				chlog.Error().Err(err).Str("channel", ch.Name()).Msg("send failed")
			}
		})
	}

	if cfg.Feishu.Enabled {
		ch, err := NewFeishuChannel(cfg.Feishu, b)
		if err != nil {
			return nil, fmt.Errorf("init feishu channel: %w", err)
		}
		m.channels[ch.Name()] = ch
		b.SubscribeOutbound(ch.Name(), func(msg bus.OutboundMessage) {
			if err := ch.Send(msg); err != nil {
				chlog.Error().Err(err).Str("channel", ch.Name()).Msg("send failed")
			}
		})
	}

	if cfg.WeCom.Enabled {
		ch, err := NewWeComChannel(cfg.WeCom, b)
		if err != nil {
			return nil, fmt.Errorf("init wecom channel: %w", err)
		}
		m.channels[ch.Name()] = ch
		b.SubscribeOutbound(ch.Name(), func(msg bus.OutboundMessage) {
			if err := ch.Send(msg); err != nil {
				chlog.Error().Err(err).Str("channel", ch.Name()).Msg("send failed")
			}
		})
	}

	if cfg.WhatsApp.Enabled {
		ch, err := NewWhatsApp(cfg.WhatsApp, b)
		if err != nil {
			return nil, fmt.Errorf("create whatsapp channel: %w", err)
		}
		m.channels[ch.Name()] = ch
		b.SubscribeOutbound(ch.Name(), func(msg bus.OutboundMessage) {
			if err := ch.Send(msg); err != nil {
				chlog.Error().Err(err).Str("channel", ch.Name()).Msg("send failed")
			}
		})
	}

	if cfg.WeChat.Enabled {
		ch, err := NewWeChatChannel(cfg.WeChat, b)
		if err != nil {
			return nil, fmt.Errorf("init wechat channel: %w", err)
		}
		m.channels[ch.Name()] = ch
		b.SubscribeOutbound(ch.Name(), func(msg bus.OutboundMessage) {
			if err := ch.Send(msg); err != nil {
				chlog.Error().Err(err).Str("channel", ch.Name()).Msg("send failed")
			}
		})
	}

	return m, nil
}

func NewChannelManagerWithGateway(cfg config.ChannelsConfig, gwCfg config.GatewayConfig, b *bus.MessageBus) (*ChannelManager, error) {
	m, err := NewChannelManager(cfg, b)
	if err != nil {
		return nil, err
	}

	if cfg.WebUI.Enabled {
		ch, err := NewWebUIChannel(cfg.WebUI, gwCfg, b)
		if err != nil {
			return nil, fmt.Errorf("init webui channel: %w", err)
		}
		m.channels[ch.Name()] = ch
		b.SubscribeOutbound(ch.Name(), func(msg bus.OutboundMessage) {
			if err := ch.Send(msg); err != nil {
				chlog.Error().Err(err).Str("channel", ch.Name()).Msg("send failed")
			}
		})
	}

	return m, nil
}

func (m *ChannelManager) StartAll(ctx context.Context) error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(m.channels))

	for name, ch := range m.channels {
		wg.Add(1)
		go func(name string, ch Channel) {
			defer wg.Done()
			chlog.Info().Str("channel", name).Msg("starting")
			if err := ch.Start(ctx); err != nil {
				errCh <- fmt.Errorf("%s: %w", name, err)
			}
		}(name, ch)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		return err
	}
	return nil
}

func (m *ChannelManager) StopAll() error {
	for name, ch := range m.channels {
		chlog.Info().Str("channel", name).Msg("stopping")
		if err := ch.Stop(); err != nil {
			chlog.Error().Err(err).Str("channel", name).Msg("error stopping")
		}
	}
	return nil
}

func (m *ChannelManager) EnabledChannels() []string {
	names := make([]string, 0, len(m.channels))
	for name := range m.channels {
		names = append(names, name)
	}
	return names
}

// GetChannel returns a channel by name, or nil if not found.
func (m *ChannelManager) GetChannel(name string) Channel {
	return m.channels[name]
}
