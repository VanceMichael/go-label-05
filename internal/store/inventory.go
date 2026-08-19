package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"go-base/internal/domain"
	"go-base/internal/inventory"
)

type InventoryRepository struct{ DB *Database }

func (repository InventoryRepository) Receive(ctx context.Context, lot inventory.Lot) error {
	if err := lot.Validate(lot.ReceivedAt); err != nil {
		return err
	}
	_, err := repository.DB.Pool.Exec(ctx, `INSERT INTO feed_lots(id,tenant_id,feed_code,supplier_id,quantity_kg,reserved_kg,consumed_kg,produced_at,expires_at,received_at,status,version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, lot.ID, lot.TenantID, lot.FeedCode, lot.SupplierID, lot.QuantityKg, lot.ReservedKg, lot.ConsumedKg, lot.ProducedAt, lot.ExpiresAt, lot.ReceivedAt, lot.Status, lot.Version)
	return err
}

func (repository InventoryRepository) Release(ctx context.Context, tenant, lotID, certificate string, at time.Time, expectedVersion int64) (inventory.Lot, error) {
	var result inventory.Lot
	err := repository.DB.WithTx(ctx, func(tx pgx.Tx) error {
		lot, err := loadLot(ctx, tx, tenant, lotID, true)
		if err != nil {
			return err
		}
		if lot.Version != expectedVersion {
			return fmt.Errorf("%w: feed lot version", domain.ErrConflict)
		}
		updated, entry, err := inventory.Release(lot, certificate, at)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE feed_lots SET status=$3,version=$4,updated_at=$5 WHERE tenant_id=$1 AND id=$2 AND version=$6`, tenant, lotID, updated.Status, updated.Version, at, expectedVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return domain.ErrConflict
		}
		if _, err := tx.Exec(ctx, `INSERT INTO feed_ledger(id,tenant_id,feed_code,lot_id,reference_id,kind,quantity_kg,occurred_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, entry.ID, entry.TenantID, entry.FeedCode, entry.LotID, entry.ReferenceID, entry.Kind, entry.QuantityKg, entry.OccurredAt); err != nil {
			return err
		}
		result = updated
		return nil
	})
	return result, err
}

func (repository InventoryRepository) Allocate(ctx context.Context, tenant, planID, feedCode string, requiredKg float64, at time.Time) (inventory.Reservation, error) {
	var result inventory.Reservation
	err := repository.DB.WithTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id,tenant_id,feed_code,supplier_id,quantity_kg,reserved_kg,consumed_kg,produced_at,expires_at,received_at,status,version FROM feed_lots WHERE tenant_id=$1 AND feed_code=$2 AND status='released' AND expires_at>$3 ORDER BY expires_at,received_at,id FOR UPDATE`, tenant, feedCode, at)
		if err != nil {
			return err
		}
		lots := []inventory.Lot{}
		for rows.Next() {
			lot, err := scanLot(rows)
			if err != nil {
				rows.Close()
				return err
			}
			lots = append(lots, lot)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		reservation, updated, err := inventory.Allocate(lots, tenant, planID, feedCode, requiredKg, at)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO feed_reservations(id,tenant_id,plan_id,feed_code,total_kg,status,version,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, reservation.ID, reservation.TenantID, reservation.PlanID, reservation.FeedCode, reservation.TotalKg, reservation.Status, reservation.Version, reservation.CreatedAt); err != nil {
			return err
		}
		byID := make(map[string]inventory.Lot, len(updated))
		for _, lot := range updated {
			byID[lot.ID] = lot
		}
		for _, line := range reservation.Lines {
			lot := byID[line.LotID]
			tag, err := tx.Exec(ctx, `UPDATE feed_lots SET reserved_kg=$3,version=$4,updated_at=$5 WHERE tenant_id=$1 AND id=$2 AND version=$6`, tenant, lot.ID, lot.ReservedKg, lot.Version, at, lot.Version-1)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return fmt.Errorf("%w: feed lot %s", domain.ErrConflict, lot.ID)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO feed_reservation_lines(reservation_id,lot_id,kg) VALUES($1,$2,$3)`, reservation.ID, line.LotID, line.Kg); err != nil {
				return err
			}
		}
		result = reservation
		return nil
	})
	return result, err
}

func (repository InventoryRepository) Cancel(ctx context.Context, tenant, reservationID string, at time.Time, expectedVersion int64) (inventory.Reservation, error) {
	var result inventory.Reservation
	err := repository.DB.WithTx(ctx, func(tx pgx.Tx) error {
		reservation, lots, err := loadReservation(ctx, tx, tenant, reservationID)
		if err != nil {
			return err
		}
		if reservation.Version != expectedVersion {
			return fmt.Errorf("%w: reservation version", domain.ErrConflict)
		}
		updatedReservation, updatedLots, entries, err := inventory.Cancel(reservation, lots, at)
		if err != nil {
			return err
		}
		if err := persistReservationChange(ctx, tx, reservation, updatedReservation, updatedLots, entries, at); err != nil {
			return err
		}
		result = updatedReservation
		return nil
	})
	return result, err
}

func (repository InventoryRepository) Consume(ctx context.Context, tenant, reservationID string, deliveredKg float64, at time.Time, expectedVersion int64) (inventory.Reservation, error) {
	var result inventory.Reservation
	err := repository.DB.WithTx(ctx, func(tx pgx.Tx) error {
		reservation, lots, err := loadReservation(ctx, tx, tenant, reservationID)
		if err != nil {
			return err
		}
		if reservation.Version != expectedVersion {
			return fmt.Errorf("%w: reservation version", domain.ErrConflict)
		}
		updatedReservation, updatedLots, entries, err := inventory.Consume(reservation, lots, deliveredKg, at)
		if err != nil {
			return err
		}
		if err := persistReservationChange(ctx, tx, reservation, updatedReservation, updatedLots, entries, at); err != nil {
			return err
		}
		result = updatedReservation
		return nil
	})
	return result, err
}

func (repository InventoryRepository) Lots(ctx context.Context, tenant, feedCode string) ([]inventory.Lot, error) {
	rows, err := repository.DB.Pool.Query(ctx, `SELECT id,tenant_id,feed_code,supplier_id,quantity_kg,reserved_kg,consumed_kg,produced_at,expires_at,received_at,status,version FROM feed_lots WHERE tenant_id=$1 AND ($2='' OR feed_code=$2) ORDER BY expires_at,received_at,id`, tenant, feedCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	lots := []inventory.Lot{}
	for rows.Next() {
		lot, err := scanLot(rows)
		if err != nil {
			return nil, err
		}
		lots = append(lots, lot)
	}
	return lots, rows.Err()
}

type rowScanner interface{ Scan(...any) error }

func scanLot(row rowScanner) (inventory.Lot, error) {
	var lot inventory.Lot
	err := row.Scan(&lot.ID, &lot.TenantID, &lot.FeedCode, &lot.SupplierID, &lot.QuantityKg, &lot.ReservedKg, &lot.ConsumedKg, &lot.ProducedAt, &lot.ExpiresAt, &lot.ReceivedAt, &lot.Status, &lot.Version)
	return lot, err
}

func loadLot(ctx context.Context, tx pgx.Tx, tenant, lotID string, lock bool) (inventory.Lot, error) {
	query := `SELECT id,tenant_id,feed_code,supplier_id,quantity_kg,reserved_kg,consumed_kg,produced_at,expires_at,received_at,status,version FROM feed_lots WHERE tenant_id=$1 AND id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	lot, err := scanLot(tx.QueryRow(ctx, query, tenant, lotID))
	if err != nil {
		return lot, mapNotFound(err, "feed lot")
	}
	return lot, nil
}

func loadReservation(ctx context.Context, tx pgx.Tx, tenant, reservationID string) (inventory.Reservation, []inventory.Lot, error) {
	var reservation inventory.Reservation
	err := tx.QueryRow(ctx, `SELECT id,tenant_id,plan_id,feed_code,total_kg,status,created_at,version FROM feed_reservations WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, reservationID).Scan(&reservation.ID, &reservation.TenantID, &reservation.PlanID, &reservation.FeedCode, &reservation.TotalKg, &reservation.Status, &reservation.CreatedAt, &reservation.Version)
	if err != nil {
		return reservation, nil, mapNotFound(err, "feed reservation")
	}
	rows, err := tx.Query(ctx, `SELECT l.id,l.tenant_id,l.feed_code,l.supplier_id,l.quantity_kg,l.reserved_kg,l.consumed_kg,l.produced_at,l.expires_at,l.received_at,l.status,l.version,rl.kg FROM feed_reservation_lines rl JOIN feed_lots l ON l.id=rl.lot_id AND l.tenant_id=$1 WHERE rl.reservation_id=$2 ORDER BY l.expires_at,l.id FOR UPDATE OF l`, tenant, reservationID)
	if err != nil {
		return reservation, nil, err
	}
	defer rows.Close()
	lots := []inventory.Lot{}
	for rows.Next() {
		var lot inventory.Lot
		var kg float64
		if err := rows.Scan(&lot.ID, &lot.TenantID, &lot.FeedCode, &lot.SupplierID, &lot.QuantityKg, &lot.ReservedKg, &lot.ConsumedKg, &lot.ProducedAt, &lot.ExpiresAt, &lot.ReceivedAt, &lot.Status, &lot.Version, &kg); err != nil {
			return reservation, nil, err
		}
		reservation.Lines = append(reservation.Lines, inventory.ReservationLine{LotID: lot.ID, Kg: kg})
		lots = append(lots, lot)
	}
	return reservation, lots, rows.Err()
}

func persistReservationChange(ctx context.Context, tx pgx.Tx, before, after inventory.Reservation, lots []inventory.Lot, entries []inventory.LedgerEntry, at time.Time) error {
	tag, err := tx.Exec(ctx, `UPDATE feed_reservations SET status=$3,version=$4 WHERE tenant_id=$1 AND id=$2 AND version=$5`, after.TenantID, after.ID, after.Status, after.Version, before.Version)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	for _, lot := range lots {
		tag, err := tx.Exec(ctx, `UPDATE feed_lots SET reserved_kg=$3,consumed_kg=$4,status=$5,version=$6,updated_at=$7 WHERE tenant_id=$1 AND id=$2 AND version=$8`, lot.TenantID, lot.ID, lot.ReservedKg, lot.ConsumedKg, lot.Status, lot.Version, at, lot.Version-1)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("%w: feed lot %s", domain.ErrConflict, lot.ID)
		}
	}
	for _, entry := range entries {
		if _, err := tx.Exec(ctx, `INSERT INTO feed_ledger(id,tenant_id,feed_code,lot_id,reference_id,kind,quantity_kg,occurred_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, entry.ID, entry.TenantID, entry.FeedCode, entry.LotID, entry.ReferenceID, entry.Kind, entry.QuantityKg, entry.OccurredAt); err != nil {
			return err
		}
	}
	return nil
}
