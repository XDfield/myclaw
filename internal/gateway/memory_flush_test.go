package gateway

import "testing"

func TestShouldRunMemoryFlush(t *testing.T) {
	tests := []struct {
		name            string
		totalTokens     int
		contextWindow   int
		reserveTokens   int
		softThreshold   int
		compactionCount int
		memoryFlushedAt int
		want            bool
	}{
		{
			name:            "below threshold",
			totalTokens:     50000,
			contextWindow:   100000,
			reserveTokens:   20000,
			softThreshold:   4000,
			compactionCount: 1,
			memoryFlushedAt: 0,
			want:            false,
		},
		{
			name:            "at threshold",
			totalTokens:     76000,
			contextWindow:   100000,
			reserveTokens:   20000,
			softThreshold:   4000,
			compactionCount: 1,
			memoryFlushedAt: 0,
			want:            true,
		},
		{
			name:            "above threshold",
			totalTokens:     90000,
			contextWindow:   100000,
			reserveTokens:   20000,
			softThreshold:   4000,
			compactionCount: 2,
			memoryFlushedAt: 0,
			want:            true,
		},
		{
			name:            "already flushed",
			totalTokens:     90000,
			contextWindow:   100000,
			reserveTokens:   20000,
			softThreshold:   4000,
			compactionCount: 3,
			memoryFlushedAt: 3,
			want:            false,
		},
		{
			name:            "zero totalTokens",
			totalTokens:     0,
			contextWindow:   100000,
			reserveTokens:   20000,
			softThreshold:   4000,
			compactionCount: 0,
			memoryFlushedAt: 0,
			want:            false,
		},
		{
			name:            "zero contextWindow",
			totalTokens:     50000,
			contextWindow:   0,
			reserveTokens:   20000,
			softThreshold:   4000,
			compactionCount: 0,
			memoryFlushedAt: 0,
			want:            false,
		},
		{
			name:            "threshold calculation non-positive",
			totalTokens:     90000,
			contextWindow:   100000,
			reserveTokens:   98000,
			softThreshold:   4000,
			compactionCount: 1,
			memoryFlushedAt: 0,
			want:            false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldRunMemoryFlush(tt.totalTokens, tt.contextWindow, tt.reserveTokens, tt.softThreshold, tt.compactionCount, tt.memoryFlushedAt)
			if got != tt.want {
				t.Errorf("shouldRunMemoryFlush() = %v, want %v", got, tt.want)
			}
		})
	}
}
