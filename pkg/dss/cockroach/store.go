package cockroach

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/golang/geo/s2"
	"github.com/golang/protobuf/ptypes"
	"github.com/lib/pq" // Pull in the postgres database driver
	"github.com/steeling/InterUSS-Platform/pkg/dss/models"
	dspb "github.com/steeling/InterUSS-Platform/pkg/dssproto"
	"go.uber.org/multierr"
)

type scanner interface {
	Scan(fields ...interface{}) error
}

type identificationServiceAreaRow struct {
	id        string
	owner     string
	url       string
	startsAt  time.Time
	endsAt    time.Time
	updatedAt time.Time
}

func (isar *identificationServiceAreaRow) scan(scanner scanner) error {
	return scanner.Scan(
		&isar.id,
		&isar.owner,
		&isar.url,
		&isar.startsAt,
		&isar.endsAt,
		&isar.updatedAt,
	)
}

func (isar *identificationServiceAreaRow) toProtobuf() (*dspb.IdentificationServiceArea, error) {
	result := &dspb.IdentificationServiceArea{
		Id:         isar.id,
		Owner:      isar.owner,
		FlightsUrl: isar.url,
		Version:    strconv.FormatInt(isar.updatedAt.UnixNano(), 10),
	}

	ts, err := ptypes.TimestampProto(isar.startsAt)
	if err != nil {
		return nil, err
	}
	result.Extents = &dspb.Volume4D{
		TimeStart: ts,
	}

	ts, err = ptypes.TimestampProto(isar.endsAt)
	if err != nil {
		return nil, err
	}
	result.Extents.TimeEnd = ts

	return result, nil
}

// Store is an implementation of dss.Store using
// Cockroach DB as its backend store.
type Store struct {
	*sql.DB
}

// Close closes the underlying DB connection.
func (s *Store) Close() error {
	return s.DB.Close()
}

// InsertSubscription inserts subscription into the store and returns
// the resulting subscription including its ID.
func (s *Store) InsertSubscription(ctx context.Context, sub *models.Subscription) (*dspb.Subscription, error) {
	const subscriptionCellQuery = `
		INSERT INTO
			cells_subscriptions
		VALUES
			($1, $2, $3, transaction_timestamp())
		`
	tx, err := s.Begin()
	if err != nil {
		return nil, err
	}

	if err := sub.Insert(ctx, tx); err != nil {
		return nil, multierr.Combine(err, tx.Rollback())
	}

	for _, cell := range sub.S2Cells {
		if _, err := tx.ExecContext(ctx, subscriptionCellQuery, cell.Id(), cell.Level(), sub.Id); err != nil {
			return nil, multierr.Combine(err, tx.Rollback())
		}
	}

	result, err := sub.ToProto()
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// UpdatesSubscription updates the subscription  and returns
// the resulting subscription including its ID.
func (s *Store) UpdateSubscription(ctx context.Context, subscription *dspb.Subscription, cells s2.CellUnion) (*dspb.Subscription, error) {
	const (
		subscriptionCellQuery = `
		UPSERT INTO
			cells_subscriptions
		VALUES
			($1, $2, $3, transaction_timestamp())
		`
	)

	tx, err := s.Begin()
	if err != nil {
		return nil, err
	}

	if err := s.Update(ctx, tx); err != nil {
		return nil, multierr.Combine(err, tx.Rollback())
	}

	// TODO(steeling) we also need to delete any leftover cells.
	for _, cell := range cells {
		if _, err := tx.ExecContext(ctx, subscriptionCellQuery, cell, cell.Level(), sr.id); err != nil {
			return nil, multierr.Combine(err, tx.Rollback())
		}
	}

	result, err := sr.ToProto()
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// GetSubscription returns the subscription identified by "id".
func (s *Store) GetSubscription(ctx context.Context, id string) (*dspb.Subscription, error) {
	tx, err := s.Begin()
	if err != nil {
		return nil, err
	}

	sub, err := models.GetSubscription(ctx, tx)
	if err != nil {
		return nil, multierr.Combine(err, tx.Rollback())
	}

	result, err := sub.ToProto()
	if err != nil {
		return nil, multierr.Combine(err, tx.Rollback())
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return result, nil
}

// DeleteSubscription deletes the subscription identified by "id" and
// returns the deleted subscription.
func (s *Store) DeleteSubscription(ctx context.Context, sub *model.Subscription) (*dspb.Subscription, error) {
	tx, err := s.Begin()
	if err != nil {
		return nil, err
	}

	err := sub.Delete(tx)
	if err != nil {
		return nil, multierr.Combine(err, tx.Rollback())
	}

	result, err := sub.ToProto()
	if err != nil {
		return nil, multierr.Combine(err, tx.Rollback())
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return result, nil
}

// SearchSubscriptions returns all subscriptions in "cells".
func (s *Store) SearchSubscriptions(ctx context.Context, cells s2.CellUnion, owner string) ([]*dspb.Subscription, error) {
	if len(cells) == 0 {
		return nil, errors.New("missing cell IDs for query")
	}

	tx, err := s.Begin()
	if err != nil {
		return nil, err
	}
	result, err := models.SearchSubscriptions(ctx, tx, cells, owner)
	if err != nil {
		return nil, multierr.Combine(err, tx.Rollback())
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return result, nil
}

func (s *Store) insertIdentificationServiceAreaUnchecked(ctx context.Context, serviceArea *dspb.IdentificationServiceArea, cells s2.CellUnion) (*dspb.IdentificationServiceArea, error) {
	const (
		subscriptionQuery = `
		INSERT INTO
			identification_service_areas
		VALUES
			($1, $2, $3, $4, $5, transaction_timestamp())
		RETURNING
			*`
		subscriptionCellQuery = `
		INSERT INTO
			cells_identification_service_areas
		VALUES
			($1, $2, $3, transaction_timestamp())
		`
	)

	isar := identificationServiceAreaRow{
		id:    serviceArea.GetId(),
		owner: serviceArea.GetOwner(),
		url:   serviceArea.GetFlightsUrl(),
	}

	starts, err := ptypes.Timestamp(serviceArea.GetExtents().GetTimeStart())
	if err != nil {
		return nil, err
	}
	isar.startsAt = starts

	ends, err := ptypes.Timestamp(serviceArea.GetExtents().GetTimeEnd())
	if err != nil {
		return nil, err
	}
	isar.endsAt = ends

	tx, err := s.Begin()
	if err != nil {
		return nil, err
	}

	if err := isar.scan(tx.QueryRowContext(
		ctx,
		subscriptionQuery,
		isar.id,
		isar.owner,
		isar.url,
		isar.startsAt,
		isar.endsAt,
	)); err != nil {
		return nil, multierr.Combine(err, tx.Rollback())
	}

	for _, cell := range cells {
		if _, err := tx.ExecContext(ctx, subscriptionCellQuery, cell, cell.Level(), isar.id); err != nil {
			return nil, multierr.Combine(err, tx.Rollback())
		}
	}

	result := &dspb.IdentificationServiceArea{
		Id:         isar.id,
		Owner:      isar.owner,
		FlightsUrl: isar.url,
	}

	ts, err := ptypes.TimestampProto(isar.startsAt)
	if err != nil {
		return nil, multierr.Combine(err, tx.Rollback())
	}
	result.Extents = &dspb.Volume4D{
		SpatialVolume: serviceArea.GetExtents().GetSpatialVolume(),
		TimeStart:     ts,
	}

	ts, err = ptypes.TimestampProto(isar.endsAt)
	if err != nil {
		return nil, multierr.Combine(err, tx.Rollback())
	}
	result.Extents.TimeEnd = ts

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return result, err
}

// DeleteIdentificationServiceArea deletes the IdentificationServiceArea identified by "id" and owned by "owner".
// Returns the delete IdentificationServiceArea and all Subscriptions affected by the delete.
func (s *Store) DeleteIdentificationServiceArea(ctx context.Context, id string, owner string) (*dspb.IdentificationServiceArea, []*dspb.SubscriberToNotify, error) {
	const (
		getAffectedCellsAndSubscriptions = `
			SELECT
				cells_identification_service_areas.cell_id,
				affected_subscriptions.subscription_id
			FROM
				cells_identification_service_areas
			LEFT JOIN
				(SELECT DISTINCT cell_id, subscription_id FROM cells_subscriptions)
			AS
				affected_subscriptions
			ON
				affected_subscriptions.cell_id = cells_identification_service_areas.cell_id
			WHERE
				cells_identification_service_areas.identification_service_area_id = $1
		`
		getSubscriptionDetailsForAffectedCells = `
			SELECT
				id, url, notification_index
			FROM
				subscriptions
			WHERE
				id = ANY($1)
			AND
				owner != $2
			AND
				begins_at IS NULL OR transaction_timestamp() >= begins_at
			AND
				expires_at IS NULL OR transaction_timestamp() <= expires_at
		`
		deleteIdentificationServiceAreaQuery = `
			DELETE FROM
				identification_service_areas
			WHERE
				id = $1
			AND
				owner = $2
			RETURNING
				*
		`
	)

	tx, err := s.Begin()
	if err != nil {
		return nil, nil, err
	}

	var (
		cells         []int64
		subscriptions []string
		cell          int64
		subscription  string
	)

	rows, err := tx.QueryContext(ctx, getAffectedCellsAndSubscriptions, id)
	if err != nil {
		return nil, nil, multierr.Combine(err, tx.Rollback())
	}
	defer rows.Close()

	for rows.Next() {
		if err := rows.Scan(&cell, &subscription); err != nil {
			return nil, nil, multierr.Combine(err, tx.Rollback())
		}
		cells = append(cells, cell)
		subscriptions = append(subscriptions, subscription)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, multierr.Combine(err, tx.Rollback())
	}

	isar := &identificationServiceAreaRow{}
	if err := isar.scan(tx.QueryRowContext(ctx, deleteIdentificationServiceAreaQuery, id, owner)); err != nil {
		// This error condition will be triggered if the owner does not match.
		multierr.Combine(err, tx.Rollback())
	}

	var (
		subscribers []subscriberRow
		subscriber  subscriberRow
	)

	rows, err = tx.QueryContext(ctx, getSubscriptionDetailsForAffectedCells, pq.Array(subscriptions), owner)
	if err != nil {
		return nil, nil, multierr.Combine(err, tx.Rollback())
	}
	defer rows.Close()

	for rows.Next() {
		if err := rows.Scan(&subscriber.id, &subscriber.url, &subscriber.notificationIndex); err != nil {
			return nil, nil, multierr.Combine(err, tx.Rollback())
		}

		subscribers = append(subscribers, subscriber)
	}

	if err := rows.Err(); err != nil {
		return nil, nil, multierr.Combine(err, tx.Rollback())
	}

	isa, err := isar.toProtobuf()
	if err != nil {
		return nil, nil, multierr.Combine(err, tx.Rollback())
	}

	subscribersToNotify := []*dspb.SubscriberToNotify{}
	for _, subscriber := range subscribers {
		subscriberToNotify, err := subscriber.toProtobuf()
		if err != nil {
			return nil, nil, multierr.Combine(err, tx.Rollback())
		}
		subscribersToNotify = append(subscribersToNotify, subscriberToNotify)
	}

	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}

	return isa, subscribersToNotify, nil
}

// Bootstrap bootstraps the underlying database with required tables.
//
// TODO: We should handle database migrations properly, but bootstrap both us
// *and* the database with this manual approach here.
func (s *Store) Bootstrap(ctx context.Context) error {
	const query = `
	CREATE TABLE IF NOT EXISTS subscriptions (
		id UUID PRIMARY KEY,
		owner STRING NOT NULL,
		url STRING NOT NULL,
		notification_index INT4 DEFAULT 0,
		begins_at TIMESTAMPTZ,
		expires_at TIMESTAMPTZ,
		updated_at TIMESTAMPTZ NOT NULL,
		INDEX begins_at_idx (begins_at),
		INDEX expires_at_idx (expires_at),
		CHECK (begins_at IS NULL OR expires_at IS NULL OR begins_at < expires_at)
	);
	CREATE TABLE IF NOT EXISTS cells_subscriptions (
		cell_id INT64 NOT NULL,
		cell_level INT CHECK (cell_level BETWEEN 0 and 30),
		subscription_id UUID NOT NULL REFERENCES subscriptions (id) ON DELETE CASCADE,
		updated_at TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (cell_id, subscription_id),
		INDEX cell_id_idx (cell_id),
		INDEX subscription_id_idx (subscription_id)
	);
	CREATE TABLE IF NOT EXISTS identification_service_areas (
		id UUID PRIMARY KEY,
		owner STRING NOT NULL,
		url STRING NOT NULL,
		starts_at TIMESTAMPTZ NOT NULL,
		ends_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		INDEX starts_at_idx (starts_at),
		INDEX ends_at_idx (ends_at),
		CHECK (starts_at IS NULL OR ends_at IS NULL OR starts_at < ends_at)
	);
	CREATE TABLE IF NOT EXISTS cells_identification_service_areas (
		cell_id INT64 NOT NULL,
		cell_level INT CHECK (cell_level BETWEEN 0 and 30),
		identification_service_area_id UUID NOT NULL REFERENCES identification_service_areas (id) ON DELETE CASCADE,
		updated_at TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (cell_id, identification_service_area_id),
		INDEX cell_id_idx (cell_id),
		INDEX identification_service_area_id_idx (identification_service_area_id)
	);
	`

	_, err := s.ExecContext(ctx, query)
	return err
}

// cleanUp drops all required tables from the store, useful for testing.
func (s *Store) cleanUp(ctx context.Context) error {
	const query = `
	DROP TABLE IF EXISTS cells_subscriptions;
	DROP TABLE IF EXISTS subscriptions;
	DROP TABLE IF EXISTS cells_identification_service_areas;
	DROP TABLE IF EXISTS identification_service_areas;`

	_, err := s.ExecContext(ctx, query)
	return err
}
