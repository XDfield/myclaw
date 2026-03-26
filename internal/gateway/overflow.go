package gateway

import "strings"

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
