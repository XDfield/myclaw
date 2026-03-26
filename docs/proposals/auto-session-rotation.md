# 会话自动轮转设计提案

- **状态**: 已实施
- **作者**: myclaw team
- **日期**: 2026-03-25
- **实施日期**: 2026-03-25
- **参考**: openclaw 项目 (`D:\DEV\openclaw`)
- **进度跟踪**: [auto-session-rotation-progress.md](../../todo/auto-session-rotation-progress.md)

## 实施完成情况

三个阶段均已实施完毕，并修复了实施过程中发现的 2 个 bug。

### 阶段完成状态

| 阶段 | 状态 | 说明 |
|------|------|------|
| 阶段 1：基于时间的会话重置 | ✅ 已完成 | daily/idle 两种模式，SessionState 结构化，旧格式自动迁移 |
| 阶段 2：溢出重试 | ✅ 已完成 | 10 种 overflow pattern 匹配，最多 3 次 compact+rotate 重试 |
| 阶段 3：压缩前记忆刷盘 | ✅ 已完成 | token 阈值触发，异步写入 memory 日志 |

### 与提案的差异

| 项目 | 提案设计 | 实际实现 | 原因 |
|------|----------|----------|------|
| contextWindow 来源 | 从 `api.Response` 读取或查表 | 用户在 `agent.contextWindow` 中配置 | agentsdk-go 不暴露此值，查表需持续维护模型列表，配置方式最简单可靠 |
| MemoryFlushedAt 类型 | 提案中为 `int`（compactionCount 快照） | 初始实现误用 `int64`（时间戳），已修正为 `int` | 实施时的 bug，已修复 |

### 实施过程中发现并修复的 Bug

1. **MemoryFlushedAt 语义错误**：`MarkMemoryFlushed` 写入 `nowMs()`（时间戳）而非 `CompactionCount`，导致去重检查 `flushedAt == compactionCount` 永远不命中，每次消息到达都会重复触发刷盘。修复：改为存储 `state.CompactionCount`。

2. **contextWindow 始终为 0**：agentsdk-go 的 `model.Usage` 不含 context window 信息，`UpdateUsage` 硬编码传入 0，导致 `shouldRunMemoryFlush` 第一行就 return false，刷盘永远不触发。修复：在 `AgentConfig` 中新增 `ContextWindow` 配置字段。

### 新增/变更文件

| 文件 | 类型 |
|------|------|
| `internal/config/config.go` | 修改 |
| `internal/config/config_test.go` | 修改 |
| `internal/session/router.go` | 重写 |
| `internal/session/freshness.go` | 新增 |
| `internal/session/freshness_test.go` | 新增 |
| `internal/gateway/gateway.go` | 修改 |
| `internal/gateway/post_actions.go` | 修改 |
| `internal/gateway/overflow.go` | 新增 |
| `internal/gateway/overflow_test.go` | 新增 |
| `internal/gateway/memory_flush.go` | 新增 |
| `internal/gateway/memory_flush_test.go` | 新增 |
| `config.example.json` | 修改 |

---

## 1. 背景与动机

### 1.1 现状

myclaw 当前的会话管理存在以下局限：

- **手动轮转**：只能通过 `/new`（清空重开）和 `/compact`（压缩后轮转）两个命令手动触发会话切换
- **SDK 内部压缩**：agentsdk-go 的 `AutoCompact` 配置（阈值 80%、保留 5 条）仅在同一 session 内做 in-place 消息缩减，不涉及会话轮转
- **无时间感知**：长期不活跃的会话不会自动清理，用户隔天回来仍延续旧上下文，可能导致过时信息干扰
- **无溢出恢复**：上下文溢出时直接报错，无重试机制
- **无压缩前保护**：自动压缩丢弃旧消息时，重要决策和上下文可能永久丢失

### 1.2 openclaw 的做法

openclaw 实现了三层自动轮转机制：

| 层级 | 机制 | 触发条件 | 效果 |
|------|------|----------|------|
| 层 1 | 时间会话重置 | 每日定时 / 空闲超时 | 创建全新 session |
| 层 2 | Token 阈值压缩 + 溢出重试 | 上下文接近/超过窗口限制 | in-place 压缩 + 最多 3 次重试 |
| 层 3 | 压缩前记忆刷盘 | 即将触发压缩时 | 静默写入 memory 文件 |

### 1.3 目标

为 myclaw 设计并实现自动轮转能力，使其能够：

1. 基于时间策略自动重置过期会话
2. 在上下文溢出时自动压缩并重试
3. 在自动压缩前将关键信息持久化到记忆系统

## 2. 总体架构

```
消息到达 processLoop
    │
    ├── [层 1] 时间会话重置 ─── 检查 freshness ─── 过期则 Rotate()
    │
    ├── [层 3] 压缩前记忆刷盘 ─── 检查 token 用量 ─── 接近阈值则静默刷盘
    │
    └── 调用 AI runtime
        │
        └── [层 2] 溢出重试 ─── 捕获 overflow 错误 ─── 压缩 + 重试（最多 3 次）
```

三层机制独立运作，按消息处理流程依次执行。

## 3. 详细设计

### 3.1 层 1：基于时间的会话重置

#### 3.1.1 设计思路

在每条消息进入 `processLoop` 时，检查该会话的最后活动时间。如果满足过期条件（每日定时重置或空闲超时），自动执行 `sessions.Rotate()` 创建新会话。

#### 3.1.2 配置结构

在 `config.go` 中新增 `SessionConfig`：

```go
type SessionConfig struct {
    Reset SessionResetConfig `json:"reset"`
}

type SessionResetConfig struct {
    // 重置模式："daily"（每日定时）或 "idle"（空闲超时）
    // 默认 "daily"
    Mode string `json:"mode,omitempty"`

    // daily 模式：每日重置时刻（0-23），默认 4（凌晨 4 点）
    AtHour int `json:"atHour,omitempty"`

    // idle 模式：空闲多少分钟后重置，默认 120
    IdleMinutes int `json:"idleMinutes,omitempty"`
}
```

`config.json` 示例：

```json
{
  "session": {
    "reset": {
      "mode": "daily",
      "atHour": 4,
      "idleMinutes": 120
    }
  }
}
```

默认值：

```go
Session: SessionConfig{
    Reset: SessionResetConfig{
        Mode:        "daily",
        AtHour:      4,
        IdleMinutes: 120,
    },
},
```

#### 3.1.3 会话状态追踪

当前 `session.Router` 的 `sessions` 字段是 `map[string]string`（key → sessionID），不记录活动时间。需要扩展为结构化存储。

新增 `SessionState` 类型：

```go
// internal/session/router.go

type SessionState struct {
    SessionID string `json:"sessionId"`
    UpdatedAt int64  `json:"updatedAt"` // Unix 毫秒时间戳
}
```

`Router.sessions` 从 `map[string]string` 改为 `map[string]SessionState`。

持久化文件 `session-router.json` 格式变化：

```json
// 旧格式
{
  "telegram:123456": "telegram:123456#1735123456789"
}

// 新格式
{
  "telegram:123456": {
    "sessionId": "telegram:123456#1735123456789",
    "updatedAt": 1735123456789
  }
}
```

**兼容性处理**：加载时检测值类型，如果是字符串则自动迁移为 `SessionState`（`updatedAt` 设为 0，触发下次消息时立即刷新）。

#### 3.1.4 新鲜度评估

新增 `internal/session/freshness.go`：

```go
package session

import "time"

type ResetPolicy struct {
    Mode        string // "daily" | "idle"
    AtHour      int    // 0-23
    IdleMinutes int
}

type Freshness struct {
    Fresh        bool
    DailyResetAt int64 // 每日重置边界（Unix ms），仅 daily 模式
    IdleExpiresAt int64 // 空闲过期时刻（Unix ms），仅 idle 模式
}

// EvaluateFreshness 判断会话是否过期
func EvaluateFreshness(updatedAt, now int64, policy ResetPolicy) Freshness {
    f := Freshness{Fresh: true}

    // 每日重置检查
    if policy.Mode == "daily" {
        f.DailyResetAt = resolveDailyResetAtMs(now, policy.AtHour)
        if updatedAt < f.DailyResetAt {
            f.Fresh = false
        }
    }

    // 空闲超时检查（daily 模式下也生效，两者取先到期的）
    if policy.IdleMinutes > 0 {
        f.IdleExpiresAt = updatedAt + int64(policy.IdleMinutes)*60*1000
        if now > f.IdleExpiresAt {
            f.Fresh = false
        }
    }

    return f
}

// resolveDailyResetAtMs 计算最近一次每日重置时刻
// 参考 openclaw: reset.ts:74-82
func resolveDailyResetAtMs(nowMs int64, atHour int) int64 {
    if atHour < 0 || atHour > 23 {
        atHour = 4
    }
    t := time.UnixMilli(nowMs)
    resetAt := time.Date(t.Year(), t.Month(), t.Day(), atHour, 0, 0, 0, t.Location())
    if t.Before(resetAt) {
        resetAt = resetAt.AddDate(0, 0, -1)
    }
    return resetAt.UnixMilli()
}
```

#### 3.1.5 Router 扩展

`Router` 新增方法：

```go
// Touch 更新会话的最后活动时间，不存在则创建
func (r *Router) Touch(key string) error

// ResolveWithState 返回 sessionID 和完整状态
func (r *Router) ResolveWithState(key string) (sessionID string, state SessionState, exists bool)

// CheckAndRotateIfStale 检查新鲜度，如果过期则自动轮转
// 返回 (sessionID, rotated, error)
func (r *Router) CheckAndRotateIfStale(key string, policy ResetPolicy) (string, bool, error)
```

`CheckAndRotateIfStale` 实现：

```go
func (r *Router) CheckAndRotateIfStale(key string, policy ResetPolicy) (string, bool, error) {
    r.mu.Lock()
    defer r.mu.Unlock()

    state, exists := r.sessions[key]
    if !exists {
        // 首次交互，创建新状态
        state = SessionState{
            SessionID: key,
            UpdatedAt: time.Now().UnixMilli(),
        }
        r.sessions[key] = state
        _ = r.persistLocked()
        return state.SessionID, false, nil
    }

    now := time.Now().UnixMilli()
    freshness := EvaluateFreshness(state.UpdatedAt, now, policy)

    if freshness.Fresh {
        return state.SessionID, false, nil
    }

    // 过期，执行轮转
    oldID := state.SessionID
    newID := nextSessionID(key)
    r.sessions[key] = SessionState{
        SessionID: newID,
        UpdatedAt: now,
    }
    if err := r.persistLocked(); err != nil {
        // 回滚
        r.sessions[key] = SessionState{SessionID: oldID, UpdatedAt: state.UpdatedAt}
        return "", false, err
    }

    return newID, true, nil
}
```

#### 3.1.6 Gateway 集成

在 `processLoop` 中，消息 Resolve sessionKey 前插入新鲜度检查：

```go
// gateway.go processLoop 内

sessionKey := msg.SessionKey()

if shouldIsolateSession(msg.Metadata) {
    sessionKey = isolatedSessionKey(msg)
} else if g.sessions != nil {
    // 新增：自动时间重置检查
    policy := session.ResetPolicy{
        Mode:        g.cfg.Session.Reset.Mode,
        AtHour:      g.cfg.Session.Reset.AtHour,
        IdleMinutes: g.cfg.Session.Reset.IdleMinutes,
    }
    resolved, rotated, err := g.sessions.CheckAndRotateIfStale(msg.SessionKey(), policy)
    if err != nil {
        log.Printf("[gateway] session freshness check error: %v", err)
        resolved = g.sessions.Resolve(msg.SessionKey(), msg.SessionKey())
    }
    if rotated {
        log.Printf("[gateway] session auto-rotated for %s", msg.SessionKey())
    }
    sessionKey = resolved

    // 更新活动时间
    _ = g.sessions.Touch(msg.SessionKey())
}
```

---

### 3.2 层 2：溢出重试

#### 3.2.1 设计思路

当 AI runtime 调用返回上下文溢出错误时，自动执行压缩（compact + rotate），然后用新 session 重试用户消息。最多重试 3 次。

#### 3.2.2 溢出错误检测

新增 `internal/gateway/overflow.go`：

```go
package gateway

import "strings"

// 上下文溢出错误的关键词模式
// 参考 openclaw: errors.ts:123-153
var overflowPatterns = []string{
    "prompt is too long",
    "context length exceeded",
    "context_length_exceeded",
    "maximum context length",
    "token limit",
    "too many tokens",
    "context window",
    "content would exceed",
    "request too large",
    "input is too long",
}

func isContextOverflowError(err error) bool {
    if err == nil {
        return false
    }
    msg := strings.ToLower(err.Error())
    for _, pattern := range overflowPatterns {
        if strings.Contains(msg, pattern) {
            return true
        }
    }
    return false
}
```

#### 3.2.3 重试流程

在 `processLoop` 非流式路径中包装重试逻辑：

```go
const maxOverflowRetries = 3

// gateway.go processLoop 非流式路径

var resp *api.Response
var lastErr error

for attempt := 0; attempt <= maxOverflowRetries; attempt++ {
    resp, lastErr = g.runAgentResponse(msgCtx, msg.Content, sessionKey, msg.ContentBlocks)

    if lastErr == nil {
        break
    }
    if !isContextOverflowError(lastErr) {
        break // 非溢出错误，不重试
    }
    if attempt == maxOverflowRetries {
        break // 已达最大重试次数
    }

    log.Printf("[gateway] context overflow (attempt %d/%d), compacting...",
        attempt+1, maxOverflowRetries)

    // 执行压缩 + 轮转
    compactSessionKey := msg.SessionKey()
    summary, compactErr := g.compactAndRotate(msgCtx, compactSessionKey, sessionKey)
    if compactErr != nil {
        log.Printf("[gateway] overflow compact failed: %v", compactErr)
        break
    }

    // 使用新的 sessionID 重试
    sessionKey = g.sessions.Resolve(compactSessionKey, compactSessionKey)
    log.Printf("[gateway] overflow compact succeeded, retrying with new session (summary: %d chars)",
        len(summary))
}
```

`compactAndRotate` 方法：

```go
func (g *Gateway) compactAndRotate(ctx context.Context, chatSessionKey, currentSessionID string) (string, error) {
    // 1. 让 AI 生成当前对话摘要
    prompt := "Create a compact continuation summary for this conversation. " +
        "Capture goals, decisions, constraints, preferences, important file paths, " +
        "open questions, and the best next steps. Write only the summary text."

    summary, err := g.runAgent(ctx, prompt, currentSessionID, nil)
    if err != nil {
        return "", fmt.Errorf("generate compact summary: %w", err)
    }
    summary = strings.TrimSpace(summary)
    if summary == "" {
        return "", fmt.Errorf("compact summary is empty")
    }

    // 2. 轮转会话
    _, newSessionID, err := g.sessions.Rotate(chatSessionKey)
    if err != nil {
        return "", fmt.Errorf("rotate session: %w", err)
    }

    // 3. 将摘要种入新会话
    if err := seedSessionSummary(g.cfg.Agent.Workspace, newSessionID, summary); err != nil {
        return "", fmt.Errorf("seed summary: %w", err)
    }

    return summary, nil
}
```

#### 3.2.4 配置

溢出重试默认开启，无需额外配置。如需关闭，在 `AutoCompactConfig` 中新增：

```go
type AutoCompactConfig struct {
    Enabled          bool    `json:"enabled"`
    Threshold        float64 `json:"threshold,omitempty"`
    PreserveCount    int     `json:"preserveCount,omitempty"`
    OverflowRetry    bool    `json:"overflowRetry,omitempty"`    // 默认 true
    MaxOverflowRetry int     `json:"maxOverflowRetry,omitempty"` // 默认 3
}
```

---

### 3.3 层 3：压缩前记忆刷盘

#### 3.3.1 设计思路

在自动压缩即将发生但尚未发生时，先运行一轮静默的 AI 调用，让 AI 将当前对话中的重要信息写入 memory 目录，防止压缩后关键上下文永久丢失。

**触发时机**：在 `processLoop` 中，每次调用 AI 之前，检查当前 session 的 token 使用量是否接近压缩阈值。

#### 3.3.2 Token 使用量追踪

当前 myclaw 的 `session.Router` 不追踪 token 用量。需要在 `SessionState` 中扩展：

```go
type SessionState struct {
    SessionID       string `json:"sessionId"`
    UpdatedAt       int64  `json:"updatedAt"`

    // Token 追踪
    TotalTokens     int    `json:"totalTokens,omitempty"`     // 最近一次调用后的上下文总 token 数
    ContextWindow   int    `json:"contextWindow,omitempty"`   // 模型上下文窗口大小
    CompactionCount int    `json:"compactionCount,omitempty"` // 已执行的压缩次数
    MemoryFlushedAt int    `json:"memoryFlushedAt,omitempty"` // 上次刷盘时的 compactionCount
}
```

在 `processLoop` 中，AI runtime 调用完成后更新 token 用量：

```go
// runtime 调用完成后
if resp != nil && resp.Usage != nil {
    _ = g.sessions.UpdateUsage(msg.SessionKey(), resp.Usage.TotalTokens, resp.Usage.ContextWindow)
}
```

> 注意：token 用量的具体字段取决于 agentsdk-go 的 `api.Response` 结构。需要确认 agentsdk-go 是否在 Response 中暴露了 token 使用信息。如果没有，需要向 agentsdk-go 提交 PR 增加此能力，或通过 `TokenTracking` 配置间接获取。

#### 3.3.3 刷盘阈值计算

```go
// 参考 openclaw: memory-flush.ts:124-170

const (
    DefaultFlushSoftThreshold = 4000  // 提前触发的 token 余量
)

func shouldRunMemoryFlush(state SessionState, reserveTokens int, softThreshold int) bool {
    if state.TotalTokens <= 0 || state.ContextWindow <= 0 {
        return false
    }

    // threshold = contextWindow - reserveTokens - softThreshold
    threshold := state.ContextWindow - reserveTokens - softThreshold
    if threshold <= 0 {
        return false
    }

    if state.TotalTokens < threshold {
        return false
    }

    // 防止同一压缩周期内重复刷盘
    if state.MemoryFlushedAt == state.CompactionCount {
        return false
    }

    return true
}
```

以 200K 上下文窗口、20K reserve tokens 为例：
- 压缩阈值 ≈ 160K (80%)
- 刷盘阈值 = 200K - 20K - 4K = **176K**
- 实际上刷盘会在 SDK 内部压缩之前触发

#### 3.3.4 刷盘执行

在 `processLoop` 中，AI 调用之前检查是否需要刷盘：

```go
// gateway.go processLoop，在 runtime.Run() 之前

if g.shouldFlushMemory(msg.SessionKey()) {
    go g.runMemoryFlush(ctx, msg.SessionKey(), sessionKey)
}
```

刷盘方法：

```go
const memoryFlushPrompt = `Pre-compaction memory flush. Store durable memories now.
Write important decisions, conclusions, user preferences, key file paths, and open questions
to the memory file (memory/` + "YYYY-MM-DD" + `.md` + `).
IMPORTANT: If the file already exists, APPEND new content only and do not overwrite existing entries.
If nothing important to store, reply with a single word: SKIP`

func (g *Gateway) runMemoryFlush(ctx context.Context, chatSessionKey, sessionID string) {
    log.Printf("[gateway] running pre-compaction memory flush for %s", chatSessionKey)

    prompt := strings.ReplaceAll(memoryFlushPrompt, "YYYY-MM-DD", time.Now().Format("2006-01-02"))
    _, err := g.runAgent(ctx, prompt, sessionID, nil)
    if err != nil {
        log.Printf("[gateway] memory flush error: %v", err)
        return
    }

    // 标记已刷盘，防止重复
    _ = g.sessions.MarkMemoryFlushed(chatSessionKey)
    log.Printf("[gateway] memory flush completed for %s", chatSessionKey)
}

func (g *Gateway) shouldFlushMemory(chatSessionKey string) bool {
    if g.sessions == nil {
        return false
    }
    _, state, exists := g.sessions.ResolveWithState(chatSessionKey)
    if !exists {
        return false
    }

    // reserveTokens 由 autoCompact.threshold 反推
    // 假设模型上下文窗口为 contextWindow，则:
    // reserveTokens = contextWindow * (1 - threshold)
    reserveTokens := int(float64(state.ContextWindow) * (1.0 - g.cfg.AutoCompact.Threshold))

    return shouldRunMemoryFlush(state, reserveTokens, DefaultFlushSoftThreshold)
}
```

#### 3.3.5 配置

在 `AutoCompactConfig` 中新增记忆刷盘配置：

```go
type MemoryFlushConfig struct {
    Enabled            bool   `json:"enabled"`
    SoftThresholdTokens int   `json:"softThresholdTokens,omitempty"` // 默认 4000
    Prompt             string `json:"prompt,omitempty"`               // 自定义刷盘 prompt
}

type AutoCompactConfig struct {
    Enabled          bool              `json:"enabled"`
    Threshold        float64           `json:"threshold,omitempty"`
    PreserveCount    int               `json:"preserveCount,omitempty"`
    OverflowRetry    bool              `json:"overflowRetry,omitempty"`
    MaxOverflowRetry int               `json:"maxOverflowRetry,omitempty"`
    MemoryFlush      MemoryFlushConfig `json:"memoryFlush,omitempty"`
}
```

默认值：

```go
AutoCompact: AutoCompactConfig{
    Enabled:          true,
    Threshold:        0.8,
    PreserveCount:    5,
    OverflowRetry:    true,
    MaxOverflowRetry: 3,
    MemoryFlush: MemoryFlushConfig{
        Enabled:             true,
        SoftThresholdTokens: 4000,
    },
},
```

## 4. 配置总览

完整的新增配置项一览：

```json
{
  "session": {
    "reset": {
      "mode": "daily",
      "atHour": 4,
      "idleMinutes": 120
    }
  },
  "autoCompact": {
    "enabled": true,
    "threshold": 0.8,
    "preserveCount": 5,
    "overflowRetry": true,
    "maxOverflowRetry": 3,
    "memoryFlush": {
      "enabled": true,
      "softThresholdTokens": 4000,
      "prompt": ""
    }
  }
}
```

## 5. 数据流全景

```
消息到达 processLoop
    │
    ▼
┌───────────────────────────────────────────────┐
│ [层 1] 时间会话重置                             │
│                                               │
│   读取 SessionState.UpdatedAt                  │
│   调用 EvaluateFreshness(updatedAt, now, policy)│
│                                               │
│   daily 模式: updatedAt < 今日凌晨 atHour?     │
│   idle 模式:  now > updatedAt + idleMinutes?   │
│                                               │
│   过期 → Rotate() → 新 sessionID              │
│   新鲜 → 继续使用当前 sessionID               │
└───────────────────┬───────────────────────────┘
                    │
                    ▼
┌───────────────────────────────────────────────┐
│ [层 3] 压缩前记忆刷盘                           │
│                                               │
│   读取 SessionState.TotalTokens               │
│   threshold = contextWindow - reserve - 4000   │
│                                               │
│   totalTokens >= threshold                     │
│   且 memoryFlushedAt != compactionCount        │
│                                               │
│   满足 → 静默 AI 调用写入 memory/YYYY-MM-DD.md │
│         标记 memoryFlushedAt = compactionCount  │
│   不满足 → 跳过                                │
└───────────────────┬───────────────────────────┘
                    │
                    ▼
┌───────────────────────────────────────────────┐
│ 调用 AI runtime.Run(sessionID)                │
│                                               │
│   ┌─ 正常返回 → 更新 token 用量 → 输出结果    │
│   │                                           │
│   └─ 上下文溢出错误                            │
│      │                                        │
│      ▼                                        │
│   ┌─────────────────────────────────────┐     │
│   │ [层 2] 溢出重试 (最多 3 次)          │     │
│   │                                     │     │
│   │  1. 生成当前对话摘要                  │     │
│   │  2. Rotate() 创建新 session          │     │
│   │  3. 将摘要种入新 session 历史         │     │
│   │  4. 用新 sessionID 重试原始消息       │     │
│   │                                     │     │
│   │  失败 → 返回错误提示给用户            │     │
│   └─────────────────────────────────────┘     │
└───────────────────────────────────────────────┘
```

## 6. 文件变更清单

| 文件 | 变更类型 | 说明 |
|------|----------|------|
| `internal/config/config.go` | 修改 | 新增 `SessionConfig`、扩展 `AutoCompactConfig` |
| `internal/session/router.go` | 修改 | `SessionState` 结构化、新增 `Touch`/`CheckAndRotateIfStale`/`UpdateUsage`/`MarkMemoryFlushed` 方法、兼容旧格式迁移 |
| `internal/session/freshness.go` | 新增 | `EvaluateFreshness`、`resolveDailyResetAtMs` |
| `internal/session/freshness_test.go` | 新增 | 新鲜度评估的单元测试 |
| `internal/gateway/gateway.go` | 修改 | `processLoop` 集成时间重置检查、记忆刷盘、溢出重试 |
| `internal/gateway/overflow.go` | 新增 | `isContextOverflowError`、溢出错误检测 |
| `internal/gateway/overflow_test.go` | 新增 | 溢出检测的单元测试 |
| `internal/gateway/post_actions.go` | 修改 | 复用 `compactAndRotate` 逻辑 |
| `config.example.json` | 修改 | 新增 `session` 和 `autoCompact.memoryFlush` 配置示例 |

## 7. 实施计划

建议分三个阶段递进实施：

### 阶段 1：时间会话重置（优先级最高，1-2 天）

- 影响最大，实现最简单
- 不依赖 agentsdk-go 的任何变更
- 核心改动：`SessionState` 结构化 + `freshness.go` + `processLoop` 集成

### 阶段 2：溢出重试（优先级高，1 天）

- 提升系统稳定性
- 核心改动：`overflow.go` + `processLoop` 重试循环
- 复用已有的 `seedSessionSummary` 逻辑

### 阶段 3：压缩前记忆刷盘（优先级中，1-2 天）

- 提升记忆持久性
- **前置依赖**：需要确认 agentsdk-go 的 `api.Response` 是否暴露 token 使用信息（`TotalTokens`、`ContextWindow`）。如果没有，有两个选项：
  - A. 向 agentsdk-go 提交 PR 添加 token 用量到 Response
  - B. 通过估算 transcript 文件大小（参考 openclaw 的 `forceFlushTranscriptBytes` 策略）作为近似指标

## 8. 与 openclaw 的差异说明

| 方面 | openclaw | myclaw 方案 | 原因 |
|------|----------|-------------|------|
| 时间重置粒度 | 支持 per-channel、per-type 覆盖 | 仅全局配置 | myclaw 渠道数少，初期不需要细粒度控制 |
| 压缩实现 | in-place 压缩（同一 session 内替换消息） | compact + rotate（创建新 session） | myclaw 基于 agentsdk-go，session 模型不同 |
| 溢出重试层级 | 3 层（SDK 内部压缩 → 显式压缩 → 工具结果截断） | 1 层（显式压缩 + 轮转） | 简化实现，agentsdk-go SDK 已有内部压缩 |
| 记忆刷盘 | 基于精确 token 计数 + transcript 字节数 | 基于 token 计数（或 transcript 字节数兜底） | 取决于 agentsdk-go 暴露的信息 |
| 压缩后上下文注入 | 注入 AGENTS.md 的 "Session Startup" 和 "Red Lines" 段落 | 暂不实现 | 可作为后续优化 |
| 会话状态存储 | 独立 sessions.json，带 TTL 内存缓存 | 复用 session-router.json，加锁保护 | 保持架构简单 |

## 9. 测试策略

### 单元测试

- `freshness_test.go`：覆盖 daily/idle 两种模式的边界条件（跨天、跨夏令时、刚好到期、未到期）
- `router_test.go`：覆盖 `CheckAndRotateIfStale` 的正常轮转、首次交互、已过期、未过期等场景
- `overflow_test.go`：覆盖各种 LLM 错误消息的分类判断

### 集成测试

- `gateway_test.go`：模拟 inbound 消息，验证：
  - 过期会话自动轮转后使用新 sessionID
  - 溢出错误触发自动压缩 + 重试
  - 记忆刷盘在正确时机触发
  - 多次溢出不超过最大重试次数

## 10. 风险与缓解

| 风险 | 影响 | 缓解措施 |
|------|------|----------|
| agentsdk-go 不暴露 token 用量 | 层 3 无法精确触发 | 使用 transcript 文件大小作为近似指标 |
| 溢出压缩时 AI 调用本身也溢出 | 压缩摘要生成失败 | 捕获错误后直接 Rotate（不种入摘要），或截断历史后重试 |
| session-router.json 格式迁移 | 升级时可能丢失旧会话 | 兼容两种格式，自动迁移 |
| 记忆刷盘的 AI 调用增加延迟 | 用户感知到额外等待 | 异步执行（goroutine），不阻塞主消息处理 |
| 并发消息导致重复刷盘 | 浪费 API 调用 | `MemoryFlushedAt` 原子标记 + mutex 保护 |
