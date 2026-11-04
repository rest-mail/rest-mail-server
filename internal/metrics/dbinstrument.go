package metrics

import (
	"context"
	"database/sql/driver"
	"errors"
	"net"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// InstrumentDB puts a process's database on its /metrics: the
// restmail_db_pool_* series for its pool, and
// restmail_db_connection_errors_total for every GORM operation that fails
// because the database could not be reached (#1579). Every process that holds
// a pool calls it once, after connecting; a nil db (a gateway that started
// without its database) does nothing.
func InstrumentDB(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	TrackDBPool(sqlDB)

	after := func(tx *gorm.DB) {
		if IsDBConnectionError(tx.Error) {
			DBConnectionErrors.Inc()
		}
	}
	cb := db.Callback()
	const name = "restmail:db_connection_errors"
	return errors.Join(
		cb.Create().After("gorm:create").Register(name, after),
		cb.Query().After("gorm:query").Register(name, after),
		cb.Update().After("gorm:update").Register(name, after),
		cb.Delete().After("gorm:delete").Register(name, after),
		cb.Row().After("gorm:row").Register(name, after),
		cb.Raw().After("gorm:raw").Register(name, after),
	)
}

// IsDBConnectionError says whether err means the database could not be
// reached (refused, dropped or timed out while connecting or talking to it),
// rather than that it answered a query with an error. A cancelled context is
// the caller's choice, not an outage.
func IsDBConnectionError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var connectErr *pgconn.ConnectError
	if errors.As(err, &connectErr) {
		return true
	}
	if errors.Is(err, driver.ErrBadConn) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}
