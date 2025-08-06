package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog"
)

// Client for the Identity API
type Client struct {
	identityAPIURL string
	logger         zerolog.Logger
	httpClient     *http.Client
}

// New creates a new Client.
func New(identityAPIURL string, logger zerolog.Logger) (*Client, error) {
	parsedURL, err := url.Parse(identityAPIURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse identity API URL: %w", err)
	}
	return &Client{
		identityAPIURL: parsedURL.String(),
		logger:         logger,
		httpClient:     http.DefaultClient,
	}, nil
}

func (i *Client) VerifyDeveloperLicense(ctx context.Context, clientID string) (bool, int, error) {
	query := `
		query($clientId: Address){
		developerLicense(by: { clientId: $clientId }) {
			clientId
			tokenId
		}
	}`

	bodyBytes, err := i.SendRequest(ctx, query, map[string]any{
		"clientId": clientID,
	})
	if err != nil {
		return false, 0, err
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

func (i *Client) GetSharedVehicles(ctx context.Context, devLicense []byte) ([]cloudevent.ERC721DID, error) {
	ethAddress := common.BytesToAddress(devLicense).Hex()
	query := `
		query($clientId: Address){
		vehicles(first: 50, filterBy: { privileged: $clientId }) {
			nodes {
				tokenDID
			}
		}
	}`

	bodyBytes, err := i.SendRequest(ctx, query, map[string]any{
		"clientId": ethAddress,
	})
	if err != nil {
		return nil, err
	}
	var result IdentityResponse[SharedVehiclesResponse]
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal GraphQL response: %w", err)
	}
	if len(result.Errors) > 0 {
		return nil, richerrors.Error{
			Code:        http.StatusInternalServerError,
			ExternalMsg: "Failed to get shared vehicles",
			Err:         errors.New("GraphQL errors occurred"),
		}
	}
	dids := make([]cloudevent.ERC721DID, len(result.Data.Vehicles.Nodes))
	for i, node := range result.Data.Vehicles.Nodes {
		dids[i] = node.TokenDID
	}
	return dids, nil
}

func (i *Client) SendRequest(ctx context.Context, query string, variables map[string]any) ([]byte, error) {
	requestBody := map[string]any{
		"query":     query,
		"variables": variables,
	}

	reqBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal GraphQL request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.identityAPIURL, bytes.NewBuffer(reqBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create GraphQL request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := i.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send GraphQL request: %w", err)
	}
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read GraphQL response body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get shared vehicles: %s", string(bodyBytes))
	}
	return bodyBytes, nil
}
