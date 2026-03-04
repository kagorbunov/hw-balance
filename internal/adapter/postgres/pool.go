package postgres

import (
	"database/sql"

	"hw-balance/pkg/postgres"
)

func NewDB(databaseURL string) (*sql.DB, error) {
	return postgres.NewDB(databaseURL)
}

func NewDBWithConfig(databaseURL string, cfg postgres.PoolConfig) (*sql.DB, error) {
	return postgres.NewDBWithConfig(databaseURL, cfg)
}
