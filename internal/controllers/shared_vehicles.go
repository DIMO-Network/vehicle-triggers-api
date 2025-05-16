package controllers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ethereum/go-ethereum/common"
	"io"
	"net/http"

	"github.com/rs/zerolog"
)

type Vehicle struct {
	TokenID json.Number `json:"tokenId"`
}

func GetSharedVehicles(identityAPIURL string, devLicense []byte, logger zerolog.Logger) ([]Vehicle, error) {
	ethAddress := common.BytesToAddress(devLicense).Hex()

	if identityAPIURL == "" {
		return nil, fmt.Errorf("identity API URL not configured")
	}

	query := `
		query($eth: Address!) {
			vehicles(first: 50, filterBy: { privileged: $eth }) {
				nodes {
					tokenId
				}
			}
		}`
	payload := map[string]interface{}{
		"query": query,
		"variables": map[string]interface{}{
			"eth": ethAddress,
		},
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		logger.Error().Err(err).Msg("failed to marshal GraphQL payload")
		return nil, err
	}

	resp, err := http.Post(identityAPIURL, "application/json", bytes.NewBuffer(payloadBytes))
	if err != nil {
		logger.Error().Err(err).Msg("failed to POST to identity API")
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Error().Err(err).Msg("failed to read response body")
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("identity API returned non-2xx status: %d", resp.StatusCode)
	}

	var result struct {
		Data struct {
			Vehicles struct {
				Nodes []Vehicle `json:"nodes"`
			} `json:"vehicles"`
		} `json:"data"`
		Errors []struct {
			Message string   `json:"message"`
			Path    []string `json:"path"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		logger.Error().Err(err).Msg("failed to unmarshal JSON response")
		return nil, err
	}

	if len(result.Errors) > 0 {
		logger.Error().Msgf("GraphQL errors: %+v", result.Errors)
		return nil, errors.New("GraphQL errors occurred")
	}

	return result.Data.Vehicles.Nodes, nil
}
