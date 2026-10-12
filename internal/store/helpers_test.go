package store_test

import (
	"database/sql"

	"github.com/0xGurg/himay/internal/store"
)

func sqlOpen(path string) (*sql.DB, error) { return sql.Open("sqlite", store.DSN(path)) }
