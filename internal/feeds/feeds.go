// Package feeds ingests crises from authoritative open disaster feeds (USGS,
// GDACS) and upserts them as crises. This is the "top-down" source leg that
// complements analyst-declared and emergent (citizen-cluster) crises — matching
// how RAPIDA operates: an event is usually known from satellite/feeds first, and
// ground reports stream in to validate and detail it.
//
// Connectors are best-effort and isolated: a feed that is down or changes shape
// logs an error and is skipped — it never blocks others or crashes the server.
package feeds

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/stepanok/beacon-server/internal/model"
)

// Connector fetches a normalized set of crises from one external feed.
type Connector interface {
	Name() string
	// Fetch returns crises with deterministic ids (so re-polling is an idempotent
	// upsert, not a duplicate). source is set to "feed:<Name>".
	Fetch(ctx context.Context, client *http.Client) ([]model.Crisis, error)
}

// CrisisSink is the store surface the ingester needs (decoupled for testing).
type CrisisSink interface {
	UpsertExternalCrisis(ctx context.Context, c model.Crisis) error
	AssignPendingToCrisis(ctx context.Context, crisisID string) (int, error)
}

// SourceResult is the per-feed outcome of one ingest pass.
type SourceResult struct {
	Source   string `json:"source"`
	Fetched  int    `json:"fetched"`
	Upserted int    `json:"upserted"`
	Assigned int    `json:"assigned"` // pending reports pulled into these crises
	Error    string `json:"error,omitempty"`
}

// Summary is the full result of RunOnce.
type Summary struct {
	Sources []SourceResult `json:"sources"`
	Total   int            `json:"total"`
}

// Ingester orchestrates the connectors against the crisis store.
type Ingester struct {
	connectors []Connector
	sink       CrisisSink
	client     *http.Client
	log        *slog.Logger
}

func NewIngester(sink CrisisSink, log *slog.Logger, connectors ...Connector) *Ingester {
	return &Ingester{
		connectors: connectors,
		sink:       sink,
		client:     &http.Client{Timeout: 25 * time.Second},
		log:        log,
	}
}

// Default returns the standard connector set (the keyless, live-pollable feeds).
func Default(sink CrisisSink, log *slog.Logger) *Ingester {
	return NewIngester(sink, log, NewUSGS(""), NewGDACS(""))
}

// RunOnce pulls every connector once, upserts each crisis, then sweeps pending
// reports into freshly-known crises. Per-source errors are captured, not fatal.
func (i *Ingester) RunOnce(ctx context.Context) Summary {
	var sum Summary
	for _, c := range i.connectors {
		res := SourceResult{Source: c.Name()}
		crises, err := c.Fetch(ctx, i.client)
		if err != nil {
			res.Error = err.Error()
			i.log.Warn("feed fetch failed", "source", c.Name(), "err", err)
			sum.Sources = append(sum.Sources, res)
			continue
		}
		res.Fetched = len(crises)
		for _, cr := range crises {
			if err := i.sink.UpsertExternalCrisis(ctx, cr); err != nil {
				i.log.Warn("feed upsert failed", "source", c.Name(), "id", cr.ID, "err", err)
				continue
			}
			res.Upserted++
			if n, err := i.sink.AssignPendingToCrisis(ctx, cr.ID); err == nil {
				res.Assigned += n
			}
		}
		sum.Total += res.Upserted
		sum.Sources = append(sum.Sources, res)
		i.log.Info("feed ingested", "source", c.Name(), "fetched", res.Fetched, "upserted", res.Upserted, "assigned", res.Assigned)
	}
	return sum
}

// Start runs an immediate pass, then repeats every interval until ctx is done.
func (i *Ingester) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	go func() {
		// small initial delay so the DB pool/migrations are fully settled
		select {
		case <-ctx.Done():
			return
		case <-time.After(8 * time.Second):
		}
		i.RunOnce(ctx)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				i.RunOnce(ctx)
			}
		}
	}()
}

// ── shared helpers ──────────────────────────────────────────────────────

// haversineKm is the great-circle distance between two lat/lng points.
func haversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	const r = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLng := (lng2 - lng1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return r * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func ptr(s string) *string { return &s }
