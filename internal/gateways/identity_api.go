package gateways

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog"
)

// IdentityVehicle represents a vehicle record returned by identity-api.
type IdentityVehicle struct {
	TokenID json.Number `json:"tokenId"`
}

// IdentityAPI defines methods used to interact with identity-api.
type IdentityAPI interface {
	VerifyDeveloperLicense(clientID string) (bool, int, error)
	HasVehiclePermissions(vehicleTokenID string, devLicense []byte) (bool, error)
	GetSharedVehicles(devLicense []byte) ([]IdentityVehicle, error)
}

type cachedPerm struct {
	allowed bool
	expires time.Time
}

type identityAPIService struct {
	url        string
	httpClient *http.Client
	logger     zerolog.Logger
	mu         sync.Mutex
	permCache  map[string]cachedPerm
}

// NewIdentityAPIService creates a new IdentityAPI implementation.
func NewIdentityAPIService(url string, logger zerolog.Logger) IdentityAPI {
	return &identityAPIService{
		url:        url,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		logger:     logger,
		permCache:  make(map[string]cachedPerm),
	}
}

func (i *identityAPIService) VerifyDeveloperLicense(clientID string) (bool, int, error) {
	query := `query($clientId: Address!) {\n  developerLicense(by: { clientId: $clientId }) {\n    clientId\n    tokenId\n  }\n}`
	payload := map[string]any{
		"query":     query,
		"variables": map[string]any{"clientId": clientID},
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		i.logger.Error().Err(err).Msg("failed to marshal GraphQL payload")
		return false, 0, err
	}

	resp, err := i.httpClient.Post(i.url, "application/json", bytes.NewBuffer(payloadBytes))
	if err != nil {
		i.logger.Error().Err(err).Msg("failed to POST to identity API")
		return false, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		i.logger.Error().Err(err).Msg("failed to read response body")
		return false, 0, err
	}

	i.logger.Debug().Int("status_code", resp.StatusCode).Str("response_body", string(body)).Msg("identity API response")
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		errMsg := fmt.Sprintf("identity API returned non-2xx status: %d", resp.StatusCode)
		i.logger.Error().Msg(errMsg)
		return false, 0, errors.New(errMsg)
	}

	var result struct {
		Data *struct {
			DeveloperLicense *struct {
				ClientID string `json:"clientId"`
				TokenID  int    `json:"tokenId"`
			} `json:"developerLicense"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		i.logger.Error().Err(err).Msg("failed to unmarshal JSON response")
		return false, 0, err
	}
	if len(result.Errors) > 0 {
		i.logger.Error().Msgf("GraphQL errors: %+v", result.Errors)
		return false, 0, nil
	}
	if result.Data == nil || result.Data.DeveloperLicense == nil {
		i.logger.Error().Msg("no developerLicense data found in response")
		return false, 0, nil
	}
	tokenID := result.Data.DeveloperLicense.TokenID
	return true, tokenID, nil
}

func (i *identityAPIService) HasVehiclePermissions(vehicleTokenID string, devLicense []byte) (bool, error) {
	if i.url == "" {
		return false, fmt.Errorf("identity API URL not configured")
	}

	key := vehicleTokenID + string(devLicense)
	i.mu.Lock()
	if c, ok := i.permCache[key]; ok && time.Now().Before(c.expires) {
		allowed := c.allowed
		i.mu.Unlock()
		return allowed, nil
	}
	i.mu.Unlock()

	query := `query ($tokenId: Int!) {\n  vehicle(tokenId: $tokenId) {\n    sacds(first:100) {\n      nodes {\n        grantee\n        permissions\n      }\n    }\n  }\n}`
	payload := map[string]any{
		"query":     query,
		"variables": map[string]any{"tokenId": vehicleTokenID},
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		i.logger.Error().Err(err).Msg("failed to marshal GraphQL payload")
		return false, err
	}
	resp, err := i.httpClient.Post(i.url, "application/json", bytes.NewBuffer(payloadBytes))
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return false, fmt.Errorf("identity API returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}

	var result struct {
		Data struct {
			Vehicle struct {
				Sacds []struct {
					Grantee     string `json:"grantee"`
					Permissions string `json:"permissions"`
				} `json:"sacds"`
			} `json:"vehicle"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		i.logger.Error().Err(err).Msg("failed to unmarshal GraphQL response")
		return false, err
	}
	if len(result.Errors) > 0 {
		i.logger.Error().Msgf("identity GraphQL errors: %+v", result.Errors)
		return false, fmt.Errorf("identity API error")
	}

	target := common.BytesToAddress(devLicense).Hex()
	allowed := false
	for _, item := range result.Data.Vehicle.Sacds {
		if strings.EqualFold(item.Grantee, target) && hasPrivBits(item.Permissions, []uint{1, 3, 4}) {
			allowed = true
			break
		}
	}

	i.mu.Lock()
	i.permCache[key] = cachedPerm{allowed: allowed, expires: time.Now().Add(time.Minute)}
	i.mu.Unlock()

	return allowed, nil
}

func (i *identityAPIService) GetSharedVehicles(devLicense []byte) ([]IdentityVehicle, error) {
	ethAddress := common.BytesToAddress(devLicense).Hex()
	if i.url == "" {
		return nil, fmt.Errorf("identity API URL not configured")
	}

	query := `query($eth: Address!) { vehicles(first: 50, filterBy: { privileged: $eth }) { nodes { tokenId } } }`
	payload := map[string]any{
		"query":     query,
		"variables": map[string]any{"eth": ethAddress},
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		i.logger.Error().Err(err).Msg("failed to marshal GraphQL payload")
		return nil, err
	}
	resp, err := i.httpClient.Post(i.url, "application/json", bytes.NewBuffer(payloadBytes))
	if err != nil {
		i.logger.Error().Err(err).Msg("failed to POST to identity API")
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		i.logger.Error().Err(err).Msg("failed to read response body")
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("identity API returned non-2xx status: %d", resp.StatusCode)
	}

	var result struct {
		Data struct {
			Vehicles struct {
				Nodes []IdentityVehicle `json:"nodes"`
			} `json:"vehicles"`
		} `json:"data"`
		Errors []struct {
			Message string   `json:"message"`
			Path    []string `json:"path"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		i.logger.Error().Err(err).Msg("failed to unmarshal JSON response")
		return nil, err
	}
	if len(result.Errors) > 0 {
		i.logger.Error().Msgf("GraphQL errors: %+v", result.Errors)
		return nil, errors.New("GraphQL errors occurred")
	}
	return result.Data.Vehicles.Nodes, nil
}

func hasPrivBits(permHex string, bits []uint) bool {
	permHex = strings.TrimPrefix(permHex, "0x")
	n := new(big.Int)
	n.SetString(permHex, 16)
	for _, b := range bits {
		if n.Bit(int(b)) == 0 {
			return false
		}
	}
	return true
}
