package repository

import (
	"database/sql"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestQoderLockedStateMatchesRejectsConcurrentRuntimeChanges(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	proxyID := int64(17)
	expected := &service.Account{
		Status:                  service.StatusActive,
		ErrorMessage:            "",
		Schedulable:             true,
		ProxyID:                 &proxyID,
		LastUsedAt:              timePtr(now),
		RateLimitedAt:           timePtr(now.Add(time.Minute)),
		RateLimitResetAt:        timePtr(now.Add(2 * time.Minute)),
		OverloadUntil:           timePtr(now.Add(3 * time.Minute)),
		TempUnschedulableUntil:  timePtr(now.Add(4 * time.Minute)),
		TempUnschedulableReason: "refreshing",
		SessionWindowStart:      timePtr(now.Add(5 * time.Minute)),
		SessionWindowEnd:        timePtr(now.Add(6 * time.Minute)),
		SessionWindowStatus:     "active",
		Credentials:             map[string]any{"site": "cn", "pat": "pat-token", "model_mapping": map[string]any{"alias": "qmodel"}},
		Extra:                   map[string]any{"model_rate_limits": map[string]any{"qmodel": "limited"}},
	}
	matching := lockedQoderRuntimeState{
		status:                  expected.Status,
		errorMessage:            expected.ErrorMessage,
		schedulable:             expected.Schedulable,
		proxyID:                 sql.NullInt64{Int64: proxyID, Valid: true},
		lastUsedAt:              sql.NullTime{Time: *expected.LastUsedAt, Valid: true},
		rateLimitedAt:           sql.NullTime{Time: *expected.RateLimitedAt, Valid: true},
		rateLimitResetAt:        sql.NullTime{Time: *expected.RateLimitResetAt, Valid: true},
		overloadUntil:           sql.NullTime{Time: *expected.OverloadUntil, Valid: true},
		tempUnschedulableUntil:  sql.NullTime{Time: *expected.TempUnschedulableUntil, Valid: true},
		tempUnschedulableReason: expected.TempUnschedulableReason,
		sessionWindowStart:      sql.NullTime{Time: *expected.SessionWindowStart, Valid: true},
		sessionWindowEnd:        sql.NullTime{Time: *expected.SessionWindowEnd, Valid: true},
		sessionWindowStatus:     expected.SessionWindowStatus,
		extra:                   []byte(`{"model_rate_limits":{"qmodel":"limited"}}`),
	}
	credentials := []byte(`{"model_mapping":{"alias":"qmodel"},"pat":"pat-token","site":"cn"}`)
	require.True(t, qoderLockedStateMatches(expected, credentials, matching))

	tests := []struct {
		name   string
		mutate func(*lockedQoderRuntimeState, *[]byte)
	}{
		{name: "status", mutate: func(state *lockedQoderRuntimeState, _ *[]byte) { state.status = service.StatusError }},
		{name: "proxy", mutate: func(state *lockedQoderRuntimeState, _ *[]byte) { state.proxyID.Int64++ }},
		{name: "rate limit", mutate: func(state *lockedQoderRuntimeState, _ *[]byte) {
			state.rateLimitResetAt.Time = state.rateLimitResetAt.Time.Add(time.Minute)
		}},
		{name: "overload", mutate: func(state *lockedQoderRuntimeState, _ *[]byte) {
			state.overloadUntil.Time = state.overloadUntil.Time.Add(time.Minute)
		}},
		{name: "temporary block", mutate: func(state *lockedQoderRuntimeState, _ *[]byte) { state.tempUnschedulableReason = "concurrent refresh" }},
		{name: "session window", mutate: func(state *lockedQoderRuntimeState, _ *[]byte) {
			state.sessionWindowEnd.Time = state.sessionWindowEnd.Time.Add(time.Minute)
		}},
		{name: "last used", mutate: func(state *lockedQoderRuntimeState, _ *[]byte) {
			state.lastUsedAt.Time = state.lastUsedAt.Time.Add(time.Second)
		}},
		{name: "extra", mutate: func(state *lockedQoderRuntimeState, _ *[]byte) {
			state.extra = []byte(`{"model_rate_limits":{"qmodel":"changed"}}`)
		}},
		{name: "credentials", mutate: func(_ *lockedQoderRuntimeState, value *[]byte) {
			*value = []byte(`{"model_mapping":{"alias":"other"},"pat":"pat-token","site":"cn"}`)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := matching
			currentCredentials := append([]byte(nil), credentials...)
			tt.mutate(&state, &currentCredentials)
			require.False(t, qoderLockedStateMatches(expected, currentCredentials, state))
		})
	}
}

func timePtr(value time.Time) *time.Time {
	return &value
}
