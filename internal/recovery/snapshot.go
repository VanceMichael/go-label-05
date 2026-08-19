package recovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"go-base/internal/domain"
)

type Record struct {
	Kind      string
	ID        string
	TenantID  string
	Version   int64
	UpdatedAt time.Time
	Payload   json.RawMessage
}

type Snapshot struct {
	TenantID   string
	CreatedAt  time.Time
	Schema     int
	Records    []Record
	RecordHash string
}

type RestorePlan struct {
	Insert   []Record
	Update   []Record
	Skip     []Record
	Conflict []Conflict
}

type Conflict struct {
	Kind            string
	ID              string
	SnapshotVersion int64
	CurrentVersion  int64
	Reason          string
}

func Build(tenant string, schema int, records []Record, at time.Time) (Snapshot, error) {
	if tenant == "" || schema < 1 || at.IsZero() {
		return Snapshot{}, fmt.Errorf("%w: snapshot identity", domain.ErrInvalid)
	}
	copyRecords := make([]Record, len(records))
	copy(copyRecords, records)
	seen := map[string]struct{}{}
	for index := range copyRecords {
		record := &copyRecords[index]
		if record.Kind == "" || record.ID == "" || record.TenantID != tenant || record.Version < 1 || record.UpdatedAt.IsZero() || !json.Valid(record.Payload) {
			return Snapshot{}, fmt.Errorf("%w: snapshot record %s/%s", domain.ErrInvalid, record.Kind, record.ID)
		}
		key := record.Kind + "\x00" + record.ID
		if _, exists := seen[key]; exists {
			return Snapshot{}, fmt.Errorf("%w: duplicate snapshot record %s/%s", domain.ErrConflict, record.Kind, record.ID)
		}
		seen[key] = struct{}{}
		record.Payload = append(json.RawMessage(nil), record.Payload...)
	}
	sortRecords(copyRecords)
	snapshot := Snapshot{TenantID: tenant, CreatedAt: at, Schema: schema, Records: copyRecords}
	snapshot.RecordHash = Hash(snapshot)
	return snapshot, nil
}

func Verify(snapshot Snapshot) error {
	if snapshot.TenantID == "" || snapshot.Schema < 1 || snapshot.CreatedAt.IsZero() || len(snapshot.RecordHash) != 64 {
		return fmt.Errorf("%w: snapshot header", domain.ErrInvalid)
	}
	for _, record := range snapshot.Records {
		if record.TenantID != snapshot.TenantID || record.Kind == "" || record.ID == "" || record.Version < 1 || !json.Valid(record.Payload) {
			return fmt.Errorf("%w: snapshot record", domain.ErrInvalid)
		}
	}
	if Hash(snapshot) != snapshot.RecordHash {
		return fmt.Errorf("%w: snapshot checksum", domain.ErrConflict)
	}
	return nil
}

func Hash(snapshot Snapshot) string {
	records := append([]Record(nil), snapshot.Records...)
	sortRecords(records)
	hasher := sha256.New()
	_, _ = fmt.Fprintf(hasher, "%s\x00%d\x00%s\x00", snapshot.TenantID, snapshot.Schema, snapshot.CreatedAt.UTC().Format(time.RFC3339Nano))
	for _, record := range records {
		_, _ = fmt.Fprintf(hasher, "%s\x00%s\x00%s\x00%d\x00%s\x00", record.Kind, record.ID, record.TenantID, record.Version, record.UpdatedAt.UTC().Format(time.RFC3339Nano))
		hasher.Write(record.Payload)
		hasher.Write([]byte{0})
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func Plan(snapshot Snapshot, current []Record, allowedKinds map[string]bool) (RestorePlan, error) {
	if err := Verify(snapshot); err != nil {
		return RestorePlan{}, err
	}
	currentByKey := make(map[string]Record, len(current))
	for _, record := range current {
		if record.TenantID != snapshot.TenantID {
			continue
		}
		currentByKey[record.Kind+"\x00"+record.ID] = record
	}
	plan := RestorePlan{}
	for _, record := range snapshot.Records {
		if len(allowedKinds) > 0 && !allowedKinds[record.Kind] {
			plan.Skip = append(plan.Skip, record)
			continue
		}
		key := record.Kind + "\x00" + record.ID
		present, exists := currentByKey[key]
		if !exists {
			plan.Insert = append(plan.Insert, record)
			continue
		}
		if present.Version > record.Version {
			plan.Conflict = append(plan.Conflict, Conflict{Kind: record.Kind, ID: record.ID, SnapshotVersion: record.Version, CurrentVersion: present.Version, Reason: "current record is newer"})
			continue
		}
		if present.Version == record.Version {
			if string(present.Payload) == string(record.Payload) {
				plan.Skip = append(plan.Skip, record)
			} else {
				plan.Conflict = append(plan.Conflict, Conflict{Kind: record.Kind, ID: record.ID, SnapshotVersion: record.Version, CurrentVersion: present.Version, Reason: "same version has different payload"})
			}
			continue
		}
		plan.Update = append(plan.Update, record)
	}
	sortRecords(plan.Insert)
	sortRecords(plan.Update)
	sortRecords(plan.Skip)
	sort.Slice(plan.Conflict, func(i, j int) bool {
		if plan.Conflict[i].Kind == plan.Conflict[j].Kind {
			return plan.Conflict[i].ID < plan.Conflict[j].ID
		}
		return plan.Conflict[i].Kind < plan.Conflict[j].Kind
	})
	return plan, nil
}

func Apply(current []Record, plan RestorePlan) ([]Record, error) {
	if len(plan.Conflict) > 0 {
		return nil, fmt.Errorf("%w: restore plan has %d conflicts", domain.ErrConflict, len(plan.Conflict))
	}
	byKey := make(map[string]Record, len(current)+len(plan.Insert))
	for _, record := range current {
		byKey[record.Kind+"\x00"+record.ID] = cloneRecord(record)
	}
	for _, record := range plan.Insert {
		key := record.Kind + "\x00" + record.ID
		if _, exists := byKey[key]; exists {
			return nil, fmt.Errorf("%w: restore insert already exists", domain.ErrConflict)
		}
		byKey[key] = cloneRecord(record)
	}
	for _, record := range plan.Update {
		key := record.Kind + "\x00" + record.ID
		currentRecord, exists := byKey[key]
		if !exists {
			return nil, fmt.Errorf("%w: restore update is missing", domain.ErrNotFound)
		}
		if record.Version <= currentRecord.Version {
			return nil, fmt.Errorf("%w: restore update version", domain.ErrConflict)
		}
		byKey[key] = cloneRecord(record)
	}
	result := make([]Record, 0, len(byKey))
	for _, record := range byKey {
		result = append(result, record)
	}
	sortRecords(result)
	return result, nil
}

func sortRecords(records []Record) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].Kind == records[j].Kind {
			return records[i].ID < records[j].ID
		}
		return records[i].Kind < records[j].Kind
	})
}

func cloneRecord(record Record) Record {
	out := record
	out.Payload = append(json.RawMessage(nil), record.Payload...)
	return out
}
