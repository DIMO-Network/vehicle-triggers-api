// Package rpc provides the gRPC server implementation for the index repo service.
package e2e_test

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	pb "github.com/DIMO-Network/token-exchange-api/pkg/grpc"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// mockTokenExchangeServer wraps the gRPC server and contains test configuration
type mockTokenExchangeServer struct {
	grpcServer        *grpc.Server
	listener          net.Listener
	port              int
	mutex             sync.Mutex
	accessCheckReturn map[string]bool
	pb.UnimplementedTokenExchangeServiceServer
}

// NewTestTokenExchangeAPI creates and starts a gRPC server on a random available port
func NewTestTokenExchangeAPI(t *testing.T) *mockTokenExchangeServer {
	// Find an available port
	listener, err := net.Listen("tcp", ":0")
	require.NoError(t, err)

	// Create the gRPC server
	grpcServer := grpc.NewServer()
	testServer := &mockTokenExchangeServer{
		grpcServer:        grpcServer,
		listener:          listener,
		port:              listener.Addr().(*net.TCPAddr).Port,
		accessCheckReturn: make(map[string]bool),
	}

	pb.RegisterTokenExchangeServiceServer(grpcServer, testServer)

	// Start the server
	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			t.Logf("server stopped: %v", err)
		}
	}()

	// Wait a moment for the server to start
	time.Sleep(100 * time.Millisecond)

	return testServer

}

// Stop gracefully stops the test server
func (ts *mockTokenExchangeServer) Close() {
	ts.grpcServer.GracefulStop()

	if ts.listener != nil {
		ts.listener.Close() //nolint:errcheck
	}
}

func (ts *mockTokenExchangeServer) SetAccessCheckReturn(devLicense string, access bool) {
	ts.mutex.Lock()
	defer ts.mutex.Unlock()
	ts.accessCheckReturn[devLicense] = access
}

// GetAddress returns the full address of the server
func (ts *mockTokenExchangeServer) URL() string {
	return fmt.Sprintf("localhost:%d", ts.port)
}

func (ts *mockTokenExchangeServer) AccessCheck(ctx context.Context, req *pb.AccessCheckRequest) (*pb.AccessCheckResponse, error) {
	ts.mutex.Lock()
	defer ts.mutex.Unlock()

	access, ok := ts.accessCheckReturn[req.GetGrantee()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "devl")
	}

	return &pb.AccessCheckResponse{
		HasAccess: access,
	}, nil
}
