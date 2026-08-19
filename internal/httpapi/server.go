package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go-base/internal/auth"
	"go-base/internal/domain"
	"go-base/internal/feed"
	"go-base/internal/manure"
	"go-base/internal/store"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type Server struct {
	DB     *store.Database
	Auth   auth.Service
	Feed   feed.Service
	Manure manure.Service
	Logger *slog.Logger
	Mux    *http.ServeMux
}

func New(db *store.Database, ttl time.Duration, logger *slog.Logger) *Server {
	repo := store.AuthRepository{DB: db}
	fr := store.FeedRepository{DB: db}
	mr := store.ManureRepository{DB: db}
	s := &Server{DB: db, Auth: auth.Service{Repo: repo, TTL: ttl, Now: time.Now}, Feed: feed.Service{Repo: fr, Now: time.Now}, Manure: manure.Service{Repo: mr, Now: time.Now}, Logger: logger, Mux: http.NewServeMux()}
	s.routes()
	return s
}
func (s *Server) Handler() http.Handler { return s.recover(s.requestID(s.Mux)) }
func (s *Server) routes() {
	s.Mux.HandleFunc("GET /healthz", s.health)
	s.Mux.HandleFunc("GET /readyz", s.ready)
	s.Mux.HandleFunc("POST /api/login", s.login)
	s.Mux.HandleFunc("POST /api/logout", s.logout)
	s.Mux.HandleFunc("POST /api/feed-plans", s.schedule)
	s.Mux.HandleFunc("POST /api/feed-plans/complete", s.complete)
	s.Mux.HandleFunc("GET /api/feed-plans", s.listPlans)
	s.Mux.HandleFunc("POST /api/manure-batches/inspect", s.inspect)
	s.Mux.HandleFunc("POST /api/manure-batches/approve", s.approve)
	s.Mux.HandleFunc("GET /api/manure-batches", s.listManure)
}
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	write(w, 200, map[string]string{"status": "ok"})
}
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.DB.Ping(r.Context()); err != nil {
		errorJSON(w, r, err)
		return
	}
	write(w, 200, map[string]string{"status": "ready"})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Tenant, Email, Password string }
	if err := decode(r, &in); err != nil {
		errorJSON(w, r, err)
		return
	}
	u, tok, err := s.Auth.Login(r.Context(), in.Tenant, in.Email, in.Password)
	if err != nil {
		errorJSON(w, r, err)
		return
	}
	write(w, 200, map[string]any{"user": map[string]any{"id": u.ID, "email": u.Email, "role": u.Role}, "token": tok})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	u, sess, err := s.user(r)
	if err != nil {
		errorJSON(w, r, err)
		return
	}
	_ = u
	if err = s.Auth.Logout(r.Context(), sess); err != nil {
		errorJSON(w, r, err)
		return
	}
	write(w, 200, map[string]string{"status": "revoked"})
}
func (s *Server) schedule(w http.ResponseWriter, r *http.Request) {
	u, _, err := s.user(r)
	if err != nil {
		errorJSON(w, r, err)
		return
	}
	var in struct {
		GroupID, OperatorID, FeedCode string
		FeedKg                        float64
		ScheduledFor                  time.Time
	}
	if err = decode(r, &in); err != nil {
		errorJSON(w, r, err)
		return
	}
	p, err := s.Feed.Schedule(r.Context(), u, in.GroupID, in.OperatorID, in.FeedCode, in.FeedKg, in.ScheduledFor, requestID(r))
	if err != nil {
		errorJSON(w, r, err)
		return
	}
	write(w, 201, p)
}
func (s *Server) complete(w http.ResponseWriter, r *http.Request) {
	u, _, err := s.user(r)
	if err != nil {
		errorJSON(w, r, err)
		return
	}
	var in struct {
		PlanID, IdempotencyKey string
		DeliveredKg, ManureKg  float64
		Version                int64
	}
	if err = decode(r, &in); err != nil {
		errorJSON(w, r, err)
		return
	}
	round, b, err := s.Feed.Complete(r.Context(), u, in.PlanID, in.IdempotencyKey, in.DeliveredKg, in.ManureKg, in.Version, requestID(r))
	if err != nil {
		errorJSON(w, r, err)
		return
	}
	write(w, 200, map[string]any{"round": round, "manure_batch": b})
}
func (s *Server) listPlans(w http.ResponseWriter, r *http.Request) {
	u, _, err := s.user(r)
	if err != nil {
		errorJSON(w, r, err)
		return
	}
	items, total, err := s.Feed.List(r.Context(), u, r.URL.Query().Get("status"), intParam(r, "page", 1), intParam(r, "size", 25))
	if err != nil {
		errorJSON(w, r, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "total": total})
}
func (s *Server) inspect(w http.ResponseWriter, r *http.Request) {
	u, _, err := s.user(r)
	if err != nil {
		errorJSON(w, r, err)
		return
	}
	var in struct {
		BatchID string
		Version int64
	}
	if err = decode(r, &in); err != nil {
		errorJSON(w, r, err)
		return
	}
	b, err := s.Manure.Inspect(r.Context(), u, in.BatchID, requestID(r), in.Version)
	if err != nil {
		errorJSON(w, r, err)
		return
	}
	write(w, 200, b)
}
func (s *Server) approve(w http.ResponseWriter, r *http.Request) {
	u, _, err := s.user(r)
	if err != nil {
		errorJSON(w, r, err)
		return
	}
	var in struct {
		BatchID  string
		Version  int64
		Moisture float64
	}
	if err = decode(r, &in); err != nil {
		errorJSON(w, r, err)
		return
	}
	b, l, err := s.Manure.Approve(r.Context(), u, in.BatchID, requestID(r), in.Version, in.Moisture)
	if err != nil {
		errorJSON(w, r, err)
		return
	}
	write(w, 200, map[string]any{"batch": b, "compost_lot": l})
}
func (s *Server) listManure(w http.ResponseWriter, r *http.Request) {
	u, _, err := s.user(r)
	if err != nil {
		errorJSON(w, r, err)
		return
	}
	items, total, err := s.Manure.List(r.Context(), u, r.URL.Query().Get("status"), intParam(r, "page", 1), intParam(r, "size", 25))
	if err != nil {
		errorJSON(w, r, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "total": total})
}
func (s *Server) user(r *http.Request) (domain.User, domain.Session, error) {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(h, "Bearer ") {
		return domain.User{}, domain.Session{}, domain.ErrUnauthorized
	}
	returnUser, session, err := s.Auth.Authenticate(r.Context(), strings.TrimSpace(strings.TrimPrefix(h, "Bearer ")))
	return returnUser, session, err
}
func decode(r *http.Request, v any) error {
	defer r.Body.Close()
	d := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return fmt.Errorf("%w: invalid JSON: %v", domain.ErrInvalid, err)
	}
	return nil
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func errorJSON(w http.ResponseWriter, r *http.Request, err error) {
	status := 500
	code := "internal_error"
	switch {
	case errors.Is(err, domain.ErrInvalid):
		status = 400
		code = "invalid_request"
	case errors.Is(err, auth.ErrCredentials), errors.Is(err, domain.ErrUnauthorized), errors.Is(err, domain.ErrExpired):
		status = 401
		code = "unauthorized"
	case errors.Is(err, domain.ErrForbidden):
		status = 403
		code = "forbidden"
	case errors.Is(err, domain.ErrConflict):
		status = 409
		code = "state_conflict"
	case errors.Is(err, domain.ErrNotFound):
		status = 404
		code = "not_found"
	}
	write(w, status, map[string]any{"error": map[string]string{"code": code, "message": err.Error(), "request_id": requestID(r)}})
}
func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = fmt.Sprintf("req-%d", time.Now().UnixNano())
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}
func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.Logger.Error("panic recovered", "value", v)
				errorJSON(w, r, fmt.Errorf("internal server error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type key string

const requestIDKey key = "request-id"

func requestID(r *http.Request) string {
	if v, ok := r.Context().Value(requestIDKey).(string); ok {
		return v
	}
	return "unknown"
}
func intParam(r *http.Request, k string, d int) int {
	var n int
	if _, err := fmt.Sscanf(r.URL.Query().Get(k), "%d", &n); err != nil || n < 1 {
		return d
	}
	return n
}
