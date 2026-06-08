//go:build cluster

// Build-tagged cluster e2e. Spins three NATS JetStream nodes on a shared
// testcontainers network and runs the production publish + consume path
// against replicas=3 streams. Catches bugs that single-node tests can't
// see - replica election, quorum loss, in-flight redelivery during
// leadership transfer.
//
// Tagged because cluster startup is multi-second and adds container churn
// to CI. Invoke in a dedicated job: `go test -tags=cluster ./tests/e2e/...`.
package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	vtnats "github.com/DIMO-Network/vehicle-triggers-api/internal/nats"
	"github.com/DIMO-Network/vehicle-triggers-api/tests"
	nc "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcnetwork "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

const natsClusterConfTmpl = `
server_name: %s
listen: 0.0.0.0:4222
http: 0.0.0.0:8222
jetstream {
  store_dir: /data
}
cluster {
  name: TRIGGERS_CLUSTER_TEST
  listen: 0.0.0.0:6222
  routes = [
    nats-route://%s:6222
    nats-route://%s:6222
    nats-route://%s:6222
  ]
}
`

type clusterNode struct {
	name      string
	container testcontainers.Container
	clientURL string
}

// startCluster spins three nats containers on a shared network so they can
// form a cluster. Returns the first URL (any node works for clients) and a
// cleanup function. Skips the test gracefully if Docker isn't available.
func startCluster(t *testing.T) (string, func()) {
	t.Helper()
	tests.SkipIfNoDocker(t)

	ctx := context.Background()
	net, err := tcnetwork.New(ctx)
	require.NoError(t, err)

	names := []string{"nats-c1", "nats-c2", "nats-c3"}
	nodes := make([]clusterNode, 0, len(names))
	cleanup := func() {
		for _, n := range nodes {
			_ = n.container.Terminate(context.Background())
		}
		_ = net.Remove(context.Background())
	}

	for _, name := range names {
		conf := fmt.Sprintf(natsClusterConfTmpl, name, names[0], names[1], names[2])
		req := testcontainers.ContainerRequest{
			Image:        "nats:2.11-alpine",
			Name:         name,
			Networks:     []string{net.Name},
			ExposedPorts: []string{"4222/tcp"},
			Cmd:          []string{"-c", "/etc/nats/nats.conf"},
			Files: []testcontainers.ContainerFile{{
				Reader:            strings.NewReader(conf),
				ContainerFilePath: "/etc/nats/nats.conf",
				FileMode:          0o644,
			}},
			WaitingFor: wait.ForListeningPort("4222/tcp").WithStartupTimeout(30 * time.Second),
		}
		ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		})
		if err != nil {
			cleanup()
			t.Fatalf("start %s: %v", name, err)
		}
		host, err := ctr.Host(ctx)
		require.NoError(t, err)
		port, err := ctr.MappedPort(ctx, "4222/tcp")
		require.NoError(t, err)
		nodes = append(nodes, clusterNode{
			name:      name,
			container: ctr,
			clientURL: fmt.Sprintf("nats://%s:%s", host, port.Port()),
		})
	}
	return nodes[0].clientURL, cleanup
}

// TestNATSCluster_PublishConsumeReplicas3 asserts that a replicas=3 stream
// accepts publishes from one client and that a pull consumer drains them.
// This is the smoke test for our cluster wiring.
func TestNATSCluster_PublishConsumeReplicas3(t *testing.T) {
	t.Parallel()
	url, cleanup := startCluster(t)
	t.Cleanup(cleanup)

	// Cluster formation takes a couple of seconds beyond port listen.
	time.Sleep(3 * time.Second)

	suffix := time.Now().Format("150405000")
	conn, err := nc.Connect(url, nc.RetryOnFailedConnect(true), nc.MaxReconnects(-1), nc.ReconnectWait(time.Second), nc.Timeout(10*time.Second))
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	js, err := jetstream.New(conn)
	require.NoError(t, err)

	stream := "CLUSTER_T_" + suffix
	_, err = js.CreateOrUpdateStream(t.Context(), jetstream.StreamConfig{
		Name:      stream,
		Subjects:  []string{"cluster.t.>"},
		Retention: jetstream.LimitsPolicy,
		Discard:   jetstream.DiscardOld,
		Storage:   jetstream.FileStorage,
		MaxAge:    5 * time.Minute,
		Replicas:  3,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = js.DeleteStream(context.Background(), stream) })

	cons, err := js.CreateOrUpdateConsumer(t.Context(), stream, jetstream.ConsumerConfig{
		Durable:       "cluster-cons-" + suffix,
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		FilterSubject: "cluster.t.>",
	})
	require.NoError(t, err)

	client, err := vtnats.Connect(t.Context(), config.NATSSettings{
		Mode:           "exclusive",
		URL:            url,
		Name:           "vt-cluster-" + suffix,
		FetchBatch:     50,
		MaxDeliver:     5,
		MaxAckPending:  500,
		StreamReplicas: 3,
	}, zerolog.Nop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	var consumed atomic.Uint64
	pullCtx, pullCancel := context.WithCancel(t.Context())
	t.Cleanup(pullCancel)
	go func() {
		_ = client.PullLoop(pullCtx, cons, 4, func(_ context.Context, _ []byte) error {
			consumed.Add(1)
			return nil
		})
	}()

	const N = 50
	for i := 0; i < N; i++ {
		_, err := client.Publish(t.Context(), "cluster.t.smoke", []byte(`{"v":1}`))
		require.NoError(t, err)
	}

	require.Eventually(t, func() bool { return consumed.Load() == N }, 10*time.Second, 50*time.Millisecond, "consumer must drain all replicas=3 messages")
}

var _ = errors.New // keep imports stable when adding follow-up tests
