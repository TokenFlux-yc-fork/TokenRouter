//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestListDueUpstreamBillingProbeAccountsHandlesInvalidCalendarDate(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	_, err := tx.ExecContext(ctx, `
		UPDATE accounts
		SET extra = extra - 'upstream_billing_probe_enabled' - 'upstream_billing_probe'
	`)
	require.NoError(t, err)

	insert := func(name, nextProbeAt string) int64 {
		t.Helper()
		var id int64
		extra := fmt.Sprintf(`{
			"upstream_billing_probe_enabled": true,
			"upstream_billing_probe": {"status": "ok", "next_probe_at": %q}
		}`, nextProbeAt)
		err := scanSingleRow(ctx, tx, `
			INSERT INTO accounts (name, platform, type, status, extra)
			VALUES ($1, 'openai', $2, 'active', $3::jsonb)
			RETURNING id
		`, []any{name, service.AccountTypeAPIKey, extra}, &id)
		require.NoError(t, err)
		return id
	}

	invalidID := insert("probe-invalid-calendar-date", "2026-99-99T12:00:00Z")
	dueID := insert("probe-due", "2026-07-14T11:59:59Z")
	_ = insert("probe-not-due", "2026-07-14T12:00:01Z")

	accounts, err := repo.ListDueUpstreamBillingProbeAccounts(ctx, now, 20)
	require.NoError(t, err)
	require.Len(t, accounts, 2)
	require.Equal(t, invalidID, accounts[0].ID)
	require.Equal(t, dueID, accounts[1].ID)
}

func insertUpstreamBillingProbeAccount(ctx context.Context, t *testing.T, tx sqlQueryer, name, nextProbeAt string) int64 {
	t.Helper()
	var id int64
	extra := fmt.Sprintf(`{
		"upstream_billing_probe_enabled": true,
		"upstream_billing_probe": {"status": "ok", "next_probe_at": %q}
	}`, nextProbeAt)
	err := scanSingleRow(ctx, tx, `
		INSERT INTO accounts (name, platform, type, status, extra)
		VALUES ($1, 'openai', $2, 'active', $3::jsonb)
		RETURNING id
	`, []any{name, service.AccountTypeAPIKey, extra}, &id)
	require.NoError(t, err)
	return id
}

// rfc3339WithFraction 使用显式小数秒后缀输出 UTC 时间，例如
// "2026-07-25T17:29:00.123456789Z"。探测逻辑的 Go 写入端使用 RFC3339Nano，
// 因此持久化的小数秒最多包含 9 位。
func rfc3339WithFraction(t time.Time, fraction string) string {
	return fmt.Sprintf("%s.%sZ", t.UTC().Format("2006-01-02T15:04:05"), fraction)
}

// 回归定时探测饿死问题：Go 使用 RFC3339Nano 持久化 next_probe_at，7 到 9 位
// 小数秒无法被 jsonpath datetime() 解析。修复前这些记录都会被视为非法并按开放策略
// 判定到期，使每轮总是返回 ID 最小的账号，而 ID 较大的账号永远得不到探测。
func TestListDueUpstreamBillingProbeAccountsParsesNanosecondTimestamps(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	now := time.Date(2026, time.July, 25, 17, 30, 0, 0, time.UTC)
	_, err := tx.ExecContext(ctx, `
		UPDATE accounts
		SET extra = extra - 'upstream_billing_probe_enabled' - 'upstream_billing_probe'
	`)
	require.NoError(t, err)

	// 插入 22 个尚未到期的低 ID 账号，使用旧写入端生成的 9 位小数时间戳，
	// 无需迁移已有数据即可覆盖真实存量格式。
	notDue := now.Add(time.Hour)
	for i := 0; i < 22; i++ {
		insertUpstreamBillingProbeAccount(ctx, t, tx,
			fmt.Sprintf("probe-nano-not-due-%02d", i),
			rfc3339WithFraction(notDue.Add(time.Duration(i)*time.Second), "123456789"))
	}
	// 再插入三个已经到期的高 ID 账号，分别使用 9、8、7 位小数；解析后的顺序
	// 必须由到期时间决定，而不是由插入顺序或 ID 决定。
	dueThird := insertUpstreamBillingProbeAccount(ctx, t, tx,
		"probe-nano-due-9digits", rfc3339WithFraction(now.Add(-time.Minute), "123456789"))
	dueFirst := insertUpstreamBillingProbeAccount(ctx, t, tx,
		"probe-nano-due-8digits", rfc3339WithFraction(now.Add(-3*time.Minute), "12345678"))
	dueSecond := insertUpstreamBillingProbeAccount(ctx, t, tx,
		"probe-nano-due-7digits", rfc3339WithFraction(now.Add(-2*time.Minute), "1234567"))

	accounts, err := repo.ListDueUpstreamBillingProbeAccounts(ctx, now, 20)
	require.NoError(t, err)
	require.Len(t, accounts, 3)
	require.Equal(t, dueFirst, accounts[0].ID)
	require.Equal(t, dueSecond, accounts[1].ID)
	require.Equal(t, dueThird, accounts[2].ID)
}

// 到期账号多于单轮上限时，必须按解析后的到期时间选择，确保最初 limit 个 ID 之后的
// 账号也能轮到；修复前每轮都由同一批低 ID 账号占满名额。
func TestListDueUpstreamBillingProbeAccountsSelectsEarliestDueAcrossIDs(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	now := time.Date(2026, time.July, 25, 17, 30, 0, 0, time.UTC)
	_, err := tx.ExecContext(ctx, `
		UPDATE accounts
		SET extra = extra - 'upstream_billing_probe_enabled' - 'upstream_billing_probe'
	`)
	require.NoError(t, err)

	// 插入 25 个到期账号；ID 最小的账号到期时间最晚，因此正确查询应在本轮选择
	// 这里 ID 最大的 20 条记录。
	ids := make([]int64, 0, 25)
	for i := 0; i < 25; i++ {
		due := now.Add(-time.Duration(i+1) * time.Minute)
		ids = append(ids, insertUpstreamBillingProbeAccount(ctx, t, tx,
			fmt.Sprintf("probe-nano-order-%02d", i),
			rfc3339WithFraction(due, "123456789")))
	}

	accounts, err := repo.ListDueUpstreamBillingProbeAccounts(ctx, now, 20)
	require.NoError(t, err)
	require.Len(t, accounts, 20)
	got := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		got = append(got, account.ID)
	}
	// 最早到期的记录排在前面，即索引最高的记录在前；最近到期的五条低索引、
	// 低 ID 记录不会进入本轮限额。
	want := make([]int64, 0, 20)
	for i := 24; i >= 5; i-- {
		want = append(want, ids[i])
	}
	require.Equal(t, want, got)
}
