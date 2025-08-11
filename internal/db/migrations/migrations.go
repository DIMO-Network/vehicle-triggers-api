package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sync"

	"github.com/DIMO-Network/shared/pkg/db"
	"github.com/pressly/goose/v3"
)

// SchemaName is the name of the schema to use for the database.
const SchemaName = "vehicle_triggers_api"

//go:embed *.sql
var baseFS embed.FS

var migrationLock sync.Mutex

// RunGoose runs the goose command with the provided arguments.
// args should be the command and the arguments to pass to goose.
// eg RunGoose(ctx, []string{"up", "-v"}, db).
func RunGoose(ctx context.Context, gooseArgs []string, settings db.Settings) error {
	db, err := setupDatabase(ctx, settings)
	if err != nil {
		return fmt.Errorf("failed to setup database: %w", err)
	}
	migrationLock.Lock()
	defer migrationLock.Unlock()
	if len(gooseArgs) == 0 {
		return fmt.Errorf("command not provided")
	}
	cmd := gooseArgs[0]
	var args []string
	if len(gooseArgs) > 1 {
		args = gooseArgs[1:]
	}
	setMigrations(baseFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("failed to set dialect: %w", err)
	}
	goose.SetTableName(SchemaName + ".migrations")
	err = goose.RunContext(ctx, cmd, db, ".", args...)
	if err != nil {
		return fmt.Errorf("failed to run goose command: %w", err)
	}
	return nil
}

// setMigrations sets the migrations for the goose tool.
// this will reset the global migrations and FS to avoid any unwanted migrations registers.
func setMigrations(baseFS embed.FS) {
	goose.SetBaseFS(baseFS)
	goose.ResetGlobalMigrations()
}
func setupDatabase(ctx context.Context, settings db.Settings) (*sql.DB, error) {
	// setup database
	db, err := sql.Open("postgres", settings.BuildConnectionString(true))
	if err != nil {
		return nil, fmt.Errorf("failed to open db connection: %w", err)
	}
	if err = db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping db: %w", err)
	}

	_, err = db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS "+SchemaName+";")
	if err != nil {
		return nil, fmt.Errorf("could not create schema: %w", err)
	}

	return db, nil
}
