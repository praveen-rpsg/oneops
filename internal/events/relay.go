package events

import (
	"context"
	"log/slog"
	"time"
)

// IDGen generates unique delivery ids. A default (crypto/rand hex) is provided.
type IDGen func() string

// RelayConfig parameterises the relay.
type RelayConfig struct {
	// Interval between tail passes (default 5s).
	Interval time.Duration
	// BatchLimit bounds events read per chain per pass (default 200).
	BatchLimit int
}

func (c *RelayConfig) withDefaults() {
	if c.Interval <= 0 {
		c.Interval = 5 * time.Second
	}
	if c.BatchLimit <= 0 {
		c.BatchLimit = 200
	}
}

// Relay tails the committed audit log and enqueues webhook deliveries for
// matching, enabled webhooks. It is the single "publish after commit" point: it
// only ever observes committed audit events, so a rolled-back operation (which
// leaves no audit event) produces no delivery.
type Relay struct {
	source   EventSource
	cursors  CursorStore
	webhooks WebhookStore
	deliv    DeliveryStore
	metrics  Metrics
	log      *slog.Logger
	newID    IDGen
	now      func() time.Time
	cfg      RelayConfig
}

// NewRelay builds a relay. source, cursors, webhooks, and deliv are required.
func NewRelay(source EventSource, cursors CursorStore, webhooks WebhookStore, deliv DeliveryStore, metrics Metrics, log *slog.Logger, cfg RelayConfig) *Relay {
	if metrics == nil {
		metrics = NopMetrics{}
	}
	if log == nil {
		log = slog.Default()
	}
	cfg.withDefaults()
	return &Relay{
		source: source, cursors: cursors, webhooks: webhooks, deliv: deliv,
		metrics: metrics, log: log, newID: newDeliveryID, now: func() time.Time { return time.Now().UTC() }, cfg: cfg,
	}
}

// Run tails on Interval until ctx is cancelled, then returns ctx.Err().
func (r *Relay) Run(ctx context.Context) error {
	r.log.Info("event relay started", "interval", r.cfg.Interval.String())
	r.RunOnce(ctx)
	t := time.NewTicker(r.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			r.log.Info("event relay stopped", "reason", ctx.Err())
			return ctx.Err()
		case <-t.C:
			r.RunOnce(ctx)
		}
	}
}

// RunOnce performs one tail pass across all chains. Errors are logged and do not
// abort the pass; a chain's cursor only advances past events whose deliveries
// were durably enqueued.
func (r *Relay) RunOnce(ctx context.Context) {
	chains, err := r.source.ListChainIDs(ctx)
	if err != nil {
		r.log.Error("event relay: list chains", "err", err)
		return
	}
	subs, err := r.webhooks.ListEnabled(ctx)
	if err != nil {
		r.log.Error("event relay: list webhooks", "err", err)
		return
	}
	r.metrics.SetActiveWebhooks(len(subs))
	if len(subs) == 0 {
		return // nothing subscribed; do not advance cursors (no backfill needed)
	}

	for _, chainID := range chains {
		if ctx.Err() != nil {
			return
		}
		r.tailChain(ctx, chainID, subs)
	}
}

func (r *Relay) tailChain(ctx context.Context, chainID string, subs []Webhook) {
	cursor, err := r.cursors.GetCursor(ctx, chainID)
	if err != nil {
		r.log.Error("event relay: get cursor", "chain_id", chainID, "err", err)
		return
	}
	evs, err := r.source.ListEvents(ctx, chainID, cursor, false, r.cfg.BatchLimit, "")
	if err != nil {
		r.log.Error("event relay: list events", "chain_id", chainID, "err", err)
		return
	}
	if len(evs) == 0 {
		return
	}

	now := r.now()
	var deliveries []Delivery
	maxSeq := cursor
	for i := range evs {
		ev := eventFrom(evs[i])
		for _, wh := range subs {
			if wh.Matches(ev) {
				deliveries = append(deliveries, Delivery{
					ID:            r.newID(),
					WebhookID:     wh.ID,
					Event:         ev,
					Status:        StatusPending,
					NextAttemptAt: now,
					CreatedAt:     now,
				})
			}
		}
		if evs[i].Seq > maxSeq {
			maxSeq = evs[i].Seq
		}
	}

	if len(deliveries) > 0 {
		if err := r.deliv.Enqueue(ctx, deliveries); err != nil {
			r.log.Error("event relay: enqueue", "chain_id", chainID, "err", err)
			return // do not advance cursor; retry this range next pass
		}
	}
	if maxSeq > cursor {
		if err := r.cursors.SetCursor(ctx, chainID, maxSeq); err != nil {
			r.log.Error("event relay: set cursor", "chain_id", chainID, "err", err)
		}
	}
}
