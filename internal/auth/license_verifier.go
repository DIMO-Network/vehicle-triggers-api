package auth

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
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
func VerifyDeveloperLicense(clientID string, identityAPIURL string, logger zerolog.Logger) (bool, error) {
	query := `query($clientId: String!) {
		developerLicenses(first: 1, filter: {clientId: {equalTo: $clientId}}) {
			nodes {
				clientId
			}
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
		return false, err
	}

	resp, err := http.Post(identityAPIURL, "application/json", bytes.NewBuffer(payloadBytes))
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}

	var result struct {
		Data struct {
			DeveloperLicenses struct {
				Nodes []struct {
					ClientID string `json:"clientId"`
				} `json:"nodes"`
			} `json:"developerLicenses"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return false, err
	}

	// If at least one node is returned, the clientId is valid.
	if len(result.Data.DeveloperLicenses.Nodes) > 0 {
		return true, nil
	}
	return false, nil
}

// EnsureDeveloperLicenseExists verifies the clientID against identity API.
// If valid and not already in our DB, it inserts a new DeveloperLicense record.
func EnsureDeveloperLicenseExists(clientID, identityAPIURL string, store db.Store, logger zerolog.Logger) error {
	// Verify that the clientID exists on identity API.
	valid, err := VerifyDeveloperLicense(clientID, identityAPIURL, logger)
	if err != nil {
		logger.Error().Err(err).Msg("failed to verify developer license on identity API")
		return err
	}
	if !valid {
		return errors.New("invalid developer license")
	}

	// Remove the "0x" prefix (if present) and decode the hex string.
	hexStr := strings.TrimPrefix(clientID, "0x")
	licenseBytes, err := hex.DecodeString(hexStr)
	if err != nil {
		logger.Error().Err(err).Msgf("failed to decode client id: %s", clientID)
		return err
	}

	existing, err := models.DeveloperLicenses(
		qm.Where("developer_id = ?", clientID),
	).One(context.Background(), store.DBS().Reader)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		logger.Error().Err(err).Msg("error querying developer license")
		return err
	}
	if existing != nil {
		// License already exists.
		return nil
	}

	// Insert a new DeveloperLicense record.
	dl := models.DeveloperLicense{
		LicenseAddress: licenseBytes,
		DeveloperID:    clientID,
		Status:         "Active",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	if err := dl.Insert(context.Background(), store.DBS().Writer, boil.Infer()); err != nil {
		logger.Error().Err(err).Msgf("error inserting developer license %s", clientID)
		return err
	}

	logger.Info().Msgf("inserted new developer license %s", clientID)
	return nil
}
