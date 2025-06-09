package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog"
)

// HasVehiclePermissions checks if the given developer license has privileges 1,3,4 for the vehicle.
func HasVehiclePermissions(identityAPIURL, vehicleTokenID string, devLicense []byte, logger zerolog.Logger) (bool, error) {
	if identityAPIURL == "" {
		return false, fmt.Errorf("identity API URL not configured")
	}

	query := `query($tokenId: Int!) {
  vehicle(tokenId: $tokenId) {
    sacds {
      edges {
        node {
          grantee
          permissions
        }
      }
    }
  }
}`

	payload := map[string]interface{}{
		"query": query,
		"variables": map[string]interface{}{
			"tokenId": vehicleTokenID,
		},
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		logger.Error().Err(err).Msg("failed to marshal GraphQL payload")
		return false, err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(identityAPIURL, "application/json", bytes.NewBuffer(payloadBytes))
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
		logger.Error().Err(err).Msg("failed to unmarshal GraphQL response")
		return false, err
	}

	if len(result.Errors) > 0 {
		logger.Error().Msgf("identity GraphQL errors: %+v", result.Errors)
		return false, fmt.Errorf("identity API error")
	}

	target := common.BytesToAddress(devLicense).Hex()
	for _, item := range result.Data.Vehicle.Sacds {
		if strings.EqualFold(item.Grantee, target) {
			if hasPrivBits(item.Permissions, []uint{1, 3, 4}) {
				return true, nil
			}
		}
	}
	return false, nil
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
