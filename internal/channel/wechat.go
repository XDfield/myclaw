package channel

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	wechatbot "github.com/corespeed-io/wechatbot/golang"
	qrterminal "github.com/mdp/qrterminal/v3"
	"github.com/stellarlinkco/myclaw/internal/bus"
	"github.com/stellarlinkco/myclaw/internal/config"
)

const wechatChannelName = "wechat"

type WeChatChannel struct {
	BaseChannel
	bot    *wechatbot.Bot
	cancel context.CancelFunc
}

func NewWeChatChannel(cfg config.WeChatConfig, b *bus.MessageBus) (*WeChatChannel, error) {
	opts := wechatbot.Options{
		CredPath: cfg.CredPath,
		OnQRURL: func(url string) {
			qrterminal.Generate(url, qrterminal.L, log.Writer())
			log.Printf("[wechat] scan the QR code above with WeChat to log in")
		},
		OnScanned: func() {
			log.Printf("[wechat] QR code scanned, waiting for confirmation...")
		},
		OnExpired: func() {
			log.Printf("[wechat] session expired, re-login required")
		},
		OnError: func(err error) {
			log.Printf("[wechat] error: %v", err)
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
	log.Printf("[wechat] logged in")

	w.bot.OnMessage(func(msg *wechatbot.IncomingMessage) {
		if !w.IsAllowed(msg.UserID) {
			log.Printf("[wechat] rejected message from %s", msg.UserID)
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
			log.Printf("[wechat] poll loop exited: %v", err)
		}
	}()

	log.Printf("[wechat] started")
	return nil
}

func (w *WeChatChannel) Stop() error {
	if w.cancel != nil {
		w.cancel()
	}
	w.bot.Stop()
	log.Printf("[wechat] stopped")
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
