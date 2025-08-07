package e2e_test

import (
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	"github.com/DIMO-Network/vehicle-triggers-api/tests"
	"github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog"
)

var (
	testServices        *TestServices
	globalTestContainer sync.Once
	srvcLock            sync.Mutex
)

type TestServices struct {
	Identity      *mockIdentityServer
	Auth          *mockAuthServer
	Kafka         *mockKafkaServer
	Postgres      *tests.TestContainer
	TokenExchange *mockTokenExchangeServer
	refs          atomic.Int64
	Settings      config.Settings
}

func GetTestServices(t *testing.T) *TestServices {
	t.Helper()
	srvcLock.Lock()
	globalTestContainer.Do(func() {
		logger := zerolog.New(os.Stdout).Level(zerolog.WarnLevel)
		zerolog.DefaultContextLogger = &logger
		settings := config.Settings{
			Port:    8080,
			MonPort: 9090,
			// TokenExchangeIssuer:          "http://127.0.0.1:3003",
			VehicleNFTAddress:   common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
			DIMORegistryChainID: 137,
		}

		// Setup services
		testServices = &TestServices{
			Settings: settings,
		}
		var wg sync.WaitGroup
		waitForSetup(t, &wg, func(t *testing.T) {
			identity := setupIdentityServer(t)
			testServices.Identity = identity
			testServices.Settings.IdentityAPIURL = identity.URL()
		})
		waitForSetup(t, &wg, func(t *testing.T) {
			auth := setupAuthServer(t)
			testServices.Auth = auth
			// testServices.Settings.TokenExchangeJWTKeySetURL = auth.URL() + "/keys"
		})
		waitForSetup(t, &wg, func(t *testing.T) {
			kafka := setupMockKafkaServer(t)
			testServices.Kafka = kafka
			testServices.Settings.KafkaBrokers = kafka.GetBrokerAddress(t)
			testServices.Settings.DeviceSignalsTopic = "test.default.signals.topic"
		})
		waitForSetup(t, &wg, func(t *testing.T) {
			db := tests.SetupTestContainer(t)
			testServices.Postgres = db
			testServices.Settings.DB = db.Settings
		})
		waitForSetup(t, &wg, func(t *testing.T) {
			tokenExchange := NewTestTokenExchangeAPI(t)
			testServices.Settings.TokenExchangeGRPCAddr = tokenExchange.URL()
			testServices.TokenExchange = tokenExchange
		})
		wg.Wait()
	})
	srvcLock.Unlock()
	testServices.TeardownIfLastTest(t)
	testServices.Postgres.TeardownIfLastTest(t)
	return testServices
}

func (tc *TestServices) TeardownIfLastTest(t *testing.T) {
	tc.refs.Add(1)
	t.Cleanup(func() {
		refs := tc.refs.Add(-1)
		if refs != 0 {
			return
		}
		tc.Identity.Close()
		tc.Auth.Close()
		if err := tc.Kafka.Close(); err != nil {
			t.Logf("Error closing Kafka: %v", err)
		}
		tc.TokenExchange.Close()
		// reset the onceSetup to allow the next test to run if this one is closed
		globalTestContainer = sync.Once{}
	})
}

func waitForSetup(t *testing.T, wg *sync.WaitGroup, setup func(*testing.T)) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		setup(t)
	}()
}
