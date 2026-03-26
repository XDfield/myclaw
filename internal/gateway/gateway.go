package gateway

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cexll/agentsdk-go/pkg/api"
	"github.com/cexll/agentsdk-go/pkg/model"
	"github.com/stellarlinkco/myclaw/internal/bus"
	"github.com/stellarlinkco/myclaw/internal/channel"
	"github.com/stellarlinkco/myclaw/internal/config"
	"github.com/stellarlinkco/myclaw/internal/cron"
	"github.com/stellarlinkco/myclaw/internal/heartbeat"
	"github.com/stellarlinkco/myclaw/internal/logging"
	"github.com/stellarlinkco/myclaw/internal/memory"
	"github.com/stellarlinkco/myclaw/internal/runtimecmd"
	"github.com/stellarlinkco/myclaw/internal/session"
	"github.com/stellarlinkco/myclaw/internal/skills"
)

var glog = logging.Component("gateway")

// Runtime interface for agent runtime (allows mocking in tests)
type Runtime interface {
	Run(ctx context.Context, req api.Request) (*api.Response, error)
	RunStream(ctx context.Context, req api.Request) (<-chan api.StreamEvent, error)
	Close()
}

// runtimeAdapter wraps api.Runtime to implement Runtime interface
type runtimeAdapter struct {
	rt *api.Runtime
}

func (r *runtimeAdapter) Run(ctx context.Context, req api.Request) (*api.Response, error) {
	return r.rt.Run(ctx, req)
}

func (r *runtimeAdapter) RunStream(ctx context.Context, req api.Request) (<-chan api.StreamEvent, error) {
	return r.rt.RunStream(ctx, req)
}

func (r *runtimeAdapter) Close() {
	r.rt.Close()
}

// RuntimeFactory creates a Runtime instance
type RuntimeFactory func(cfg *config.Config, sysPrompt string) (Runtime, error)

// Options for creating a Gateway
type Options struct {
	RuntimeFactory RuntimeFactory
	SignalChan     chan os.Signal // for testing signal handling
}

// DefaultRuntimeFactory creates the default agentsdk-go runtime
func DefaultRuntimeFactory(cfg *config.Config, sysPrompt string) (Runtime, error) {
	return newRuntime(cfg, sysPrompt, nil)
}

func newRuntime(cfg *config.Config, sysPrompt string, skillRegs []api.SkillRegistration) (Runtime, error) {
	var provider api.ModelFactory
	switch cfg.Provider.Type {
	case "openai":
		provider = &model.OpenAIProvider{
			APIKey:    cfg.Provider.APIKey,
			BaseURL:   cfg.Provider.BaseURL,
			ModelName: cfg.Agent.Model,
			MaxTokens: cfg.Agent.MaxTokens,
		}
	default:
		provider = &model.AnthropicProvider{
			APIKey:    cfg.Provider.APIKey,
			BaseURL:   cfg.Provider.BaseURL,
			ModelName: cfg.Agent.Model,
			MaxTokens: cfg.Agent.MaxTokens,
		}
	}

	rt, err := api.New(context.Background(), api.Options{
		ProjectRoot:   cfg.Agent.Workspace,
		ModelFactory:  provider,
		SystemPrompt:  sysPrompt,
		MaxIterations: cfg.Agent.MaxToolIterations,
		MCPServers:    cfg.MCP.Servers,
		TokenTracking: cfg.TokenTracking.Enabled,
		AutoCompact: api.CompactConfig{
			Enabled:       cfg.AutoCompact.Enabled,
			Threshold:     cfg.AutoCompact.Threshold,
			PreserveCount: cfg.AutoCompact.PreserveCount,
		},
		Skills:   skillRegs,
		Commands: runtimecmd.Build(),
	})
	if err != nil {
		return nil, fmt.Errorf("create runtime: %w", err)
	}
	return &runtimeAdapter{rt: rt}, nil
}

type Gateway struct {
	cfg        *config.Config
	bus        *bus.MessageBus
	runtime    Runtime
	channels   *channel.ChannelManager
	cron       *cron.Service
	hb         *heartbeat.Service
	mem        *memory.MemoryStore
	skillRegs  []api.SkillRegistration
	sessions   *session.Router
	signalChan chan os.Signal // for testing
}

// New creates a Gateway with default options
func New(cfg *config.Config) (*Gateway, error) {
	return NewWithOptions(cfg, Options{})
}

// NewWithOptions creates a Gateway with custom options for testing
func NewWithOptions(cfg *config.Config, opts Options) (*Gateway, error) {
	g := &Gateway{cfg: cfg}

	// Message bus
	g.bus = bus.NewMessageBus(config.DefaultBufSize)

	// Memory
	g.mem = memory.NewMemoryStore(cfg.Agent.Workspace)

	router, routerErr := session.New(filepath.Join(cfg.Agent.Workspace, ".myclaw", "session-router.json"))
	if routerErr != nil {
		return nil, routerErr
	}
	g.sessions = router

	// Build system prompt
	sysPrompt := g.buildSystemPrompt()

	if cfg.Skills.Enabled {
		skillDir := cfg.Skills.Dir
		if skillDir == "" {
			skillDir = filepath.Join(cfg.Agent.Workspace, "skills")
		}
		skillRegs, err := skills.LoadSkills(skillDir)
		if err != nil {
			glog.Warn().Err(err).Msg("skills load warning")
		}
		g.skillRegs = skillRegs
	}

	// Create runtime using factory (allows injection for testing)
	factory := opts.RuntimeFactory
	var (
		rt  Runtime
		err error
	)
	if factory == nil {
		rt, err = newRuntime(cfg, sysPrompt, g.skillRegs)
	} else {
		rt, err = factory(cfg, sysPrompt)
	}
	if err != nil {
		return nil, err
	}
	g.runtime = rt

	// Signal channel for testing
	g.signalChan = opts.SignalChan

	// runAgent helper for cron/heartbeat
	runAgent := func(prompt string) (string, error) {
		return g.runAgent(context.Background(), prompt, "system", nil)
	}

	// Cron
	cronStorePath := filepath.Join(config.ConfigDir(), "data", "cron", "jobs.json")
	g.cron = cron.NewService(cronStorePath)
	g.cron.OnJob = func(job cron.CronJob) (string, error) {
		result, err := runAgent(job.Payload.Message)
		if err != nil {
			return "", err
		}
		if job.Payload.Deliver && job.Payload.Channel != "" {
			g.bus.Outbound <- bus.OutboundMessage{
				Channel: job.Payload.Channel,
				ChatID:  job.Payload.To,
				Content: result,
			}
		}
		return result, nil
	}

	// Heartbeat
	g.hb = heartbeat.New(cfg.Agent.Workspace, runAgent, 0)

	// Channels (with gateway config for WebUI port)
	chMgr, err := channel.NewChannelManagerWithGateway(cfg.Channels, cfg.Gateway, g.bus)
	if err != nil {
		return nil, fmt.Errorf("create channel manager: %w", err)
	}
	g.channels = chMgr

	// Set workspace on Telegram channel for file saving.
	if tc, ok := chMgr.GetChannel("telegram").(*channel.TelegramChannel); ok {
		tc.SetWorkspace(cfg.Agent.Workspace)
	}

	return g, nil
}

func (g *Gateway) buildSystemPrompt() string {
	var sb strings.Builder

	if data, err := os.ReadFile(filepath.Join(g.cfg.Agent.Workspace, "AGENTS.md")); err == nil {
		sb.Write(data)
		sb.WriteString("\n\n")
	}

	if data, err := os.ReadFile(filepath.Join(g.cfg.Agent.Workspace, "SOUL.md")); err == nil {
		sb.Write(data)
		sb.WriteString("\n\n")
	}

	if memCtx := g.mem.GetMemoryContext(); memCtx != "" {
		sb.WriteString(memCtx)
	}

	return sb.String()
}

func (g *Gateway) runAgent(ctx context.Context, prompt, sessionID string, contentBlocks []model.ContentBlock) (string, error) {
	resp, err := g.runAgentResponse(ctx, prompt, sessionID, contentBlocks)
	if err != nil {
		return "", err
	}
	if resp == nil || resp.Result == nil {
		return "", nil
	}
	return resp.Result.Output, nil
}

func (g *Gateway) runAgentResponse(ctx context.Context, prompt, sessionID string, contentBlocks []model.ContentBlock) (*api.Response, error) {
	// Workaround: agentsdk-go drops Prompt when ContentBlocks exist (anthropic.go:420-431).
	// Merge text prompt into ContentBlocks so both text and media reach the API.
	blocks := contentBlocks
	if len(contentBlocks) > 0 && strings.TrimSpace(prompt) != "" {
		blocks = make([]model.ContentBlock, 0, len(contentBlocks)+1)
		blocks = append(blocks, model.ContentBlock{Type: model.ContentBlockText, Text: prompt})
		blocks = append(blocks, contentBlocks...)
		prompt = "" // clear to avoid duplication if SDK is fixed later
	}

	resp, err := g.runtime.Run(ctx, api.Request{
		Prompt:        prompt,
		ContentBlocks: blocks,
		SessionID:     sessionID,
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (g *Gateway) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go g.bus.DispatchOutbound(ctx)

	if err := g.channels.StartAll(ctx); err != nil {
		return fmt.Errorf("start channels: %w", err)
	}
	glog.Info().Msgf("channels started: %v", g.channels.EnabledChannels())

	if err := g.cron.Start(ctx); err != nil {
		glog.Warn().Err(err).Msg("cron start warning")
	}

	go func() {
		if err := g.hb.Start(ctx); err != nil {
			glog.Error().Err(err).Msg("heartbeat error")
		}
	}()

	go g.processLoop(ctx)

	glog.Info().Str("host", g.cfg.Gateway.Host).Int("port", g.cfg.Gateway.Port).Msg("gateway running")

	// Use injected signal channel for testing, or create default
	sigCh := g.signalChan
	if sigCh == nil {
		sigCh = make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	}
	<-sigCh

	glog.Info().Msg("shutting down")
	return g.Shutdown()
}

// StreamSender is an optional interface channels can implement for streaming output.
type StreamSender interface {
	SendStream(ctx context.Context, chatID string, metadata map[string]any, events <-chan api.StreamEvent) error
}

func (g *Gateway) processLoop(ctx context.Context) {
	for {
		select {
		case msg := <-g.bus.Inbound:
			glog.Info().Str("channel", msg.Channel).Str("sender", msg.SenderID).Str("content", truncate(msg.Content, 80)).Msg("inbound message")

			// Prepend current datetime so the agent knows when the message was sent.
			if g.cfg.Gateway.TimestampPrefixEnabled() {
				msg.Content = fmt.Sprintf("[%s] %s", time.Now().Format("2006-01-02 15:04:05"), msg.Content)
			}

			if handled, err := g.handleBuiltinCommand(msg); handled {
				if err != nil {
					glog.Error().Err(err).Msg("builtin command error")
					g.bus.Outbound <- bus.OutboundMessage{
						Channel: msg.Channel,
						ChatID:  msg.ChatID,
						Content: "Sorry, I encountered an error processing your command.",
					}
				} else {
					g.bus.Outbound <- bus.OutboundMessage{
						Channel:  msg.Channel,
						ChatID:   msg.ChatID,
						Content:  "✅ Started a fresh session.",
						Metadata: msg.Metadata,
					}
				}
				continue
			}

			msgCtx := context.WithValue(ctx, "channel", msg.Channel)
			msgCtx = context.WithValue(msgCtx, "chatID", msg.ChatID)

			sessionKey := msg.SessionKey()
			if shouldIsolateSession(msg.Metadata) {
				sessionKey = isolatedSessionKey(msg)
			} else if g.sessions != nil {
				// Time-based auto-reset
				policy := session.ResetPolicy{
					Mode:        g.cfg.Session.Reset.Mode,
					AtHour:      g.cfg.Session.Reset.AtHour,
					IdleMinutes: g.cfg.Session.Reset.IdleMinutes,
				}
				resolved, rotated, err := g.sessions.CheckAndRotateIfStale(msg.SessionKey(), policy)
				if err != nil {
					glog.Error().Err(err).Msg("session freshness check error")
					resolved = g.sessions.Resolve(msg.SessionKey(), msg.SessionKey())
				}
				if rotated {
					glog.Info().Str("sessionKey", msg.SessionKey()).Msg("session auto-rotated")
				}
				sessionKey = resolved
				_ = g.sessions.Touch(msg.SessionKey())
			}

			var ch channel.Channel
			if g.channels != nil {
				ch = g.channels.GetChannel(msg.Channel)
			}

			// Pre-processing feedback
			if ch != nil {
				if tc, ok := ch.(*channel.TelegramChannel); ok {
					chatIDInt := mustParseChatID(msg.ChatID)
					msgID := extractMessageID(msg.Metadata)
					tc.PreProcessFeedback(chatIDInt, msgID)
				}
			}

			// Check if channel supports streaming
			if ch != nil && !shouldForceNonStreaming(msg.Metadata) {
				if ss, ok := ch.(StreamSender); ok {
					events, err := g.runAgentStream(msgCtx, msg.Content, sessionKey, msg.ContentBlocks)
					if err != nil {
						glog.Error().Err(err).Msg("agent stream error")
						g.bus.Outbound <- bus.OutboundMessage{
							Channel: msg.Channel,
							ChatID:  msg.ChatID,
							Content: "Sorry, I encountered an error processing your message.",
						}
						continue
					}
					if err := ss.SendStream(ctx, msg.ChatID, msg.Metadata, events); err != nil {
						glog.Error().Err(err).Msg("SendStream error")
					}
					continue
				}
			}

			// Pre-call memory flush check
			if g.shouldFlushMemory(msg.SessionKey()) {
				g.runMemoryFlush(msgCtx, msg.SessionKey(), sessionKey)
			}

			// Non-streaming path with overflow retry
			maxRetries := g.cfg.AutoCompact.MaxOverflowRetry
			if maxRetries <= 0 {
				maxRetries = 3
			}
			if !g.cfg.AutoCompact.OverflowRetry {
				maxRetries = 0
			}

			var resp *api.Response
			var lastErr error
			for attempt := 0; attempt <= maxRetries; attempt++ {
				resp, lastErr = g.runAgentResponse(msgCtx, msg.Content, sessionKey, msg.ContentBlocks)
				if lastErr == nil {
					break
				}
				if !isContextOverflowError(lastErr) {
					break
				}
				if attempt == maxRetries {
					break
				}
				glog.Warn().Int("attempt", attempt+1).Int("maxRetries", maxRetries).Msg("context overflow, compacting")
				if g.sessions == nil {
					break
				}
				_, compactErr := g.compactAndRotate(msgCtx, msg.SessionKey(), sessionKey)
				if compactErr != nil {
					glog.Error().Err(compactErr).Msg("overflow compact failed")
					break
				}
				sessionKey = g.sessions.Resolve(msg.SessionKey(), msg.SessionKey())
				glog.Info().Msg("overflow compact succeeded, retrying with new session")
			}

			if lastErr != nil {
				glog.Error().Err(lastErr).Msg("agent error")
				g.bus.Outbound <- bus.OutboundMessage{
					Channel: msg.Channel,
					ChatID:  msg.ChatID,
					Content: "Sorry, I encountered an error processing your message.",
				}
				continue
			}

			// Update token usage after successful call
			if resp != nil && resp.Result != nil && g.sessions != nil {
				usage := resp.Result.Usage
				if usage.TotalTokens > 0 {
					_ = g.sessions.UpdateUsage(msg.SessionKey(), usage.TotalTokens, g.cfg.Agent.ContextWindow)
				}
			}

			result := ""
			if resp != nil && resp.Result != nil {
				result = resp.Result.Output
			}
			if postResult, handled, postErr := g.handlePostResponse(msg.SessionKey(), resp); handled || postErr != nil {
				if postErr != nil {
					glog.Error().Err(postErr).Msg("post action error")
					result = "Sorry, I encountered an error processing your command."
				} else {
					result = postResult
				}
			}

			if result != "" {
				g.bus.Outbound <- bus.OutboundMessage{
					Channel:  msg.Channel,
					ChatID:   msg.ChatID,
					Content:  result,
					Metadata: msg.Metadata,
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

func shouldForceNonStreaming(meta map[string]any) bool {
	if meta == nil {
		return false
	}
	v, ok := meta["force_non_streaming"]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

func shouldIsolateSession(meta map[string]any) bool {
	return metadataString(meta, "session_mode") == "isolated"
}

func isolatedSessionKey(msg bus.InboundMessage) string {
	return msg.SessionKey() + "#isolated#" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

func (g *Gateway) Shutdown() error {
	g.cron.Stop()
	_ = g.channels.StopAll()
	if g.runtime != nil {
		g.runtime.Close()
	}
	glog.Info().Msg("shutdown complete")
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// runAgentStream calls RunStream on the runtime and returns the event channel.
func (g *Gateway) runAgentStream(ctx context.Context, prompt, sessionID string, contentBlocks []model.ContentBlock) (<-chan api.StreamEvent, error) {
	blocks := contentBlocks
	if len(contentBlocks) > 0 && strings.TrimSpace(prompt) != "" {
		blocks = make([]model.ContentBlock, 0, len(contentBlocks)+1)
		blocks = append(blocks, model.ContentBlock{Type: model.ContentBlockText, Text: prompt})
		blocks = append(blocks, contentBlocks...)
		prompt = ""
	}
	return g.runtime.RunStream(ctx, api.Request{
		Prompt:        prompt,
		ContentBlocks: blocks,
		SessionID:     sessionID,
	})
}

// mustParseChatID parses a chat ID string, returning 0 on error.
func mustParseChatID(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

// extractMessageID extracts message_id from metadata map.
func extractMessageID(meta map[string]any) int {
	if meta == nil {
		return 0
	}
	if v, ok := meta["message_id"]; ok {
		switch id := v.(type) {
		case int:
			return id
		case int64:
			return int(id)
		case float64:
			return int(id)
		}
	}
	return 0
}
