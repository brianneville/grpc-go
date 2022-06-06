package latency

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/benchmark/latency/defaults"
	"google.golang.org/grpc/benchmark/latency/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

type Service struct {
	proto.UnsafeMockRPCServer
}

func (s Service) Get(ctx context.Context, req *proto.GetRequest) (*proto.GetResponse, error) {
	return &proto.GetResponse{
		B: []byte(defaults.BigResponse),
	}, nil
}

// TestGrpcbug emulates the setup for the unexpected EOF bug that can be hit with the setup
// https://github.com/brianneville/grpcbug.
// It uses the same proto service, request and response.
// Unfortunately right now (2022-06-06) this test fails to reproduce the bug.
func (s) TestGrpcbug(t *testing.T) {
	defer restoreHooks()()

	tn := time.Unix(123, 0)
	mu := &sync.Mutex{}
	now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return tn
	}
	sleep = func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		if d > 0 {
			tn = tn.Add(d)
		}
	}

	n := &Network{
		Kbps:    53 * 1024,              // speed measured from my laptop
		Latency: 200 * time.Millisecond, // half rtt
		MTU:     1500,                   // from ifconfig
	}

	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("Unexpected error creating listener: %v", err)
	}

	lis = n.Listener(lis)
	grpcServer := grpc.NewServer()
	reflection.Register(grpcServer)
	proto.RegisterMockRPCServer(grpcServer, &Service{})

	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			t.Errorf("failed to serve listener with err %s", err)
			return
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		grpcServer.GracefulStop()
	}()

	cfg := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			clientConn, err := n.TimeoutDialer(net.DialTimeout)(
				"tcp", addr, 2*time.Second)
			if err != nil {
				t.Fatalf("Unexpected error dialing: %v", err)
			}
			return clientConn, nil
		}),
	}

	cc, err := grpc.DialContext(ctx, lis.Addr().String(), cfg...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := cc.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	client := proto.NewMockRPCClient(cc)
	resp, err := client.Get(ctx, &proto.GetRequest{})

	if expectedErr := status.Error(codes.Internal,
		"unexpected EOF"); !errors.Is(err, expectedErr) {
		respDetail := "nil"
		if resp != nil {
			respDetail = fmt.Sprintf("of length %d", len(resp.B))
		}
		t.Fatalf("expected to get error %q, instead got error %q with response %s",
			expectedErr, err, respDetail)
	}
}
