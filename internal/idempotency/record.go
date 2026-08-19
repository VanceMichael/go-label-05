package idempotency

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"go-base/internal/domain"
)

type Scope struct {
	TenantID string
	Method   string
	Path     string
	ActorID  string
}

type Record struct {
	Scope       Scope
	Key         string
	RequestHash string
	StatusCode  int
	Response    json.RawMessage
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

type Header struct {
	Name  string
	Value string
}

type Request struct {
	Scope   Scope
	Key     string
	Headers []Header
	Body    json.RawMessage
}

func (scope Scope) Validate() error {
	if scope.TenantID == "" || scope.ActorID == "" || scope.Path == "" {
		return fmt.Errorf("%w: idempotency scope", domain.ErrInvalid)
	}
	switch strings.ToUpper(scope.Method) {
	case "POST", "PUT", "PATCH", "DELETE":
		return nil
	default:
		return fmt.Errorf("%w: idempotency method", domain.ErrInvalid)
	}
}

func Build(request Request, status int, response json.RawMessage, createdAt time.Time, ttl time.Duration) (Record, error) {
	if err := request.Scope.Validate(); err != nil {
		return Record{}, err
	}
	if strings.TrimSpace(request.Key) == "" || len(request.Key) > 200 {
		return Record{}, fmt.Errorf("%w: idempotency key", domain.ErrInvalid)
	}
	if !json.Valid(request.Body) || !json.Valid(response) {
		return Record{}, fmt.Errorf("%w: idempotency JSON", domain.ErrInvalid)
	}
	if status < 200 || status > 599 || ttl <= 0 || ttl > 30*24*time.Hour {
		return Record{}, fmt.Errorf("%w: idempotency result", domain.ErrInvalid)
	}
	record := Record{Scope: request.Scope, Key: strings.TrimSpace(request.Key), RequestHash: Hash(request), StatusCode: status, Response: append(json.RawMessage(nil), response...), CreatedAt: createdAt, ExpiresAt: createdAt.Add(ttl)}
	return record, nil
}

func Hash(request Request) string {
	headers := append([]Header(nil), request.Headers...)
	sort.SliceStable(headers, func(i, j int) bool {
		left := strings.ToLower(headers[i].Name)
		right := strings.ToLower(headers[j].Name)
		if left == right {
			return headers[i].Value < headers[j].Value
		}
		return left < right
	})
	hasher := sha256.New()
	_, _ = fmt.Fprintf(hasher, "%s\x00%s\x00%s\x00%s\x00%s\x00", request.Scope.TenantID, strings.ToUpper(request.Scope.Method), request.Scope.Path, request.Scope.ActorID, strings.TrimSpace(request.Key))
	for _, header := range headers {
		name := strings.ToLower(strings.TrimSpace(header.Name))
		if name == "authorization" || name == "cookie" || name == "date" || name == "user-agent" {
			continue
		}
		_, _ = fmt.Fprintf(hasher, "%s:%s\x00", name, strings.TrimSpace(header.Value))
	}
	hasher.Write(request.Body)
	return hex.EncodeToString(hasher.Sum(nil))
}

func Match(record Record, request Request, now time.Time) (json.RawMessage, int, error) {
	if record.Scope != request.Scope || record.Key != strings.TrimSpace(request.Key) {
		return nil, 0, fmt.Errorf("%w: idempotency scope or key", domain.ErrConflict)
	}
	if !record.ExpiresAt.After(now) {
		return nil, 0, fmt.Errorf("%w: idempotency record expired", domain.ErrNotFound)
	}
	if record.RequestHash != Hash(request) {
		return nil, 0, fmt.Errorf("%w: idempotency request differs", domain.ErrConflict)
	}
	return append(json.RawMessage(nil), record.Response...), record.StatusCode, nil
}

func Prune(records []Record, now time.Time) (active []Record, expired []Record) {
	for _, record := range records {
		copyRecord := record
		copyRecord.Response = append(json.RawMessage(nil), record.Response...)
		if record.ExpiresAt.After(now) {
			active = append(active, copyRecord)
		} else {
			expired = append(expired, copyRecord)
		}
	}
	sort.Slice(active, func(i, j int) bool {
		if active[i].ExpiresAt.Equal(active[j].ExpiresAt) {
			return active[i].Key < active[j].Key
		}
		return active[i].ExpiresAt.Before(active[j].ExpiresAt)
	})
	sort.Slice(expired, func(i, j int) bool {
		if expired[i].ExpiresAt.Equal(expired[j].ExpiresAt) {
			return expired[i].Key < expired[j].Key
		}
		return expired[i].ExpiresAt.Before(expired[j].ExpiresAt)
	})
	return active, expired
}
