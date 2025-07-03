package gateways

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/DIMO-Network/shared/pkg/http"
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
	httpClient http.ClientWrapper
	logger     zerolog.Logger
	mu         sync.Mutex
	permCache  map[string]cachedPerm
}

// NewIdentityAPIService creates a new IdentityAPI implementation.
func NewIdentityAPIService(url string, logger zerolog.Logger) IdentityAPI {
	httpClient, _ := http.NewClientWrapper(url, "", 10*time.Second, nil, true)
	return &identityAPIService{
		httpClient: httpClient,
		logger:     logger,
		permCache:  make(map[string]cachedPerm),
	}
}

func (i *identityAPIService) VerifyDeveloperLicense(clientID string) (bool, int, error) {
	query := fmt.Sprintf(`{
		developerLicense(by: { clientId: "%s" }) {
			clientId
			tokenId
		}
	}`, clientID)

	var resp struct {
		Data struct {
			DeveloperLicense struct {
				ClientID string `json:"clientId"`
				TokenID  int    `json:"tokenId"`
			} `json:"developerLicense"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}

	if err := i.httpClient.GraphQLQuery("", query, &resp); err != nil {
		return false, 0, err
	}
	if len(resp.Errors) > 0 || resp.Data.DeveloperLicense.ClientID == "" {
		return false, 0, nil
	}
	return true, resp.Data.DeveloperLicense.TokenID, nil
}

func (i *identityAPIService) HasVehiclePermissions(vehicleTokenID string, devLicense []byte) (bool, error) {
	key := vehicleTokenID + string(devLicense)
	i.mu.Lock()
	if c, ok := i.permCache[key]; ok && time.Now().Before(c.expires) {
		allowed := c.allowed
		i.mu.Unlock()
		return allowed, nil
	}
	i.mu.Unlock()

	query := fmt.Sprintf(`{
		vehicle(tokenId: %s) {
			sacds(first:100) {
				nodes {
					grantee
					permissions
				}
			}
		}
	}`, vehicleTokenID)

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

	if err := i.httpClient.GraphQLQuery("", query, &result); err != nil {
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
	query := fmt.Sprintf(`{
		vehicles(first: 50, filterBy: { privileged: "%s" }) {
			nodes {
				tokenId
			}
		}
	}`, ethAddress)

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

	if err := i.httpClient.GraphQLQuery("", query, &result); err != nil {
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
