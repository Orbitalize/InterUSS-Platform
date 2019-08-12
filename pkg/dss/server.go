package dss

import (
	"context"
	"time"

	"github.com/steeling/InterUSS-Platform/pkg/dss/models"

	"github.com/golang/protobuf/ptypes"

	"github.com/steeling/InterUSS-Platform/pkg/dss/auth"
	"github.com/steeling/InterUSS-Platform/pkg/dss/geo"
	dspb "github.com/steeling/InterUSS-Platform/pkg/dssproto"
	dsserr "github.com/steeling/InterUSS-Platform/pkg/errors"
)

var (
	WriteISAScope = "dss.write.identification_service_areas"
	ReadISAScope  = "dss.read.identification_service_areas"
)

// Server implements dssproto.DiscoveryAndSynchronizationService.
type Server struct {
	Store Store
}

func (s *Server) AuthScopes() map[string][]string {
	return map[string][]string{
		"GetIdentificationServiceArea":     []string{ReadISAScope},
		"PutIdentificationServiceArea":     []string{WriteISAScope},
		"PatchIdentificationServiceArea":   []string{WriteISAScope},
		"DeleteIdentificationServiceArea":  []string{WriteISAScope},
		"PutSubscription":                  []string{ReadISAScope},
		"PatchSubscription":                []string{ReadISAScope},
		"DeleteSubscription":               []string{ReadISAScope},
		"SearchSubscriptions":              []string{ReadISAScope},
		"SearchIdentificationServiceAreas": []string{ReadISAScope},
	}
}

func (s *Server) GetIdentificationServiceArea(ctx context.Context, req *dspb.GetIdentificationServiceAreaRequest) (*dspb.GetIdentificationServiceAreaResponse, error) {
	isa, err := s.Store.GetISA(ctx, models.ID(req.GetId()))
	if err != nil {
		return nil, err
	}
	p, err := isa.ToProto()
	if err != nil {
		return nil, err
	}
	return &dspb.GetIdentificationServiceAreaResponse{
		IdentificationServiceArea: p,
	}, nil
}

func (s *Server) PatchIdentificationServiceArea(ctx context.Context, req *dspb.PatchIdentificationServiceAreaRequest) (*dspb.PatchIdentificationServiceAreaResponse, error) {
	owner, ok := auth.OwnerFromContext(ctx)
	if !ok {
		return nil, dsserr.PermissionDenied("missing owner from context")
	}
	params := req.GetParams()
	if params == nil {
		return nil, dsserr.BadRequest("missing params")
	}

	updated, err := models.Version(params.GetVersion()).ToTimestamp()
	if err != nil {
		return nil, dsserr.BadRequest(err.Error())
	}
	isa := &models.IdentificationServiceArea{
		ID:        models.ID(req.GetId()),
		Url:       params.GetUrl().GetValue(),
		Owner:     owner,
		UpdatedAt: &updated,
	}
	if err := isa.SetExtents(params.GetExtents()); err != nil {
		return nil, err
	}

	isa, subscribers, err := s.Store.UpdateISA(ctx, isa)
	if err != nil {
		return nil, err
	}

	pbISA, err := isa.ToProto()
	if err != nil {
		return nil, dsserr.Internal(err.Error())
	}

	pbSubscribers := []*dspb.SubscriberToNotify{}
	for _, subscriber := range subscribers {
		pbSubscribers = append(pbSubscribers, subscriber.ToNotifyProto())
	}

	return &dspb.PatchIdentificationServiceAreaResponse{
		ServiceArea: pbISA,
		Subscribers: pbSubscribers,
	}, nil
}

func (s *Server) PutIdentificationServiceArea(ctx context.Context, req *dspb.PutIdentificationServiceAreaRequest) (*dspb.PutIdentificationServiceAreaResponse, error) {
	owner, ok := auth.OwnerFromContext(ctx)
	if !ok {
		return nil, dsserr.PermissionDenied("missing owner from context")
	}
	params := req.GetParams()
	if params == nil {
		return nil, dsserr.BadRequest("missing params")
	}

	isa := &models.IdentificationServiceArea{
		ID:    models.ID(req.GetId()),
		Url:   params.GetUrl(),
		Owner: owner,
	}

	if err := isa.SetExtents(params.GetExtents()); err != nil {
		return nil, err
	}

	isa, subscribers, err := s.Store.InsertISA(ctx, isa)
	if err != nil {
		return nil, err
	}

	pbISA, err := isa.ToProto()
	if err != nil {
		return nil, dsserr.Internal(err.Error())
	}

	pbSubscribers := []*dspb.SubscriberToNotify{}
	for _, subscriber := range subscribers {
		pbSubscribers = append(pbSubscribers, subscriber.ToNotifyProto())
	}

	return &dspb.PutIdentificationServiceAreaResponse{
		ServiceArea: pbISA,
		Subscribers: pbSubscribers,
	}, nil
}

func (s *Server) DeleteIdentificationServiceArea(ctx context.Context, req *dspb.DeleteIdentificationServiceAreaRequest) (*dspb.DeleteIdentificationServiceAreaResponse, error) {
	owner, ok := auth.OwnerFromContext(ctx)
	if !ok {
		return nil, dsserr.PermissionDenied("missing owner from context")
	}

	isa, subscribers, err := s.Store.DeleteISA(ctx, models.ID(req.GetId()), owner, models.Version(req.GetVersion()))
	if err != nil {
		return nil, err
	}

	p, err := isa.ToProto()
	if err != nil {
		return nil, dsserr.Internal(err.Error())
	}
	sp := make([]*dspb.SubscriberToNotify, len(subscribers))
	for i, _ := range subscribers {
		sp[i] = subscribers[i].ToNotifyProto()
	}

	return &dspb.DeleteIdentificationServiceAreaResponse{
		ServiceArea: p,
		Subscribers: sp,
	}, nil
}

func (s *Server) DeleteSubscription(ctx context.Context, req *dspb.DeleteSubscriptionRequest) (*dspb.DeleteSubscriptionResponse, error) {
	owner, ok := auth.OwnerFromContext(ctx)
	if !ok {
		return nil, dsserr.PermissionDenied("missing owner from context")
	}
	subscription, err := s.Store.DeleteSubscription(ctx, models.ID(req.GetId()), owner, models.Version(req.GetVersion()))
	if err != nil {
		return nil, err
	}
	p, err := subscription.ToProto()
	if err != nil {
		return nil, dsserr.Internal(err.Error())
	}
	return &dspb.DeleteSubscriptionResponse{
		Subscription: p,
	}, nil
}

func (s *Server) SearchIdentificationServiceAreas(ctx context.Context, req *dspb.SearchIdentificationServiceAreasRequest) (*dspb.SearchIdentificationServiceAreasResponse, error) {
	cu, err := geo.AreaToCellIDs(req.GetArea())
	if err != nil {
		return nil, dsserr.Internal(err.Error())
	}

	var (
		earliest *time.Time
		latest   *time.Time
	)

	if et := req.GetEarliestTime(); et != nil {
		if ts, err := ptypes.Timestamp(et); err == nil {
			earliest = &ts
		} else {
			return nil, dsserr.Internal(err.Error())
		}
	}

	if lt := req.GetLatestTime(); lt != nil {
		if ts, err := ptypes.Timestamp(lt); err == nil {
			latest = &ts
		} else {
			return nil, dsserr.Internal(err.Error())
		}
	}

	isas, err := s.Store.SearchISAs(ctx, cu, earliest, latest)
	if err != nil {
		return nil, err
	}

	areas := make([]*dspb.IdentificationServiceArea, len(isas))
	for i := range isas {
		a, err := isas[i].ToProto()
		if err != nil {
			return nil, err
		}
		areas[i] = a
	}

	return &dspb.SearchIdentificationServiceAreasResponse{
		ServiceAreas: areas,
	}, nil
}

func (s *Server) SearchSubscriptions(ctx context.Context, req *dspb.SearchSubscriptionsRequest) (*dspb.SearchSubscriptionsResponse, error) {
	owner, ok := auth.OwnerFromContext(ctx)
	if !ok {
		return nil, dsserr.PermissionDenied("missing owner from context")
	}

	cu, err := geo.AreaToCellIDs(req.GetArea())
	if err != nil {
		return nil, err
	}

	subscriptions, err := s.Store.SearchSubscriptions(ctx, cu, owner)
	if err != nil {
		return nil, err
	}
	sp := make([]*dspb.Subscription, len(subscriptions))
	for i, _ := range subscriptions {
		sp[i], err = subscriptions[i].ToProto()
		if err != nil {
			return nil, err
		}
	}

	return &dspb.SearchSubscriptionsResponse{
		Subscriptions: sp,
	}, nil
}

func (s *Server) GetSubscription(ctx context.Context, req *dspb.GetSubscriptionRequest) (*dspb.GetSubscriptionResponse, error) {
	subscription, err := s.Store.GetSubscription(ctx, models.ID(req.GetId()))
	if err != nil {
		return nil, err
	}
	p, err := subscription.ToProto()
	if err != nil {
		return nil, err
	}
	return &dspb.GetSubscriptionResponse{
		Subscription: p,
	}, nil
}

func (s *Server) PatchSubscription(ctx context.Context, req *dspb.PatchSubscriptionRequest) (*dspb.PatchSubscriptionResponse, error) {
	owner, ok := auth.OwnerFromContext(ctx)
	if !ok {
		return nil, dsserr.PermissionDenied("missing owner from context")
	}
	params := req.GetParams()
	if params == nil {
		return nil, dsserr.BadRequest("missing params")
	}
	updated, err := models.Version(params.GetVersion()).ToTimestamp()
	if err != nil {
		return nil, dsserr.BadRequest(err.Error())
	}

	sub := &models.Subscription{
		ID:        models.ID(req.GetId()),
		Url:       params.GetUrl().GetValue(),
		Owner:     owner,
		UpdatedAt: &updated,
	}
	if err := sub.SetExtents(params.GetExtents()); err != nil {
		return nil, dsserr.BadRequest("bad extents")
	}

	p, err := sub.ToProto()
	if err != nil {
		return nil, err
	}
	return &dspb.PatchSubscriptionResponse{
		Subscription: p,
	}, nil
}

// TODO(steeling) openapi 2 spec requires only 1 parameter in the body
func (s *Server) PutSubscription(ctx context.Context, req *dspb.PutSubscriptionRequest) (*dspb.PutSubscriptionResponse, error) {
	owner, ok := auth.OwnerFromContext(ctx)
	if !ok {
		return nil, dsserr.PermissionDenied("missing owner from context")
	}
	params := req.GetParams()
	if params == nil {
		return nil, dsserr.BadRequest("missing params")
	}

	sub := &models.Subscription{
		ID:    models.ID(req.GetId()),
		Url:   params.GetUrl(),
		Owner: owner,
	}
	if err := sub.SetExtents(params.GetExtents()); err != nil {
		return nil, dsserr.BadRequest("bad extents")
	}

	p, err := sub.ToProto()
	if err != nil {
		return nil, err
	}
	return &dspb.PutSubscriptionResponse{
		Subscription: p,
	}, nil
}
