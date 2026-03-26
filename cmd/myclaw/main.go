package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cexll/agentsdk-go/pkg/api"
	"github.com/cexll/agentsdk-go/pkg/model"
	runtimeskills "github.com/cexll/agentsdk-go/pkg/runtime/skills"
	"github.com/spf13/cobra"
	"github.com/stellarlinkco/myclaw/internal/config"
	"github.com/stellarlinkco/myclaw/internal/gateway"
	"github.com/stellarlinkco/myclaw/internal/logging"
	"github.com/stellarlinkco/myclaw/internal/memory"
	"github.com/stellarlinkco/myclaw/internal/skills"
)

// Runtime interface for agent runtime (allows mocking in tests)
type Runtime interface {
	Run(ctx context.Context, req api.Request) (*api.Response, error)
	Close()
}

// runtimeWrapper wraps api.Runtime to implement Runtime interface
type runtimeWrapper struct {
	rt *api.Runtime
}

func (r *runtimeWrapper) Run(ctx context.Context, req api.Request) (*api.Response, error) {
	return r.rt.Run(ctx, req)
}

func (r *runtimeWrapper) Close() {
	r.rt.Close()
}

// RuntimeFactory creates a Runtime instance
type RuntimeFactory func(cfg *config.Config) (Runtime, error)

// DefaultRuntimeFactory creates the default agentsdk-go runtime
func DefaultRuntimeFactory(cfg *config.Config) (Runtime, error) {
	if cfg.Provider.APIKey == "" {
		return nil, fmt.Errorf("API key not set. Run 'myclaw onboard' or set MYCLAW_API_KEY / ANTHROPIC_API_KEY")
	}

	mem := memory.NewMemoryStore(cfg.Agent.Workspace)
	sysPrompt := buildSystemPrompt(cfg, mem)
	skillRegs := loadRuntimeSkills(cfg)

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
		Skills: skillRegs,
	})
	if err != nil {
		return nil, fmt.Errorf("create runtime: %w", err)
	}
	return &runtimeWrapper{rt: rt}, nil
}

// AgentOptions for running agent with custom dependencies
type AgentOptions struct {
	RuntimeFactory RuntimeFactory
	Stdin          io.Reader
	Stdout         io.Writer
	Stderr         io.Writer
}

var rootCmd = &cobra.Command{
	Use:   "myclaw",
	Short: "myclaw - personal AI assistant",
}

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Run agent in single message or REPL mode",
	RunE:  runAgent,
}

var gatewayCmd = &cobra.Command{
	Use:   "gateway",
	Short: "Start the full gateway (channels + cron + heartbeat)",
	RunE:  runGateway,
}

var onboardCmd = &cobra.Command{
	Use:   "onboard",
	Short: "Initialize config and workspace",
	RunE:  runOnboard,
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show myclaw status",
	RunE:  runStatus,
}

var skillsCmd = &cobra.Command{
	Use:   "skills",
	Short: "Inspect configured skills",
}

var skillsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List loaded skills",
	RunE:  runSkillsList,
}

var skillsInfoCmd = &cobra.Command{
	Use:   "info <name>",
	Short: "Show skill details",
	Args:  cobra.ExactArgs(1),
	RunE:  runSkillsInfo,
}

var skillsCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Check skills directory and loading status",
	RunE:  runSkillsCheck,
}

var messageFlag string

const skillsJSONSchemaVersion = 1

func init() {
	agentCmd.Flags().StringVarP(&messageFlag, "message", "m", "", "Single message to send")
	skillsListCmd.Flags().Bool("json", false, "Output as JSON")
	skillsInfoCmd.Flags().Bool("json", false, "Output as JSON")
	skillsCheckCmd.Flags().Bool("json", false, "Output as JSON")
	skillsCmd.AddCommand(skillsListCmd, skillsInfoCmd, skillsCheckCmd)
	rootCmd.AddCommand(agentCmd, gatewayCmd, onboardCmd, statusCmd, skillsCmd)
}

func main() {
	// Pre-load config to initialize logging before any command runs.
	cfg, err := config.LoadConfig()
	if err == nil {
		logging.Init(cfg.Log)
	} else {
		// Fallback: initialize with defaults so logging works even if config fails.
		logging.Init(logging.DefaultLogConfig())
	}

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// runAgent is the command handler that uses default options
func runAgent(cmd *cobra.Command, args []string) error {
	return runAgentWithOptions(AgentOptions{})
}

// runAgentWithOptions runs the agent with injectable dependencies for testing
func runAgentWithOptions(opts AgentOptions) error {
	cfg, err := config.LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Use injected factory or default
	factory := opts.RuntimeFactory
	if factory == nil {
		factory = DefaultRuntimeFactory
	}

	rt, err := factory(cfg)
	if err != nil {
		return err
	}
	defer rt.Close()

	// Use injected IO or defaults
	stdin := opts.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	ctx := context.Background()

	// Single message mode
	if messageFlag != "" {
		resp, err := rt.Run(ctx, api.Request{
			Prompt:    messageFlag,
			SessionID: "cli",
		})
		if err != nil {
			return fmt.Errorf("agent error: %w", err)
		}
		if resp != nil && resp.Result != nil {
			fmt.Fprintln(stdout, resp.Result.Output)
		}
		return nil
	}

	// REPL mode
	fmt.Fprintln(stdout, "myclaw agent (type 'exit' to quit)")
	scanner := bufio.NewScanner(stdin)
	for {
		fmt.Fprint(stdout, "\n> ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		if input == "exit" || input == "quit" {
			break
		}

		resp, err := rt.Run(ctx, api.Request{
			Prompt:    input,
			SessionID: "cli-repl",
		})
		if err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			continue
		}
		if resp != nil && resp.Result != nil {
			fmt.Fprintln(stdout, resp.Result.Output)
		}
	}
	return nil
}

func runGateway(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if cfg.Provider.APIKey == "" {
		return fmt.Errorf("API key not set. Run 'myclaw onboard' or set MYCLAW_API_KEY / ANTHROPIC_API_KEY")
	}

	gw, err := gateway.New(cfg)
	if err != nil {
		return fmt.Errorf("create gateway: %w", err)
	}

	return gw.Run(context.Background())
}

func runOnboard(cmd *cobra.Command, args []string) error {
	cfgDir := config.ConfigDir()
	cfgPath := config.ConfigPath()

	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		cfg := config.DefaultConfig()
		data, _ := json.MarshalIndent(cfg, "", "  ")
		if err := os.WriteFile(cfgPath, data, 0644); err != nil {
			return fmt.Errorf("write config: %w", err)
		}
		fmt.Printf("Created config: %s\n", cfgPath)
	} else {
		fmt.Printf("Config already exists: %s\n", cfgPath)
	}

	cfg, _ := config.LoadConfig()
	ws := cfg.Agent.Workspace
	if err := os.MkdirAll(filepath.Join(ws, "memory"), 0755); err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}
	if err := os.MkdirAll(resolveSkillsDir(cfg), 0755); err != nil {
		return fmt.Errorf("create skills dir: %w", err)
	}

	writeIfNotExists(filepath.Join(ws, "AGENTS.md"), defaultAgentsMD)
	writeIfNotExists(filepath.Join(ws, "SOUL.md"), defaultSoulMD)
	writeIfNotExists(filepath.Join(ws, "memory", "MEMORY.md"), "")
	writeIfNotExists(filepath.Join(ws, "HEARTBEAT.md"), "")

	fmt.Printf("Workspace ready: %s\n", ws)
	fmt.Printf("Skills dir: %s\n", resolveSkillsDir(cfg))
	fmt.Println("\nNext steps:")
	fmt.Printf("  1. Edit %s to set your API key\n", cfgPath)
	fmt.Println("  2. Or set MYCLAW_API_KEY environment variable")
	fmt.Printf("  3. Add skills under %s (optional)\n", resolveSkillsDir(cfg))
	fmt.Println("  4. Run 'myclaw agent -m \"Hello\"' to test")

	return nil
}

func runStatus(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Printf("Config: error (%v)\n", err)
		return nil
	}

	fmt.Printf("Config: %s\n", config.ConfigPath())
	fmt.Printf("Workspace: %s\n", cfg.Agent.Workspace)
	fmt.Printf("Model: %s\n", cfg.Agent.Model)
	fmt.Printf("Provider: %s\n", providerDisplay(cfg.Provider.Type))
	if cfg.Provider.APIKey != "" && len(cfg.Provider.APIKey) > 8 {
		masked := cfg.Provider.APIKey[:4] + "..." + cfg.Provider.APIKey[len(cfg.Provider.APIKey)-4:]
		fmt.Printf("API Key: %s\n", masked)
	} else if cfg.Provider.APIKey != "" {
		fmt.Println("API Key: set")
	} else {
		fmt.Println("API Key: not set")
	}
	fmt.Printf("Telegram: enabled=%v\n", cfg.Channels.Telegram.Enabled)
	fmt.Printf("Feishu: enabled=%v\n", cfg.Channels.Feishu.Enabled)
	fmt.Printf("WeCom: enabled=%v\n", cfg.Channels.WeCom.Enabled)
	fmt.Printf("Skills: enabled=%v dir=%s\n", cfg.Skills.Enabled, resolveSkillsDir(cfg))

	if _, err := os.Stat(cfg.Agent.Workspace); err != nil {
		fmt.Println("Workspace: not found (run 'myclaw onboard')")
	} else {
		mem := memory.NewMemoryStore(cfg.Agent.Workspace)
		lt, _ := mem.ReadLongTerm()
		if lt != "" {
			fmt.Printf("Memory: %d bytes\n", len(lt))
		} else {
			fmt.Println("Memory: empty")
		}
	}

	return nil
}

func runSkillsList(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	skillDir := resolveSkillsDir(cfg)
	jsonOutput := readJSONFlag(cmd)
	if !jsonOutput {
		fmt.Printf("Skills: enabled=%v dir=%s\n", cfg.Skills.Enabled, skillDir)
	}
	if !cfg.Skills.Enabled {
		if jsonOutput {
			return printJSON(map[string]any{
				"schemaVersion": skillsJSONSchemaVersion,
				"command":       "skills.list",
				"ok":            true,
				"enabled":       cfg.Skills.Enabled,
				"dir":           skillDir,
				"loaded":        0,
				"skills":        []map[string]any{},
			})
		}
		fmt.Println("Skills are disabled in config.")
		return nil
	}

	registrations, err := skills.LoadSkills(skillDir)
	if err != nil {
		return fmt.Errorf("load skills: %w", err)
	}

	if !jsonOutput {
		fmt.Printf("Loaded skills: %d\n", len(registrations))
	}
	if len(registrations) == 0 {
		if jsonOutput {
			return printJSON(map[string]any{
				"schemaVersion": skillsJSONSchemaVersion,
				"command":       "skills.list",
				"ok":            true,
				"enabled":       cfg.Skills.Enabled,
				"dir":           skillDir,
				"loaded":        0,
				"skills":        []map[string]any{},
			})
		}
		fmt.Println("No skills found.")
		return nil
	}

	if jsonOutput {
		skillsJSON := make([]map[string]any, 0, len(registrations))
		for _, registration := range registrations {
			desc := strings.TrimSpace(registration.Definition.Description)
			if desc == "" {
				desc = "(no description)"
			}
			skillsJSON = append(skillsJSON, map[string]any{
				"name":        registration.Definition.Name,
				"description": desc,
				"keywords":    extractSkillKeywords(registration),
			})
		}
		return printJSON(map[string]any{
			"schemaVersion": skillsJSONSchemaVersion,
			"command":       "skills.list",
			"ok":            true,
			"enabled":       cfg.Skills.Enabled,
			"dir":           skillDir,
			"loaded":        len(registrations),
			"skills":        skillsJSON,
		})
	}

	for _, registration := range registrations {
		desc := strings.TrimSpace(registration.Definition.Description)
		if desc == "" {
			desc = "(no description)"
		}
		fmt.Printf("- %s: %s\n", registration.Definition.Name, desc)
	}

	return nil
}

func runSkillsInfo(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	jsonOutput := readJSONFlag(cmd)
	if !cfg.Skills.Enabled {
		return fmt.Errorf("skills are disabled in config")
	}

	target := strings.TrimSpace(args[0])
	if target == "" {
		return fmt.Errorf("skill name is required")
	}

	skillDir := resolveSkillsDir(cfg)
	registrations, err := skills.LoadSkills(skillDir)
	if err != nil {
		return fmt.Errorf("load skills: %w", err)
	}

	registration := findSkillRegistration(registrations, target)
	if registration == nil {
		return fmt.Errorf("skill not found: %s", target)
	}

	var sourcePath string
	var preview string
	var handlerError string
	result, execErr := registration.Handler.Execute(context.Background(), runtimeskills.ActivationContext{})
	if execErr != nil {
		handlerError = execErr.Error()
	} else {
		if source, ok := result.Metadata["source_path"].(string); ok {
			sourcePath = source
		}
		if outputText, ok := result.Output.(string); ok {
			preview = summarizeSkillOutput(outputText)
		}
	}
	keywords := extractSkillKeywords(*registration)
	if jsonOutput {
		payload := map[string]any{
			"schemaVersion": skillsJSONSchemaVersion,
			"command":       "skills.info",
			"ok":            true,
			"name":          registration.Definition.Name,
			"description":   strings.TrimSpace(registration.Definition.Description),
			"dir":           skillDir,
			"keywords":      keywords,
			"source":        sourcePath,
			"preview":       preview,
		}
		if handlerError != "" {
			payload["handlerError"] = handlerError
		}
		if payload["description"] == "" {
			payload["description"] = "(no description)"
		}
		return printJSON(payload)
	}

	fmt.Printf("Name: %s\n", registration.Definition.Name)
	desc := strings.TrimSpace(registration.Definition.Description)
	if desc == "" {
		desc = "(no description)"
	}
	fmt.Printf("Description: %s\n", desc)
	fmt.Printf("Skills dir: %s\n", skillDir)

	if len(keywords) == 0 {
		fmt.Println("Keywords: (none)")
	} else {
		fmt.Printf("Keywords: %s\n", strings.Join(keywords, ", "))
	}

	if sourcePath != "" {
		fmt.Printf("Source: %s\n", sourcePath)
	}
	if preview != "" {
		fmt.Println("Prompt preview:")
		fmt.Println(preview)
	}

	return nil
}

func runSkillsCheck(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	skillDir := resolveSkillsDir(cfg)
	jsonOutput := readJSONFlag(cmd)
	if !jsonOutput {
		fmt.Printf("Skills: enabled=%v dir=%s\n", cfg.Skills.Enabled, skillDir)
	}
	if !cfg.Skills.Enabled {
		if jsonOutput {
			return printJSON(map[string]any{
				"schemaVersion":  skillsJSONSchemaVersion,
				"command":        "skills.check",
				"ok":             true,
				"enabled":        cfg.Skills.Enabled,
				"dir":            skillDir,
				"skillFolders":   0,
				"loaded":         0,
				"missingSkillMD": []string{},
				"result":         "disabled",
			})
		}
		fmt.Println("Result: disabled")
		return nil
	}

	info, statErr := os.Stat(skillDir)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			if jsonOutput {
				return printJSON(map[string]any{
					"schemaVersion":  skillsJSONSchemaVersion,
					"command":        "skills.check",
					"ok":             true,
					"enabled":        cfg.Skills.Enabled,
					"dir":            skillDir,
					"skillFolders":   0,
					"loaded":         0,
					"missingSkillMD": []string{},
					"result":         "ok",
					"note":           "skills directory not found",
				})
			}
			fmt.Println("Skills directory: not found")
			fmt.Println("Result: ok (no skills loaded)")
			return nil
		}
		return fmt.Errorf("stat skills dir: %w", statErr)
	}
	if !info.IsDir() {
		return fmt.Errorf("skills path is not a directory: %s", skillDir)
	}

	entries, err := os.ReadDir(skillDir)
	if err != nil {
		return fmt.Errorf("read skills dir: %w", err)
	}

	skillFolders := 0
	missingSkillFile := make([]string, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillFolders++
		skillPath := filepath.Join(skillDir, entry.Name(), "SKILL.md")
		if _, err := os.Stat(skillPath); os.IsNotExist(err) {
			missingSkillFile = append(missingSkillFile, entry.Name())
		}
	}
	sort.Strings(missingSkillFile)

	registrations, err := skills.LoadSkills(skillDir)
	if err != nil {
		return fmt.Errorf("load skills: %w", err)
	}
	if jsonOutput {
		return printJSON(map[string]any{
			"schemaVersion":  skillsJSONSchemaVersion,
			"command":        "skills.check",
			"ok":             true,
			"enabled":        cfg.Skills.Enabled,
			"dir":            skillDir,
			"skillFolders":   skillFolders,
			"loaded":         len(registrations),
			"missingSkillMD": missingSkillFile,
			"result":         "ok",
		})
	}

	fmt.Printf("Skill folders: %d\n", skillFolders)
	fmt.Printf("Loaded skills: %d\n", len(registrations))
	if len(missingSkillFile) > 0 {
		fmt.Printf("Missing SKILL.md: %s\n", strings.Join(missingSkillFile, ", "))
	}
	fmt.Println("Result: ok")
	return nil
}

func providerDisplay(t string) string {
	if t == "" {
		return "anthropic (default)"
	}
	return t
}

func resolveSkillsDir(cfg *config.Config) string {
	if cfg.Skills.Dir != "" {
		return cfg.Skills.Dir
	}
	return filepath.Join(cfg.Agent.Workspace, "skills")
}

func loadRuntimeSkills(cfg *config.Config) []api.SkillRegistration {
	if !cfg.Skills.Enabled {
		return nil
	}

	skillRegs, err := skills.LoadSkills(resolveSkillsDir(cfg))
	if err != nil {
		logging.Logger.Warn().Err(err).Msg("skills load warning")
		return nil
	}
	return skillRegs
}

func findSkillRegistration(
	registrations []api.SkillRegistration,
	name string,
) *api.SkillRegistration {
	target := strings.TrimSpace(name)
	if target == "" {
		return nil
	}
	targetLower := strings.ToLower(target)
	for index := range registrations {
		if registrations[index].Definition.Name == target {
			return &registrations[index]
		}
	}
	for index := range registrations {
		if strings.ToLower(registrations[index].Definition.Name) == targetLower {
			return &registrations[index]
		}
	}
	return nil
}

func extractSkillKeywords(registration api.SkillRegistration) []string {
	collected := make([]string, 0)
	for _, matcher := range registration.Definition.Matchers {
		switch typed := matcher.(type) {
		case runtimeskills.KeywordMatcher:
			collected = append(collected, typed.Any...)
		}
	}
	if len(collected) == 0 {
		return nil
	}

	unique := make(map[string]struct{}, len(collected))
	out := make([]string, 0, len(collected))
	for _, keyword := range collected {
		normalized := strings.ToLower(strings.TrimSpace(keyword))
		if normalized == "" {
			continue
		}
		if _, exists := unique[normalized]; exists {
			continue
		}
		unique[normalized] = struct{}{}
		out = append(out, normalized)
	}
	sort.Strings(out)
	return out
}

func summarizeSkillOutput(output string) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(trimmed, "\n")
	maxLines := 8
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return strings.Join(lines, "\n")
}

func readJSONFlag(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	flag := cmd.Flags().Lookup("json")
	if flag == nil {
		return false
	}
	value, err := cmd.Flags().GetBool("json")
	return err == nil && value
}

func printJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	fmt.Println(string(data))
	return nil
}

func buildSystemPrompt(cfg *config.Config, mem *memory.MemoryStore) string {
	var sb strings.Builder

	if data, err := os.ReadFile(filepath.Join(cfg.Agent.Workspace, "AGENTS.md")); err == nil {
		sb.Write(data)
		sb.WriteString("\n\n")
	}

	if data, err := os.ReadFile(filepath.Join(cfg.Agent.Workspace, "SOUL.md")); err == nil {
		sb.Write(data)
		sb.WriteString("\n\n")
	}

	if memCtx := mem.GetMemoryContext(); memCtx != "" {
		sb.WriteString(memCtx)
	}

	return sb.String()
}

func writeIfNotExists(path, content string) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		_ = os.WriteFile(path, []byte(content), 0644)
		fmt.Printf("  Created: %s\n", path)
	}
}

const defaultAgentsMD = `# myclaw Agent

You are myclaw, a personal AI assistant.

You have access to tools for file operations, web search, and command execution.
Use them to help the user accomplish tasks.

## Guidelines
- Be concise and helpful
- Use tools proactively when needed
- Remember information the user tells you by writing to memory
- Check your memory context for previously stored information
`

const defaultSoulMD = `# Soul

You are a capable personal assistant that helps with daily tasks,
research, coding, and general questions.

Your personality:
- Direct and efficient
- Technical when needed, simple when possible
- Proactive about using tools to get real answers
`
