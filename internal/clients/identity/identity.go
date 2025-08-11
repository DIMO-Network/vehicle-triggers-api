package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"

	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	"github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog"
)

// Client for the Identity API
type Client struct {
	identityAPIURL         string
	logger                 zerolog.Logger
	httpClient             *http.Client
	vehicleContractAddress common.Address
	chainID                uint64
}

// New creates a new Client.
func New(settings *config.Settings, logger zerolog.Logger) (*Client, error) {
	parsedURL, err := url.Parse(settings.IdentityAPIURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse identity API URL: %w", err)
	}
	return &Client{
		identityAPIURL:         parsedURL.String(),
		logger:                 logger,
		httpClient:             http.DefaultClient,
		vehicleContractAddress: settings.VehicleNFTAddress,
		chainID:                settings.DIMORegistryChainID,
	}, nil
}

func (c *Client) VerifyDeveloperLicense(ctx context.Context, clientID string) (bool, int, error) {
	query := `
		query($clientId: Address){
		developerLicense(by: { clientId: $clientId }) {
			clientId
			tokenId
		}
	}`

	bodyBytes, err := c.SendRequest(ctx, query, map[string]any{
		"clientId": clientID,
	})
	if err != nil {
		return false, 0, richerrors.Error{
			Code:        http.StatusInternalServerError,
			ExternalMsg: "Failed to verify developer license",
			Err:         err,
		}
	}

	var resp IdentityResponse[DeveloperLicenseResponse]
	if err := json.Unmarshal(bodyBytes, &resp); err != nil {
		return false, 0, fmt.Errorf("failed to unmarshal GraphQL response: %w", err)
	}
	if len(resp.Errors) > 0 || resp.Data.DeveloperLicense.ClientID == "" {
		return false, 0, nil
	}
	return true, resp.Data.DeveloperLicense.TokenID, nil
}

func (c *Client) GetSharedVehicles(ctx context.Context, devLicense []byte) ([]*big.Int, error) {
	ethAddress := common.BytesToAddress(devLicense).Hex()
	query := `
		query($clientId: Address){
		vehicles(first: 50, filterBy: { privileged: $clientId }) {
			nodes {
				tokenDID
			}
		}
	}`

	bodyBytes, err := c.SendRequest(ctx, query, map[string]any{
		"clientId": ethAddress,
	})
	if err != nil {
		return nil, richerrors.Error{
			Code:        http.StatusInternalServerError,
			ExternalMsg: "Failed to get shared vehicles",
			Err:         err,
		}
	}
	var result IdentityResponse[SharedVehiclesResponse]
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, richerrors.Error{
			Code:        http.StatusInternalServerError,
			ExternalMsg: "Failed to get shared vehicles",
			Err:         fmt.Errorf("failed to unmarshal GraphQL response: %w", err),
		}
	}
	if len(result.Errors) > 0 {
		return nil, richerrors.Error{
			Code:        http.StatusInternalServerError,
			ExternalMsg: "Failed to get shared vehicles",
			Err:         errors.New("GraphQL errors occurred"),
		}
	}
	tokenIDs := make([]*big.Int, len(result.Data.Vehicles.Nodes))
	for i, node := range result.Data.Vehicles.Nodes {
		if node.TokenDID.ContractAddress != c.vehicleContractAddress || node.TokenDID.ChainID != c.chainID {
			return nil, richerrors.Error{
				Code:        http.StatusInternalServerError,
				ExternalMsg: "Failed to get shared vehicles",
				Err:         errors.New("vehicle contract address or chain ID mismatch"),
			}
		}
		tokenIDs[i] = node.TokenDID.TokenID
	}
	return tokenIDs, nil
}

func (c *Client) SendRequest(ctx context.Context, query string, variables map[string]any) ([]byte, error) {
	requestBody := map[string]any{
		"query":     query,
		"variables": variables,
	}

	reqBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal GraphQL request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.identityAPIURL, bytes.NewBuffer(reqBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create GraphQL request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send GraphQL request: %w", err)
	}
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read GraphQL response body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("non-200 status code: %s", string(bodyBytes))
	}
	return bodyBytes, nil
}
