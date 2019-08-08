package cockroach

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/golang/geo/s2"
	uuid "github.com/satori/go.uuid"
	"github.com/steeling/InterUSS-Platform/pkg/dss/models"
	"github.com/stretchr/testify/require"
)

var (
	serviceAreasPool = []struct {
		name  string
		input *models.IdentificationServiceArea
	}{
		{
			name: "a subscription without startTime and endTime",
			input: &models.IdentificationServiceArea{
				ID:        uuid.NewV4().String(),
				Owner:     "me-myself-and-i",
				Url:       "https://no/place/like/home/for/flights",
				StartTime: startTime,
				EndTime:   endTime,
				Cells: s2.CellUnion{
					s2.CellID(42),
				},
			},
		},
	}
)

func TestStoreSearchIdentificationServiceAreas(t *testing.T) {
	var (
		ctx   = context.Background()
		cells = s2.CellUnion{
			s2.CellID(42),
			s2.CellID(84),
			s2.CellID(126),
			s2.CellID(168),
		}
		insertedServiceAreas = []*models.IdentificationServiceArea{}
		store, tearDownStore = setUpStore(ctx, t)
	)
	defer func() {
		require.NoError(t, tearDownStore())
	}()

	for _, r := range serviceAreasPool {
		input := r.input.Apply(&models.IdentificationServiceArea{Cells: cells})
		saOut, _, err := store.InsertISA(ctx, input)
		require.NoError(t, err)
		require.NotNil(t, saOut)
		require.Equal(t, r.input.ID, saOut.ID)
		insertedServiceAreas = append(insertedServiceAreas, saOut)
	}

	for _, r := range []struct {
		name             string
		cells            s2.CellUnion
		timestampMutator func(time.Time, time.Time) (*time.Time, *time.Time)
		expectedLen      int
	}{
		{
			name:  "search for empty cell",
			cells: s2.CellUnion{s2.CellID(210)},
			timestampMutator: func(time.Time, time.Time) (*time.Time, *time.Time) {
				return nil, nil
			},
			expectedLen: 0,
		},
		{
			name:  "search for only one cell",
			cells: s2.CellUnion{s2.CellID(42)},
			timestampMutator: func(time.Time, time.Time) (*time.Time, *time.Time) {
				return nil, nil
			},
			expectedLen: 1,
		},
		{
			name:  "search with nil timestamps",
			cells: cells,
			timestampMutator: func(time.Time, time.Time) (*time.Time, *time.Time) {
				return nil, nil
			},
			expectedLen: 1,
		},
		{
			name:  "search with exact timestamps",
			cells: cells,
			timestampMutator: func(start time.Time, end time.Time) (*time.Time, *time.Time) {
				return &start, &end
			},
			expectedLen: 1,
		},
		{
			name:  "search with non-matching time span",
			cells: cells,
			timestampMutator: func(start time.Time, end time.Time) (*time.Time, *time.Time) {
				var (
					offset   = time.Duration(100 * time.Second)
					earliest = end.Add(offset)
					latest   = end.Add(offset * 2)
				)

				return &earliest, &latest
			},
			expectedLen: 0,
		},
		{
			name:  "search with expanded time span",
			cells: cells,
			timestampMutator: func(start time.Time, end time.Time) (*time.Time, *time.Time) {
				var (
					offset   = time.Duration(100 * time.Second)
					earliest = start.Add(-offset)
					latest   = end.Add(offset)
				)

				return &earliest, &latest
			},
			expectedLen: 1,
		},
	} {
		t.Run(r.name, func(t *testing.T) {
			for _, sa := range insertedServiceAreas {
				fmt.Println(sa.StartTime)
				fmt.Println(r.name, len(insertedServiceAreas), len(serviceAreasPool))
				earliest, latest := r.timestampMutator(sa.StartTime.Time, sa.EndTime.Time)

				serviceAreas, err := store.SearchISAs(ctx, r.cells, earliest, latest)
				require.NoError(t, err)
				require.Len(t, serviceAreas, r.expectedLen)
			}
		})
	}
}

func TestStoreDeleteIdentificationServiceAreas(t *testing.T) {
	var (
		ctx                  = context.Background()
		store, tearDownStore = setUpStore(ctx, t)
	)
	defer func() {
		require.NoError(t, tearDownStore())
	}()

	var (
		insertedServiceAreas  = []*models.IdentificationServiceArea{}
		insertedSubscriptions = []*models.Subscription{}
	)

	for _, r := range subscriptionsPool {
		input := r.input.Apply(&models.Subscription{Cells: s2.CellUnion{s2.CellID(42)}})
		s1, err := store.InsertSubscription(ctx, input)
		require.NoError(t, err)
		require.NotNil(t, s1)
		insertedSubscriptions = append(insertedSubscriptions, s1)
	}

	for _, r := range serviceAreasPool {
		tx, _ := store.Begin()
		_, err := store.pushISA(ctx, tx, r.input)
		tx.Commit()
		require.NoError(t, err)

		insertedServiceAreas = append(insertedServiceAreas, r.input)
	}

	for _, sa := range insertedServiceAreas {
		serviceAreaOut, subscriptionsOut, err := store.DeleteISA(ctx, sa.ID, sa.Owner, "")
		require.NoError(t, err)
		require.NotNil(t, serviceAreaOut)
		require.NotNil(t, subscriptionsOut)
	}
}
