package e2e_test

import (
	"net/http"
	"os"
	"testing"

	"github.com/DIMO-Network/vehicle-triggers-api/internal/app"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestBasic(t *testing.T) {
	t.Parallel()
	tc := GetTestServices(t)
	fiberApp, err := app.CreateServers(t.Context(), &tc.Settings, zerolog.New(os.Stdout))
	if err != nil {
		require.NoError(t, err)
	}
	req, err := http.NewRequestWithContext(t.Context(), "GET", "/health", nil)
	if err != nil {
		require.NoError(t, err)
	}

	resp, err := fiberApp.Test(req)
	if err != nil {
		require.NoError(t, err)
	}

	require.Equal(t, http.StatusOK, resp.StatusCode)
}
