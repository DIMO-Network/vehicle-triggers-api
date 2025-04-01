package controllers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/rs/zerolog"
)

type Vehicle struct {
	TokenID string `json:"tokenId"`
}

func GetSharedVehicles(devLicense []byte, logger zerolog.Logger) ([]Vehicle, error) {
	ethAddress := fmt.Sprintf("0x%x", devLicense)

	identityAPIURL := settings.IdentityAPIURL

	query := `
		query($eth: String!) {
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
		errMsg := fmt.Sprintf("identity API returned non-2xx status: %d", resp.StatusCode)
		logger.Error().Msg(errMsg)
		return nil, errors.New(errMsg)
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
