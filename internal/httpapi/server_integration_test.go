package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go-base/internal/store"
)

var httpDatabaseSequence atomic.Uint64

func httpTestDatabase(t *testing.T) *store.Database {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	baseURL := os.Getenv("TEST_DATABASE_URL")
	if baseURL == "" {
		baseURL = "postgres://farm:farm@127.0.0.1:55432/farm?sslmode=disable"
	}
	adminConfig, err := pgxpool.ParseConfig(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	adminConfig.ConnConfig.Database = "postgres"
	admin, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("herdcycle_http_%d_%d", time.Now().UnixNano(), httpDatabaseSequence.Add(1))
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	appURL, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	appURL.Path = "/" + name
	db, err := store.Open(ctx, appURL.String())
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	if err := db.Bootstrap(ctx); err != nil {
		db.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dropCancel()
		if _, err := admin.Exec(dropCtx, "DROP DATABASE "+identifier+" WITH (FORCE)"); err != nil {
			t.Errorf("drop HTTP test database: %v", err)
		}
		admin.Close()
	})
	return db
}

type apiResponse struct {
	Status int
	Header http.Header
	Body   map[string]any
}

func apiRequest(t *testing.T, handler http.Handler, method, path, token string, body any) apiResponse {
	t.Helper()
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(payload)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("X-Request-ID", "request-test-123")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	decoded := map[string]any{}
	if recorder.Body.Len() > 0 {
		if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("decode response status=%d body=%s: %v", recorder.Code, recorder.Body.String(), err)
		}
	}
	return apiResponse{Status: recorder.Code, Header: recorder.Header(), Body: decoded}
}

func loginToken(t *testing.T, handler http.Handler, email, password string) string {
	t.Helper()
	response := apiRequest(t, handler, http.MethodPost, "/api/login", "", map[string]any{
		"Tenant":   "demo",
		"Email":    email,
		"Password": password,
	})
	if response.Status != http.StatusOK {
		t.Fatalf("login %s status=%d body=%v", email, response.Status, response.Body)
	}
	token, ok := response.Body["token"].(string)
	if !ok || token == "" {
		t.Fatalf("login %s returned token %#v", email, response.Body["token"])
	}
	return token
}

func newHTTPTestServer(t *testing.T, ttl time.Duration) (*Server, http.Handler) {
	t.Helper()
	db := httpTestDatabase(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(db, ttl, logger)
	return server, server.Handler()
}

func responseErrorMessage(response apiResponse) string {
	errorBody, _ := response.Body["error"].(map[string]any)
	message, _ := errorBody["message"].(string)
	return message
}

func responseErrorCode(response apiResponse) string {
	errorBody, _ := response.Body["error"].(map[string]any)
	code, _ := errorBody["code"].(string)
	return code
}

func TestHealthReadinessAndRequestID(t *testing.T) {
	_, handler := newHTTPTestServer(t, time.Hour)
	for _, path := range []string{"/healthz", "/readyz"} {
		response := apiRequest(t, handler, http.MethodGet, path, "", nil)
		if response.Status != http.StatusOK || response.Body["status"] == "" {
			t.Fatalf("GET %s status=%d body=%v", path, response.Status, response.Body)
		}
		if response.Header.Get("X-Request-ID") != "request-test-123" {
			t.Fatalf("GET %s request ID = %q", path, response.Header.Get("X-Request-ID"))
		}
	}
}

func TestLoginRejectsBadCredentialsAndUnknownJSONFields(t *testing.T) {
	_, handler := newHTTPTestServer(t, time.Hour)
	badPassword := apiRequest(t, handler, http.MethodPost, "/api/login", "", map[string]any{
		"Tenant":   "demo",
		"Email":    "manager@herd.local",
		"Password": "wrong",
	})
	if badPassword.Status != http.StatusUnauthorized || responseErrorCode(badPassword) != "unauthorized" || !strings.Contains(responseErrorMessage(badPassword), "invalid credentials") {
		t.Fatalf("bad password response = %+v", badPassword)
	}
	unknownField := apiRequest(t, handler, http.MethodPost, "/api/login", "", map[string]any{
		"Tenant":   "demo",
		"Email":    "manager@herd.local",
		"Password": "manager-pass",
		"Admin":    true,
	})
	if unknownField.Status != http.StatusBadRequest || responseErrorCode(unknownField) != "invalid_request" || !strings.Contains(responseErrorMessage(unknownField), "unknown field") {
		t.Fatalf("unknown field response = %+v", unknownField)
	}
}

func TestMissingIdentityAndRoleDenialHaveDifferentStatuses(t *testing.T) {
	_, handler := newHTTPTestServer(t, time.Hour)
	missing := apiRequest(t, handler, http.MethodGet, "/api/feed-plans", "", nil)
	if missing.Status != http.StatusUnauthorized || responseErrorCode(missing) != "unauthorized" {
		t.Fatalf("missing token status=%d body=%v", missing.Status, missing.Body)
	}
	operator := loginToken(t, handler, "operator@herd.local", "operator-pass")
	denied := apiRequest(t, handler, http.MethodPost, "/api/feed-plans", operator, map[string]any{
		"GroupID":      "group-a",
		"OperatorID":   "op-1",
		"FeedCode":     "TMR-01",
		"FeedKg":       100,
		"ScheduledFor": time.Now().Add(time.Hour),
	})
	if denied.Status != http.StatusForbidden || responseErrorCode(denied) != "forbidden" || !strings.Contains(responseErrorMessage(denied), "role operator") {
		t.Fatalf("role denial response = %+v", denied)
	}
}

func TestLogoutRevokesOpaqueSession(t *testing.T) {
	_, handler := newHTTPTestServer(t, time.Hour)
	token := loginToken(t, handler, "manager@herd.local", "manager-pass")
	if strings.Contains(token, "manager") || strings.Count(token, ".") == 2 {
		t.Fatalf("token appears to expose identity or JWT structure: %q", token)
	}
	logout := apiRequest(t, handler, http.MethodPost, "/api/logout", token, map[string]any{})
	if logout.Status != http.StatusOK || logout.Body["status"] != "revoked" {
		t.Fatalf("logout response = %+v", logout)
	}
	after := apiRequest(t, handler, http.MethodGet, "/api/feed-plans", token, nil)
	if after.Status != http.StatusUnauthorized || !strings.Contains(responseErrorMessage(after), "session expired") {
		t.Fatalf("request after logout = %+v", after)
	}
}

func TestExpiredSessionCannotAccessBusinessEndpoints(t *testing.T) {
	server, handler := newHTTPTestServer(t, 15*time.Minute)
	issuedAt := time.Unix(1_700_000_000, 0).UTC()
	server.Auth.Now = func() time.Time { return issuedAt }
	token := loginToken(t, handler, "manager@herd.local", "manager-pass")
	server.Auth.Now = func() time.Time { return issuedAt.Add(16 * time.Minute) }
	response := apiRequest(t, handler, http.MethodGet, "/api/feed-plans", token, nil)
	if response.Status != http.StatusUnauthorized || !strings.Contains(responseErrorMessage(response), "session expired") {
		t.Fatalf("expired session response = %+v", response)
	}
}

func TestDisabledAccountCannotUseTokenIssuedEarlier(t *testing.T) {
	server, handler := newHTTPTestServer(t, time.Hour)
	token := loginToken(t, handler, "operator@herd.local", "operator-pass")
	if _, err := server.DB.Pool.Exec(context.Background(), "UPDATE users SET disabled=true WHERE id='op-1'"); err != nil {
		t.Fatal(err)
	}
	response := apiRequest(t, handler, http.MethodGet, "/api/feed-plans", token, nil)
	if response.Status != http.StatusUnauthorized || !strings.Contains(responseErrorMessage(response), "unauthorized") {
		t.Fatalf("disabled session response = %+v", response)
	}
}

func TestFeedAndManureWorkflowAcrossAllRoles(t *testing.T) {
	_, handler := newHTTPTestServer(t, time.Hour)
	manager := loginToken(t, handler, "manager@herd.local", "manager-pass")
	operator := loginToken(t, handler, "operator@herd.local", "operator-pass")
	environment := loginToken(t, handler, "environment@herd.local", "environment-pass")

	scheduled := apiRequest(t, handler, http.MethodPost, "/api/feed-plans", manager, map[string]any{
		"GroupID":      "group-a",
		"OperatorID":   "op-1",
		"FeedCode":     "TMR-01",
		"FeedKg":       250,
		"ScheduledFor": time.Now().Add(time.Hour),
	})
	if scheduled.Status != http.StatusCreated {
		t.Fatalf("schedule response = %+v", scheduled)
	}
	planID, _ := scheduled.Body["ID"].(string)
	if planID == "" {
		planID, _ = scheduled.Body["id"].(string)
	}
	if planID == "" {
		t.Fatalf("schedule body has no plan ID: %v", scheduled.Body)
	}

	listed := apiRequest(t, handler, http.MethodGet, "/api/feed-plans?status=scheduled&page=1&size=1", manager, nil)
	items, _ := listed.Body["items"].([]any)
	if listed.Status != http.StatusOK || listed.Body["total"] != float64(1) || len(items) != 1 {
		t.Fatalf("list plans response = %+v", listed)
	}

	completed := apiRequest(t, handler, http.MethodPost, "/api/feed-plans/complete", operator, map[string]any{
		"PlanID":         planID,
		"IdempotencyKey": "http-completion-key",
		"DeliveredKg":    245,
		"ManureKg":       110,
		"Version":        1,
	})
	if completed.Status != http.StatusOK {
		t.Fatalf("complete response = %+v", completed)
	}
	batch, _ := completed.Body["manure_batch"].(map[string]any)
	batchID, _ := batch["ID"].(string)
	if batchID == "" {
		batchID, _ = batch["id"].(string)
	}
	if batchID == "" {
		t.Fatalf("complete body has no manure batch ID: %v", completed.Body)
	}

	inspected := apiRequest(t, handler, http.MethodPost, "/api/manure-batches/inspect", environment, map[string]any{
		"BatchID": batchID,
		"Version": 1,
	})
	if inspected.Status != http.StatusOK || inspected.Body["Status"] != "inspected" {
		if inspected.Body["status"] != "inspected" {
			t.Fatalf("inspect response = %+v", inspected)
		}
	}

	approved := apiRequest(t, handler, http.MethodPost, "/api/manure-batches/approve", environment, map[string]any{
		"BatchID":  batchID,
		"Version":  2,
		"Moisture": 0.35,
	})
	if approved.Status != http.StatusOK {
		t.Fatalf("approve response = %+v", approved)
	}
	approvedBatch, _ := approved.Body["batch"].(map[string]any)
	status, _ := approvedBatch["Status"].(string)
	if status == "" {
		status, _ = approvedBatch["status"].(string)
	}
	if status != "approved" {
		t.Fatalf("approved batch = %v", approvedBatch)
	}

	manureList := apiRequest(t, handler, http.MethodGet, "/api/manure-batches?status=approved&page=1&size=10", environment, nil)
	approvedItems, _ := manureList.Body["items"].([]any)
	if manureList.Status != http.StatusOK || manureList.Body["total"] != float64(1) || len(approvedItems) != 1 {
		t.Fatalf("list manure response = %+v", manureList)
	}
}

func TestBusinessValidationAndVersionConflictsUseStableErrors(t *testing.T) {
	_, handler := newHTTPTestServer(t, time.Hour)
	manager := loginToken(t, handler, "manager@herd.local", "manager-pass")
	operator := loginToken(t, handler, "operator@herd.local", "operator-pass")
	invalid := apiRequest(t, handler, http.MethodPost, "/api/feed-plans", manager, map[string]any{
		"GroupID":      "group-a",
		"OperatorID":   "op-1",
		"FeedCode":     "TMR-01",
		"FeedKg":       -1,
		"ScheduledFor": time.Now().Add(time.Hour),
	})
	if invalid.Status != http.StatusBadRequest {
		t.Fatalf("invalid plan response = %+v", invalid)
	}
	scheduled := apiRequest(t, handler, http.MethodPost, "/api/feed-plans", manager, map[string]any{
		"GroupID":      "group-a",
		"OperatorID":   "op-1",
		"FeedCode":     "TMR-01",
		"FeedKg":       100,
		"ScheduledFor": time.Now().Add(time.Hour),
	})
	planID, _ := scheduled.Body["ID"].(string)
	conflict := apiRequest(t, handler, http.MethodPost, "/api/feed-plans/complete", operator, map[string]any{
		"PlanID":         planID,
		"IdempotencyKey": "stale-http-key",
		"DeliveredKg":    100,
		"ManureKg":       40,
		"Version":        99,
	})
	if conflict.Status != http.StatusConflict || responseErrorCode(conflict) != "state_conflict" || !strings.Contains(responseErrorMessage(conflict), "expected version") {
		t.Fatalf("version conflict response = %+v", conflict)
	}
	if errorBody, ok := conflict.Body["error"].(map[string]any); !ok || errorBody["request_id"] != "request-test-123" {
		t.Fatalf("conflict request ID missing: %v", conflict.Body)
	}
}
