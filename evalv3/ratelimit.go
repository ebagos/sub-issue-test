// ratelimit.go

package main

import (
	"context"
	"log"
	"time"

	"github.com/google/go-github/v69/github"
)

type RateLimitHandler struct {
	client *github.Client
}

func NewRateLimitHandler(client *github.Client) *RateLimitHandler {
	return &RateLimitHandler{
		client: client,
	}
}

func (h *RateLimitHandler) WaitForRateLimit(ctx context.Context) error {
	rate, _, err := h.client.RateLimits(ctx)
	if err != nil {
		return err
	}

	if rate.Core.Remaining == 0 {
		waitDuration := time.Until(rate.Core.Reset.Time)
		log.Printf("Rate limit reached. Waiting for %v minutes...", waitDuration.Minutes())
		time.Sleep(waitDuration)
	}

	return nil
}

func (h *RateLimitHandler) CheckRateLimit(ctx context.Context) {
	rate, _, err := h.client.RateLimits(ctx)
	if err != nil {
		log.Printf("Error checking rate limit: %v", err)
		return
	}

	log.Printf("API Rate Limit - Remaining: %d, Reset: %v",
		rate.Core.Remaining,
		time.Until(rate.Core.Reset.Time).Minutes())
}
