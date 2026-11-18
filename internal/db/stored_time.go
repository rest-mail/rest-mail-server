package db

import (
	"reflect"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// storedTimePrecision is the finest instant a PostgreSQL timestamp column holds.
const storedTimePrecision = time.Microsecond

var (
	storedTimeType        = reflect.TypeFor[time.Time]()
	storedTimePointerType = reflect.TypeFor[*time.Time]()
)

// StoredClock reads now at the precision the database stores, so a row's timestamps are
// the same in the answer to its write as in every later read (#2044).
func StoredClock(now func() time.Time) func() time.Time {
	return func() time.Time { return now().Truncate(storedTimePrecision) }
}

// keepStoredTimes makes every created row carry its timestamps as they are stored,
// including those its caller set from the system clock, which is finer than a column on
// Linux. It runs after the models' own create hooks, which can set timestamps too.
func keepStoredTimes(database *gorm.DB) error {
	return database.Callback().Create().Before("gorm:create").Register("restmail:stored_times", storedTimes)
}

func storedTimes(tx *gorm.DB) {
	statement := tx.Statement
	if statement.Schema == nil {
		return
	}
	var fields []*schema.Field
	for _, field := range statement.Schema.Fields {
		if field.DBName != "" && (field.FieldType == storedTimeType || field.FieldType == storedTimePointerType) {
			fields = append(fields, field)
		}
	}
	if len(fields) == 0 {
		return
	}
	rows := statement.ReflectValue
	switch rows.Kind() {
	case reflect.Struct:
		storeRowTimes(tx, fields, rows)
	case reflect.Slice, reflect.Array:
		for index := range rows.Len() {
			if row := reflect.Indirect(rows.Index(index)); row.Kind() == reflect.Struct {
				storeRowTimes(tx, fields, row)
			}
		}
	}
}

func storeRowTimes(tx *gorm.DB, fields []*schema.Field, row reflect.Value) {
	context := tx.Statement.Context
	for _, field := range fields {
		value, zero := field.ValueOf(context, row)
		if zero {
			continue
		}
		target := field.ReflectValueOf(context, row)
		if !target.CanSet() {
			continue
		}
		switch at := value.(type) {
		case time.Time:
			target.Set(reflect.ValueOf(at.Truncate(storedTimePrecision)))
		case *time.Time:
			// A new value: the caller may hold this pointer, or share it between fields.
			stored := at.Truncate(storedTimePrecision)
			target.Set(reflect.ValueOf(&stored))
		}
	}
}
