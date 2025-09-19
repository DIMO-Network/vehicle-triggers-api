package triggersrepo

import (
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/tests"
	"github.com/aarondl/null/v8"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateTrigger(t *testing.T) {
	t.Parallel()
	tc := tests.SetupTestContainer(t)

	repo := NewRepository(tc.DB)
	ctx := context.Background()

	baseReq := CreateTriggerRequest{
		Service:                 "Telemetry",
		MetricName:              "speed",
		Condition:               "valueNumber > 20",
		TargetURI:               "https://example.com/webhook",
		Status:                  StatusEnabled,
		Description:             "Alert when vehicle speed exceeds 20 kph",
		CooldownPeriod:          10,
		DeveloperLicenseAddress: tests.RandomAddr(t),
	}

	t.Run("success", func(t *testing.T) {
		trigger, err := repo.CreateTrigger(ctx, baseReq)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		assert.NotEmpty(t, trigger.ID)
		assert.Equal(t, baseReq.Service, trigger.Service)
		assert.Equal(t, baseReq.MetricName, trigger.MetricName)
		assert.Equal(t, baseReq.Condition, trigger.Condition)
		assert.Equal(t, baseReq.TargetURI, trigger.TargetURI)
		assert.Equal(t, baseReq.Status, trigger.Status)
		assert.Equal(t, baseReq.Description, trigger.Description.String)
		assert.Equal(t, baseReq.CooldownPeriod, trigger.CooldownPeriod)
		assert.Equal(t, baseReq.DeveloperLicenseAddress.Bytes(), trigger.DeveloperLicenseAddress)
	})

	t.Run("allow duplicates triggers", func(t *testing.T) {
		// Create first trigger
		trigger1, err := repo.CreateTrigger(ctx, baseReq)
		require.NoError(t, err)
		// This should succeed since UUIDs are auto-generated
		trigger2, err := repo.CreateTrigger(ctx, baseReq)
		require.NoError(t, err)
		assert.NotEqual(t, trigger1.ID, trigger2.ID)
	})

	t.Run("disallow duplicates triggers with display name", func(t *testing.T) {
		// Create first trigger
		duplicateReq := baseReq
		duplicateReq.DisplayName = "Duplicate Trigger"
		_, err := repo.CreateTrigger(ctx, duplicateReq)
		require.NoError(t, err)

		// This should fail since display name must be unique
		_, err = repo.CreateTrigger(ctx, duplicateReq)
		require.Error(t, err)
		var richErr richerrors.Error
		require.ErrorAs(t, err, &richErr)
		assert.Equal(t, richErr.Code, http.StatusBadRequest)
	})

	t.Run("Missing Metric Name", func(t *testing.T) {
		reqCopy := baseReq
		reqCopy.MetricName = ""
		_, err := repo.CreateTrigger(ctx, reqCopy)
		require.Error(t, err)
		require.ErrorIs(t, err, ValidationError)
	})

	t.Run("Missing Developer License Address", func(t *testing.T) {
		reqCopy := baseReq
		reqCopy.DeveloperLicenseAddress = common.Address{}
		_, err := repo.CreateTrigger(ctx, reqCopy)
		require.Error(t, err)
		require.ErrorIs(t, err, ValidationError)
	})

	t.Run("Missing Service", func(t *testing.T) {
		reqCopy := baseReq
		reqCopy.Service = ""
		_, err := repo.CreateTrigger(ctx, reqCopy)
		require.Error(t, err)
		require.ErrorIs(t, err, ValidationError)
	})

	t.Run("Missing Condition", func(t *testing.T) {
		reqCopy := baseReq
		reqCopy.Condition = ""
		_, err := repo.CreateTrigger(ctx, reqCopy)
		require.Error(t, err)
		require.ErrorIs(t, err, ValidationError)
	})

	t.Run("Missing Target URI", func(t *testing.T) {
		reqCopy := baseReq
		reqCopy.TargetURI = ""
		_, err := repo.CreateTrigger(ctx, reqCopy)
		require.Error(t, err)
		require.ErrorIs(t, err, ValidationError)
	})

	t.Run("Missing Status", func(t *testing.T) {
		reqCopy := baseReq
		reqCopy.Status = ""
		_, err := repo.CreateTrigger(ctx, reqCopy)
		require.Error(t, err)
		require.ErrorIs(t, err, ValidationError)
	})

}

func TestGetTriggersByDeveloperLicense(t *testing.T) {
	t.Parallel()
	tc := tests.SetupTestContainer(t)

	repo := NewRepository(tc.DB)
	ctx := context.Background()

	baseReq := CreateTriggerRequest{
		Service:                 "Telemetry",
		MetricName:              "speed",
		Condition:               "valueNumber > 20",
		TargetURI:               "https://example.com/webhook",
		Status:                  StatusEnabled,
		Description:             "Speed alert",
		CooldownPeriod:          10,
		DeveloperLicenseAddress: tests.RandomAddr(t),
	}

	t.Run("success with multiple triggers", func(t *testing.T) {
		devAddress1 := tests.RandomAddr(t)
		// Create test triggers for devAddress1
		req1 := baseReq
		req1.MetricName = "speed"
		req1.Condition = "valueNumber > 20"
		req1.TargetURI = "https://example.com/webhook1"
		req1.Description = "Speed alert"
		req1.DeveloperLicenseAddress = devAddress1

		req2 := baseReq
		req2.MetricName = "temperature"
		req2.Condition = "valueNumber > 30"
		req2.TargetURI = "https://example.com/webhook2"
		req2.Description = "Temperature alert"
		req2.CooldownPeriod = 15
		req2.DeveloperLicenseAddress = devAddress1

		trigger1, err := repo.CreateTrigger(ctx, req1)
		require.NoError(t, err)
		require.NotNil(t, trigger1)

		trigger2, err := repo.CreateTrigger(ctx, req2)
		require.NoError(t, err)
		require.NotNil(t, trigger2)

		// Test getting triggers by developer license
		triggers, err := repo.GetTriggersByDeveloperLicense(ctx, devAddress1)
		require.NoError(t, err)
		require.Len(t, triggers, 2)

		// Verify all triggers belong to the correct developer license
		for _, trigger := range triggers {
			assert.Equal(t, devAddress1.Bytes(), trigger.DeveloperLicenseAddress)
		}
	})

	t.Run("success with single trigger", func(t *testing.T) {
		// Create a trigger for devAddress2
		req := baseReq
		req.DeveloperLicenseAddress = tests.RandomAddr(t)
		req.MetricName = "fuel_level"
		req.Condition = "valueNumber < 10"
		req.TargetURI = "https://example.com/webhook3"
		req.Description = "Low fuel alert"

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		// Test getting triggers by developer license
		triggers, err := repo.GetTriggersByDeveloperLicense(ctx, req.DeveloperLicenseAddress)
		require.NoError(t, err)
		require.Len(t, triggers, 1)

		// Verify the trigger belongs to the correct developer license
		assert.Equal(t, req.DeveloperLicenseAddress.Bytes(), triggers[0].DeveloperLicenseAddress)
		assert.Equal(t, trigger.ID, triggers[0].ID)
	})

	t.Run("empty result for non-existent developer license", func(t *testing.T) {
		nonExistentAddress := tests.RandomAddr(t)
		triggers, err := repo.GetTriggersByDeveloperLicense(ctx, nonExistentAddress)
		require.NoError(t, err)
		require.Len(t, triggers, 0)
	})

	t.Run("empty result for zero address", func(t *testing.T) {
		zeroAddress := common.Address{}
		triggers, err := repo.GetTriggersByDeveloperLicense(ctx, zeroAddress)
		require.NoError(t, err)
		require.Len(t, triggers, 0)
	})

	t.Run("isolation between different developer licenses", func(t *testing.T) {
		// Create triggers for different developer licenses
		req1 := baseReq
		req1.DeveloperLicenseAddress = tests.RandomAddr(t)
		req1.MetricName = "battery"
		req1.Condition = "valueNumber < 20"
		req1.TargetURI = "https://example.com/webhook4"
		req1.Description = "Low battery alert"

		req2 := baseReq
		req2.DeveloperLicenseAddress = tests.RandomAddr(t)
		req2.MetricName = "engine_temp"
		req2.Condition = "valueNumber > 100"
		req2.TargetURI = "https://example.com/webhook5"
		req2.Description = "High engine temperature alert"

		_, err := repo.CreateTrigger(ctx, req1)
		require.NoError(t, err)

		_, err = repo.CreateTrigger(ctx, req2)
		require.NoError(t, err)

		// Verify devAddress1 only gets its own triggers
		triggers1, err := repo.GetTriggersByDeveloperLicense(ctx, req1.DeveloperLicenseAddress)
		require.NoError(t, err)
		assert.Len(t, triggers1, 1)
		for _, trigger := range triggers1 {
			assert.Equal(t, req1.DeveloperLicenseAddress.Bytes(), trigger.DeveloperLicenseAddress)
		}

		// Verify devAddress2 only gets its own triggers
		triggers2, err := repo.GetTriggersByDeveloperLicense(ctx, req2.DeveloperLicenseAddress)
		require.NoError(t, err)
		assert.Len(t, triggers2, 1)
		for _, trigger := range triggers2 {
			assert.Equal(t, req2.DeveloperLicenseAddress.Bytes(), trigger.DeveloperLicenseAddress)
		}
	})
}

func TestGetTriggerByIDAndDeveloperLicense(t *testing.T) {
	t.Parallel()
	tc := tests.SetupTestContainer(t)

	repo := NewRepository(tc.DB)
	ctx := context.Background()

	baseReq := CreateTriggerRequest{
		Service:                 "Telemetry",
		MetricName:              "speed",
		Condition:               "valueNumber > 20",
		TargetURI:               "https://example.com/webhook",
		Status:                  StatusEnabled,
		Description:             "Speed alert",
		CooldownPeriod:          10,
		DeveloperLicenseAddress: tests.RandomAddr(t),
	}

	t.Run("existing trigger", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress

		createdTrigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, createdTrigger)

		trigger, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, createdTrigger.ID, devAddress)
		require.NoError(t, err)
		require.NotNil(t, trigger)
		assert.Equal(t, createdTrigger.ID, trigger.ID)
		assert.Equal(t, devAddress.Bytes(), trigger.DeveloperLicenseAddress)
	})

	t.Run("non-existent trigger", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress

		createdTrigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, createdTrigger)
		nonExistentTrigger, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, uuid.New().String(), devAddress)
		require.Error(t, err)
		assert.Nil(t, nonExistentTrigger)
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("wrong developer license", func(t *testing.T) {
		devAddress1 := tests.RandomAddr(t)
		devAddress2 := tests.RandomAddr(t)

		req := baseReq
		req.DeveloperLicenseAddress = devAddress1

		createdTrigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, createdTrigger)

		// Try to get the trigger with a different developer license
		wrongTrigger, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, createdTrigger.ID, devAddress2)
		require.Error(t, err)
		assert.Nil(t, wrongTrigger)
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("empty trigger id", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)
		trigger, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, "", devAddress)
		require.Error(t, err)
		assert.Nil(t, trigger)
		assert.ErrorIs(t, err, ValidationError)
	})

	t.Run("zero address", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress

		createdTrigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, createdTrigger)

		// Try to get the trigger with zero address
		zeroAddress := common.Address{}
		trigger, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, createdTrigger.ID, zeroAddress)
		require.Error(t, err)
		assert.Nil(t, trigger)
		assert.ErrorIs(t, err, ValidationError)
	})

	t.Run("isolation between different developer licenses", func(t *testing.T) {
		devAddress1 := tests.RandomAddr(t)
		devAddress2 := tests.RandomAddr(t)

		// Create triggers for different developer licenses
		req1 := baseReq
		req1.DeveloperLicenseAddress = devAddress1
		req1.MetricName = "battery"
		req1.Condition = "valueNumber < 20"
		req1.TargetURI = "https://example.com/webhook1"
		req1.Description = "Low battery alert"

		req2 := baseReq
		req2.DeveloperLicenseAddress = devAddress2
		req2.MetricName = "engine_temp"
		req2.Condition = "valueNumber > 100"
		req2.TargetURI = "https://example.com/webhook2"
		req2.Description = "High engine temperature alert"

		trigger1, err := repo.CreateTrigger(ctx, req1)
		require.NoError(t, err)
		require.NotNil(t, trigger1)

		trigger2, err := repo.CreateTrigger(ctx, req2)
		require.NoError(t, err)
		require.NotNil(t, trigger2)

		// Verify devAddress1 can only access its own trigger
		retrievedTrigger1, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger1.ID, devAddress1)
		require.NoError(t, err)
		require.NotNil(t, retrievedTrigger1)
		assert.Equal(t, trigger1.ID, retrievedTrigger1.ID)
		assert.Equal(t, devAddress1.Bytes(), retrievedTrigger1.DeveloperLicenseAddress)

		// Verify devAddress1 cannot access devAddress2's trigger
		wrongTrigger1, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger2.ID, devAddress1)
		require.Error(t, err)
		assert.Nil(t, wrongTrigger1)
		assert.ErrorIs(t, err, sql.ErrNoRows)

		// Verify devAddress2 can only access its own trigger
		retrievedTrigger2, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger2.ID, devAddress2)
		require.NoError(t, err)
		require.NotNil(t, retrievedTrigger2)
		assert.Equal(t, trigger2.ID, retrievedTrigger2.ID)
		assert.Equal(t, devAddress2.Bytes(), retrievedTrigger2.DeveloperLicenseAddress)

		// Verify devAddress2 cannot access devAddress1's trigger
		wrongTrigger2, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger1.ID, devAddress2)
		require.Error(t, err)
		assert.Nil(t, wrongTrigger2)
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})
}

func TestUpdateTrigger(t *testing.T) {
	t.Parallel()
	tc := tests.SetupTestContainer(t)

	repo := NewRepository(tc.DB)
	ctx := context.Background()

	baseReq := CreateTriggerRequest{
		Service:                 "Telemetry",
		MetricName:              "speed",
		Condition:               "valueNumber > 20",
		TargetURI:               "https://example.com/webhook",
		Status:                  StatusEnabled,
		Description:             "Speed alert",
		CooldownPeriod:          10,
		DeveloperLicenseAddress: tests.RandomAddr(t),
	}

	t.Run("successful update", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		// Update the trigger
		trigger.Status = StatusDisabled
		trigger.Description.String = "Updated speed alert"
		trigger.CooldownPeriod = 20

		err = repo.UpdateTrigger(ctx, trigger)
		require.NoError(t, err)

		// Verify the update
		updatedTrigger, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger.ID, devAddress)
		require.NoError(t, err)
		require.NotNil(t, updatedTrigger)

		assert.Equal(t, StatusDisabled, updatedTrigger.Status)
		assert.Equal(t, "Updated speed alert", updatedTrigger.Description.String)
		assert.Equal(t, 20, updatedTrigger.CooldownPeriod)
		// other fields should be the same
		assert.Equal(t, trigger.ID, updatedTrigger.ID)
		assert.Equal(t, trigger.DeveloperLicenseAddress, updatedTrigger.DeveloperLicenseAddress)
		assert.Equal(t, trigger.Service, updatedTrigger.Service)
		assert.Equal(t, trigger.MetricName, updatedTrigger.MetricName)
		assert.Equal(t, trigger.Condition, updatedTrigger.Condition)
		assert.Equal(t, trigger.TargetURI, updatedTrigger.TargetURI)
	})

	t.Run("update non-existent trigger", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		// Create a trigger with a different ID
		nonExistentTrigger := trigger
		nonExistentTrigger.ID = uuid.New().String()

		err = repo.UpdateTrigger(ctx, nonExistentTrigger)
		require.Error(t, err)
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("update multiple fields", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress
		req.MetricName = "temperature"
		req.Condition = "valueNumber > 30"
		req.TargetURI = "https://example.com/webhook2"
		req.Description = "Temperature alert"

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		// Update multiple fields
		trigger.Status = StatusDisabled
		trigger.Description.String = "Updated temperature alert"
		trigger.CooldownPeriod = 25
		trigger.Condition = "valueNumber > 35"
		trigger.TargetURI = "https://example.com/webhook3"

		err = repo.UpdateTrigger(ctx, trigger)
		require.NoError(t, err)

		// Verify all updates
		updatedTrigger, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger.ID, devAddress)
		require.NoError(t, err)
		require.NotNil(t, updatedTrigger)

		assert.Equal(t, StatusDisabled, updatedTrigger.Status)
		assert.Equal(t, "Updated temperature alert", updatedTrigger.Description.String)
		assert.Equal(t, 25, updatedTrigger.CooldownPeriod)
		assert.Equal(t, "valueNumber > 35", updatedTrigger.Condition)
		assert.Equal(t, "https://example.com/webhook3", updatedTrigger.TargetURI)
		// Verify unchanged fields
		assert.Equal(t, trigger.ID, updatedTrigger.ID)
		assert.Equal(t, trigger.DeveloperLicenseAddress, updatedTrigger.DeveloperLicenseAddress)
		assert.Equal(t, trigger.Service, updatedTrigger.Service)
		assert.Equal(t, trigger.MetricName, updatedTrigger.MetricName)
	})
}

func TestDeleteTrigger(t *testing.T) {
	t.Parallel()
	tc := tests.SetupTestContainer(t)

	repo := NewRepository(tc.DB)
	ctx := context.Background()

	baseReq := CreateTriggerRequest{
		Service:                 "Telemetry",
		MetricName:              "speed",
		Condition:               "valueNumber > 20",
		TargetURI:               "https://example.com/webhook",
		Status:                  StatusEnabled,
		Description:             "Speed alert",
		CooldownPeriod:          10,
		DeveloperLicenseAddress: tests.RandomAddr(t),
	}

	t.Run("successful delete", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		// Verify trigger exists
		existingTrigger, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger.ID, devAddress)
		require.NoError(t, err)
		require.NotNil(t, existingTrigger)

		// Delete the trigger
		err = repo.DeleteTrigger(ctx, trigger.ID, devAddress)
		require.NoError(t, err)

		// Verify trigger is deleted
		deletedTrigger, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger.ID, devAddress)
		require.Error(t, err)
		assert.Nil(t, deletedTrigger)
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("delete non-existent trigger", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)
		// Test deleting non-existent trigger (should not error)
		err := repo.DeleteTrigger(ctx, uuid.New().String(), devAddress)
		require.Error(t, err)
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("delete with wrong developer license", func(t *testing.T) {
		devAddress1 := tests.RandomAddr(t)
		devAddress2 := tests.RandomAddr(t)

		// Create a trigger for devAddress1
		req := baseReq
		req.DeveloperLicenseAddress = devAddress1
		req.MetricName = "temperature"
		req.Condition = "valueNumber > 30"
		req.TargetURI = "https://example.com/webhook2"
		req.Description = "Temperature alert"
		req.CooldownPeriod = 15

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		// Try to delete with wrong developer license
		err = repo.DeleteTrigger(ctx, trigger.ID, devAddress2)
		require.Error(t, err)
		assert.ErrorIs(t, err, sql.ErrNoRows)

		// Verify trigger still exists
		existingTrigger, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger.ID, devAddress1)
		require.NoError(t, err)
		assert.NotNil(t, existingTrigger)
	})

	t.Run("delete multiple triggers", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)

		// Create multiple triggers
		req1 := baseReq
		req1.DeveloperLicenseAddress = devAddress
		req1.MetricName = "battery"
		req1.Condition = "valueNumber < 20"
		req1.TargetURI = "https://example.com/webhook3"
		req1.Description = "Low battery alert"

		req2 := baseReq
		req2.DeveloperLicenseAddress = devAddress
		req2.MetricName = "engine_temp"
		req2.Condition = "valueNumber > 100"
		req2.TargetURI = "https://example.com/webhook4"
		req2.Description = "High engine temperature alert"

		trigger1, err := repo.CreateTrigger(ctx, req1)
		require.NoError(t, err)
		require.NotNil(t, trigger1)

		trigger2, err := repo.CreateTrigger(ctx, req2)
		require.NoError(t, err)
		require.NotNil(t, trigger2)

		// Verify both triggers exist
		existingTrigger1, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger1.ID, devAddress)
		require.NoError(t, err)
		require.NotNil(t, existingTrigger1)

		existingTrigger2, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger2.ID, devAddress)
		require.NoError(t, err)
		require.NotNil(t, existingTrigger2)

		// Delete first trigger
		err = repo.DeleteTrigger(ctx, trigger1.ID, devAddress)
		require.NoError(t, err)

		// Verify first trigger is deleted
		deletedTrigger1, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger1.ID, devAddress)
		require.Error(t, err)
		assert.Nil(t, deletedTrigger1)
		assert.ErrorIs(t, err, sql.ErrNoRows)

		// Verify second trigger still exists
		remainingTrigger2, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger2.ID, devAddress)
		require.NoError(t, err)
		assert.NotNil(t, remainingTrigger2)

		// Delete second trigger
		err = repo.DeleteTrigger(ctx, trigger2.ID, devAddress)
		require.NoError(t, err)

		// Verify second trigger is deleted
		deletedTrigger2, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger2.ID, devAddress)
		require.Error(t, err)
		assert.Nil(t, deletedTrigger2)
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("isolation between different developer licenses", func(t *testing.T) {
		devAddress1 := tests.RandomAddr(t)
		devAddress2 := tests.RandomAddr(t)

		// Create triggers for different developer licenses
		req1 := baseReq
		req1.DeveloperLicenseAddress = devAddress1
		req1.MetricName = "battery"
		req1.Condition = "valueNumber < 20"
		req1.TargetURI = "https://example.com/webhook5"
		req1.Description = "Low battery alert"

		req2 := baseReq
		req2.DeveloperLicenseAddress = devAddress2
		req2.MetricName = "engine_temp"
		req2.Condition = "valueNumber > 100"
		req2.TargetURI = "https://example.com/webhook6"
		req2.Description = "High engine temperature alert"

		trigger1, err := repo.CreateTrigger(ctx, req1)
		require.NoError(t, err)
		require.NotNil(t, trigger1)

		trigger2, err := repo.CreateTrigger(ctx, req2)
		require.NoError(t, err)
		require.NotNil(t, trigger2)

		// Delete trigger1
		err = repo.DeleteTrigger(ctx, trigger1.ID, devAddress1)
		require.NoError(t, err)

		// Verify trigger1 is deleted
		deletedTrigger1, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger1.ID, devAddress1)
		require.Error(t, err)
		assert.Nil(t, deletedTrigger1)
		assert.ErrorIs(t, err, sql.ErrNoRows)

		// Verify trigger2 still exists
		remainingTrigger2, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger2.ID, devAddress2)
		require.NoError(t, err)
		assert.NotNil(t, remainingTrigger2)

		// Try to delete trigger2 with wrong developer license
		err = repo.DeleteTrigger(ctx, trigger2.ID, devAddress1)
		require.Error(t, err)
		assert.ErrorIs(t, err, sql.ErrNoRows)

		// Verify trigger2 still exists
		stillExistingTrigger2, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger2.ID, devAddress2)
		require.NoError(t, err)
		assert.NotNil(t, stillExistingTrigger2)
	})
}

func TestCreateVehicleSubscription(t *testing.T) {
	t.Parallel()
	tc := tests.SetupTestContainer(t)

	repo := NewRepository(tc.DB)
	ctx := context.Background()

	baseReq := CreateTriggerRequest{
		Service:                 "Telemetry",
		MetricName:              "speed",
		Condition:               "valueNumber > 20",
		TargetURI:               "https://example.com/webhook",
		Status:                  StatusEnabled,
		Description:             "Speed alert",
		CooldownPeriod:          10,
		DeveloperLicenseAddress: tests.RandomAddr(t),
	}

	t.Run("successful creation", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		assetDid := cloudevent.ERC721DID{
			ChainID:         137,
			ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
			TokenID:         big.NewInt(12345),
		}

		// Create vehicle subscription
		subscription, err := repo.CreateVehicleSubscription(ctx, assetDid, trigger.ID)
		require.NoError(t, err)
		require.NotNil(t, subscription)

		assert.Equal(t, assetDid.String(), subscription.AssetDid)
		assert.Equal(t, trigger.ID, subscription.TriggerID)
		assert.NotZero(t, subscription.CreatedAt)
		assert.NotZero(t, subscription.UpdatedAt)
	})

	t.Run("create multiple subscriptions for same trigger", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress
		req.MetricName = "temperature"
		req.Condition = "valueNumber > 30"
		req.TargetURI = "https://example.com/webhook2"
		req.Description = "Temperature alert"

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		assetDid1 := cloudevent.ERC721DID{
			ChainID:         137,
			ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
			TokenID:         big.NewInt(11111),
		}
		assetDid2 := cloudevent.ERC721DID{
			ChainID:         137,
			ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
			TokenID:         big.NewInt(12345),
		}
		assetDid3 := cloudevent.ERC721DID{
			ChainID:         137,
			ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
			TokenID:         big.NewInt(67890),
		}
		// Create multiple subscriptions for the same trigger
		subscription1, err := repo.CreateVehicleSubscription(ctx, assetDid1, trigger.ID)
		require.NoError(t, err)
		require.NotNil(t, subscription1)

		subscription2, err := repo.CreateVehicleSubscription(ctx, assetDid2, trigger.ID)
		require.NoError(t, err)
		require.NotNil(t, subscription2)

		subscription3, err := repo.CreateVehicleSubscription(ctx, assetDid3, trigger.ID)
		require.NoError(t, err)
		require.NotNil(t, subscription3)

		// Verify all subscriptions were created
		subscriptions, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions, 3)

		slices.SortFunc(subscriptions, func(a, b *models.VehicleSubscription) int {
			return cmp.Compare(a.AssetDid, b.AssetDid)
		})

		assert.Equal(t, assetDid1.String(), subscriptions[0].AssetDid)
		assert.Equal(t, assetDid2.String(), subscriptions[1].AssetDid)
		assert.Equal(t, assetDid3.String(), subscriptions[2].AssetDid)
		assert.Equal(t, trigger.ID, subscriptions[0].TriggerID)
		assert.Equal(t, trigger.ID, subscriptions[1].TriggerID)
		assert.Equal(t, trigger.ID, subscriptions[2].TriggerID)
		assert.NotZero(t, subscriptions[0].CreatedAt)
		assert.NotZero(t, subscriptions[1].CreatedAt)
		assert.NotZero(t, subscriptions[2].CreatedAt)
		assert.NotZero(t, subscriptions[0].UpdatedAt)
		assert.NotZero(t, subscriptions[1].UpdatedAt)
		assert.NotZero(t, subscriptions[2].UpdatedAt)
	})

	t.Run("create subscription for non-existent trigger", func(t *testing.T) {
		assetDid := cloudevent.ERC721DID{
			ChainID:         137,
			ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
			TokenID:         big.NewInt(12345),
		}
		nonExistentTriggerID := uuid.New().String()

		// Try to create subscription for non-existent trigger
		_, err := repo.CreateVehicleSubscription(ctx, assetDid, nonExistentTriggerID)
		require.Error(t, err)
		var richErr richerrors.Error
		require.ErrorAs(t, err, &richErr)
		require.Equal(t, http.StatusNotFound, richErr.Code)
	})

	t.Run("create subscription with zero vehicle token ID", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		var zeroAssetDID cloudevent.ERC721DID

		// Try to create subscription with zero vehicle token ID
		_, err = repo.CreateVehicleSubscription(ctx, zeroAssetDID, trigger.ID)
		require.Error(t, err)
		assert.ErrorIs(t, err, ValidationError)
	})

	t.Run("create subscription with empty trigger ID", func(t *testing.T) {
		assetDid := cloudevent.ERC721DID{
			ChainID:         137,
			ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
			TokenID:         big.NewInt(12345),
		}
		emptyTriggerID := ""

		// Try to create subscription with empty trigger ID
		_, err := repo.CreateVehicleSubscription(ctx, assetDid, emptyTriggerID)
		require.Error(t, err)
		assert.ErrorIs(t, err, ValidationError)
	})

	t.Run("create duplicate subscription", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress
		req.MetricName = "battery"
		req.Condition = "valueNumber < 20"
		req.TargetURI = "https://example.com/webhook3"
		req.Description = "Low battery alert"

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		assetDid := cloudevent.ERC721DID{
			ChainID:         137,
			ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
			TokenID:         big.NewInt(12345),
		}

		// Create first subscription
		subscription1, err := repo.CreateVehicleSubscription(ctx, assetDid, trigger.ID)
		require.NoError(t, err)
		require.NotNil(t, subscription1)

		// Try to create duplicate subscription
		_, err = repo.CreateVehicleSubscription(ctx, assetDid, trigger.ID)
		require.Error(t, err)
		assert.True(t, IsDuplicateKeyError(err))

		// Verify only one subscription exists
		subscriptions, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions, 1)
		assert.Equal(t, assetDid.String(), subscriptions[0].AssetDid)
		assert.Equal(t, trigger.ID, subscriptions[0].TriggerID)
	})
}

func TestGetVehicleSubscriptionsByTriggerID(t *testing.T) {
	t.Parallel()
	tc := tests.SetupTestContainer(t)

	repo := NewRepository(tc.DB)
	ctx := context.Background()

	baseReq := CreateTriggerRequest{
		Service:                 "Telemetry",
		MetricName:              "speed",
		Condition:               "valueNumber > 20",
		TargetURI:               "https://example.com/webhook",
		Status:                  StatusEnabled,
		Description:             "Speed alert",
		CooldownPeriod:          10,
		DeveloperLicenseAddress: tests.RandomAddr(t),
	}

	t.Run("successful retrieval", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)

		// Create triggers
		req1 := baseReq
		req1.DeveloperLicenseAddress = devAddress
		req1.MetricName = "speed"
		req1.Condition = "valueNumber > 20"
		req1.TargetURI = "https://example.com/webhook1"
		req1.Description = "Speed alert"

		req2 := baseReq
		req2.DeveloperLicenseAddress = devAddress
		req2.MetricName = "temperature"
		req2.Condition = "valueNumber > 30"
		req2.TargetURI = "https://example.com/webhook2"
		req2.Description = "Temperature alert"
		req2.CooldownPeriod = 15

		trigger1, err := repo.CreateTrigger(ctx, req1)
		require.NoError(t, err)
		require.NotNil(t, trigger1)

		trigger2, err := repo.CreateTrigger(ctx, req2)
		require.NoError(t, err)
		require.NotNil(t, trigger2)

		// Create vehicle subscriptions
		assetDid1 := randAssetDID(t)
		assetDid2 := randAssetDID(t)

		_, err = repo.CreateVehicleSubscription(ctx, assetDid1, trigger1.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, assetDid2, trigger1.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, assetDid1, trigger2.ID)
		require.NoError(t, err)

		// Test getting subscriptions by trigger IDs
		subscriptions1, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger1.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions1, 2)

		subscriptions2, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger2.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions2, 1)

		// Verify subscriptions
		for _, sub := range subscriptions1 {
			assert.Equal(t, trigger1.ID, sub.TriggerID)
		}

		for _, sub := range subscriptions2 {
			assert.Equal(t, trigger2.ID, sub.TriggerID)
		}
	})

	t.Run("empty result for non-existent trigger ID", func(t *testing.T) {
		nonExistentTriggerID := uuid.New().String()

		subscriptions, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, nonExistentTriggerID)
		require.NoError(t, err)
		require.Len(t, subscriptions, 0)
	})

}

func TestGetVehicleSubscriptionsByVehicleAndDeveloperLicense(t *testing.T) {
	t.Parallel()
	tc := tests.SetupTestContainer(t)

	repo := NewRepository(tc.DB)
	ctx := context.Background()

	baseReq := CreateTriggerRequest{
		Service:                 "Telemetry",
		MetricName:              "speed",
		Condition:               "valueNumber > 20",
		TargetURI:               "https://example.com/webhook",
		Status:                  StatusEnabled,
		Description:             "Speed alert",
		CooldownPeriod:          10,
		DeveloperLicenseAddress: tests.RandomAddr(t),
	}

	t.Run("successful retrieval for different developers", func(t *testing.T) {
		devAddress1 := tests.RandomAddr(t)

		devAddress2 := tests.RandomAddr(t)

		// Create triggers for different developers
		req1 := baseReq
		req1.DeveloperLicenseAddress = devAddress1
		req1.MetricName = "speed"
		req1.Condition = "valueNumber > 20"
		req1.TargetURI = "https://example.com/webhook1"
		req1.Description = "Speed alert"

		req2 := baseReq
		req2.DeveloperLicenseAddress = devAddress2
		req2.MetricName = "temperature"
		req2.Condition = "valueNumber > 30"
		req2.TargetURI = "https://example.com/webhook2"
		req2.Description = "Temperature alert"
		req2.CooldownPeriod = 15

		trigger1, err := repo.CreateTrigger(ctx, req1)
		require.NoError(t, err)
		require.NotNil(t, trigger1)

		trigger2, err := repo.CreateTrigger(ctx, req2)
		require.NoError(t, err)
		require.NotNil(t, trigger2)

		assetDid := randAssetDID(t)

		// Create vehicle subscriptions
		_, err = repo.CreateVehicleSubscription(ctx, assetDid, trigger1.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, assetDid, trigger2.ID)
		require.NoError(t, err)

		// Test getting subscriptions for devAddress1
		subscriptions, err := repo.GetVehicleSubscriptionsByVehicleAndDeveloperLicense(ctx, assetDid, devAddress1)
		require.NoError(t, err)
		require.Len(t, subscriptions, 1)
		assert.Equal(t, trigger1.ID, subscriptions[0].TriggerID)
		assert.Equal(t, assetDid.String(), subscriptions[0].AssetDid)

		// Test getting subscriptions for devAddress2
		subscriptions, err = repo.GetVehicleSubscriptionsByVehicleAndDeveloperLicense(ctx, assetDid, devAddress2)
		require.NoError(t, err)
		require.Len(t, subscriptions, 1)
		assert.Equal(t, trigger2.ID, subscriptions[0].TriggerID)
		assert.Equal(t, assetDid.String(), subscriptions[0].AssetDid)
	})

	t.Run("empty result for non-existent vehicle", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		assetDid := randAssetDID(t)
		_, err = repo.CreateVehicleSubscription(ctx, assetDid, trigger.ID)
		require.NoError(t, err)

		// Test getting subscriptions for non-existent vehicle
		nonExistentAssetDid := randAssetDID(t)
		subscriptions, err := repo.GetVehicleSubscriptionsByVehicleAndDeveloperLicense(ctx, nonExistentAssetDid, devAddress)
		require.NoError(t, err)
		require.Len(t, subscriptions, 0)
	})

	t.Run("empty result for non-existent developer license", func(t *testing.T) {
		devAddress1 := tests.RandomAddr(t)
		devAddress2 := tests.RandomAddr(t)

		req := baseReq
		req.DeveloperLicenseAddress = devAddress1

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)
		assetDid := randAssetDID(t)
		_, err = repo.CreateVehicleSubscription(ctx, assetDid, trigger.ID)
		require.NoError(t, err)

		// Test getting subscriptions for non-existent developer license
		subscriptions, err := repo.GetVehicleSubscriptionsByVehicleAndDeveloperLicense(ctx, assetDid, devAddress2)
		require.NoError(t, err)
		require.Len(t, subscriptions, 0)
	})

	t.Run("multiple subscriptions for same vehicle and developer", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)

		// Create multiple triggers for the same developer
		req1 := baseReq
		req1.DeveloperLicenseAddress = devAddress
		req1.MetricName = "speed"
		req1.Condition = "valueNumber > 20"
		req1.TargetURI = "https://example.com/webhook1"
		req1.Description = "Speed alert"

		req2 := baseReq
		req2.DeveloperLicenseAddress = devAddress
		req2.MetricName = "temperature"
		req2.Condition = "valueNumber > 30"
		req2.TargetURI = "https://example.com/webhook2"
		req2.Description = "Temperature alert"

		req3 := baseReq
		req3.DeveloperLicenseAddress = devAddress
		req3.MetricName = "battery"
		req3.Condition = "valueNumber < 20"
		req3.TargetURI = "https://example.com/webhook3"
		req3.Description = "Low battery alert"

		trigger1, err := repo.CreateTrigger(ctx, req1)
		require.NoError(t, err)
		require.NotNil(t, trigger1)

		trigger2, err := repo.CreateTrigger(ctx, req2)
		require.NoError(t, err)
		require.NotNil(t, trigger2)

		trigger3, err := repo.CreateTrigger(ctx, req3)
		require.NoError(t, err)
		require.NotNil(t, trigger3)

		assetDid := randAssetDID(t)

		// Create subscriptions for all triggers
		_, err = repo.CreateVehicleSubscription(ctx, assetDid, trigger1.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, assetDid, trigger2.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, assetDid, trigger3.ID)
		require.NoError(t, err)

		// Test getting all subscriptions for the vehicle and developer
		subscriptions, err := repo.GetVehicleSubscriptionsByVehicleAndDeveloperLicense(ctx, assetDid, devAddress)
		require.NoError(t, err)
		require.Len(t, subscriptions, 3)

		// Sort subscriptions for consistent ordering
		slices.SortFunc(subscriptions, func(a, b *models.VehicleSubscription) int {
			return a.CreatedAt.Compare(b.CreatedAt)
		})

		// Verify all subscriptions belong to the correct vehicle and developer
		require.Len(t, subscriptions, 3)
		assert.Equal(t, assetDid.String(), subscriptions[0].AssetDid)
		assert.Equal(t, assetDid.String(), subscriptions[1].AssetDid)
		assert.Equal(t, assetDid.String(), subscriptions[2].AssetDid)
		assert.Equal(t, trigger1.ID, subscriptions[0].TriggerID)
		assert.Equal(t, trigger2.ID, subscriptions[1].TriggerID)
		assert.Equal(t, trigger3.ID, subscriptions[2].TriggerID)
	})

	t.Run("isolation between different developers", func(t *testing.T) {
		devAddress1 := tests.RandomAddr(t)
		devAddress2 := tests.RandomAddr(t)

		// Create triggers for different developers
		req1 := baseReq
		req1.DeveloperLicenseAddress = devAddress1
		req1.MetricName = "speed"
		req1.Condition = "valueNumber > 20"
		req1.TargetURI = "https://example.com/webhook1"
		req1.Description = "Speed alert"

		req2 := baseReq
		req2.DeveloperLicenseAddress = devAddress2
		req2.MetricName = "temperature"
		req2.Condition = "valueNumber > 30"
		req2.TargetURI = "https://example.com/webhook2"
		req2.Description = "Temperature alert"

		trigger1, err := repo.CreateTrigger(ctx, req1)
		require.NoError(t, err)
		require.NotNil(t, trigger1)

		trigger2, err := repo.CreateTrigger(ctx, req2)
		require.NoError(t, err)
		require.NotNil(t, trigger2)

		assetDid := randAssetDID(t)

		// Create subscriptions for both triggers
		_, err = repo.CreateVehicleSubscription(ctx, assetDid, trigger1.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, assetDid, trigger2.ID)
		require.NoError(t, err)

		// Test getting subscriptions for devAddress1
		subscriptions1, err := repo.GetVehicleSubscriptionsByVehicleAndDeveloperLicense(ctx, assetDid, devAddress1)
		require.NoError(t, err)
		require.Len(t, subscriptions1, 1)
		assert.Equal(t, trigger1.ID, subscriptions1[0].TriggerID)
		assert.Equal(t, assetDid.String(), subscriptions1[0].AssetDid)

		// Test getting subscriptions for devAddress2
		subscriptions2, err := repo.GetVehicleSubscriptionsByVehicleAndDeveloperLicense(ctx, assetDid, devAddress2)
		require.NoError(t, err)
		require.Len(t, subscriptions2, 1)
		assert.Equal(t, trigger2.ID, subscriptions2[0].TriggerID)
		assert.Equal(t, assetDid.String(), subscriptions2[0].AssetDid)

		// Verify no cross-contamination
		assert.NotEqual(t, subscriptions1[0].TriggerID, subscriptions2[0].TriggerID)
	})
}

func TestDeleteVehicleSubscription(t *testing.T) {
	t.Parallel()
	tc := tests.SetupTestContainer(t)

	repo := NewRepository(tc.DB)
	ctx := context.Background()

	baseReq := CreateTriggerRequest{
		Service:                 "Telemetry",
		MetricName:              "speed",
		Condition:               "valueNumber > 20",
		TargetURI:               "https://example.com/webhook",
		Status:                  StatusEnabled,
		Description:             "Speed alert",
		CooldownPeriod:          10,
		DeveloperLicenseAddress: tests.RandomAddr(t),
	}

	t.Run("successful deletion", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		assetDid := randAssetDID(t)

		// Create vehicle subscription
		_, err = repo.CreateVehicleSubscription(ctx, assetDid, trigger.ID)
		require.NoError(t, err)

		// Verify subscription exists
		subscriptions, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions, 1)

		// Delete the subscription
		deleted, err := repo.DeleteVehicleSubscription(ctx, trigger.ID, assetDid)
		require.NoError(t, err)
		assert.Equal(t, int64(1), deleted)

		// Verify subscription is deleted
		subscriptions, err = repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions, 0)
	})

	t.Run("delete non-existent subscription", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		nonExistentAssetDid := randAssetDID(t)

		// Test deleting non-existent subscription
		deleted, err := repo.DeleteVehicleSubscription(ctx, trigger.ID, nonExistentAssetDid)
		require.NoError(t, err)
		assert.Equal(t, int64(0), deleted)
	})

	t.Run("delete from non-existent trigger", func(t *testing.T) {
		assetDid := randAssetDID(t)
		nonExistentTriggerID := uuid.New().String()

		// Test deleting from non-existent trigger
		deleted, err := repo.DeleteVehicleSubscription(ctx, nonExistentTriggerID, assetDid)
		require.NoError(t, err)
		assert.Equal(t, int64(0), deleted)
	})

	t.Run("delete specific subscription from multiple", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress
		req.MetricName = "temperature"
		req.Condition = "valueNumber > 30"
		req.TargetURI = "https://example.com/webhook2"
		req.Description = "Temperature alert"

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		// Create multiple vehicle subscriptions
		assetDid1 := randAssetDID(t)
		assetDid2 := randAssetDID(t)
		assetDid3 := randAssetDID(t)

		_, err = repo.CreateVehicleSubscription(ctx, assetDid1, trigger.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, assetDid2, trigger.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, assetDid3, trigger.ID)
		require.NoError(t, err)

		// Verify all subscriptions exist
		subscriptions, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions, 3)

		// Delete only one subscription
		deleted, err := repo.DeleteVehicleSubscription(ctx, trigger.ID, assetDid2)
		require.NoError(t, err)
		assert.Equal(t, int64(1), deleted)

		// Verify only the specific subscription was deleted
		remainingSubscriptions, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger.ID)
		require.NoError(t, err)
		require.Len(t, remainingSubscriptions, 2)

		// Verify the remaining subscriptions are correct
		remainingAssetDids := make(map[string]bool)
		for _, sub := range remainingSubscriptions {
			remainingAssetDids[sub.AssetDid] = true
		}

		assert.True(t, remainingAssetDids[assetDid1.String()])
		assert.True(t, remainingAssetDids[assetDid3.String()])
		assert.False(t, remainingAssetDids[assetDid2.String()])
	})

	t.Run("isolation between different triggers", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)

		// Create two triggers
		req1 := baseReq
		req1.DeveloperLicenseAddress = devAddress
		req1.MetricName = "speed"
		req1.Condition = "valueNumber > 20"
		req1.TargetURI = "https://example.com/webhook1"
		req1.Description = "Speed alert"

		req2 := baseReq
		req2.DeveloperLicenseAddress = devAddress
		req2.MetricName = "temperature"
		req2.Condition = "valueNumber > 30"
		req2.TargetURI = "https://example.com/webhook2"
		req2.Description = "Temperature alert"

		trigger1, err := repo.CreateTrigger(ctx, req1)
		require.NoError(t, err)
		require.NotNil(t, trigger1)

		trigger2, err := repo.CreateTrigger(ctx, req2)
		require.NoError(t, err)
		require.NotNil(t, trigger2)

		assetDid := randAssetDID(t)

		// Create subscriptions for both triggers
		_, err = repo.CreateVehicleSubscription(ctx, assetDid, trigger1.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, assetDid, trigger2.ID)
		require.NoError(t, err)

		// Verify both subscriptions exist
		subscriptions1, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger1.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions1, 1)

		subscriptions2, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger2.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions2, 1)

		// Delete subscription from trigger1 only
		deleted, err := repo.DeleteVehicleSubscription(ctx, trigger1.ID, assetDid)
		require.NoError(t, err)
		assert.Equal(t, int64(1), deleted)

		// Verify trigger1 subscription is deleted
		remainingSubscriptions1, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger1.ID)
		require.NoError(t, err)
		require.Len(t, remainingSubscriptions1, 0)

		// Verify trigger2 subscription still exists
		remainingSubscriptions2, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger2.ID)
		require.NoError(t, err)
		require.Len(t, remainingSubscriptions2, 1)
		assert.Equal(t, assetDid.String(), remainingSubscriptions2[0].AssetDid)
	})
}

func TestDeleteAllVehicleSubscriptionsForTrigger(t *testing.T) {
	t.Parallel()
	tc := tests.SetupTestContainer(t)

	repo := NewRepository(tc.DB)
	ctx := context.Background()

	baseReq := CreateTriggerRequest{
		Service:                 "Telemetry",
		MetricName:              "speed",
		Condition:               "valueNumber > 20",
		TargetURI:               "https://example.com/webhook",
		Status:                  StatusEnabled,
		Description:             "Speed alert",
		CooldownPeriod:          10,
		DeveloperLicenseAddress: tests.RandomAddr(t),
	}

	t.Run("successful deletion of all subscriptions", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		// Create multiple vehicle subscriptions
		assetDid1 := randAssetDID(t)
		assetDid2 := randAssetDID(t)
		assetDid3 := randAssetDID(t)

		_, err = repo.CreateVehicleSubscription(ctx, assetDid1, trigger.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, assetDid2, trigger.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, assetDid3, trigger.ID)
		require.NoError(t, err)

		// Verify subscriptions exist
		subscriptions, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions, 3)

		// Delete all subscriptions for the trigger
		deleted, err := repo.DeleteAllVehicleSubscriptionsForTrigger(ctx, trigger.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(3), deleted)

		// Verify all subscriptions are deleted
		subscriptions, err = repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions, 0)
	})

	t.Run("delete from trigger with no subscriptions", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress
		req.MetricName = "temperature"
		req.Condition = "valueNumber > 30"
		req.TargetURI = "https://example.com/webhook2"
		req.Description = "Temperature alert"

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		// Delete all subscriptions for trigger with no subscriptions
		deleted, err := repo.DeleteAllVehicleSubscriptionsForTrigger(ctx, trigger.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(0), deleted)

		// Verify no subscriptions exist
		subscriptions, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions, 0)
	})

	t.Run("delete from non-existent trigger", func(t *testing.T) {
		nonExistentTriggerID := uuid.New().String()

		// Delete all subscriptions for non-existent trigger
		deleted, err := repo.DeleteAllVehicleSubscriptionsForTrigger(ctx, nonExistentTriggerID)
		require.NoError(t, err)
		assert.Equal(t, int64(0), deleted)
	})

	t.Run("isolation between different triggers", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)

		// Create two triggers
		req1 := baseReq
		req1.DeveloperLicenseAddress = devAddress
		req1.MetricName = "speed"
		req1.Condition = "valueNumber > 20"
		req1.TargetURI = "https://example.com/webhook1"
		req1.Description = "Speed alert"

		req2 := baseReq
		req2.DeveloperLicenseAddress = devAddress
		req2.MetricName = "temperature"
		req2.Condition = "valueNumber > 30"
		req2.TargetURI = "https://example.com/webhook2"
		req2.Description = "Temperature alert"

		trigger1, err := repo.CreateTrigger(ctx, req1)
		require.NoError(t, err)
		require.NotNil(t, trigger1)

		trigger2, err := repo.CreateTrigger(ctx, req2)
		require.NoError(t, err)
		require.NotNil(t, trigger2)

		// Create subscriptions for both triggers
		assetDid1 := randAssetDID(t)
		assetDid2 := randAssetDID(t)
		assetDid3 := randAssetDID(t)

		_, err = repo.CreateVehicleSubscription(ctx, assetDid1, trigger1.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, assetDid2, trigger1.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, assetDid3, trigger2.ID)
		require.NoError(t, err)

		// Verify subscriptions exist for both triggers
		subscriptions1, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger1.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions1, 2)

		subscriptions2, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger2.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions2, 1)

		// Delete all subscriptions for trigger1 only
		deleted, err := repo.DeleteAllVehicleSubscriptionsForTrigger(ctx, trigger1.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(2), deleted)

		// Verify trigger1 subscriptions are deleted
		remainingSubscriptions1, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger1.ID)
		require.NoError(t, err)
		require.Len(t, remainingSubscriptions1, 0)

		// Verify trigger2 subscription still exists
		remainingSubscriptions2, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger2.ID)
		require.NoError(t, err)
		require.Len(t, remainingSubscriptions2, 1)
		assert.Equal(t, assetDid3.String(), remainingSubscriptions2[0].AssetDid)
	})

	t.Run("delete large number of subscriptions", func(t *testing.T) {
		devAddress := tests.RandomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress
		req.MetricName = "battery"
		req.Condition = "valueNumber < 20"
		req.TargetURI = "https://example.com/webhook3"
		req.Description = "Low battery alert"

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		// Create many vehicle subscriptions
		subscriptionCount := 10
		assetDids := make([]cloudevent.ERC721DID, subscriptionCount)
		for i := 0; i < subscriptionCount; i++ {
			assetDids[i] = randAssetDID(t)
			_, err = repo.CreateVehicleSubscription(ctx, assetDids[i], trigger.ID)
			require.NoError(t, err)
		}

		// Verify all subscriptions exist
		subscriptions, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions, subscriptionCount)

		// Delete all subscriptions for the trigger
		deleted, err := repo.DeleteAllVehicleSubscriptionsForTrigger(ctx, trigger.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(subscriptionCount), deleted)

		// Verify all subscriptions are deleted
		subscriptions, err = repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions, 0)
	})
}

func TestRepository_HandleTriggerReset(t *testing.T) {
	t.Parallel()
	tc := tests.SetupTestContainer(t)

	repo := NewRepository(tc.DB)
	ctx := context.Background()

	t.Run("successful reset when trigger has failures", func(t *testing.T) {
		// Create a trigger with failures
		trigger := createTestTriggerWithFailures(t, repo, ctx, 3, StatusFailed)

		err := repo.ResetTriggerFailureCount(ctx, trigger)
		require.NoError(t, err)

		// Verify trigger was reset
		updatedTrigger, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger.ID, common.BytesToAddress(trigger.DeveloperLicenseAddress))
		require.NoError(t, err)
		assert.Equal(t, 0, updatedTrigger.FailureCount)
		assert.Equal(t, StatusEnabled, updatedTrigger.Status)
	})

	t.Run("no action when trigger has no failures", func(t *testing.T) {
		trigger := createTestTriggerWithFailures(t, repo, ctx, 0, StatusDisabled)

		err := repo.ResetTriggerFailureCount(ctx, trigger)
		require.NoError(t, err)

		// Verify trigger unchanged
		updatedTrigger, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger.ID, common.BytesToAddress(trigger.DeveloperLicenseAddress))
		require.NoError(t, err)
		assert.Equal(t, 0, updatedTrigger.FailureCount)
		assert.Equal(t, StatusDisabled, updatedTrigger.Status)
		assert.Equal(t, trigger.UpdatedAt.UTC(), updatedTrigger.UpdatedAt.UTC())
	})
}

func TestRepository_IncrementTriggerFailureCount(t *testing.T) {
	t.Parallel()
	tc := tests.SetupTestContainer(t)

	repo := NewRepository(tc.DB)
	ctx := context.Background()

	t.Run("successful failure increment", func(t *testing.T) {
		trigger := createTestTriggerWithFailures(t, repo, ctx, 2, StatusEnabled)

		err := repo.IncrementTriggerFailureCount(ctx, trigger, errors.New("webhook timeout"), 5)
		require.NoError(t, err)

		// Verify failure count incremented
		updatedTrigger, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger.ID, common.BytesToAddress(trigger.DeveloperLicenseAddress))
		require.NoError(t, err)
		assert.Equal(t, 3, updatedTrigger.FailureCount)
		assert.Equal(t, StatusEnabled, updatedTrigger.Status) // Still enabled
	})

	t.Run("disable webhook when reaching failure threshold", func(t *testing.T) {
		trigger := createTestTriggerWithFailures(t, repo, ctx, 4, StatusEnabled)

		err := repo.IncrementTriggerFailureCount(ctx, trigger, errors.New("webhook error"), 5)
		require.NoError(t, err)

		// Verify webhook was disabled
		updatedTrigger, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger.ID, common.BytesToAddress(trigger.DeveloperLicenseAddress))
		require.NoError(t, err)
		assert.Equal(t, 5, updatedTrigger.FailureCount)
		assert.Equal(t, StatusFailed, updatedTrigger.Status)
	})
}

// Helper function
func createTestTriggerWithFailures(t *testing.T, repo *Repository, ctx context.Context, failureCount int, status string) *models.Trigger {
	req := CreateTriggerRequest{
		Service:                 "Telemetry",
		MetricName:              "speed",
		Condition:               "valueNumber > 20",
		TargetURI:               "https://example.com/webhook",
		Status:                  status,
		Description:             "Test trigger",
		CooldownPeriod:          10,
		DeveloperLicenseAddress: tests.RandomAddr(t),
	}

	trigger, err := repo.CreateTrigger(ctx, req)
	require.NoError(t, err)

	// Update failure count if needed
	if failureCount > 0 {
		trigger.FailureCount = failureCount
		trigger.Status = status
		err = repo.UpdateTrigger(ctx, trigger)
		require.NoError(t, err)
	}

	err = trigger.Reload(ctx, repo.db)
	require.NoError(t, err)

	return trigger
}

func TestCreateTriggerLog(t *testing.T) {
	t.Parallel()
	tc := tests.SetupTestContainer(t)

	repo := NewRepository(tc.DB)
	ctx := context.Background()

	// Create a test trigger first
	baseReq := CreateTriggerRequest{
		Service:                 "Telemetry",
		MetricName:              "speed",
		Condition:               "valueNumber > 20",
		TargetURI:               "https://example.com/webhook",
		Status:                  StatusEnabled,
		Description:             "Speed alert",
		CooldownPeriod:          10,
		DeveloperLicenseAddress: tests.RandomAddr(t),
	}

	trigger, err := repo.CreateTrigger(ctx, baseReq)
	require.NoError(t, err)
	require.NotNil(t, trigger)

	assetDid := randAssetDID(t)

	t.Run("successful creation", func(t *testing.T) {
		log := &models.TriggerLog{
			ID:              uuid.New().String(),
			TriggerID:       trigger.ID,
			AssetDid:        assetDid.String(),
			SnapshotData:    []byte(`{"speed": 25, "timestamp": "2023-10-01T12:00:00Z"}`),
			LastTriggeredAt: time.Now().UTC(),
			CreatedAt:       time.Now().UTC(),
		}

		err := repo.CreateTriggerLog(ctx, log)
		require.NoError(t, err)
	})

	t.Run("create multiple logs for same trigger", func(t *testing.T) {
		log1 := &models.TriggerLog{
			ID:              uuid.New().String(),
			TriggerID:       trigger.ID,
			AssetDid:        assetDid.String(),
			SnapshotData:    []byte(`{"speed": 30, "timestamp": "2023-10-01T13:00:00Z"}`),
			LastTriggeredAt: time.Now().UTC(),
			CreatedAt:       time.Now().UTC(),
		}

		log2 := &models.TriggerLog{
			ID:              uuid.New().String(),
			TriggerID:       trigger.ID,
			AssetDid:        assetDid.String(),
			SnapshotData:    []byte(`{"speed": 35, "timestamp": "2023-10-01T14:00:00Z"}`),
			LastTriggeredAt: time.Now().UTC(),
			CreatedAt:       time.Now().UTC(),
		}

		err := repo.CreateTriggerLog(ctx, log1)
		require.NoError(t, err)

		err = repo.CreateTriggerLog(ctx, log2)
		require.NoError(t, err)
	})

	t.Run("create log with failure reason", func(t *testing.T) {
		log := &models.TriggerLog{
			ID:              uuid.New().String(),
			TriggerID:       trigger.ID,
			AssetDid:        assetDid.String(),
			SnapshotData:    []byte(`{"speed": 40, "timestamp": "2023-10-01T15:00:00Z"}`),
			LastTriggeredAt: time.Now().UTC(),
			CreatedAt:       time.Now().UTC(),
			FailureReason:   null.StringFrom("Webhook timeout after 30 seconds"),
		}

		err := repo.CreateTriggerLog(ctx, log)
		require.NoError(t, err)
	})

	t.Run("create log for non-existent trigger", func(t *testing.T) {
		nonExistentTriggerID := uuid.New().String()
		log := &models.TriggerLog{
			ID:              uuid.New().String(),
			TriggerID:       nonExistentTriggerID,
			AssetDid:        assetDid.String(),
			SnapshotData:    []byte(`{"speed": 45, "timestamp": "2023-10-01T16:00:00Z"}`),
			LastTriggeredAt: time.Now().UTC(),
			CreatedAt:       time.Now().UTC(),
		}

		err := repo.CreateTriggerLog(ctx, log)
		require.Error(t, err)
		var richErr richerrors.Error
		require.ErrorAs(t, err, &richErr)
		assert.Equal(t, http.StatusInternalServerError, richErr.Code)
	})

	t.Run("create log with missing required fields", func(t *testing.T) {
		// Test with empty trigger ID
		log := &models.TriggerLog{
			ID:              uuid.New().String(),
			TriggerID:       "",
			AssetDid:        assetDid.String(),
			SnapshotData:    []byte(`{"speed": 50, "timestamp": "2023-10-01T17:00:00Z"}`),
			LastTriggeredAt: time.Now().UTC(),
			CreatedAt:       time.Now().UTC(),
		}

		err := repo.CreateTriggerLog(ctx, log)
		require.Error(t, err)
		var richErr richerrors.Error
		require.ErrorAs(t, err, &richErr)
		assert.Equal(t, http.StatusBadRequest, richErr.Code)
	})

	t.Run("create log with empty asset DID", func(t *testing.T) {
		log := &models.TriggerLog{
			ID:              uuid.New().String(),
			TriggerID:       trigger.ID,
			AssetDid:        "",
			SnapshotData:    []byte(`{"speed": 55, "timestamp": "2023-10-01T18:00:00Z"}`),
			LastTriggeredAt: time.Now().UTC(),
			CreatedAt:       time.Now().UTC(),
		}

		err := repo.CreateTriggerLog(ctx, log)
		require.Error(t, err)
		var richErr richerrors.Error
		require.ErrorAs(t, err, &richErr)
		assert.Equal(t, http.StatusBadRequest, richErr.Code)
	})

	t.Run("create log with duplicate ID", func(t *testing.T) {
		logID := uuid.New().String()

		log1 := &models.TriggerLog{
			ID:              logID,
			TriggerID:       trigger.ID,
			AssetDid:        assetDid.String(),
			SnapshotData:    []byte(`{"speed": 60, "timestamp": "2023-10-01T19:00:00Z"}`),
			LastTriggeredAt: time.Now().UTC(),
			CreatedAt:       time.Now().UTC(),
		}

		log2 := &models.TriggerLog{
			ID:              logID, // Same ID
			TriggerID:       trigger.ID,
			AssetDid:        assetDid.String(),
			SnapshotData:    []byte(`{"speed": 65, "timestamp": "2023-10-01T20:00:00Z"}`),
			LastTriggeredAt: time.Now().UTC(),
			CreatedAt:       time.Now().UTC(),
		}

		// First log should succeed
		err := repo.CreateTriggerLog(ctx, log1)
		require.NoError(t, err)

		// Second log with duplicate ID should fail
		err = repo.CreateTriggerLog(ctx, log2)
		require.Error(t, err)
		var richErr richerrors.Error
		require.ErrorAs(t, err, &richErr)
		assert.Equal(t, http.StatusInternalServerError, richErr.Code)
	})

	t.Run("create log with complex snapshot data", func(t *testing.T) {
		complexSnapshot := map[string]interface{}{
			"speed":       25.5,
			"temperature": 85.2,
			"fuel_level":  0.75,
			"location": map[string]float64{
				"latitude":  40.7128,
				"longitude": -74.0060,
			},
			"metadata": map[string]interface{}{
				"vehicle_id": "VIN123456789",
				"trip_id":    "TRIP789",
				"event_time": "2023-10-01T21:00:00Z",
			},
		}

		snapshotBytes, err := json.Marshal(complexSnapshot)
		require.NoError(t, err)

		log := &models.TriggerLog{
			ID:              uuid.New().String(),
			TriggerID:       trigger.ID,
			AssetDid:        assetDid.String(),
			SnapshotData:    snapshotBytes,
			LastTriggeredAt: time.Now().UTC(),
			CreatedAt:       time.Now().UTC(),
		}

		err = repo.CreateTriggerLog(ctx, log)
		require.NoError(t, err)
	})
}

func randAssetDID(t *testing.T) cloudevent.ERC721DID {
	tokenID := make([]byte, 32)
	_, err := rand.Read(tokenID)
	if err != nil {
		t.Fatalf("couldn't create a test token ID: %v", err)
	}
	return cloudevent.ERC721DID{
		ChainID:         137,
		ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
		TokenID:         new(big.Int).SetBytes(tokenID),
	}
}
