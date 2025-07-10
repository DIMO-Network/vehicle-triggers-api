package auth

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/DIMO-Network/vehicle-events-api/internal/gateways"
	"strings"
	"time"

	"github.com/DIMO-Network/shared/pkg/db"
	"github.com/DIMO-Network/vehicle-events-api/internal/db/models"
	"github.com/rs/zerolog"
	"github.com/volatiletech/sqlboiler/v4/boil"
	"github.com/volatiletech/sqlboiler/v4/queries/qm"
)

// EnsureDeveloperLicenseExists verifies the clientID against identity API.
// If valid and not already in our DB, it inserts a new DeveloperLicense record.
func EnsureDeveloperLicenseExists(clientID string, api gateways.IdentityAPI, store db.Store, logger zerolog.Logger) error {
	valid, tokenID, err := api.VerifyDeveloperLicense(clientID)
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
		qm.Where("developer_id = ?", fmt.Sprintf("%d", tokenID)),
	).One(context.Background(), store.DBS().Reader)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		logger.Error().Err(err).Msg("error querying developer license")
		return err
	}
	if existing != nil {
		return nil
	}

	dl := models.DeveloperLicense{
		LicenseAddress:    licenseBytes,
		LicenseAddressHex: []byte(hexStr),
		DeveloperID:       fmt.Sprintf("%d", tokenID),
		Status:            "Active",
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}

	if err := dl.Insert(context.Background(), store.DBS().Writer, boil.Infer()); err != nil {
		logger.Error().Err(err).Msgf("error inserting developer license %s", clientID)
		return err
	}

	logger.Info().Msgf("inserted new developer license %s with tokenId %d", clientID, tokenID)
	return nil
}
