package auth

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/DIMO-Network/shared/db"
	"github.com/DIMO-Network/vehicle-events-api/internal/db/models"
	"github.com/rs/zerolog"
	"github.com/volatiletech/sqlboiler/v4/boil"
	"github.com/volatiletech/sqlboiler/v4/queries/qm"
)

// VerifyDeveloperLicense sends a GraphQL query to the identity API to check if the given clientID exists.
func VerifyDeveloperLicense(clientID string, identityAPIURL string, logger zerolog.Logger) (bool, int, error) {
	query := `query($clientId: Address!) {
  developerLicense(by: { clientId: $clientId }) {
    clientId
    tokenId
  }
}`

	payload := map[string]interface{}{
		"query": query,
		"variables": map[string]interface{}{
			"clientId": clientID,
		},
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		logger.Error().Err(err).Msg("failed to marshal GraphQL payload")
		return false, 0, err
	}

	resp, err := http.Post(identityAPIURL, "application/json", bytes.NewBuffer(payloadBytes))
	if err != nil {
		logger.Error().Err(err).Msg("failed to POST to identity API")
		return false, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Error().Err(err).Msg("failed to read response body")
		return false, 0, err
	}

	logger.Debug().
		Int("status_code", resp.StatusCode).
		Str("response_body", string(body)).
		Msg("identity API response")

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		errMsg := fmt.Sprintf("identity API returned non-2xx status: %d", resp.StatusCode)
		logger.Error().Msg(errMsg)
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
			Message    string   `json:"message"`
			Path       []string `json:"path"`
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		logger.Error().Err(err).Msg("failed to unmarshal JSON response")
		return false, 0, err
	}

	if len(result.Errors) > 0 {
		logger.Error().Msgf("GraphQL errors: %+v", result.Errors)
		return false, 0, nil
	}

	if result.Data == nil || result.Data.DeveloperLicense == nil {
		logger.Error().Msg("no developerLicense data found in response")
		return false, 0, nil
	}

	tokenId := result.Data.DeveloperLicense.TokenID
	logger.Debug().Msgf("Developer license verified: %s, tokenId: %d", result.Data.DeveloperLicense.ClientID, tokenId)
	return true, tokenId, nil
}

// EnsureDeveloperLicenseExists verifies the clientID against identity API.
// If valid and not already in our DB, it inserts a new DeveloperLicense record,
// storing the JWT's Ethereum address (decoded) in LicenseAddress and the tokenId (from the query)
// as DeveloperID.
func EnsureDeveloperLicenseExists(clientID, identityAPIURL string, store db.Store, logger zerolog.Logger) error {
	valid, tokenId, err := VerifyDeveloperLicense(clientID, identityAPIURL, logger)
	if err != nil {
		logger.Error().Err(err).Msg("failed to verify developer license on identity API")
		return err
	}
	if !valid {
		return errors.New("invalid developer license")
	}

	hexStr := strings.TrimPrefix(clientID, "0x")
	licenseBytes, err := hex.DecodeString(hexStr)
	if err != nil {
		logger.Error().Err(err).Msgf("failed to decode client id: %s", clientID)
		return err
	}

	existing, err := models.DeveloperLicenses(
		qm.Where("developer_id = ?", fmt.Sprintf("%d", tokenId)),
	).One(context.Background(), store.DBS().Reader)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		logger.Error().Err(err).Msg("error querying developer license")
		return err
	}
	if existing != nil {
		return nil
	}

	dl := models.DeveloperLicense{
		LicenseAddress: licenseBytes,
		DeveloperID:    fmt.Sprintf("%d", tokenId),
		Status:         "Active",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	if err := dl.Insert(context.Background(), store.DBS().Writer, boil.Infer()); err != nil {
		logger.Error().Err(err).Msgf("error inserting developer license %s", clientID)
		return err
	}

	logger.Info().Msgf("inserted new developer license %s with tokenId %d", clientID, tokenId)
	return nil
}
