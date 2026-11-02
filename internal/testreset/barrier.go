package testreset

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

const deliveryLock = int64(13850001)
const DrainTimeout = 30 * time.Second

func Delivery(db *gorm.DB, work func(*gorm.DB)) error {
	return locked(context.Background(), db, true, func(conn *gorm.DB) error {
		work(conn)
		return nil
	})
}

func Reset(ctx context.Context, db *gorm.DB, work func(*gorm.DB) error) error {
	return locked(ctx, db, false, work)
}

func locked(ctx context.Context, db *gorm.DB, shared bool, work func(*gorm.DB) error) error {
	return db.WithContext(ctx).Connection(func(conn *gorm.DB) (result error) {
		lock, unlock := "pg_advisory_lock", "pg_advisory_unlock"
		if shared {
			lock, unlock = "pg_advisory_lock_shared", "pg_advisory_unlock_shared"
		}
		if err := conn.WithContext(ctx).Exec("SELECT "+lock+"(?)", deliveryLock).Error; err != nil {
			return fmt.Errorf("delivery drain did not complete; reset not performed: %w", err)
		}
		defer func() {
			if err := conn.WithContext(context.Background()).Exec("SELECT "+unlock+"(?)", deliveryLock).Error; err != nil && result == nil {
				result = fmt.Errorf("release test reset barrier: %w", err)
			}
		}()
		return work(conn.WithContext(ctx))
	})
}
