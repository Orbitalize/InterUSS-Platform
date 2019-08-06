package cockroach

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang/geo/s2"
	"github.com/golang/protobuf/ptypes"
	"github.com/lib/pq" // Pull in the postgres database driver
	dspb "github.com/steeling/InterUSS-Platform/pkg/dssproto"
	"go.uber.org/multierr"
)

type scanner interface {
	Scan(fields ...interface{}) error
}

type subscriberRow struct {
	id                string
	url               string
	notificationIndex int32
}

func (sr *subscriberRow) scan(scanner scanner) error {
	return scanner.Scan(
		&sr.url,
		&sr.notificationIndex,
	)
}

func (sr *subscriberRow) toProtobuf() (*dspb.SubscriberToNotify, error) {
	return &dspb.SubscriberToNotify{
		Url: sr.url,
		Subscriptions: []*dspb.SubscriptionState{
			&dspb.SubscriptionState{
				NotificationIndex: sr.notificationIndex,
				Subscription:      sr.id,
			},
		},
	}, nil
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

// Convert updatedAt to a string, why not make it smaller
// WARNING: Changing this will cause RMW errors
// 32 is the highest value allowed by strconv
var versionBase = 32

// nullTime models a timestamp that could be NULL in the database. The model and
// implementation follows prior art as in sql.Null* types.
//
// Please note that this is rather driver-specific. The postgres sql driver
// errors out when trying to Scan a time.Time from a nil value. Other drivers
// might behave differently.
type nullTime struct {
	Time  time.Time
	Valid bool // Valid indicates whether Time carries a non-NULL value.
}

func (nt *nullTime) Scan(value interface{}) error {
	if value == nil {
		nt.Time = time.Time{}
		nt.Valid = false
		return nil
	}

	t, ok := value.(time.Time)
	if !ok {
		return fmt.Errorf("failed to cast database value, expected time.Time, got %T", value)
	}
	nt.Time, nt.Valid = t, ok

	return nil
}

func (nt nullTime) Value() (driver.Value, error) {
	if !nt.Valid {
		return nil, nil
	}
	return nt.Time, nil
}

type subscriptionsRow struct {
	id                string
	owner             string
	url               string
	typesFilter       sql.NullString
	notificationIndex int32
	lastUsedAt        pq.NullTime
	beginsAt          pq.NullTime
	expiresAt         pq.NullTime
	updatedAt         time.Time
}

func (sr *subscriptionsRow) scan(scanner scanner) error {
	return scanner.Scan(&sr.id,
		&sr.owner,
		&sr.url,
		&sr.typesFilter,
		&sr.notificationIndex,
		&sr.lastUsedAt,
		&sr.beginsAt,
		&sr.expiresAt,
		&sr.updatedAt,
	)
}

func (sr *subscriptionsRow) toProtobuf() (*dspb.Subscription, error) {
	result := &dspb.Subscription{
		Id:    sr.id,
		Owner: sr.owner,
		Callbacks: &dspb.SubscriptionCallbacks{
			IdentificationServiceAreaUrl: sr.url,
		},
		NotificationIndex: int32(sr.notificationIndex),
		Version:           timestampToVersionString(sr.updatedAt),
	}

	if sr.beginsAt.Valid {
		ts, err := ptypes.TimestampProto(sr.beginsAt.Time)
		if err != nil {
			return nil, err
		}
		result.Begins = ts
	}

	if sr.expiresAt.Valid {
		ts, err := ptypes.TimestampProto(sr.expiresAt.Time)
		if err != nil {
			return nil, err
		}
		result.Expires = ts
	}

	return result, nil
}

func versionStringToTimestamp(s string) (time.Time, error) {
	var t time.Time
	nanos, err := strconv.ParseUint(s, versionBase, 64)
	if err != nil {
		return t, err
	}
	return time.Unix(0, int64(nanos)), nil
}

func timestampToVersionString(t time.Time) string {
	return strconv.FormatUint(uint64(t.UnixNano()), versionBase)
}

func (sr *subscriptionsRow) versionOK(version string) bool {
	return version == "" || version == timestampToVersionString(sr.updatedAt)
}

// from or apply
func (sr *subscriptionsRow) applyProtobuf(subscription *dspb.Subscription) error {
	if subscription.Id != "" {
		sr.id = subscription.Id
	}

	if subscription.Owner != "" {
		sr.owner = subscription.Owner
	}
	if subscription.Callbacks.GetIdentificationServiceAreaUrl() != "" {
		sr.url = subscription.Callbacks.GetIdentificationServiceAreaUrl()
	}
	if ts := subscription.GetBegins(); ts != nil {
		begins, err := ptypes.Timestamp(ts)
		if err != nil {
			return err
		}
		sr.beginsAt.Time = begins
		sr.beginsAt.Valid = true
	}

	if ts := subscription.GetExpires(); ts != nil {
		expires, err := ptypes.Timestamp(ts)
		if err != nil {
			return err
		}
		sr.expiresAt.Time = expires
		sr.expiresAt.Valid = true
	}
	return nil
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

// insertSubscription inserts subscription into the store and returns
// the resulting subscription including its ID.
func (s *Store) insertSubscription(ctx context.Context, subscription *dspb.Subscription, cells s2.CellUnion) (*dspb.Subscription, error) {
	const (
		insertQuery = `
		INSERT INTO
			subscriptions
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8, transaction_timestamp())
		RETURNING
			*`
		subscriptionCellQuery = `
		INSERT INTO
			cells_subscriptions
		VALUES
			($1, $2, $3, transaction_timestamp())
		`
	)

	tx, err := s.Begin()
	if err != nil {
		return nil, err
	}

	sr := &subscriptionsRow{}

	sr.applyProtobuf(subscription)

	if err := sr.scan(tx.QueryRowContext(
		ctx,
		insertQuery,
		sr.id,
		sr.owner,
		sr.url,
		sr.typesFilter,
		sr.notificationIndex,
		sr.lastUsedAt,
		sr.beginsAt,
		sr.expiresAt,
	)); err != nil {
		return nil, multierr.Combine(err, tx.Rollback())
	}

	for _, cell := range cells {
		if _, err := tx.ExecContext(ctx, subscriptionCellQuery, cell, cell.Level(), sr.id); err != nil {
			return nil, multierr.Combine(err, tx.Rollback())
		}
	}

	result, err := sr.toProtobuf()
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// updatesSubscription updates the subscription  and returns
// the resulting subscription including its ID.
func (s *Store) updateSubscription(ctx context.Context, subscription *dspb.Subscription, cells s2.CellUnion) (*dspb.Subscription, error) {
	const (
		// We use an upsert so we don't have to specify the column
		updateQuery = `
		UPSERT INTO
		  subscriptions
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8, transaction_timestamp())
		RETURNING
			*`
		subscriptionCellQuery = `
		UPSERT INTO
			cells_subscriptions
		VALUES
			($1, $2, $3, transaction_timestamp())
		`
		getQuery = `
		SELECT * FROM
			subscriptions
		WHERE
			id = $1`
	)

	tx, err := s.Begin()
	if err != nil {
		return nil, err
	}

	sr := &subscriptionsRow{}

	err = sr.scan(tx.QueryRowContext(ctx, getQuery, subscription.Id))

	switch {
	case err == sql.ErrNoRows: // Do nothing here.
		return nil, multierr.Combine(err, tx.Rollback())
	case err != nil:
		return nil, multierr.Combine(err, tx.Rollback())
	case !sr.versionOK(subscription.Version):
		err := fmt.Errorf("version mismatch for subscription %s", subscription.Id)
		return nil, multierr.Combine(err, tx.Rollback())
	}

	sr.applyProtobuf(subscription)

	if err := sr.scan(tx.QueryRowContext(
		ctx,
		updateQuery,
		sr.id,
		sr.owner,
		sr.url,
		sr.typesFilter,
		sr.notificationIndex,
		sr.lastUsedAt,
		sr.beginsAt,
		sr.expiresAt,
	)); err != nil {
		return nil, multierr.Combine(err, tx.Rollback())
	}

	// TODO(steeling) we also need to delete any leftover cells.
	for _, cell := range cells {
		if _, err := tx.ExecContext(ctx, subscriptionCellQuery, cell, cell.Level(), sr.id); err != nil {
			return nil, multierr.Combine(err, tx.Rollback())
		}
	}

	result, err := sr.toProtobuf()
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
	const (
		subscriptionQuery = `
		SELECT * FROM
			subscriptions
		WHERE
			id = $1`
	)

	tx, err := s.Begin()
	if err != nil {
		return nil, err
	}

	sr := &subscriptionsRow{}

	if err := sr.scan(tx.QueryRowContext(ctx, subscriptionQuery, id)); err != nil {
		return nil, multierr.Combine(err, tx.Rollback())
	}

	result, err := sr.toProtobuf()
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
func (s *Store) DeleteSubscription(ctx context.Context, id, version string) (*dspb.Subscription, error) {
	const (
		blindQuery = `
		DELETE FROM
			subscriptions
		WHERE
			id = $1
		RETURNING
			*`

		idempotentQuery = `
		DELETE FROM
			subscriptions
		WHERE
			id = $1
			AND updated_at = $2
		RETURNING
			*`
	)

	tx, err := s.Begin()
	if err != nil {
		return nil, err
	}

	sr := &subscriptionsRow{}
	switch version {
	case "":
		if err := sr.scan(tx.QueryRowContext(ctx, blindQuery, id)); err != nil {
			return nil, multierr.Combine(err, tx.Rollback())
		}
	default:
		updatedAt, err := versionStringToTimestamp(version)
		if err != nil {
			return nil, multierr.Combine(err, tx.Rollback())
		}
		if err := sr.scan(tx.QueryRowContext(ctx, idempotentQuery, id, updatedAt)); err != nil {
			return nil, multierr.Combine(err, tx.Rollback())
		}
	}

	result, err := sr.toProtobuf()
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
	const (
		subscriptionsInCellsQuery = `
			SELECT
				subscriptions.*
			FROM
				subscriptions
			LEFT JOIN 
				(SELECT DISTINCT cells_subscriptions.subscription_id FROM cells_subscriptions WHERE cells_subscriptions.cell_id = ANY($1))
			AS
				unique_subscription_ids
			ON
				subscriptions.id = unique_subscription_ids.subscription_id
			WHERE
				subscriptions.owner = $2`
	)

	if len(cells) == 0 {
		return nil, errors.New("missing cell IDs for query")
	}

	tx, err := s.Begin()
	if err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, subscriptionsInCellsQuery, pq.Array(cells), owner)
	if err != nil {
		return nil, multierr.Combine(err, tx.Rollback())
	}
	defer rows.Close()

	var (
		row    = &subscriptionsRow{}
		result = []*dspb.Subscription{}
	)

	for rows.Next() {
		if err := row.scan(rows); err != nil {
			return nil, multierr.Combine(err, tx.Rollback())
		}
		pb, err := row.toProtobuf()
		if err != nil {
			return nil, multierr.Combine(err, tx.Rollback())
		}

		result = append(result, pb)
	}

	if err := rows.Err(); err != nil {
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
		types_filter STRING,
		notification_index INT4 DEFAULT 0,
		last_used_at TIMESTAMPTZ,
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
