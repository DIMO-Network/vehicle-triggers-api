package auth

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/shared/pkg/db"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/gateways"
	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog"
	"github.com/volatiletech/sqlboiler/v4/boil"
	"github.com/volatiletech/sqlboiler/v4/queries/qm"
)

// EnsureDeveloperLicenseExists verifies the clientID against identity API.
// If valid and not already in our DB, it inserts a new DeveloperLicense record.
func EnsureDeveloperLicenseExists(clientID string, api gateways.IdentityAPI, store db.Store, logger zerolog.Logger) error {
	valid, tokenID, err := api.VerifyDeveloperLicense(clientID)
	if err != nil {
		return richerrors.Error{
			ExternalMsg: "Failed to verify developer license with identity API",
			Err:         err,
			Code:        fiber.StatusInternalServerError,
		}
	}
	if !valid {
		return richerrors.Error{
			ExternalMsg: "Invalid developer license",
			Code:        fiber.StatusUnauthorized,
		}
	}

	hexStr := strings.TrimPrefix(clientID, "0x")
	licenseBytes, err := hex.DecodeString(hexStr)
	if err != nil {
		return richerrors.Error{
			ExternalMsg: "Failed to decode client ID from developer license",
			Err:         err,
			Code:        fiber.StatusUnauthorized,
		}
	}

	existing, err := models.DeveloperLicenses(
		qm.Where("developer_id = ?", fmt.Sprintf("%d", tokenID)),
	).One(context.Background(), store.DBS().Reader)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return richerrors.Error{
			ExternalMsg: "Error querying developer license",
			Err:         err,
			Code:        fiber.StatusInternalServerError,
		}
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
		return richerrors.Error{
			ExternalMsg: "Failed to add developer license",
			Err:         err,
			Code:        fiber.StatusInternalServerError,
		}
	}

	logger.Info().Msgf("inserted new developer license %s with tokenId %d", clientID, tokenID)
	return nil
}
