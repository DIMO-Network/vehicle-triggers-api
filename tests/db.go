package tests

import (
	"context"
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/DIMO-Network/shared/pkg/db"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/migrations"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

type TestContainer struct {
	container testcontainers.Container
	DB        *sql.DB
	Settings  db.Settings
	onceSetup sync.Once
	refs      atomic.Int64
}

var globalTestContainer TestContainer

func (tc *TestContainer) TeardownIfLastTest(t *testing.T) {
	tc.refs.Add(1)
	t.Cleanup(func() {
		refs := tc.refs.Add(-1)
		if refs != 0 {
			return
		}
		tc.Close()
		// reset the onceSetup to allow the next test to run if this one is closed
		globalTestContainer.onceSetup = sync.Once{}
	})
}

func (tc *TestContainer) Close() {
	_ = tc.container.Terminate(context.Background())
	_ = tc.DB.Close()
}

func SetupTestContainer(t *testing.T) *TestContainer {
	globalTestContainer.onceSetup.Do(func() {
		ctx := context.Background()
		var err error
		globalTestContainer.container, err = postgres.Run(ctx,
			"postgres:15",
			postgres.WithDatabase(migrations.SchemaName),
			postgres.WithUsername("postgres"),
			postgres.WithPassword("postgres"),
			postgres.BasicWaitStrategies(),
		)
		require.NoError(t, err)

		host, err := globalTestContainer.container.Host(ctx)
		require.NoError(t, err)
		port, err := globalTestContainer.container.MappedPort(ctx, "5432")
		require.NoError(t, err)

		globalTestContainer.Settings = db.Settings{
			Host:     host,
			Port:     port.Port(),
			User:     "postgres",
			Password: "postgres",
			Name:     migrations.SchemaName,
			SSLMode:  "disable",
		}

		globalTestContainer.DB, err = sql.Open("postgres", globalTestContainer.Settings.BuildConnectionString(true))
		require.NoError(t, err)

		err = migrations.RunGoose(ctx, []string{"up"}, globalTestContainer.Settings)
		require.NoError(t, err)
	})
	globalTestContainer.TeardownIfLastTest(t)
	return &globalTestContainer
}
