package gateway

import (
	"context"
	"strings"
	"time"
)

const (
	DefaultFlushSoftThreshold = 4000
	defaultMemoryFlushPrompt  = `Pre-compaction memory flush. Store durable memories now.
Write important decisions, conclusions, user preferences, key file paths, and open questions
to the memory file (memory/YYYY-MM-DD.md).
IMPORTANT: If the file already exists, APPEND new content only and do not overwrite existing entries.
If nothing important to store, reply with a single word: SKIP`
)

func shouldRunMemoryFlush(totalTokens, contextWindow, reserveTokens, softThreshold, compactionCount, memoryFlushedAt int) bool {
	if totalTokens <= 0 || contextWindow <= 0 {
		return false
	}
	threshold := contextWindow - reserveTokens - softThreshold
	if threshold <= 0 {
		return false
	}
	if totalTokens < threshold {
		return false
	}
	// Prevent duplicate flush in same compaction cycle
	if memoryFlushedAt == compactionCount {
		return false
	}
	return true
}

func (g *Gateway) shouldFlushMemory(chatSessionKey string) bool {
	if g.sessions == nil || !g.cfg.AutoCompact.Enabled {
		return false
	}
	flush := g.cfg.AutoCompact.MemoryFlush
	if !flush.Enabled {
		return false
	}
	_, state, exists := g.sessions.ResolveWithState(chatSessionKey)
	if !exists {
		return false
	}
	reserveTokens := int(float64(state.ContextWindow) * (1.0 - g.cfg.AutoCompact.Threshold))
	softThreshold := flush.SoftThresholdTokens
	if softThreshold <= 0 {
		softThreshold = DefaultFlushSoftThreshold
	}
	return shouldRunMemoryFlush(state.TotalTokens, state.ContextWindow, reserveTokens, softThreshold, state.CompactionCount, state.MemoryFlushedAt)
}

func (g *Gateway) runMemoryFlush(ctx context.Context, chatSessionKey, sessionID string) {
	glog.Info().Str("session", chatSessionKey).Msg("running pre-compaction memory flush")
	prompt := g.cfg.AutoCompact.MemoryFlush.Prompt
	if prompt == "" {
		prompt = defaultMemoryFlushPrompt
	}
	prompt = strings.ReplaceAll(prompt, "YYYY-MM-DD", time.Now().Format("2006-01-02"))
	_, err := g.runAgent(ctx, prompt, sessionID, nil)
	if err != nil {
		glog.Error().Err(err).Msg("memory flush error")
		return
	}
	_ = g.sessions.MarkMemoryFlushed(chatSessionKey)
	glog.Info().Str("session", chatSessionKey).Msg("memory flush completed")
}
