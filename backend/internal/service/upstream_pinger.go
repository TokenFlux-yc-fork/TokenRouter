package service

import (
	"context"
)

// DummyUpstreamPinger 临时实现，用于满足 HealthChecker 的依赖
// 后续需要实现真正的 ping 逻辑
type DummyUpstreamPinger struct{}

func NewDummyUpstreamPinger() *DummyUpstreamPinger {
	return &DummyUpstreamPinger{}
}

func (d *DummyUpstreamPinger) PingAnthropic(ctx context.Context, account *Account) bool {
	// TODO: 实现真正的 Anthropic API ping
	return true
}

func (d *DummyUpstreamPinger) PingOpenAI(ctx context.Context, account *Account) bool {
	// TODO: 实现真正的 OpenAI API ping
	return true
}

func (d *DummyUpstreamPinger) PingGemini(ctx context.Context, account *Account) bool {
	// TODO: 实现真正的 Gemini API ping
	return true
}

func (d *DummyUpstreamPinger) PingAntigravity(ctx context.Context, account *Account) bool {
	// TODO: 实现真正的 Antigravity API ping
	return true
}
