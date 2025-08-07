package triggersrepo

import (
	"database/sql"
	"errors"

	"github.com/lib/pq"
)

const (
	ValidationError = constError("invalid request")
	// DuplicateKeyError is returned when a duplicate key error occurs.
	DuplicateKeyError = pq.ErrorCode("23505")

	ForeignKeyViolation = pq.ErrorCode("23503")
)

// IsDuplicateKeyError checks if the error is a duplicate key error.
func IsDuplicateKeyError(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == DuplicateKeyError
}

// IsNoRowsError checks if the error is a no rows error.
func IsNoRowsError(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

func IsValidationError(err error) bool {
	return errors.Is(err, ValidationError)
}

type constError string

func (e constError) Error() string {
	return string(e)
}
