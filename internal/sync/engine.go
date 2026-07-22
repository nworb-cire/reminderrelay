package sync

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
)

const (
	otelScope       = "reminderrelay/sync"
	spanReconcile   = "sync.reconcile"
	metricCreated   = "reminderrelay.sync.items.created"
	metricUpdated   = "reminderrelay.sync.items.updated"
	metricDeleted   = "reminderrelay.sync.items.deleted"
	metricConflicts = "reminderrelay.sync.conflicts"
	metricErrors    = "reminderrelay.sync.errors"
)

// HAConnector provides WebSocket lifecycle methods for the Engine.
// Implemented by [homeassistant.Adapter].
type HAConnector interface {
	HASource
	Connect(ctx context.Context) error
	Close() error
	SubscribeChanges(ctx context.Context, entityIDs []string, callback func(entityID string)) error
}

// RemindersConnector receives native EventKit database-change notifications.
// Implemented by [reminders.Adapter].
type RemindersConnector interface {
	WatchChanges(ctx context.Context) (<-chan struct{}, error)
}

// Engine orchestrates the sync lifecycle from EventKit notifications and Home
// Assistant WebSocket events. A low-frequency recovery pass protects against
// process/network gaps; it is not the primary change-detection mechanism.
type Engine struct {
	reconciler       *Reconciler
	remConn          RemindersConnector
	haConn           HAConnector
	listMappings     map[string]string
	recoveryInterval time.Duration
	log              *slog.Logger

	// OTel instruments — always non-nil (no-op when telemetry is disabled).
	tracer       trace.Tracer
	cntCreated   metric.Int64Counter
	cntUpdated   metric.Int64Counter
	cntDeleted   metric.Int64Counter
	cntConflicts metric.Int64Counter
	cntErrors    metric.Int64Counter
}

// NewEngine creates an Engine. Nil connectors disable that side's push stream;
// the recovery pass remains available as a safety net.
func NewEngine(reconciler *Reconciler, remConn RemindersConnector, haConn HAConnector, listMappings map[string]string, recoveryInterval time.Duration, logger *slog.Logger) *Engine {
	tracer := otel.Tracer(otelScope)
	meter := otel.Meter(otelScope)

	mustCounter := func(name, desc string) metric.Int64Counter {
		c, err := meter.Int64Counter(name, metric.WithDescription(desc))
		if err != nil {
			logger.Error("creating OTel counter", "name", name, "error", err)
			return noop.Int64Counter{}
		}
		return c
	}

	return &Engine{
		reconciler:       reconciler,
		remConn:          remConn,
		haConn:           haConn,
		listMappings:     listMappings,
		recoveryInterval: recoveryInterval,
		log:              logger,

		tracer:       tracer,
		cntCreated:   mustCounter(metricCreated, "Number of items created during sync"),
		cntUpdated:   mustCounter(metricUpdated, "Number of items updated during sync"),
		cntDeleted:   mustCounter(metricDeleted, "Number of items deleted during sync"),
		cntConflicts: mustCounter(metricConflicts, "Number of conflict resolutions during sync"),
		cntErrors:    mustCounter(metricErrors, "Number of errors encountered during sync"),
	}
}

// reconcile runs one full reconcile pass, recording a trace span and metrics.
func (e *Engine) reconcile(ctx context.Context) (Stats, error) {
	ctx, span := e.tracer.Start(ctx, spanReconcile)
	defer span.End()

	stats, err := e.reconciler.Run(ctx, e.listMappings)

	// Record counters — these are always safe even if the span is a no-op.
	if stats.Created > 0 {
		e.cntCreated.Add(ctx, int64(stats.Created))
	}
	if stats.Updated > 0 {
		e.cntUpdated.Add(ctx, int64(stats.Updated))
	}
	if stats.Deleted > 0 {
		e.cntDeleted.Add(ctx, int64(stats.Deleted))
	}
	if stats.Conflicts > 0 {
		e.cntConflicts.Add(ctx, int64(stats.Conflicts))
	}
	if stats.Errors > 0 {
		e.cntErrors.Add(ctx, int64(stats.Errors))
	}

	span.SetAttributes(
		attribute.Int("sync.created", stats.Created),
		attribute.Int("sync.updated", stats.Updated),
		attribute.Int("sync.deleted", stats.Deleted),
		attribute.Int("sync.conflicts", stats.Conflicts),
		attribute.Int("sync.errors", stats.Errors),
	)
	if err != nil {
		span.RecordError(err)
	}
	return stats, err
}

// RunOnce performs a single reconciliation pass and returns.
func (e *Engine) RunOnce(ctx context.Context) (Stats, error) {
	return e.reconcile(ctx)
}

// Run starts EventKit and HA WebSocket listeners plus a low-frequency recovery
// ticker. Reconciliation is serialized through one queue so a burst of push
// events cannot race state database writes.
func (e *Engine) Run(ctx context.Context) error {
	triggers := make(chan string, 1)
	trigger := func(reason string) {
		select {
		case triggers <- reason:
		default:
		}
	}

	// Start WS listener if available.
	if e.haConn != nil {
		if err := e.haConn.Connect(ctx); err != nil {
			e.log.Warn("initial HA WebSocket check failed; subscription reconnect loop will continue", "error", err)
		}
		defer func() { _ = e.haConn.Close() }()

		entityIDs := make([]string, 0, len(e.listMappings))
		for _, id := range e.listMappings {
			entityIDs = append(entityIDs, id)
		}

		entitySet := make(map[string]struct{}, len(entityIDs))
		for _, entityID := range entityIDs {
			entitySet[entityID] = struct{}{}
		}
		go func() {
			err := e.haConn.SubscribeChanges(ctx, entityIDs, func(entityID string) {
				if _, ok := entitySet[entityID]; !ok {
					return
				}
				e.log.Debug("HA WebSocket change received", "entity_id", entityID)
				trigger("home_assistant")
			})
			if err != nil && ctx.Err() == nil {
				e.log.Error("WS subscription ended unexpectedly", "error", err)
			}
		}()
	}

	// EventKit changes include writes from this process, Reminders.app, and
	// iCloud propagation from other devices.
	if e.remConn != nil {
		changes, err := e.remConn.WatchChanges(ctx)
		if err != nil {
			return fmt.Errorf("subscribing to EventKit changes: %w", err)
		}
		go func() {
			for range changes {
				e.log.Debug("EventKit reminder change received")
				trigger("icloud")
			}
		}()
	}

	if e.recoveryInterval <= 0 {
		e.recoveryInterval = 6 * time.Hour
	}
	ticker := time.NewTicker(e.recoveryInterval)
	defer ticker.Stop()
	trigger("startup")

	for {
		select {
		case <-ctx.Done():
			e.log.Info("sync engine shutting down")
			return ctx.Err()
		case <-ticker.C:
			trigger("recovery")
		case reason := <-triggers:
			e.log.Info("reconcile triggered", "reason", reason)
			if _, err := e.reconcile(ctx); err != nil {
				e.log.Error("reconcile failed", "reason", reason, "error", err)
			}
		}
	}
}
