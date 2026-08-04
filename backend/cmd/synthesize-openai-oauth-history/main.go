package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/lib/pq"
)

const (
	defaultBefore       = "2026-08-04T00:00:00+08:00"
	defaultAccountCount = 90
	minimumAccountCount = 80
	maximumAccountCount = 100
	defaultTeamRatio    = 0.25
	defaultSeed         = int64(20260804)
	defaultChunkSize    = 20000
	vacuumEveryRows     = int64(500000)

	revokedErrorMessage = "Token revoked (401): Encountered invalidated oauth token for user, failing request"
	batchIDExtraKey     = "synthetic_history_batch_id"
	sourceIDExtraKey    = "synthetic_history_source_account_id"
	slotExtraKey        = "synthetic_history_slot"
)

var batchIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

type options struct {
	Before           time.Time
	AccountCount     int
	TeamRatio        float64
	Seed             int64
	BatchID          string
	ExpectedDigest   string
	SourceAccountIDs []int64
	ChunkSize        int
	Execute          bool
	RollbackBatch    string
}

type sourceAccount struct {
	ID             int64
	Name           string
	ProxyID        sql.NullInt64
	Concurrency    int
	Priority       int
	RateMultiplier float64
	UsageRows      int64
	FirstUsage     time.Time
	LastUsage      time.Time
	TotalCost      float64
	TargetCount    int
}

type accountSpec struct {
	SourceID       int64
	Slot           int
	Name           string
	PlanType       string
	Credentials    []byte
	Extra          []byte
	ProxyID        sql.NullInt64
	Concurrency    int
	Priority       int
	RateMultiplier float64
	CreatedAt      time.Time
}

type historyPlan struct {
	BatchID   string
	Before    time.Time
	Seed      int64
	Sources   []sourceAccount
	Accounts  []accountSpec
	PlusCount int
	TeamCount int
	UsageRows int64
	TotalCost float64
	Digest    string
}

type executionResult struct {
	InsertedAccounts      int64
	ReassignedUsageRows   int64
	UpdatedUsageSnapshots int64
	ErroredAccounts       int64
	SourceRowsRemaining   int64
	GeneratedRowsAtCutoff int64
}

type rollbackPlan struct {
	BatchID      string
	AccountCount int64
	UsageRows    int64
	SourceIDs    []int64
}

type rollbackResult struct {
	RestoredUsageRows int64
	DeletedAccounts   int64
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func main() {
	opts, err := parseOptions(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		log.Fatal(err)
	}

	db, err := openDatabase()
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	if opts.RollbackBatch != "" {
		plan, result, err := runRollback(ctx, db, opts)
		if err != nil {
			log.Fatalf("rollback failed: %v", err)
		}
		printRollback(plan, result, opts.Execute)
		return
	}

	if opts.Execute {
		plan, result, err := executeHistory(ctx, db, opts)
		if err != nil {
			log.Fatalf("execute failed: %v", err)
		}
		printPlan(plan, true)
		fmt.Printf("inserted_accounts=%d reassigned_usage_rows=%d updated_usage_snapshots=%d errored_accounts=%d source_rows_remaining=%d generated_rows_at_or_after_cutoff=%d\n",
			result.InsertedAccounts,
			result.ReassignedUsageRows,
			result.UpdatedUsageSnapshots,
			result.ErroredAccounts,
			result.SourceRowsRemaining,
			result.GeneratedRowsAtCutoff,
		)
		return
	}

	plan, err := buildHistoryPlan(ctx, db, opts)
	if err != nil {
		log.Fatalf("plan failed: %v", err)
	}
	printPlan(plan, false)
}

func parseOptions(args []string) (options, error) {
	fs := flag.NewFlagSet("synthesize-openai-oauth-history", flag.ContinueOnError)
	beforeRaw := fs.String("before", defaultBefore, "RFC3339 cutoff with +08:00 offset; rows at or after it are excluded")
	accountCount := fs.Int("account-count", defaultAccountCount, "number of OAuth accounts in this batch (80-100)")
	teamRatio := fs.Float64("team-ratio", defaultTeamRatio, "fraction of generated accounts using plan_type=team")
	seed := fs.Int64("seed", defaultSeed, "deterministic random seed")
	batchID := fs.String("batch-id", "", "stable batch identifier; generated from cutoff and seed when omitted")
	expectedDigest := fs.String("expected-plan-digest", "", "require this dry-run SHA-256 plan digest before --execute")
	sourceIDsRaw := fs.String("source-account-ids", "", "optional comma-separated OpenAI API-key account IDs")
	chunkSize := fs.Int("chunk-size", defaultChunkSize, "usage rows updated per transaction")
	execute := fs.Bool("execute", false, "commit the planned write (default is dry-run)")
	rollbackBatch := fs.String("rollback-batch", "", "restore usage rows and remove one generated batch")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	before, err := time.Parse(time.RFC3339, strings.TrimSpace(*beforeRaw))
	if err != nil {
		return options{}, fmt.Errorf("invalid --before: %w", err)
	}
	_, offset := before.Zone()
	if offset != 8*60*60 {
		return options{}, errors.New("--before must use the +08:00 offset")
	}
	if *accountCount < minimumAccountCount || *accountCount > maximumAccountCount {
		return options{}, fmt.Errorf("--account-count must be between %d and %d", minimumAccountCount, maximumAccountCount)
	}
	if math.IsNaN(*teamRatio) || math.IsInf(*teamRatio, 0) || *teamRatio <= 0 || *teamRatio >= 1 {
		return options{}, errors.New("--team-ratio must be greater than 0 and less than 1")
	}
	if *chunkSize <= 0 || *chunkSize > 100000 {
		return options{}, errors.New("--chunk-size must be between 1 and 100000")
	}

	sourceIDs, err := parseIDList(*sourceIDsRaw)
	if err != nil {
		return options{}, fmt.Errorf("invalid --source-account-ids: %w", err)
	}

	normalizedBatchID := strings.TrimSpace(*batchID)
	if normalizedBatchID == "" {
		normalizedBatchID = fmt.Sprintf("openai-oauth-history-%s-%d", before.Format("20060102"), *seed)
	}
	if !batchIDPattern.MatchString(normalizedBatchID) {
		return options{}, errors.New("--batch-id must be 1-64 characters using letters, numbers, dot, underscore, colon, or dash")
	}
	normalizedExpectedDigest := strings.ToLower(strings.TrimSpace(*expectedDigest))
	if normalizedExpectedDigest != "" {
		if len(normalizedExpectedDigest) != sha256.Size*2 {
			return options{}, errors.New("--expected-plan-digest must be a 64-character SHA-256 hex digest")
		}
		if _, err := hex.DecodeString(normalizedExpectedDigest); err != nil {
			return options{}, errors.New("--expected-plan-digest must be a 64-character SHA-256 hex digest")
		}
	}
	normalizedRollbackBatch := strings.TrimSpace(*rollbackBatch)
	if normalizedRollbackBatch != "" && !batchIDPattern.MatchString(normalizedRollbackBatch) {
		return options{}, errors.New("--rollback-batch has an invalid format")
	}
	if normalizedRollbackBatch != "" && normalizedExpectedDigest != "" {
		return options{}, errors.New("--expected-plan-digest cannot be combined with --rollback-batch")
	}
	if *execute && normalizedRollbackBatch == "" && normalizedExpectedDigest == "" {
		return options{}, errors.New("--execute requires --expected-plan-digest")
	}

	return options{
		Before:           before,
		AccountCount:     *accountCount,
		TeamRatio:        *teamRatio,
		Seed:             *seed,
		BatchID:          normalizedBatchID,
		ExpectedDigest:   normalizedExpectedDigest,
		SourceAccountIDs: sourceIDs,
		ChunkSize:        *chunkSize,
		Execute:          *execute,
		RollbackBatch:    normalizedRollbackBatch,
	}, nil
}

func parseIDList(raw string) ([]int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	seen := make(map[int64]struct{})
	ids := make([]int64, 0)
	for _, part := range strings.Split(raw, ",") {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%q is not a positive account ID", part)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

func openDatabase() (*sql.DB, error) {
	cfg, err := config.LoadForBootstrap()
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("postgres", cfg.Database.DSNWithTimezone(cfg.Timezone))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func buildHistoryPlan(ctx context.Context, q queryer, opts options) (*historyPlan, error) {
	sources, err := loadSources(ctx, q, opts.Before, opts.SourceAccountIDs)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, errors.New("no OpenAI API-key accounts have usage rows before the cutoff")
	}
	if len(sources) > opts.AccountCount {
		return nil, fmt.Errorf("%d source accounts exceed the %d-account batch", len(sources), opts.AccountCount)
	}

	var totalRows int64
	var totalCost float64
	for i := range sources {
		totalRows += sources[i].UsageRows
		totalCost += sources[i].TotalCost
	}
	if totalRows < int64(opts.AccountCount) {
		return nil, fmt.Errorf("only %d eligible usage rows exist; at least %d are required so every generated account receives history", totalRows, opts.AccountCount)
	}
	if err := allocateTargetCounts(sources, opts.AccountCount); err != nil {
		return nil, err
	}

	accounts, plusCount, teamCount, err := generateAccountSpecs(sources, opts)
	if err != nil {
		return nil, err
	}
	plan := &historyPlan{
		BatchID:   opts.BatchID,
		Before:    opts.Before,
		Seed:      opts.Seed,
		Sources:   sources,
		Accounts:  accounts,
		PlusCount: plusCount,
		TeamCount: teamCount,
		UsageRows: totalRows,
		TotalCost: totalCost,
	}
	plan.Digest = digestPlan(plan)
	return plan, nil
}

func loadSources(ctx context.Context, q queryer, before time.Time, sourceIDs []int64) ([]sourceAccount, error) {
	query := `
		SELECT
			a.id,
			a.name,
			a.proxy_id,
			a.concurrency,
			a.priority,
			a.rate_multiplier,
			COUNT(ul.id),
			MIN(ul.created_at),
			MAX(ul.created_at),
			COALESCE(SUM(COALESCE(ul.account_stats_cost, ul.total_cost, 0) * COALESCE(ul.account_rate_multiplier, 1)), 0)
		FROM accounts a
		JOIN usage_logs ul ON ul.account_id = a.id AND ul.created_at < $1
		WHERE a.platform = 'openai'
		  AND a.type = 'apikey'`
	args := []any{before}
	if len(sourceIDs) > 0 {
		query += " AND a.id = ANY($2)"
		args = append(args, pq.Array(sourceIDs))
	}
	query += `
		GROUP BY a.id, a.name, a.proxy_id, a.concurrency, a.priority, a.rate_multiplier
		ORDER BY a.id`

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	sources := make([]sourceAccount, 0)
	for rows.Next() {
		var source sourceAccount
		if err := rows.Scan(
			&source.ID,
			&source.Name,
			&source.ProxyID,
			&source.Concurrency,
			&source.Priority,
			&source.RateMultiplier,
			&source.UsageRows,
			&source.FirstUsage,
			&source.LastUsage,
			&source.TotalCost,
		); err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(sourceIDs) > 0 && len(sources) != len(sourceIDs) {
		found := make(map[int64]struct{}, len(sources))
		for _, source := range sources {
			found[source.ID] = struct{}{}
		}
		missing := make([]string, 0)
		for _, id := range sourceIDs {
			if _, ok := found[id]; !ok {
				missing = append(missing, strconv.FormatInt(id, 10))
			}
		}
		return nil, fmt.Errorf("source accounts missing, not OpenAI API-key, or without eligible history: %s", strings.Join(missing, ","))
	}
	return sources, nil
}

func allocateTargetCounts(sources []sourceAccount, total int) error {
	if total < len(sources) {
		return errors.New("target account count is smaller than source account count")
	}
	for i := range sources {
		if sources[i].UsageRows < 1 {
			return fmt.Errorf("source account %d has no eligible history", sources[i].ID)
		}
		sources[i].TargetCount = 1
	}
	for assigned := len(sources); assigned < total; assigned++ {
		best := -1
		bestScore := -1.0
		for i := range sources {
			if int64(sources[i].TargetCount) >= sources[i].UsageRows {
				continue
			}
			score := float64(sources[i].UsageRows) / float64(sources[i].TargetCount+1)
			if score > bestScore || (score == bestScore && (best < 0 || sources[i].ID < sources[best].ID)) {
				best = i
				bestScore = score
			}
		}
		if best < 0 {
			return errors.New("eligible history cannot cover every target account")
		}
		sources[best].TargetCount++
	}
	return nil
}

func generateAccountSpecs(sources []sourceAccount, opts options) ([]accountSpec, int, int, error) {
	teamCount := int(math.Round(float64(opts.AccountCount) * opts.TeamRatio))
	teamCount = max(1, min(opts.AccountCount-1, teamCount))
	plusCount := opts.AccountCount - teamCount
	plans := make([]string, 0, opts.AccountCount)
	for i := 0; i < plusCount; i++ {
		plans = append(plans, "plus")
	}
	for i := 0; i < teamCount; i++ {
		plans = append(plans, "team")
	}

	rng := rand.New(rand.NewSource(opts.Seed))
	rng.Shuffle(len(plans), func(i, j int) { plans[i], plans[j] = plans[j], plans[i] })
	accounts := make([]accountSpec, 0, opts.AccountCount)
	planIndex := 0
	for _, source := range sources {
		batchAnchor := source.FirstUsage.Add(-time.Duration(7+rng.Intn(24)) * 24 * time.Hour)
		for slot := 0; slot < source.TargetCount; slot++ {
			planType := plans[planIndex]
			planIndex++
			createdAt := batchAnchor.Add(time.Duration(rng.Intn(6*60)) * time.Minute).UTC()
			localPart := randomAlphaNumeric(rng, 10+rng.Intn(5))
			email := localPart + "@example.invalid"
			accountID := randomUUID(rng)
			userID := randomUUID(rng)
			credentials, err := json.Marshal(map[string]any{
				"access_token":            "fixture-at-" + randomHex(rng, 24),
				"refresh_token":           "fixture-rt-" + randomHex(rng, 24),
				"expires_at":              createdAt.Add(time.Duration(7+rng.Intn(30)) * 24 * time.Hour).Format(time.RFC3339),
				"email":                   email,
				"chatgpt_account_id":      accountID,
				"chatgpt_user_id":         userID,
				"organization_id":         accountID,
				"plan_type":               planType,
				"subscription_expires_at": createdAt.Add(time.Duration(180+rng.Intn(366)) * 24 * time.Hour).Format(time.RFC3339),
			})
			if err != nil {
				return nil, 0, 0, err
			}
			extra, err := json.Marshal(map[string]any{
				batchIDExtraKey:          opts.BatchID,
				sourceIDExtraKey:         source.ID,
				slotExtraKey:             slot,
				"synthetic_history_seed": opts.Seed,
			})
			if err != nil {
				return nil, 0, 0, err
			}
			concurrency := source.Concurrency
			if concurrency <= 0 {
				concurrency = 3
			}
			accounts = append(accounts, accountSpec{
				SourceID:       source.ID,
				Slot:           slot,
				Name:           email,
				PlanType:       planType,
				Credentials:    credentials,
				Extra:          extra,
				ProxyID:        source.ProxyID,
				Concurrency:    concurrency,
				Priority:       source.Priority,
				RateMultiplier: source.RateMultiplier,
				CreatedAt:      createdAt,
			})
		}
	}
	return accounts, plusCount, teamCount, nil
}

func randomAlphaNumeric(rng *rand.Rand, length int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	value := make([]byte, length)
	for i := range value {
		value[i] = alphabet[rng.Intn(len(alphabet))]
	}
	return string(value)
}

func randomHex(rng *rand.Rand, byteCount int) string {
	value := make([]byte, byteCount)
	_, _ = rng.Read(value)
	return hex.EncodeToString(value)
}

func randomUUID(rng *rand.Rand) string {
	value := make([]byte, 16)
	_, _ = rng.Read(value)
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

func digestPlan(plan *historyPlan) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%d\x00", plan.BatchID, plan.Before.UTC().Format(time.RFC3339Nano), plan.Seed)
	for _, source := range plan.Sources {
		_, _ = fmt.Fprintf(hash, "s:%d:%d:%d:%.10f:%s:%s\x00", source.ID, source.UsageRows, source.TargetCount, source.TotalCost, source.FirstUsage.UTC().Format(time.RFC3339Nano), source.LastUsage.UTC().Format(time.RFC3339Nano))
	}
	for _, account := range plan.Accounts {
		_, _ = fmt.Fprintf(hash, "a:%d:%d:%s:%s:%s\x00", account.SourceID, account.Slot, account.PlanType, account.Name, account.CreatedAt.UTC().Format(time.RFC3339Nano))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func executeHistory(ctx context.Context, db *sql.DB, opts options) (*historyPlan, *executionResult, error) {
	chunkSize := opts.ChunkSize
	if chunkSize == 0 {
		chunkSize = defaultChunkSize
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock(hashtext('synthesize_openai_oauth_history'))"); err != nil {
		return nil, nil, err
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(unlockCtx, "SELECT pg_advisory_unlock(hashtext('synthesize_openai_oauth_history'))")
	}()

	tx, err := beginMaintenanceTx(ctx, conn)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var existing int64
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM accounts WHERE extra->>$1 = $2", batchIDExtraKey, opts.BatchID).Scan(&existing); err != nil {
		return nil, nil, err
	}
	if existing != 0 {
		return nil, nil, fmt.Errorf("batch %q already has %d accounts", opts.BatchID, existing)
	}

	plan, err := buildHistoryPlan(ctx, tx, opts)
	if err != nil {
		return nil, nil, err
	}
	if plan.Digest != opts.ExpectedDigest {
		return nil, nil, fmt.Errorf("plan digest mismatch: current=%s expected=%s", plan.Digest, opts.ExpectedDigest)
	}
	inserted, err := insertAccounts(ctx, tx, plan)
	if err != nil {
		return nil, nil, err
	}
	reassigned, err := seedUsageRows(ctx, tx, plan)
	if err != nil {
		return nil, nil, err
	}
	if reassigned != int64(len(plan.Accounts)) {
		return nil, nil, fmt.Errorf("seeded %d usage rows, expected %d", reassigned, len(plan.Accounts))
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}

	sourceIDs := make([]int64, 0, len(plan.Sources))
	for _, source := range plan.Sources {
		sourceIDs = append(sourceIDs, source.ID)
	}
	rowsSinceVacuum := reassigned
	batchNumber := 0
	for {
		chunkTx, err := beginMaintenanceTx(ctx, conn)
		if err != nil {
			return nil, nil, fmt.Errorf("batch %q prepared after %d rows: %w", plan.BatchID, reassigned, err)
		}
		chunkRows, chunkErr := reassignUsageChunk(ctx, chunkTx, plan, sourceIDs, chunkSize)
		if chunkErr == nil {
			chunkErr = chunkTx.Commit()
		} else {
			_ = chunkTx.Rollback()
		}
		if chunkErr != nil {
			return nil, nil, fmt.Errorf("batch %q prepared after %d rows: %w", plan.BatchID, reassigned, chunkErr)
		}
		if chunkRows == 0 {
			break
		}
		batchNumber++
		reassigned += chunkRows
		rowsSinceVacuum += chunkRows
		if batchNumber%10 == 0 || reassigned == plan.UsageRows {
			fmt.Printf("progress batch_id=%s reassigned_usage_rows=%d expected_usage_rows=%d\n", plan.BatchID, reassigned, plan.UsageRows)
		}
		if rowsSinceVacuum >= vacuumEveryRows {
			if err := vacuumUsageLogs(ctx, conn); err != nil {
				return nil, nil, fmt.Errorf("batch %q assigned %d rows before vacuum failed: %w", plan.BatchID, reassigned, err)
			}
			rowsSinceVacuum = 0
			fmt.Printf("progress batch_id=%s vacuumed_after_rows=%d\n", plan.BatchID, reassigned)
		}
	}
	if reassigned != plan.UsageRows {
		return nil, nil, fmt.Errorf("reassigned %d usage rows, expected %d", reassigned, plan.UsageRows)
	}
	if rowsSinceVacuum > 0 {
		if err := vacuumUsageLogs(ctx, conn); err != nil {
			return nil, nil, fmt.Errorf("batch %q assigned %d rows before final vacuum failed: %w", plan.BatchID, reassigned, err)
		}
	}

	finalTx, err := beginMaintenanceTx(ctx, conn)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = finalTx.Rollback() }()
	updatedSnapshots, err := updateUsageSnapshots(ctx, finalTx, plan)
	if err != nil {
		return nil, nil, err
	}
	if updatedSnapshots != int64(len(plan.Accounts)) {
		return nil, nil, fmt.Errorf("updated %d usage snapshots, expected %d", updatedSnapshots, len(plan.Accounts))
	}
	errored, err := markBatchRevoked(ctx, finalTx, plan.BatchID)
	if err != nil {
		return nil, nil, err
	}
	if errored != int64(len(plan.Accounts)) {
		return nil, nil, fmt.Errorf("marked %d accounts revoked, expected %d", errored, len(plan.Accounts))
	}
	if _, err := finalTx.ExecContext(ctx, `
		INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
		SELECT 'account_changed', id, NULL, NULL
		FROM accounts
		WHERE extra->>$1 = $2`, batchIDExtraKey, plan.BatchID); err != nil {
		return nil, nil, err
	}

	var sourceRemaining int64
	if err := finalTx.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_logs WHERE account_id = ANY($1) AND created_at < $2", pq.Array(sourceIDs), plan.Before).Scan(&sourceRemaining); err != nil {
		return nil, nil, err
	}
	var generatedAtCutoff int64
	if err := finalTx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM usage_logs ul
		JOIN accounts a ON a.id = ul.account_id
		WHERE a.extra->>$1 = $2 AND ul.created_at >= $3`, batchIDExtraKey, plan.BatchID, plan.Before).Scan(&generatedAtCutoff); err != nil {
		return nil, nil, err
	}
	if sourceRemaining != 0 || generatedAtCutoff != 0 {
		return nil, nil, fmt.Errorf("verification failed: source_remaining=%d generated_at_or_after_cutoff=%d", sourceRemaining, generatedAtCutoff)
	}
	if err := finalTx.Commit(); err != nil {
		return nil, nil, err
	}
	return plan, &executionResult{
		InsertedAccounts:      inserted,
		ReassignedUsageRows:   reassigned,
		UpdatedUsageSnapshots: updatedSnapshots,
		ErroredAccounts:       errored,
		SourceRowsRemaining:   sourceRemaining,
		GeneratedRowsAtCutoff: generatedAtCutoff,
	}, nil
}

func beginMaintenanceTx(ctx context.Context, conn *sql.Conn) (*sql.Tx, error) {
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout = '5s'; SET LOCAL statement_timeout = '30min'"); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func vacuumUsageLogs(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, "VACUUM (ANALYZE FALSE, INDEX_CLEANUP ON, TRUNCATE FALSE) usage_logs")
	return err
}

func insertAccounts(ctx context.Context, tx *sql.Tx, plan *historyPlan) (int64, error) {
	statement, err := tx.PrepareContext(ctx, `
		INSERT INTO accounts (
			name, notes, platform, type, credentials, extra, proxy_id,
			concurrency, priority, rate_multiplier, status, error_message,
			created_at, updated_at, schedulable, auto_pause_on_expired
		) VALUES (
			$1, $2, 'openai', 'oauth', $3::jsonb, $4::jsonb, $5,
			$6, $7, $8, 'error', $10,
			$9, $9, FALSE, TRUE
		) RETURNING id`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = statement.Close() }()

	var inserted int64
	for _, account := range plan.Accounts {
		var proxyID any
		if account.ProxyID.Valid {
			proxyID = account.ProxyID.Int64
		}
		note := "synthetic OAuth history batch " + plan.BatchID
		var accountID int64
		if err := statement.QueryRowContext(ctx,
			account.Name,
			note,
			string(account.Credentials),
			string(account.Extra),
			proxyID,
			account.Concurrency,
			account.Priority,
			account.RateMultiplier,
			account.CreatedAt,
			revokedErrorMessage,
		).Scan(&accountID); err != nil {
			return inserted, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO account_groups (account_id, group_id, priority, created_at)
			SELECT $1, group_id, priority, $2
			FROM account_groups
			WHERE account_id = $3
			ON CONFLICT (account_id, group_id) DO NOTHING`, accountID, account.CreatedAt, account.SourceID); err != nil {
			return inserted, err
		}
		inserted++
	}
	return inserted, nil
}

func seedUsageRows(ctx context.Context, tx *sql.Tx, plan *historyPlan) (int64, error) {
	result, err := tx.ExecContext(ctx, `
		WITH targets AS MATERIALIZED (
			SELECT
				id AS target_id,
				(extra->>$1)::bigint AS source_id,
				(extra->>$2)::bigint AS slot
			FROM accounts
			WHERE extra->>$3 = $4
		), target_counts AS MATERIALIZED (
			SELECT source_id, COUNT(*)::bigint AS target_count
			FROM targets
			GROUP BY source_id
		), seed_rows AS MATERIALIZED (
			SELECT
				tc.source_id,
				seed.id,
				ROW_NUMBER() OVER (PARTITION BY tc.source_id ORDER BY seed.id) - 1 AS slot
			FROM target_counts tc
			CROSS JOIN LATERAL (
				SELECT ul.id
				FROM usage_logs ul
				WHERE ul.account_id = tc.source_id AND ul.created_at < $5
				ORDER BY ul.id
				LIMIT tc.target_count
			) seed
		), mapped AS (
			SELECT seed_rows.id, targets.target_id
			FROM seed_rows
			JOIN targets ON targets.source_id = seed_rows.source_id AND targets.slot = seed_rows.slot
		)
		UPDATE usage_logs ul
		SET account_id = mapped.target_id
		FROM mapped
		WHERE ul.id = mapped.id`,
		sourceIDExtraKey,
		slotExtraKey,
		batchIDExtraKey,
		plan.BatchID,
		plan.Before,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func reassignUsageChunk(ctx context.Context, tx *sql.Tx, plan *historyPlan, sourceIDs []int64, chunkSize int) (int64, error) {
	result, err := tx.ExecContext(ctx, `
		WITH targets AS MATERIALIZED (
			SELECT
				id AS target_id,
				(extra->>$1)::bigint AS source_id,
				(extra->>$2)::bigint AS slot
			FROM accounts
			WHERE extra->>$3 = $4
		), target_counts AS MATERIALIZED (
			SELECT source_id, COUNT(*)::bigint AS target_count
			FROM targets
			GROUP BY source_id
		), candidates AS MATERIALIZED (
			SELECT ul.id, ul.account_id AS source_id
			FROM usage_logs ul
			WHERE ul.account_id = ANY($5) AND ul.created_at < $6
			ORDER BY ul.account_id, ul.id
			LIMIT $7
			FOR UPDATE OF ul SKIP LOCKED
		), mapped AS (
			SELECT candidates.id, targets.target_id
			FROM candidates
			JOIN target_counts ON target_counts.source_id = candidates.source_id
			JOIN targets ON targets.source_id = candidates.source_id
			 AND targets.slot = ((hashtextextended(candidates.id::text, $8) % target_counts.target_count) + target_counts.target_count) % target_counts.target_count
		)
		UPDATE usage_logs ul
		SET account_id = mapped.target_id
		FROM mapped
		WHERE ul.id = mapped.id`,
		sourceIDExtraKey,
		slotExtraKey,
		batchIDExtraKey,
		plan.BatchID,
		pq.Array(sourceIDs),
		plan.Before,
		chunkSize,
		plan.Seed,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func updateUsageSnapshots(ctx context.Context, tx *sql.Tx, plan *historyPlan) (int64, error) {
	updatedAt := time.Now().UTC().Format(time.RFC3339)
	result, err := tx.ExecContext(ctx, `
		WITH generated AS MATERIALIZED (
			SELECT id, credentials->>'plan_type' AS plan_type
			FROM accounts
			WHERE extra->>$1 = $2
		), stats AS (
			SELECT
				ul.account_id,
				COUNT(*) AS request_count,
				MAX(ul.created_at) AS last_used_at,
				COALESCE(SUM(COALESCE(ul.account_stats_cost, ul.total_cost, 0) * COALESCE(ul.account_rate_multiplier, 1)), 0) AS total_cost,
				COALESCE(SUM(COALESCE(ul.account_stats_cost, ul.total_cost, 0) * COALESCE(ul.account_rate_multiplier, 1)) FILTER (WHERE ul.created_at >= $3::timestamptz - INTERVAL '5 hours' AND ul.created_at < $3::timestamptz), 0) AS five_hour_cost,
				COALESCE(SUM(COALESCE(ul.account_stats_cost, ul.total_cost, 0) * COALESCE(ul.account_rate_multiplier, 1)) FILTER (WHERE ul.created_at >= $3::timestamptz - INTERVAL '7 days' AND ul.created_at < $3::timestamptz), 0) AS seven_day_cost
			FROM usage_logs ul
			JOIN generated g ON g.id = ul.account_id
			GROUP BY ul.account_id
		)
		UPDATE accounts a
		SET
			last_used_at = stats.last_used_at,
			extra = COALESCE(a.extra, '{}'::jsonb) || jsonb_build_object(
				'synthetic_history_assigned_requests', stats.request_count,
				'synthetic_history_assigned_cost_usd', ROUND(stats.total_cost::numeric, 10),
				'synthetic_history_5h_cost_usd', ROUND(stats.five_hour_cost::numeric, 10),
				'synthetic_history_7d_cost_usd', ROUND(stats.seven_day_cost::numeric, 10),
				'codex_5h_used_percent', GREATEST(0, LEAST(100, ROUND((stats.five_hour_cost / CASE WHEN g.plan_type = 'team' THEN 15.0 ELSE 21.0 END * 100)::numeric, 2))),
				'codex_7d_used_percent', GREATEST(0, LEAST(100, ROUND((stats.seven_day_cost / CASE WHEN g.plan_type = 'team' THEN 100.0 ELSE 140.0 END * 100)::numeric, 2))),
				'codex_5h_window_minutes', 300,
				'codex_7d_window_minutes', 10080,
				'codex_usage_updated_at', $4::text,
				'codex_5h_updated_at', $4::text,
				'codex_7d_updated_at', $4::text
			)
		FROM stats
		JOIN generated g ON g.id = stats.account_id
		WHERE a.id = stats.account_id`, batchIDExtraKey, plan.BatchID, plan.Before, updatedAt)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func markBatchRevoked(ctx context.Context, tx *sql.Tx, batchID string) (int64, error) {
	result, err := tx.ExecContext(ctx, `
		UPDATE accounts
		SET status = 'error', error_message = $1, schedulable = FALSE, updated_at = NOW()
		WHERE extra->>$2 = $3`, revokedErrorMessage, batchIDExtraKey, batchID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func runRollback(ctx context.Context, db *sql.DB, opts options) (*rollbackPlan, *rollbackResult, error) {
	if !opts.Execute {
		plan, err := loadRollbackPlan(ctx, db, opts.RollbackBatch)
		return plan, nil, err
	}
	chunkSize := opts.ChunkSize
	if chunkSize == 0 {
		chunkSize = defaultChunkSize
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock(hashtext('synthesize_openai_oauth_history'))"); err != nil {
		return nil, nil, err
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(unlockCtx, "SELECT pg_advisory_unlock(hashtext('synthesize_openai_oauth_history'))")
	}()
	plan, err := loadRollbackPlan(ctx, conn, opts.RollbackBatch)
	if err != nil {
		return nil, nil, err
	}
	if plan.AccountCount == 0 {
		return nil, nil, fmt.Errorf("batch %q does not exist", opts.RollbackBatch)
	}

	var restored int64
	var rowsSinceVacuum int64
	batchNumber := 0
	for {
		tx, err := beginMaintenanceTx(ctx, conn)
		if err != nil {
			return nil, nil, err
		}
		chunkRows, chunkErr := rollbackUsageChunk(ctx, tx, opts.RollbackBatch, chunkSize)
		if chunkErr == nil {
			chunkErr = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		if chunkErr != nil {
			return nil, nil, fmt.Errorf("rollback %q restored %d rows before failure: %w", opts.RollbackBatch, restored, chunkErr)
		}
		if chunkRows == 0 {
			break
		}
		batchNumber++
		restored += chunkRows
		rowsSinceVacuum += chunkRows
		if batchNumber%10 == 0 || restored == plan.UsageRows {
			fmt.Printf("progress rollback_batch=%s restored_usage_rows=%d expected_usage_rows=%d\n", opts.RollbackBatch, restored, plan.UsageRows)
		}
		if rowsSinceVacuum >= vacuumEveryRows {
			if err := vacuumUsageLogs(ctx, conn); err != nil {
				return nil, nil, fmt.Errorf("rollback %q restored %d rows before vacuum failed: %w", opts.RollbackBatch, restored, err)
			}
			rowsSinceVacuum = 0
		}
	}
	if restored != plan.UsageRows {
		return nil, nil, fmt.Errorf("restored %d usage rows, expected %d", restored, plan.UsageRows)
	}
	if rowsSinceVacuum > 0 {
		if err := vacuumUsageLogs(ctx, conn); err != nil {
			return nil, nil, fmt.Errorf("rollback %q restored rows before final vacuum failed: %w", opts.RollbackBatch, err)
		}
	}

	tx, err := beginMaintenanceTx(ctx, conn)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
		SELECT
			'account_changed',
			a.id,
			NULL,
			CASE
				WHEN COUNT(ag.group_id) = 0 THEN NULL::jsonb
				ELSE jsonb_build_object('group_ids', ARRAY_AGG(ag.group_id ORDER BY ag.priority, ag.group_id) FILTER (WHERE ag.group_id IS NOT NULL))
			END
		FROM accounts a
		LEFT JOIN account_groups ag ON ag.account_id = a.id
		WHERE a.extra->>$1 = $2
		GROUP BY a.id`, batchIDExtraKey, opts.RollbackBatch); err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM account_groups ag
		USING accounts a
		WHERE ag.account_id = a.id AND a.extra->>$1 = $2`, batchIDExtraKey, opts.RollbackBatch); err != nil {
		return nil, nil, err
	}
	deleteResult, err := tx.ExecContext(ctx, "DELETE FROM accounts WHERE extra->>$1 = $2", batchIDExtraKey, opts.RollbackBatch)
	if err != nil {
		return nil, nil, err
	}
	deleted, err := deleteResult.RowsAffected()
	if err != nil {
		return nil, nil, err
	}
	if deleted != plan.AccountCount {
		return nil, nil, fmt.Errorf("deleted %d accounts, expected %d", deleted, plan.AccountCount)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return plan, &rollbackResult{RestoredUsageRows: restored, DeletedAccounts: deleted}, nil
}

func rollbackUsageChunk(ctx context.Context, tx *sql.Tx, batchID string, chunkSize int) (int64, error) {
	result, err := tx.ExecContext(ctx, `
		WITH generated AS MATERIALIZED (
			SELECT id, (extra->>$1)::bigint AS source_id
			FROM accounts
			WHERE extra->>$2 = $3
		), candidates AS MATERIALIZED (
			SELECT ul.id, generated.source_id
			FROM usage_logs ul
			JOIN generated ON generated.id = ul.account_id
			ORDER BY ul.account_id, ul.id
			LIMIT $4
			FOR UPDATE OF ul SKIP LOCKED
		)
		UPDATE usage_logs ul
		SET account_id = candidates.source_id
		FROM candidates
		WHERE ul.id = candidates.id`, sourceIDExtraKey, batchIDExtraKey, batchID, chunkSize)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func loadRollbackPlan(ctx context.Context, q queryer, batchID string) (*rollbackPlan, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT
			a.id,
			(a.extra->>$1)::bigint,
			COUNT(ul.id)
		FROM accounts a
		LEFT JOIN usage_logs ul ON ul.account_id = a.id
		WHERE a.extra->>$2 = $3
		GROUP BY a.id, (a.extra->>$1)::bigint
		ORDER BY a.id`, sourceIDExtraKey, batchIDExtraKey, batchID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	plan := &rollbackPlan{BatchID: batchID}
	sources := make(map[int64]struct{})
	for rows.Next() {
		var accountID, sourceID, usageRows int64
		if err := rows.Scan(&accountID, &sourceID, &usageRows); err != nil {
			return nil, err
		}
		plan.AccountCount++
		plan.UsageRows += usageRows
		sources[sourceID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for sourceID := range sources {
		plan.SourceIDs = append(plan.SourceIDs, sourceID)
	}
	sort.Slice(plan.SourceIDs, func(i, j int) bool { return plan.SourceIDs[i] < plan.SourceIDs[j] })
	return plan, nil
}

func printPlan(plan *historyPlan, execute bool) {
	mode := "dry-run"
	if execute {
		mode = "execute"
	}
	fmt.Printf("mode=%s batch_id=%s cutoff_east8=%s cutoff_utc=%s seed=%d plan_digest=%s\n",
		mode,
		plan.BatchID,
		plan.Before.Format(time.RFC3339),
		plan.Before.UTC().Format(time.RFC3339),
		plan.Seed,
		plan.Digest,
	)
	fmt.Printf("generated_accounts=%d plus=%d team=%d eligible_usage_rows=%d eligible_account_cost_usd=%.10f\n",
		len(plan.Accounts), plan.PlusCount, plan.TeamCount, plan.UsageRows, plan.TotalCost)
	for _, source := range plan.Sources {
		fmt.Printf("source_account_id=%d source_account_name=%q generated_accounts=%d eligible_usage_rows=%d eligible_usage_cost_usd=%.10f first_usage=%s last_usage=%s\n",
			source.ID,
			source.Name,
			source.TargetCount,
			source.UsageRows,
			source.TotalCost,
			source.FirstUsage.UTC().Format(time.RFC3339),
			source.LastUsage.UTC().Format(time.RFC3339),
		)
	}
}

func printRollback(plan *rollbackPlan, result *rollbackResult, execute bool) {
	mode := "dry-run"
	if execute {
		mode = "execute"
	}
	fmt.Printf("mode=%s rollback_batch=%s generated_accounts=%d assigned_usage_rows=%d source_account_ids=%v\n",
		mode, plan.BatchID, plan.AccountCount, plan.UsageRows, plan.SourceIDs)
	if result != nil {
		fmt.Printf("restored_usage_rows=%d deleted_accounts=%d\n", result.RestoredUsageRows, result.DeletedAccounts)
	}
}
