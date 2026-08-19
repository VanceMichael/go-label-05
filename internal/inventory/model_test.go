package inventory

import (
	"errors"
	"testing"
	"time"

	"go-base/internal/domain"
)

func lot(id string, expires time.Time, quantity float64) Lot {
	return Lot{ID: id, TenantID: "tenant-1", FeedCode: "TMR", SupplierID: "supplier-1", QuantityKg: quantity, ProducedAt: expires.AddDate(0, -2, 0), ExpiresAt: expires, ReceivedAt: expires.AddDate(0, -1, 0), Status: LotReleased, Version: 1}
}

func TestLotValidation(t *testing.T) {
	now := time.Now().UTC()
	valid := lot("lot-1", now.AddDate(0, 1, 0), 100)
	if err := valid.Validate(now); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.ReservedKg, invalid.ConsumedKg = 80, 30
	if !errors.Is(invalid.Validate(now), domain.ErrConflict) {
		t.Fatal("overallocated lot was accepted")
	}
	expired := valid
	expired.ExpiresAt = now.Add(-time.Hour)
	if !errors.Is(expired.Validate(now), domain.ErrConflict) {
		t.Fatal("expired released lot was accepted")
	}
}

func TestReleaseRequiresCertificateAndFutureExpiry(t *testing.T) {
	now := time.Now().UTC()
	input := lot("lot-1", now.Add(time.Hour), 100)
	input.Status = LotReceived
	if _, _, err := Release(input, "", now); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("error=%v", err)
	}
	updated, entry, err := Release(input, "certificate-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != LotReleased || updated.Version != 2 || entry.QuantityKg != 100 || entry.ReferenceID != "certificate-1" {
		t.Fatalf("updated=%+v entry=%+v", updated, entry)
	}
}

func TestAllocateUsesEarliestExpiryAndClonesLots(t *testing.T) {
	now := time.Now().UTC()
	lots := []Lot{lot("later", now.Add(20*24*time.Hour), 100), lot("first", now.Add(10*24*time.Hour), 60)}
	reservation, updated, err := Allocate(lots, "tenant-1", "plan-1", "TMR", 120, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(reservation.Lines) != 2 || reservation.Lines[0].LotID != "first" || reservation.Lines[0].Kg != 60 || reservation.Lines[1].Kg != 60 {
		t.Fatalf("reservation=%+v", reservation)
	}
	if lots[0].ReservedKg != 0 || lots[1].ReservedKg != 0 {
		t.Fatal("allocation mutated input lots")
	}
	updated[0].ReservedKg = 999
	if lots[0].ReservedKg == 999 {
		t.Fatal("allocation shares output storage")
	}
}

func TestAllocateRejectsShortfallWithoutPartialOutput(t *testing.T) {
	now := time.Now().UTC()
	lots := []Lot{lot("one", now.Add(time.Hour), 50)}
	reservation, updated, err := Allocate(lots, "tenant-1", "plan-1", "TMR", 60, now)
	if !errors.Is(err, domain.ErrConflict) || reservation.ID != "" || updated != nil {
		t.Fatalf("reservation=%+v updated=%v error=%v", reservation, updated, err)
	}
	if lots[0].ReservedKg != 0 {
		t.Fatal("failed allocation mutated input")
	}
}

func TestConsumeReleasesUnusedReservationAndWritesLedger(t *testing.T) {
	now := time.Now().UTC()
	lots := []Lot{lot("a", now.AddDate(0, 1, 0), 100), lot("b", now.AddDate(0, 2, 0), 100)}
	reservation, reserved, err := Allocate(lots, "tenant-1", "plan-1", "TMR", 150, now)
	if err != nil {
		t.Fatal(err)
	}
	consumedReservation, consumedLots, entries, err := Consume(reservation, reserved, 120, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if consumedReservation.Status != "consumed" || len(entries) != 2 || entries[0].QuantityKg != -100 || entries[1].QuantityKg != -20 {
		t.Fatalf("reservation=%+v entries=%+v", consumedReservation, entries)
	}
	byID := map[string]Lot{}
	for _, item := range consumedLots {
		byID[item.ID] = item
	}
	if byID["a"].Status != LotExhausted || byID["a"].ConsumedKg != 100 || byID["b"].ConsumedKg != 20 || byID["b"].ReservedKg != 0 {
		t.Fatalf("lots=%+v", consumedLots)
	}
}

func TestCancelReturnsEveryReservedLine(t *testing.T) {
	now := time.Now().UTC()
	lots := []Lot{lot("a", now.AddDate(0, 1, 0), 50), lot("b", now.AddDate(0, 2, 0), 50)}
	reservation, reserved, _ := Allocate(lots, "tenant-1", "plan-1", "TMR", 80, now)
	cancelled, restored, entries, err := Cancel(reservation, reserved, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != "cancelled" || len(entries) != 2 {
		t.Fatalf("cancelled=%+v entries=%+v", cancelled, entries)
	}
	for _, item := range restored {
		if item.ReservedKg != 0 {
			t.Fatalf("reservation remained on %+v", item)
		}
	}
}

func TestSummarizeSeparatesExpiredAndAvailable(t *testing.T) {
	now := time.Now().UTC()
	current := lot("current", now.Add(time.Hour), 100)
	current.ReservedKg = 20
	expired := lot("expired", now.Add(-time.Hour), 50)
	expired.Status = LotExpired
	other := lot("other", now.Add(time.Hour), 100)
	other.TenantID = "tenant-2"
	balance := Summarize([]Lot{current, expired, other}, "tenant-1", "TMR", now)
	if balance.OnHandKg != 150 || balance.ReservedKg != 20 || balance.AvailableKg != 80 || balance.ExpiredKg != 50 || balance.LotCount != 2 {
		t.Fatalf("balance=%+v", balance)
	}
}
