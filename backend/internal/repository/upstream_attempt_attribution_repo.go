package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/service"
)

type upstreamAttemptAttributionRepository struct {
	db *sql.DB
}

var _ service.UpstreamAttemptAttributionRepository = (*upstreamAttemptAttributionRepository)(nil)

func NewUpstreamAttemptAttributionRepository(db *sql.DB) service.UpstreamAttemptAttributionRepository {
	return &upstreamAttemptAttributionRepository{db: db}
}

func (r *upstreamAttemptAttributionRepository) Upsert(ctx context.Context, attribution service.UpstreamAttemptAttribution) (service.UpstreamAttemptAttribution, error) {
	attribution.Normalize()
	if err := attribution.Validate(); err != nil {
		return service.UpstreamAttemptAttribution{}, err
	}
	if r == nil || r.db == nil {
		return service.UpstreamAttemptAttribution{}, errors.New("upstream attempt attribution repository db is nil")
	}

	row := r.db.QueryRowContext(ctx, `
		INSERT INTO upstream_attempt_attributions (
			attempt_id, client_request_id, gateway_request_id, upstream_request_id,
			upstream_response_id, ws_connection_id, ws_turn_id, account_id, transport,
			upstream_provider, upstream_fingerprint, effective_model, contract_version,
			state, state_version, transport_observed, http_observed, http_status,
			semantic_observed, semantic_outcome, output_item_done_count,
			compaction_item_count, malformed_item_count, terminal_event, terminal_count,
			successful_terminal, usage_observed, input_tokens, output_tokens,
			cache_creation_input_tokens, cache_read_input_tokens, image_input_tokens,
			image_output_tokens, cost_usd, delivery_observed, delivery_committed, safe_to_failover,
			started_at, completed_at, observed_at, created_at, updated_at
		) VALUES (
			$1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, ''), NULLIF($7, ''),
			$8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21,
			$22, $23, $24, $25, $26, $27, $28, $29, $30, $31, $32, $33, $34, $35,
			$36, $37, $38, $39, $40, NOW(), NOW()
		)
		ON CONFLICT (attempt_id) DO UPDATE SET
			upstream_request_id = EXCLUDED.upstream_request_id,
			upstream_response_id = EXCLUDED.upstream_response_id,
			ws_connection_id = EXCLUDED.ws_connection_id,
			ws_turn_id = EXCLUDED.ws_turn_id,
			state = EXCLUDED.state,
			state_version = EXCLUDED.state_version,
			transport_observed = EXCLUDED.transport_observed,
			http_observed = EXCLUDED.http_observed,
			http_status = EXCLUDED.http_status,
			semantic_observed = EXCLUDED.semantic_observed,
			semantic_outcome = EXCLUDED.semantic_outcome,
			output_item_done_count = EXCLUDED.output_item_done_count,
			compaction_item_count = EXCLUDED.compaction_item_count,
			malformed_item_count = EXCLUDED.malformed_item_count,
			terminal_event = EXCLUDED.terminal_event,
			terminal_count = EXCLUDED.terminal_count,
			successful_terminal = EXCLUDED.successful_terminal,
			usage_observed = EXCLUDED.usage_observed,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_creation_input_tokens = EXCLUDED.cache_creation_input_tokens,
			cache_read_input_tokens = EXCLUDED.cache_read_input_tokens,
			image_input_tokens = EXCLUDED.image_input_tokens,
			image_output_tokens = EXCLUDED.image_output_tokens,
			cost_usd = EXCLUDED.cost_usd,
			delivery_observed = EXCLUDED.delivery_observed,
			delivery_committed = EXCLUDED.delivery_committed,
			safe_to_failover = EXCLUDED.safe_to_failover,
			completed_at = EXCLUDED.completed_at,
			observed_at = EXCLUDED.observed_at,
			updated_at = NOW()
		WHERE upstream_attempt_attributions.client_request_id = EXCLUDED.client_request_id
		  AND upstream_attempt_attributions.gateway_request_id = EXCLUDED.gateway_request_id
		  AND upstream_attempt_attributions.account_id = EXCLUDED.account_id
		  AND upstream_attempt_attributions.transport = EXCLUDED.transport
		  AND upstream_attempt_attributions.upstream_provider = EXCLUDED.upstream_provider
		  AND upstream_attempt_attributions.upstream_fingerprint = EXCLUDED.upstream_fingerprint
		  AND upstream_attempt_attributions.effective_model = EXCLUDED.effective_model
		  AND upstream_attempt_attributions.contract_version = EXCLUDED.contract_version
		  AND upstream_attempt_attributions.started_at = EXCLUDED.started_at
		  AND upstream_attempt_attributions.state <> 'terminal'
		  AND EXCLUDED.state_version > upstream_attempt_attributions.state_version
		  AND CASE upstream_attempt_attributions.state
				WHEN 'started' THEN 1
				WHEN 'in_progress' THEN 2
				WHEN 'terminal' THEN 3
			  END <= CASE EXCLUDED.state
				WHEN 'started' THEN 1
				WHEN 'in_progress' THEN 2
				WHEN 'terminal' THEN 3
			  END
		RETURNING `+upstreamAttemptAttributionColumns,
		attribution.AttemptID,
		attribution.ClientRequestID,
		attribution.GatewayRequestID,
		attribution.UpstreamRequestID,
		attribution.UpstreamResponseID,
		attribution.WSConnectionID,
		attribution.WSTurnID,
		attribution.AccountID,
		attribution.Transport,
		attribution.Domain.Provider,
		attribution.Domain.UpstreamFingerprint,
		attribution.Domain.EffectiveModel,
		attribution.Domain.ContractVersion,
		attribution.State,
		attribution.StateVersion,
		attribution.TransportObserved,
		attribution.HTTPObserved,
		attribution.HTTPStatus,
		attribution.SemanticObserved,
		attribution.Semantic.Outcome,
		attribution.Semantic.OutputItemDoneCount,
		attribution.Semantic.CompactionItemCount,
		attribution.Semantic.MalformedItemCount,
		attribution.Semantic.TerminalEvent,
		attribution.Semantic.TerminalCount,
		attribution.Semantic.SuccessfulTerminal,
		attribution.Usage.Observed,
		attribution.Usage.InputTokens,
		attribution.Usage.OutputTokens,
		attribution.Usage.CacheCreationInputTokens,
		attribution.Usage.CacheReadInputTokens,
		attribution.Usage.ImageInputTokens,
		attribution.Usage.ImageOutputTokens,
		attribution.Usage.CostUSD,
		attribution.DeliveryObserved,
		attribution.DeliveryCommitted,
		attribution.SafeToFailover,
		attribution.StartedAt,
		attribution.CompletedAt,
		attribution.ObservedAt,
	)
	updated, err := scanUpstreamAttemptAttribution(row)
	if err == nil {
		return updated, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return service.UpstreamAttemptAttribution{}, err
	}

	existing, getErr := r.Get(ctx, attribution.AttemptID)
	if getErr != nil {
		return service.UpstreamAttemptAttribution{}, getErr
	}
	if existing == nil {
		return service.UpstreamAttemptAttribution{}, fmt.Errorf("upstream attempt attribution upsert returned no row: %w", service.ErrUpstreamAttemptConflict)
	}
	if !sameUpstreamAttemptIdentity(*existing, attribution) {
		return service.UpstreamAttemptAttribution{}, service.ErrUpstreamAttemptConflict
	}
	return *existing, nil
}

func (r *upstreamAttemptAttributionRepository) Get(ctx context.Context, attemptID service.AttemptID) (*service.UpstreamAttemptAttribution, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("upstream attempt attribution repository db is nil")
	}
	if attemptID == "" {
		return nil, service.ErrUpstreamAttemptInvalid
	}
	row := r.db.QueryRowContext(ctx, `SELECT `+upstreamAttemptAttributionColumns+` FROM upstream_attempt_attributions WHERE attempt_id = $1`, attemptID)
	attribution, err := scanUpstreamAttemptAttribution(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &attribution, nil
}

const upstreamAttemptAttributionColumns = `
	attempt_id, client_request_id, gateway_request_id,
	COALESCE(upstream_request_id, ''), COALESCE(upstream_response_id, ''),
	COALESCE(ws_connection_id, ''), COALESCE(ws_turn_id, ''), account_id, transport,
	upstream_provider, upstream_fingerprint, effective_model, contract_version,
	state, state_version, transport_observed, http_observed, http_status,
	semantic_observed, semantic_outcome, output_item_done_count,
	compaction_item_count, malformed_item_count, terminal_event, terminal_count,
	successful_terminal, usage_observed, input_tokens, output_tokens,
	cache_creation_input_tokens, cache_read_input_tokens, image_input_tokens,
	image_output_tokens, cost_usd, delivery_observed, delivery_committed, safe_to_failover,
	started_at, completed_at, observed_at, created_at, updated_at`

func scanUpstreamAttemptAttribution(row interface{ Scan(...any) error }) (service.UpstreamAttemptAttribution, error) {
	var (
		out         service.UpstreamAttemptAttribution
		transport   string
		state       string
		provider    string
		fingerprint string
	)
	err := row.Scan(
		&out.AttemptID, &out.ClientRequestID, &out.GatewayRequestID,
		&out.UpstreamRequestID, &out.UpstreamResponseID, &out.WSConnectionID, &out.WSTurnID,
		&out.AccountID, &transport, &provider, &fingerprint, &out.Domain.EffectiveModel,
		&out.Domain.ContractVersion, &state, &out.StateVersion, &out.TransportObserved,
		&out.HTTPObserved, &out.HTTPStatus, &out.SemanticObserved, &out.Semantic.Outcome,
		&out.Semantic.OutputItemDoneCount, &out.Semantic.CompactionItemCount,
		&out.Semantic.MalformedItemCount, &out.Semantic.TerminalEvent,
		&out.Semantic.TerminalCount, &out.Semantic.SuccessfulTerminal, &out.Usage.Observed,
		&out.Usage.InputTokens, &out.Usage.OutputTokens, &out.Usage.CacheCreationInputTokens,
		&out.Usage.CacheReadInputTokens, &out.Usage.ImageInputTokens, &out.Usage.ImageOutputTokens,
		&out.Usage.CostUSD, &out.DeliveryObserved, &out.DeliveryCommitted, &out.SafeToFailover, &out.StartedAt,
		&out.CompletedAt, &out.ObservedAt, &out.CreatedAt, &out.UpdatedAt,
	)
	if err != nil {
		return service.UpstreamAttemptAttribution{}, err
	}
	out.Transport = service.UpstreamAttemptTransport(transport)
	out.State = service.UpstreamAttemptState(state)
	out.Domain.Provider = service.OpenAIUpstreamProvider(provider)
	out.Domain.UpstreamFingerprint = service.OpenAIUpstreamFingerprint(fingerprint)
	return out, nil
}

func sameUpstreamAttemptIdentity(a, b service.UpstreamAttemptAttribution) bool {
	return a.AttemptID == b.AttemptID && a.ClientRequestID == b.ClientRequestID &&
		a.GatewayRequestID == b.GatewayRequestID && a.AccountID == b.AccountID &&
		a.Transport == b.Transport && a.Domain == b.Domain && a.StartedAt.Equal(b.StartedAt)
}
