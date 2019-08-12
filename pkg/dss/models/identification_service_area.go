package models

import (
	"time"

	"github.com/golang/geo/s2"
	"github.com/golang/protobuf/ptypes"
	dspb "github.com/steeling/InterUSS-Platform/pkg/dssproto"
)

type IdentificationServiceArea struct {
	ID         ID
	Url        string
	Owner      Owner
	Cells      s2.CellUnion
	StartTime  *time.Time
	EndTime    *time.Time
	UpdatedAt  *time.Time
	AltitudeHi *float32
	AltitudeLo *float32
}

func (i *IdentificationServiceArea) Version() Version {
	return VersionFromTimestamp(i.UpdatedAt)
}

// Apply fields from s2 onto s, preferring any fields set in i2 except for ID
// and Owner.
func (s *IdentificationServiceArea) Apply(i2 *IdentificationServiceArea) *IdentificationServiceArea {
	new := *s
	if i2.Url != "" {
		new.Url = i2.Url
	}
	if i2.Cells != nil {
		new.Cells = i2.Cells
	}
	if i2.StartTime != nil {
		new.StartTime = i2.StartTime
	}
	if i2.EndTime != nil {
		new.EndTime = i2.EndTime
	}
	if i2.UpdatedAt != nil {
		new.UpdatedAt = i2.UpdatedAt
	}
	if i2.AltitudeHi != nil {
		new.AltitudeHi = i2.AltitudeHi
	}
	if i2.AltitudeLo != nil {
		new.AltitudeLo = i2.AltitudeLo
	}
	return &new
}

func (i *IdentificationServiceArea) ToProto() (*dspb.IdentificationServiceArea, error) {
	result := &dspb.IdentificationServiceArea{
		Id:      i.ID.String(),
		Owner:   i.Owner.String(),
		Url:     i.Url,
		Version: i.Version().String(),
	}

	if i.StartTime != nil {
		ts, err := ptypes.TimestampProto(*i.StartTime)
		if err != nil {
			return nil, err
		}
		result.StartTime = ts
	}

	if i.EndTime != nil {
		ts, err := ptypes.TimestampProto(*i.EndTime)
		if err != nil {
			return nil, err
		}
		result.EndTime = ts
	}
	return result, nil
}
