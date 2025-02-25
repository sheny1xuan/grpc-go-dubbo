/*
 *
 * Copyright 2021 gRPC authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 */

package server

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"
)

import (
	v3corepb "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	v3listenerpb "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	v3routepb "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	v3httppb "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	v3tlspb "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"

	wrapperspb "github.com/golang/protobuf/ptypes/wrappers"
)

import (
	"github.com/dubbogo/grpc-go/internal/grpctest"
	"github.com/dubbogo/grpc-go/internal/testutils"
	"github.com/dubbogo/grpc-go/internal/xds/env"
	_ "github.com/dubbogo/grpc-go/xds/internal/httpfilter/router"
	"github.com/dubbogo/grpc-go/xds/internal/testutils/e2e"
	"github.com/dubbogo/grpc-go/xds/internal/testutils/fakeclient"
	"github.com/dubbogo/grpc-go/xds/internal/xdsclient"
)

const (
	fakeListenerHost         = "0.0.0.0"
	fakeListenerPort         = 50051
	testListenerResourceName = "lds.target.1.2.3.4:1111"
	defaultTestTimeout       = 1 * time.Second
	defaultTestShortTimeout  = 10 * time.Millisecond
)

var listenerWithRouteConfiguration = &v3listenerpb.Listener{
	FilterChains: []*v3listenerpb.FilterChain{
		{
			FilterChainMatch: &v3listenerpb.FilterChainMatch{
				PrefixRanges: []*v3corepb.CidrRange{
					{
						AddressPrefix: "192.168.0.0",
						PrefixLen: &wrapperspb.UInt32Value{
							Value: uint32(16),
						},
					},
				},
				SourceType: v3listenerpb.FilterChainMatch_SAME_IP_OR_LOOPBACK,
				SourcePrefixRanges: []*v3corepb.CidrRange{
					{
						AddressPrefix: "192.168.0.0",
						PrefixLen: &wrapperspb.UInt32Value{
							Value: uint32(16),
						},
					},
				},
				SourcePorts: []uint32{80},
			},
			Filters: []*v3listenerpb.Filter{
				{
					Name: "filter-1",
					ConfigType: &v3listenerpb.Filter_TypedConfig{
						TypedConfig: testutils.MarshalAny(&v3httppb.HttpConnectionManager{
							RouteSpecifier: &v3httppb.HttpConnectionManager_Rds{
								Rds: &v3httppb.Rds{
									ConfigSource: &v3corepb.ConfigSource{
										ConfigSourceSpecifier: &v3corepb.ConfigSource_Ads{Ads: &v3corepb.AggregatedConfigSource{}},
									},
									RouteConfigName: "route-1",
								},
							},
							HttpFilters: []*v3httppb.HttpFilter{e2e.RouterHTTPFilter},
						}),
					},
				},
			},
		},
	},
}

var listenerWithFilterChains = &v3listenerpb.Listener{
	FilterChains: []*v3listenerpb.FilterChain{
		{
			FilterChainMatch: &v3listenerpb.FilterChainMatch{
				PrefixRanges: []*v3corepb.CidrRange{
					{
						AddressPrefix: "192.168.0.0",
						PrefixLen: &wrapperspb.UInt32Value{
							Value: uint32(16),
						},
					},
				},
				SourceType: v3listenerpb.FilterChainMatch_SAME_IP_OR_LOOPBACK,
				SourcePrefixRanges: []*v3corepb.CidrRange{
					{
						AddressPrefix: "192.168.0.0",
						PrefixLen: &wrapperspb.UInt32Value{
							Value: uint32(16),
						},
					},
				},
				SourcePorts: []uint32{80},
			},
			TransportSocket: &v3corepb.TransportSocket{
				Name: "envoy.transport_sockets.tls",
				ConfigType: &v3corepb.TransportSocket_TypedConfig{
					TypedConfig: testutils.MarshalAny(&v3tlspb.DownstreamTlsContext{
						CommonTlsContext: &v3tlspb.CommonTlsContext{
							TlsCertificateCertificateProviderInstance: &v3tlspb.CommonTlsContext_CertificateProviderInstance{
								InstanceName:    "identityPluginInstance",
								CertificateName: "identityCertName",
							},
						},
					}),
				},
			},
			Filters: []*v3listenerpb.Filter{
				{
					Name: "filter-1",
					ConfigType: &v3listenerpb.Filter_TypedConfig{
						TypedConfig: testutils.MarshalAny(&v3httppb.HttpConnectionManager{
							RouteSpecifier: &v3httppb.HttpConnectionManager_RouteConfig{
								RouteConfig: &v3routepb.RouteConfiguration{
									Name: "routeName",
									VirtualHosts: []*v3routepb.VirtualHost{{
										Domains: []string{"lds.target.good:3333"},
										Routes: []*v3routepb.Route{{
											Match: &v3routepb.RouteMatch{
												PathSpecifier: &v3routepb.RouteMatch_Prefix{Prefix: "/"},
											},
											Action: &v3routepb.Route_NonForwardingAction{},
										}}}}},
							},
							HttpFilters: []*v3httppb.HttpFilter{e2e.RouterHTTPFilter},
						}),
					},
				},
			},
		},
	},
}

type s struct {
	grpctest.Tester
}

func Test(t *testing.T) {
	grpctest.RunSubTests(t, s{})
}

type tempError struct{}

func (tempError) Error() string {
	return "listenerWrapper test temporary error"
}

func (tempError) Temporary() bool {
	return true
}

// connAndErr wraps a net.Conn and an error.
type connAndErr struct {
	conn net.Conn
	err  error
}

// fakeListener allows the user to inject conns returned by Accept().
type fakeListener struct {
	acceptCh chan connAndErr
	closeCh  *testutils.Channel
}

func (fl *fakeListener) Accept() (net.Conn, error) {
	cne, ok := <-fl.acceptCh
	if !ok {
		return nil, errors.New("a non-temporary error")
	}
	return cne.conn, cne.err
}

func (fl *fakeListener) Close() error {
	fl.closeCh.Send(nil)
	return nil
}

func (fl *fakeListener) Addr() net.Addr {
	return &net.TCPAddr{
		IP:   net.IPv4(0, 0, 0, 0),
		Port: fakeListenerPort,
	}
}

// fakeConn overrides LocalAddr, RemoteAddr and Close methods.
type fakeConn struct {
	net.Conn
	local, remote net.Addr
	closeCh       *testutils.Channel
}

func (fc *fakeConn) LocalAddr() net.Addr {
	return fc.local
}

func (fc *fakeConn) RemoteAddr() net.Addr {
	return fc.remote
}

func (fc *fakeConn) Close() error {
	fc.closeCh.Send(nil)
	return nil
}

func newListenerWrapper(t *testing.T) (*listenerWrapper, <-chan struct{}, *fakeclient.Client, *fakeListener, func()) {
	t.Helper()

	// Create a listener wrapper with a fake listener and fake XDSClient and
	// verify that it extracts the host and port from the passed in listener.
	lis := &fakeListener{
		acceptCh: make(chan connAndErr, 1),
		closeCh:  testutils.NewChannel(),
	}
	xdsC := fakeclient.NewClient()
	lParams := ListenerWrapperParams{
		Listener:             lis,
		ListenerResourceName: testListenerResourceName,
		XDSClient:            xdsC,
	}
	l, readyCh := NewListenerWrapper(lParams)
	if l == nil {
		t.Fatalf("NewListenerWrapper(%+v) returned nil", lParams)
	}
	lw, ok := l.(*listenerWrapper)
	if !ok {
		t.Fatalf("NewListenerWrapper(%+v) returned listener of type %T want *listenerWrapper", lParams, l)
	}
	if lw.addr != fakeListenerHost || lw.port != strconv.Itoa(fakeListenerPort) {
		t.Fatalf("listenerWrapper has host:port %s:%s, want %s:%d", lw.addr, lw.port, fakeListenerHost, fakeListenerPort)
	}
	return lw, readyCh, xdsC, lis, func() { l.Close() }
}

func (s) TestNewListenerWrapper(t *testing.T) {
	_, readyCh, xdsC, _, cleanup := newListenerWrapper(t)
	defer cleanup()

	// Verify that the listener wrapper registers a listener watch for the
	// expected Listener resource name.
	ctx, cancel := context.WithTimeout(context.Background(), defaultTestTimeout)
	defer cancel()
	name, err := xdsC.WaitForWatchListener(ctx)
	if err != nil {
		t.Fatalf("error when waiting for a watch on a Listener resource: %v", err)
	}
	if name != testListenerResourceName {
		t.Fatalf("listenerWrapper registered a lds watch on %s, want %s", name, testListenerResourceName)
	}

	// Push an error to the listener update handler.
	xdsC.InvokeWatchListenerCallback(xdsclient.ListenerUpdate{}, errors.New("bad listener update"))
	timer := time.NewTimer(defaultTestShortTimeout)
	select {
	case <-timer.C:
		timer.Stop()
	case <-readyCh:
		t.Fatalf("ready channel written to after receipt of a bad Listener update")
	}

	fcm, err := xdsclient.NewFilterChainManager(listenerWithFilterChains)
	if err != nil {
		t.Fatalf("xdsclient.NewFilterChainManager() failed with error: %v", err)
	}

	// Push an update whose address does not match the address to which our
	// listener is bound, and verify that the ready channel is not written to.
	xdsC.InvokeWatchListenerCallback(xdsclient.ListenerUpdate{
		InboundListenerCfg: &xdsclient.InboundListenerConfig{
			Address:      "10.0.0.1",
			Port:         "50051",
			FilterChains: fcm,
		}}, nil)
	timer = time.NewTimer(defaultTestShortTimeout)
	select {
	case <-timer.C:
		timer.Stop()
	case <-readyCh:
		t.Fatalf("ready channel written to after receipt of a bad Listener update")
	}

	// Push a good update, and verify that the ready channel is written to.
	// Since there are no dynamic RDS updates needed to be received, the
	// ListenerWrapper does not have to wait for anything else before telling
	// that it is ready.
	xdsC.InvokeWatchListenerCallback(xdsclient.ListenerUpdate{
		InboundListenerCfg: &xdsclient.InboundListenerConfig{
			Address:      fakeListenerHost,
			Port:         strconv.Itoa(fakeListenerPort),
			FilterChains: fcm,
		}}, nil)

	select {
	case <-ctx.Done():
		t.Fatalf("timeout waiting for the ready channel to be written to after receipt of a good Listener update")
	case <-readyCh:
	}
}

// TestNewListenerWrapperWithRouteUpdate tests the scenario where the listener
// gets built, starts a watch, that watch returns a list of Route Names to
// return, than receives an update from the rds handler. Only after receiving
// the update from the rds handler should it move the server to
// ServingModeServing.
func (s) TestNewListenerWrapperWithRouteUpdate(t *testing.T) {
	oldRBAC := env.RBACSupport
	env.RBACSupport = true
	defer func() {
		env.RBACSupport = oldRBAC
	}()
	_, readyCh, xdsC, _, cleanup := newListenerWrapper(t)
	defer cleanup()

	// Verify that the listener wrapper registers a listener watch for the
	// expected Listener resource name.
	ctx, cancel := context.WithTimeout(context.Background(), defaultTestTimeout)
	defer cancel()
	name, err := xdsC.WaitForWatchListener(ctx)
	if err != nil {
		t.Fatalf("error when waiting for a watch on a Listener resource: %v", err)
	}
	if name != testListenerResourceName {
		t.Fatalf("listenerWrapper registered a lds watch on %s, want %s", name, testListenerResourceName)
	}
	fcm, err := xdsclient.NewFilterChainManager(listenerWithRouteConfiguration)
	if err != nil {
		t.Fatalf("xdsclient.NewFilterChainManager() failed with error: %v", err)
	}

	// Push a good update which contains a Filter Chain that specifies dynamic
	// RDS Resources that need to be received. This should ping rds handler
	// about which rds names to start, which will eventually start a watch on
	// xds client for rds name "route-1".
	xdsC.InvokeWatchListenerCallback(xdsclient.ListenerUpdate{
		InboundListenerCfg: &xdsclient.InboundListenerConfig{
			Address:      fakeListenerHost,
			Port:         strconv.Itoa(fakeListenerPort),
			FilterChains: fcm,
		}}, nil)

	// This should start a watch on xds client for rds name "route-1".
	routeName, err := xdsC.WaitForWatchRouteConfig(ctx)
	if err != nil {
		t.Fatalf("error when waiting for a watch on a Route resource: %v", err)
	}
	if routeName != "route-1" {
		t.Fatalf("listenerWrapper registered a lds watch on %s, want %s", routeName, "route-1")
	}

	// This shouldn't invoke good update channel, as has not received rds updates yet.
	timer := time.NewTimer(defaultTestShortTimeout)
	select {
	case <-timer.C:
		timer.Stop()
	case <-readyCh:
		t.Fatalf("ready channel written to without rds configuration specified")
	}

	// Invoke rds callback for the started rds watch. This valid rds callback
	// should trigger the listener wrapper to fire GoodUpdate, as it has
	// received both it's LDS Configuration and also RDS Configuration,
	// specified in LDS Configuration.
	xdsC.InvokeWatchRouteConfigCallback("route-1", xdsclient.RouteConfigUpdate{}, nil)

	// All of the xDS updates have completed, so can expect to send a ping on
	// good update channel.
	select {
	case <-ctx.Done():
		t.Fatalf("timeout waiting for the ready channel to be written to after receipt of a good rds update")
	case <-readyCh:
	}
}

func (s) TestListenerWrapper_Accept(t *testing.T) {
	boCh := testutils.NewChannel()
	origBackoffFunc := backoffFunc
	backoffFunc = func(v int) time.Duration {
		boCh.Send(v)
		return 0
	}
	defer func() { backoffFunc = origBackoffFunc }()

	lw, readyCh, xdsC, lis, cleanup := newListenerWrapper(t)
	defer cleanup()

	// Push a good update with a filter chain which accepts local connections on
	// 192.168.0.0/16 subnet and port 80.
	fcm, err := xdsclient.NewFilterChainManager(listenerWithFilterChains)
	if err != nil {
		t.Fatalf("xdsclient.NewFilterChainManager() failed with error: %v", err)
	}
	xdsC.InvokeWatchListenerCallback(xdsclient.ListenerUpdate{
		InboundListenerCfg: &xdsclient.InboundListenerConfig{
			Address:      fakeListenerHost,
			Port:         strconv.Itoa(fakeListenerPort),
			FilterChains: fcm,
		}}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), defaultTestTimeout)
	defer cancel()
	defer close(lis.acceptCh)
	select {
	case <-ctx.Done():
		t.Fatalf("timeout waiting for the ready channel to be written to after receipt of a good Listener update")
	case <-readyCh:
	}

	// Push a non-temporary error into Accept().
	nonTempErr := errors.New("a non-temporary error")
	lis.acceptCh <- connAndErr{err: nonTempErr}
	if _, err := lw.Accept(); err != nonTempErr {
		t.Fatalf("listenerWrapper.Accept() returned error: %v, want: %v", err, nonTempErr)
	}

	// Invoke Accept() in a goroutine since we expect it to swallow:
	// 1. temporary errors returned from the underlying listener
	// 2. errors related to finding a matching filter chain for the incoming
	// 	  connection.
	errCh := testutils.NewChannel()
	go func() {
		conn, err := lw.Accept()
		if err != nil {
			errCh.Send(err)
			return
		}
		if _, ok := conn.(*connWrapper); !ok {
			errCh.Send(errors.New("listenerWrapper.Accept() returned a Conn of type %T, want *connWrapper"))
			return
		}
		errCh.Send(nil)
	}()

	// Push a temporary error into Accept() and verify that it backs off.
	lis.acceptCh <- connAndErr{err: tempError{}}
	if _, err := boCh.Receive(ctx); err != nil {
		t.Fatalf("error when waiting for Accept() to backoff on temporary errors: %v", err)
	}

	// Push a fakeConn which does not match any filter chains configured on the
	// received Listener resource. Verify that the conn is closed.
	fc := &fakeConn{
		local:   &net.TCPAddr{IP: net.IPv4(192, 168, 1, 2), Port: 79},
		remote:  &net.TCPAddr{IP: net.IPv4(10, 1, 1, 1), Port: 80},
		closeCh: testutils.NewChannel(),
	}
	lis.acceptCh <- connAndErr{conn: fc}
	if _, err := fc.closeCh.Receive(ctx); err != nil {
		t.Fatalf("error when waiting for conn to be closed on no filter chain match: %v", err)
	}

	// Push a fakeConn which matches the filter chains configured on the
	// received Listener resource. Verify that Accept() returns.
	fc = &fakeConn{
		local:   &net.TCPAddr{IP: net.IPv4(192, 168, 1, 2)},
		remote:  &net.TCPAddr{IP: net.IPv4(192, 168, 1, 2), Port: 80},
		closeCh: testutils.NewChannel(),
	}
	lis.acceptCh <- connAndErr{conn: fc}
	if _, err := errCh.Receive(ctx); err != nil {
		t.Fatalf("error when waiting for Accept() to return the conn on filter chain match: %v", err)
	}
}
