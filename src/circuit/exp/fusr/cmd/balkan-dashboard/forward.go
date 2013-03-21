package main

import (
	"errors"
	"fmt"
	"tumblr/balkan/shard"
	"tumblr/balkan/dashboard"
	"tumblr/balkan/proto"
	"tumblr/balkan/x"
)

// forwarder is responsible for ...
type forwarder struct {
	dashboards *shard.Topo
	here       *shard.Shard
	dialer     x.Dialer
	srv        *dashboard.DashboardServer
}

func newForwarder(dialer x.Dialer, dashboards []*shard.Shard, here *shard.Shard, srv *dashboard.DashboardServer) *forwarder {
	return &forwarder{
		dashboards: shard.NewPopulate(dashboards),
		here:       here,
		dialer:     dialer,
		srv:        srv,
	}
}

func (fwd *forwarder) Forward(q *proto.XDashboardQuery, alreadyForwarded bool) ([]*proto.Post, error) {
	sh := fwd.dashboards.Find(proto.ShardKeyOf(q.DashboardID))
	if sh == nil {
		panic("no shard for dashboardID")
	}
	// Service request locally
	if sh.Pivot == fwd.here.Pivot {
		return fwd.srv.Query(q)
	}
	if alreadyForwarded {
		return nil, errors.New("re-forwarding")
	}
	// Forward request to another timeline node
	return fwd.forward(q, sh)
}

func (fwd *forwarder) forward(q *proto.XDashboardQuery, sh *shard.Shard) ([]*proto.Post, error) {
	conn := fwd.dialer.Dial(sh.Addr)
	defer conn.Close()

	if err := conn.Write(q); err != nil {
		return nil, err
	}
	result, err := conn.Read()
	if err != nil {
		return nil, err
	}
	switch p := result.(type) {
	case *proto.XError:
		return nil, errors.New(fmt.Sprintf("fwd remote dash returned error (%s)", p.Error))
	case *proto.XDashboardQuerySuccess:
		return p.Posts, nil
	}
	return nil, errors.New("unknown response")
}
