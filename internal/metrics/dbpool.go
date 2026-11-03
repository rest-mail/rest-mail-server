package metrics

import (
	"database/sql"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// IMAPCommandNames are the command label values restmail_imap_command_duration_seconds
// takes: the commands the IMAP gateway implements. A UID command is labelled
// UID whatever it wraps.
var IMAPCommandNames = []string{
	"APPEND", "AUTHENTICATE", "CAPABILITY", "CHECK", "CLOSE", "COPY", "CREATE",
	"DELETE", "ENABLE", "EXAMINE", "EXPUNGE", "FETCH", "GETQUOTA", "GETQUOTAROOT",
	"IDLE", "LIST", "LOGIN", "LOGOUT", "LSUB", "MOVE", "NAMESPACE", "NOOP",
	"RENAME", "SEARCH", "SELECT", "STARTTLS", "STATUS", "STORE", "SUBSCRIBE",
	"UID", "UNSELECT", "UNSUBSCRIBE",
}

var imapCommandSet = func() map[string]bool {
	m := make(map[string]bool, len(IMAPCommandNames))
	for _, n := range IMAPCommandNames {
		m[n] = true
	}
	return m
}()

// ObserveIMAPCommand records one IMAP command's duration, labelling any name
// outside IMAPCommandNames "other" so a client cannot grow the series.
func ObserveIMAPCommand(name string, d time.Duration) {
	label := strings.ToUpper(name)
	if !imapCommandSet[label] {
		label = "other"
	}
	IMAPCommandDuration.WithLabelValues(label).Observe(d.Seconds())
}

// dbPool exports the sql.DBStats of the process's database pool, read at
// scrape time. It is registered once in init and reports nothing until
// TrackDBPool hands it a pool, so a process without a database exports no
// pool series.
var dbPool = &dbPoolCollector{
	maxOpen:   prometheus.NewDesc("restmail_db_pool_max_open_connections", "Maximum open connections the database pool allows (0: unlimited)", nil, nil),
	open:      prometheus.NewDesc("restmail_db_pool_open_connections", "Open connections in the database pool, in use and idle", nil, nil),
	inUse:     prometheus.NewDesc("restmail_db_pool_in_use_connections", "Database pool connections in use", nil, nil),
	idle:      prometheus.NewDesc("restmail_db_pool_idle_connections", "Idle database pool connections", nil, nil),
	waitCount: prometheus.NewDesc("restmail_db_pool_wait_count_total", "Times a caller waited for a database pool connection", nil, nil),
	waitDur:   prometheus.NewDesc("restmail_db_pool_wait_duration_seconds_total", "Total time callers waited for a database pool connection", nil, nil),
}

type dbPoolCollector struct {
	db atomic.Pointer[sql.DB]

	maxOpen, open, inUse, idle, waitCount, waitDur *prometheus.Desc
}

// TrackDBPool makes db the pool the restmail_db_pool_* series describe. Every
// process that opens the database calls it through db.Connect; a process that
// opens a second pool reports the latest one.
func TrackDBPool(db *sql.DB) { dbPool.db.Store(db) }

func (c *dbPoolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{c.maxOpen, c.open, c.inUse, c.idle, c.waitCount, c.waitDur} {
		ch <- d
	}
}

func (c *dbPoolCollector) Collect(ch chan<- prometheus.Metric) {
	db := c.db.Load()
	if db == nil {
		return
	}
	s := db.Stats()
	ch <- prometheus.MustNewConstMetric(c.maxOpen, prometheus.GaugeValue, float64(s.MaxOpenConnections))
	ch <- prometheus.MustNewConstMetric(c.open, prometheus.GaugeValue, float64(s.OpenConnections))
	ch <- prometheus.MustNewConstMetric(c.inUse, prometheus.GaugeValue, float64(s.InUse))
	ch <- prometheus.MustNewConstMetric(c.idle, prometheus.GaugeValue, float64(s.Idle))
	ch <- prometheus.MustNewConstMetric(c.waitCount, prometheus.CounterValue, float64(s.WaitCount))
	ch <- prometheus.MustNewConstMetric(c.waitDur, prometheus.CounterValue, s.WaitDuration.Seconds())
}
