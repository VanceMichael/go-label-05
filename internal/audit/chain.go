package audit

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

type Event struct {
	ID           string
	TenantID     string
	ActorID      string
	Action       string
	ObjectType   string
	ObjectID     string
	Outcome      string
	RequestID    string
	Details      map[string]string
	OccurredAt   time.Time
	Sequence     int64
	PreviousHash string
	Hash         string
}

type Query struct {
	TenantID   string
	ActorID    string
	Actions    []string
	ObjectType string
	ObjectID   string
	Outcomes   []string
	From       time.Time
	Until      time.Time
	Cursor     string
	Limit      int
}

type Page struct {
	Items      []Event
	NextCursor string
	HasMore    bool
}

func NewEvent(id, tenant, actor, action, objectType, objectID, outcome, requestID string, details map[string]string, at time.Time, previous Event) (Event, error) {
	if id == "" || tenant == "" || actor == "" || action == "" || objectType == "" || objectID == "" || outcome == "" || requestID == "" {
		return Event{}, fmt.Errorf("%w: audit event identity", domain.ErrInvalid)
	}
	if at.IsZero() {
		return Event{}, fmt.Errorf("%w: audit event time", domain.ErrInvalid)
	}
	if previous.ID != "" {
		if previous.TenantID != tenant {
			return Event{}, fmt.Errorf("%w: audit chain tenant", domain.ErrConflict)
		}
		if at.Before(previous.OccurredAt) {
			return Event{}, fmt.Errorf("%w: audit chain time", domain.ErrConflict)
		}
	}
	event := Event{ID: id, TenantID: tenant, ActorID: actor, Action: action, ObjectType: objectType, ObjectID: objectID, Outcome: outcome, RequestID: requestID, Details: cloneDetails(details), OccurredAt: at, Sequence: previous.Sequence + 1, PreviousHash: previous.Hash}
	event.Hash = eventHash(event)
	return event, nil
}

func Verify(events []Event) error {
	if len(events) == 0 {
		return nil
	}
	ordered := append([]Event(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Sequence == ordered[j].Sequence {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].Sequence < ordered[j].Sequence
	})
	seenIDs := map[string]struct{}{}
	seenHashes := map[string]struct{}{}
	var previous Event
	for index, event := range ordered {
		if event.Sequence != int64(index+1) {
			return fmt.Errorf("%w: audit sequence %d", domain.ErrConflict, event.Sequence)
		}
		if index > 0 && event.TenantID != previous.TenantID {
			return fmt.Errorf("%w: mixed tenant audit chain", domain.ErrConflict)
		}
		if event.PreviousHash != previous.Hash {
			return fmt.Errorf("%w: audit previous hash at sequence %d", domain.ErrConflict, event.Sequence)
		}
		if event.Hash != eventHash(event) {
			return fmt.Errorf("%w: audit hash at sequence %d", domain.ErrConflict, event.Sequence)
		}
		if _, exists := seenIDs[event.ID]; exists {
			return fmt.Errorf("%w: duplicate audit ID %s", domain.ErrConflict, event.ID)
		}
		if _, exists := seenHashes[event.Hash]; exists {
			return fmt.Errorf("%w: duplicate audit hash", domain.ErrConflict)
		}
		seenIDs[event.ID] = struct{}{}
		seenHashes[event.Hash] = struct{}{}
		previous = event
	}
	return nil
}

func Filter(events []Event, query Query) (Page, error) {
	if query.TenantID == "" {
		return Page{}, fmt.Errorf("%w: audit query tenant", domain.ErrInvalid)
	}
	if query.Limit < 1 || query.Limit > 200 {
		query.Limit = 50
	}
	if !query.From.IsZero() && !query.Until.IsZero() && !query.Until.After(query.From) {
		return Page{}, fmt.Errorf("%w: audit query time range", domain.ErrInvalid)
	}
	actions := stringSet(query.Actions)
	outcomes := stringSet(query.Outcomes)
	items := make([]Event, 0, len(events))
	for _, event := range events {
		if event.TenantID != query.TenantID || (query.ActorID != "" && event.ActorID != query.ActorID) || (query.ObjectType != "" && event.ObjectType != query.ObjectType) || (query.ObjectID != "" && event.ObjectID != query.ObjectID) {
			continue
		}
		if len(actions) > 0 {
			if _, exists := actions[event.Action]; !exists {
				continue
			}
		}
		if len(outcomes) > 0 {
			if _, exists := outcomes[event.Outcome]; !exists {
				continue
			}
		}
		if !query.From.IsZero() && event.OccurredAt.Before(query.From) {
			continue
		}
		if !query.Until.IsZero() && !event.OccurredAt.Before(query.Until) {
			continue
		}
		if query.Cursor != "" && cursor(event) >= query.Cursor {
			continue
		}
		copyEvent := event
		copyEvent.Details = cloneDetails(event.Details)
		items = append(items, copyEvent)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].OccurredAt.Equal(items[j].OccurredAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].OccurredAt.After(items[j].OccurredAt)
	})
	page := Page{}
	if len(items) > query.Limit {
		page.HasMore = true
		items = items[:query.Limit]
	}
	page.Items = items
	if page.HasMore && len(items) > 0 {
		page.NextCursor = cursor(items[len(items)-1])
	}
	return page, nil
}

func Redact(event Event, keys []string) Event {
	out := event
	out.Details = cloneDetails(event.Details)
	for _, key := range keys {
		if _, exists := out.Details[key]; exists {
			out.Details[key] = "[redacted]"
		}
	}
	return out
}

func Summarize(events []Event, tenant string, from, until time.Time) (map[string]int, error) {
	if tenant == "" || (!from.IsZero() && !until.IsZero() && !until.After(from)) {
		return nil, fmt.Errorf("%w: audit summary query", domain.ErrInvalid)
	}
	result := map[string]int{}
	for _, event := range events {
		if event.TenantID != tenant || (!from.IsZero() && event.OccurredAt.Before(from)) || (!until.IsZero() && !event.OccurredAt.Before(until)) {
			continue
		}
		result[event.Action+":"+event.Outcome]++
	}
	return result, nil
}

func eventHash(event Event) string {
	details, _ := json.Marshal(sortedDetails(event.Details))
	payload := strings.Join([]string{event.ID, event.TenantID, event.ActorID, event.Action, event.ObjectType, event.ObjectID, event.Outcome, event.RequestID, event.OccurredAt.UTC().Format(time.RFC3339Nano), fmt.Sprintf("%d", event.Sequence), event.PreviousHash, string(details)}, "\x00")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func sortedDetails(details map[string]string) [][2]string {
	keys := make([]string, 0, len(details))
	for key := range details {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([][2]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, [2]string{key, details[key]})
	}
	return result
}

func cloneDetails(details map[string]string) map[string]string {
	result := make(map[string]string, len(details))
	for key, value := range details {
		result[key] = value
	}
	return result
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			result[value] = struct{}{}
		}
	}
	return result
}

func cursor(event Event) string {
	return event.OccurredAt.UTC().Format(time.RFC3339Nano) + ":" + event.ID
}
