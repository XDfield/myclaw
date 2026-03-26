package heartbeat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stellarlinkco/myclaw/internal/logging"
)

var hblog = logging.Component("heartbeat")

type Service struct {
	workspace   string
	onHeartbeat func(prompt string) (string, error)
	interval    time.Duration
}

func New(workspace string, onHB func(string) (string, error), interval time.Duration) *Service {
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	return &Service{
		workspace:   workspace,
		onHeartbeat: onHB,
		interval:    interval,
	}
}

func (s *Service) Start(ctx context.Context) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	hblog.Info().Dur("interval", s.interval).Msg("started")

	for {
		select {
		case <-ticker.C:
			s.tick()
		case <-ctx.Done():
			hblog.Info().Msg("stopped")
			return nil
		}
	}
}

func (s *Service) tick() {
	hbPath := filepath.Join(s.workspace, "HEARTBEAT.md")
	data, err := os.ReadFile(hbPath)
	if err != nil {
		if !os.IsNotExist(err) {
			hblog.Error().Err(err).Msg("read error")
		}
		return
	}

	content := strings.TrimSpace(string(data))
	if content == "" {
		return
	}

	hblog.Info().Int("chars", len(content)).Msg("triggering")

	if s.onHeartbeat == nil {
		hblog.Warn().Msg("no handler set")
		return
	}

	result, err := s.onHeartbeat(content)
	if err != nil {
		hblog.Error().Err(err).Msg("heartbeat error")
		return
	}

	if strings.Contains(result, "HEARTBEAT_OK") {
		hblog.Info().Msg("nothing to do")
	} else {
		hblog.Info().Str("result", truncate(result, 200)).Msg("heartbeat result")
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
