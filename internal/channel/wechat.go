package channel

import (
	"context"
	"fmt"
	"strings"
	"time"

	wechatbot "github.com/corespeed-io/wechatbot/golang"
	qrterminal "github.com/mdp/qrterminal/v3"
	"github.com/stellarlinkco/myclaw/internal/bus"
	"github.com/stellarlinkco/myclaw/internal/config"
	"github.com/stellarlinkco/myclaw/internal/logging"
)

const wechatChannelName = "wechat"

var wxlog = logging.Component("wechat")

type WeChatChannel struct {
	BaseChannel
	bot    *wechatbot.Bot
	cancel context.CancelFunc
}

func NewWeChatChannel(cfg config.WeChatConfig, b *bus.MessageBus) (*WeChatChannel, error) {
	opts := wechatbot.Options{
		CredPath: cfg.CredPath,
		OnQRURL: func(url string) {
			qrterminal.Generate(url, qrterminal.L, logging.Logger)
			wxlog.Info().Msg("scan the QR code above with WeChat to log in")
		},
		OnScanned: func() {
			wxlog.Info().Msg("QR code scanned, waiting for confirmation...")
		},
		OnExpired: func() {
			wxlog.Warn().Msg("session expired, re-login required")
		},
		OnError: func(err error) {
			wxlog.Error().Err(err).Msg("error")
		},
	}

	ch := &WeChatChannel{
		BaseChannel: NewBaseChannel(wechatChannelName, b, cfg.AllowFrom),
		bot:         wechatbot.New(opts),
	}
	return ch, nil
}

func (w *WeChatChannel) Start(ctx context.Context) error {
	if _, err := w.bot.Login(ctx, false); err != nil {
		return fmt.Errorf("wechat login: %w", err)
	}
	wxlog.Info().Msg("logged in")

	w.bot.OnMessage(func(msg *wechatbot.IncomingMessage) {
		if !w.IsAllowed(msg.UserID) {
			wxlog.Warn().Str("senderID", msg.UserID).Msg("rejected message")
			return
		}

		content := msg.Text
		if content == "" {
			switch msg.Type {
			case wechatbot.ContentImage:
				content = "[image]"
			case wechatbot.ContentVoice:
				content = "[voice]"
			case wechatbot.ContentFile:
				content = "[file]"
			case wechatbot.ContentVideo:
				content = "[video]"
			}
		}
		if content == "" {
			return
		}

		w.bus.Inbound <- bus.InboundMessage{
			Channel:   wechatChannelName,
			SenderID:  msg.UserID,
			ChatID:    msg.UserID,
			Content:   content,
			Timestamp: msg.Timestamp,
		}
	})

	ctx, w.cancel = context.WithCancel(ctx)
	go func() {
		if err := w.bot.Run(ctx); err != nil && ctx.Err() == nil {
			wxlog.Error().Err(err).Msg("poll loop exited")
		}
	}()

	wxlog.Info().Msg("started")
	return nil
}

func (w *WeChatChannel) Stop() error {
	if w.cancel != nil {
		w.cancel()
	}
	w.bot.Stop()
	wxlog.Info().Msg("stopped")
	return nil
}

func (w *WeChatChannel) Send(msg bus.OutboundMessage) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const maxLen = 2000
	text := msg.Content
	for len(text) > 0 {
		chunk := text
		if len(chunk) > maxLen {
			if idx := strings.LastIndex(chunk[:maxLen], "\n"); idx > maxLen*3/10 {
				chunk = chunk[:idx]
			} else {
				chunk = chunk[:maxLen]
			}
		}
		text = text[len(chunk):]
		if err := w.bot.Send(ctx, msg.ChatID, chunk); err != nil {
			return fmt.Errorf("wechat send: %w", err)
		}
	}
	return nil
}
