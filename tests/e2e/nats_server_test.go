package e2e_test

import (
	"context"
	"testing"

	"github.com/DIMO-Network/vehicle-triggers-api/tests"
	"github.com/testcontainers/testcontainers-go/modules/nats"
)

type mockNATSServer struct {
	container *nats.NATSContainer
	url       string
}

// setupMockNATSServer launches a JetStream-enabled NATS broker in a container
// and returns its client URL. Skips the test cleanly when Docker is not
// available so unit-style runs stay green.
func setupMockNATSServer(t *testing.T) *mockNATSServer {
	t.Helper()
	tests.SkipIfNoDocker(t)

	ctx := context.Background()
	// The default Run command already includes "-js" so we don't need to pass
	// extra args. Passing them via WithArgument/CmdArgs replaces the default
	// command and we'd lose JetStream.
	container, err := nats.Run(ctx, "nats:2.11-alpine")
	if err != nil {
		t.Fatalf("Failed to start NATS container: %v", err)
	}

	url, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("Failed to read NATS connection string: %v", err)
	}

	return &mockNATSServer{container: container, url: url}
}

func (m *mockNATSServer) URL() string { return m.url }

func (m *mockNATSServer) Close() error {
	if m == nil || m.container == nil {
		return nil
	}
	return m.container.Terminate(context.Background())
}
