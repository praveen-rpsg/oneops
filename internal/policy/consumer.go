package policy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// ConsumerConfig parameterises the policy consumer.
type ConsumerConfig struct {
	Interval   time.Duration // between tail passes (default 5s)
	BatchLimit int           // events read per chain per pass (default 200)
}

func (c *ConsumerConfig) withDefaults() {
	if c.Interval <= 0 {
		c.Interval = 5 * time.Second
	}
	if c.BatchLimit <= 0 {
		c.BatchLimit = 200
	}
}

// Consumer tails the committed audit log with its OWN cursor, evaluates enabled
// policies against each committed event, and enqueues a Execution for each
// match. It executes nothing here — the Executor runs actions asynchronously.
// Because it only ever observes committed audit events, a policy fires strictly
// after the governance operation committed.
type Consumer struct {
	source   EventSource
	cursor   CursorStore
	policies Store
	execs    ExecutionStore
	metrics  Metrics
	log      *slog.Logger
	newID    func() string
	now      func() time.Time
	cfg      ConsumerConfig
}

// NewConsumer builds the consumer. All stores are required.
func NewConsumer(source EventSource, cursor CursorStore, policies Store, execs ExecutionStore, metrics Metrics, log *slog.Logger, cfg ConsumerConfig) *Consumer {
	if metrics == nil {
		metrics = NopMetrics{}
	}
	if log == nil {
		log = slog.Default()
	}
	cfg.withDefaults()
	return &Consumer{
		source: source, cursor: cursor, policies: policies, execs: execs, metrics: metrics,
		log: log, newID: newExecutionID, now: func() time.Time { return time.Now().UTC() }, cfg: cfg,
	}
}

// Run tails on Interval until ctx is cancelled.
func (c *Consumer) Run(ctx context.Context) error {
	c.log.Info("policy consumer started", "interval", c.cfg.Interval.String())
	c.RunOnce(ctx)
	t := time.NewTicker(c.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			c.log.Info("policy consumer stopped", "reason", ctx.Err())
			return ctx.Err()
		case <-t.C:
			c.RunOnce(ctx)
		}
	}
}

// RunOnce performs one tail pass. Evaluate reuses the same path a replayed event
// would flow through — any committed event the consumer observes is evaluated.
func (c *Consumer) RunOnce(ctx context.Context) {
	chains, err := c.source.ListChainIDs(ctx)
	if err != nil {
		c.log.Error("policy consumer: list chains", "err", err)
		return
	}
	pols, err := c.policies.ListEnabled(ctx)
	if err != nil {
		c.log.Error("policy consumer: list policies", "err", err)
		return
	}
	c.metrics.SetActivePolicies(len(pols))
	if len(pols) == 0 {
		return
	}
	for _, chainID := range chains {
		if ctx.Err() != nil {
			return
		}
		c.tailChain(ctx, chainID, pols)
	}
}

func (c *Consumer) tailChain(ctx context.Context, chainID string, pols []Policy) {
	cursor, err := c.cursor.GetPolicyCursor(ctx, chainID)
	if err != nil {
		c.log.Error("policy consumer: get cursor", "chain_id", chainID, "err", err)
		return
	}
	evs, err := c.source.ListEvents(ctx, chainID, cursor, false, c.cfg.BatchLimit, "")
	if err != nil {
		c.log.Error("policy consumer: list events", "chain_id", chainID, "err", err)
		return
	}
	if len(evs) == 0 {
		return
	}
	now := c.now()
	maxSeq := cursor
	var execs []Execution
	for i := range evs {
		ev := eventFrom(evs[i])
		execs = append(execs, c.Evaluate(ev, pols, now)...)
		if evs[i].Seq > maxSeq {
			maxSeq = evs[i].Seq
		}
	}
	if len(execs) > 0 {
		if err := c.execs.Enqueue(ctx, execs); err != nil {
			c.log.Error("policy consumer: enqueue executions", "chain_id", chainID, "err", err)
			return // do not advance cursor; re-evaluate next pass
		}
	}
	if maxSeq > cursor {
		if err := c.cursor.SetPolicyCursor(ctx, chainID, maxSeq); err != nil {
			c.log.Error("policy consumer: set cursor", "chain_id", chainID, "err", err)
		}
	}
}

// Evaluate returns pending executions for the policies matching ev. It is the
// single evaluation path used for both live and replayed committed events.
func (c *Consumer) Evaluate(ev Event, pols []Policy, now time.Time) []Execution {
	var out []Execution
	// domain.FanOut confines the cross-tenant policy list to the event's owner
	// before any condition is evaluated. The consumer tails every tenant's
	// audit chains on a privileged connection, and policy actions make outbound
	// HTTP calls carrying operator-supplied configuration — so a policy
	// selecting another tenant's events would be an oracle over their
	// governance activity, and a request made on their behalf.
	for _, p := range domain.FanOut(ev, pols, func(p Policy) bool {
		return p.Enabled && p.Condition.Matches(ev)
	}) {
		{
			out = append(out, Execution{
				ID: c.newID(), PolicyID: p.ID, Event: ev, Status: ExecPending,
				NextAttemptAt: now, CreatedAt: now,
			})
		}
	}
	return out
}

func newExecutionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("policy: id generation failed")
	}
	return "pex_" + hex.EncodeToString(b[:])
}
