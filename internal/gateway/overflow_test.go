package gateway

import (
	"errors"
	"testing"
)

func TestIsContextOverflowError_Nil(t *testing.T) {
	if isContextOverflowError(nil) {
		t.Error("expected false for nil error")
	}
}

func TestIsContextOverflowError_OverflowPatterns(t *testing.T) {
	tests := []struct {
		msg string
	}{
		{"prompt is too long"},
		{"context length exceeded"},
		{"context_length_exceeded"},
		{"maximum context length is 4096 tokens"},
		{"token limit reached"},
		{"too many tokens in request"},
		{"context window full"},
		{"content would exceed the limit"},
		{"request too large for model"},
		{"input is too long"},
	}

	for _, tt := range tests {
		err := errors.New(tt.msg)
		if !isContextOverflowError(err) {
			t.Errorf("expected true for %q", tt.msg)
		}
	}
}

func TestIsContextOverflowError_NonOverflow(t *testing.T) {
	tests := []struct {
		msg string
	}{
		{"connection refused"},
		{"timeout"},
		{"internal server error"},
		{"invalid api key"},
	}

	for _, tt := range tests {
		err := errors.New(tt.msg)
		if isContextOverflowError(err) {
			t.Errorf("expected false for %q", tt.msg)
		}
	}
}

func TestIsContextOverflowError_CaseInsensitive(t *testing.T) {
	tests := []struct {
		msg string
	}{
		{"Context Length Exceeded"},
		{"PROMPT IS TOO LONG"},
		{"Token Limit reached"},
	}

	for _, tt := range tests {
		err := errors.New(tt.msg)
		if !isContextOverflowError(err) {
			t.Errorf("expected true for case-insensitive match %q", tt.msg)
		}
	}
}
