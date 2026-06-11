package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/go-chi/httprate"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stepanok/beacon-server/internal/config"
	"github.com/stepanok/beacon-server/internal/handler"
)

// NewRouter builds the full HTTP router: middleware stack, health checks and the
// /api/v1 surface for both clients.
func NewRouter(cfg config.Config, pool *pgxpool.Pool, h *handler.Handlers, logger *slog.Logger) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(accessLog(logger))
	r.Use(middleware.Timeout(25 * time.Second))
	r.Use(httprate.LimitByIP(cfg.RateLimitRPS, time.Second))
	r.Use(authenticate(cfg.JWTSecret)) // attach JWT user (if any) to context
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   cfg.CORSOrigins,
		AllowedMethods:   []string{"GET", "POST", "PATCH", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Content-Type", "Authorization", "X-Device-Id", "X-Analyst-Id", "X-Idempotency-Key"},
		ExposedHeaders:   []string{"Content-Disposition"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	// health / readiness
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	r.Get("/readyz", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "application/json")
		if err := pool.Ping(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"degraded","db":false}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ready","db":true}`))
	})

	r.Route("/api/v1", func(r chi.Router) {
		// auth
		r.Post("/auth/login", h.Login)
		r.With(requireAnalyst).Get("/auth/me", h.Me)

		// reports
		r.Route("/reports", func(r chi.Router) {
			r.Post("/", h.SubmitReport)                            // mobile: anonymous (X-Device-Id)
			r.With(requireAnalyst).Get("/", h.ListReports)         // analyst console (scoped)
			r.With(requireAnalyst).Get("/export", h.ExportReports) // analyst console (scoped)
			r.Get("/area-groups", h.AreaGroups)                    // public aggregate
			r.Get("/latest-per-building", h.LatestPerBuilding)     // public map pins
			r.Get("/{id}", h.GetReport)                            // public detail
			r.Post("/{id}/photo", h.UploadPhoto)                   // mobile: anonymous photo upload
			r.Get("/{id}/photo", h.GetPhoto)                       // public: serve report photo
			r.Post("/{id}/withdraw", h.WithdrawReport)             // mobile: reporter erases own report (X-Device-Id)
			r.With(requireMutator).Patch("/{id}/verification", h.PatchVerification)
		})

		// buildings
		r.Get("/buildings/{buildingId}/timeline", h.BuildingTimeline)

		// map
		r.Get("/map/features", h.MapFeatures)
		// scalable vector tiles (clusters at low zoom, points at high zoom)
		r.Get("/tiles/reports/{z}/{x}/{y}", h.ReportTile)

		// modular capture-form schema (public: the anonymous mobile app downloads
		// the Appendix-1 sections, resolved with the crisis's overrides)
		r.Get("/form-schema", h.GetFormSchema)

		// stats (analyst, scoped)
		r.With(requireAnalyst).Get("/stats/overview", h.StatsOverview)

		// crises
		r.Route("/crises", func(r chi.Router) {
			r.Get("/", h.ListCrises)
			r.Get("/active", h.ActiveCrisis)
			r.Get("/near", h.NearbyCrises) // public: mobile location-first launch
			r.Get("/{id}", h.GetCrisis)
			r.With(requireMutator).Patch("/{id}/status", h.SetCrisisStatus) // analyst confirm/dismiss emergent
			r.With(requireMutator).Patch("/{id}/form", h.PatchCrisisForm)   // senior analyst adjusts the modular form
		})

		// profile
		r.Get("/profile", h.GetProfile)
		r.Post("/profile/points", h.AwardPoints)
	})

	return r
}
