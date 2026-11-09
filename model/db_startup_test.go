package model

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql/driver"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrimaryDatabaseStartupConfiguration(t *testing.T) {
	for _, dsn := range []string{"", "local", "local-test", "clickhouse://127.0.0.1:1/default"} {
		t.Run("skip_"+strings.Split(dsn, ":")[0], func(t *testing.T) {
			t.Setenv("SQL_DSN", dsn)
			t.Setenv("SQL_STARTUP_TIMEOUT_SECONDS", "1")
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			require.NoError(t, waitForPrimaryDatabase(ctx))
		})
	}
	t.Setenv("SQL_DSN", "invalid-dsn")
	t.Setenv("SQL_STARTUP_TIMEOUT_SECONDS", "0")
	require.NoError(t, waitForPrimaryDatabase(context.Background()))
	for _, value := range []string{"-1", "not-a-number", "3601"} {
		t.Run("invalid_budget_"+value, func(t *testing.T) {
			t.Setenv("SQL_STARTUP_TIMEOUT_SECONDS", value)
			err := waitForPrimaryDatabase(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "SQL_STARTUP_TIMEOUT_SECONDS")
		})
	}
	for _, dsn := range []string{
		"probe:startup-secret@tcp(127.0.0.1:1)/probe?tls=unregistered-startup-test",
		"probe:startup-secret@tcp(broken-address/probe",
		"postgres://probe:startup-secret@localhost/probe?sslmode=invalid",
		"postgres://probe:startup-secret@localhost/probe?sslrootcert=/missing-startup-cert&sslmode=verify-full",
	} {
		t.Setenv("SQL_DSN", dsn)
		t.Setenv("SQL_STARTUP_TIMEOUT_SECONDS", "20")
		started := time.Now()
		err := waitForPrimaryDatabase(context.Background())
		require.Error(t, err)
		assert.False(t, strings.Contains(err.Error(), "startup-secret"), "configuration errors must redact passwords")
		assert.Less(t, time.Since(started), 2*time.Second, "configuration errors must fail immediately")
	}
	for _, network := range []string{"tcp", "tcp4", "tcp6"} {
		t.Run("invalid_port_"+network, func(t *testing.T) {
			t.Setenv("SQL_DSN", "probe:startup-secret@"+network+"(mysql:3306x)/probe")
			t.Setenv("SQL_STARTUP_TIMEOUT_SECONDS", "20")
			started := time.Now()
			err := waitForPrimaryDatabase(context.Background())
			require.Error(t, err)
			assert.False(t, strings.Contains(err.Error(), "startup-secret"), "invalid-port errors must redact passwords")
			assert.Less(t, time.Since(started), 2*time.Second, "invalid ports must fail before DNS or startup retries")
			t.Logf("invalid port %s rejected after %s", network, time.Since(started).Round(time.Millisecond))
		})
	}
	// Docker service names need not resolve while the connector is constructed.
	db, err := openPrimaryStartupDatabase("probe:startup-secret@tcp(mysql:3306)/probe")
	require.NoError(t, err)
	require.NoError(t, db.Close())
}

func TestPrimaryDatabaseStartupErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"refused", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, true},
		{"temporary_dns", &net.DNSError{Err: "temporary failure", IsTemporary: true}, true},
		{"eof", io.EOF, true},
		{"bad_connection", driver.ErrBadConn, true},
		{"mysql_invalid_connection", mysqldriver.ErrInvalidConn, true},
		{"mysql_starting", &mysqldriver.MySQLError{Number: 1053}, true},
		{"postgres_starting", &pgconn.PgError{Code: "57P03"}, true},
		{"mysql_password", &mysqldriver.MySQLError{Number: 1045}, false},
		{"mysql_database", &mysqldriver.MySQLError{Number: 1049}, false},
		{"postgres_password", &pgconn.PgError{Code: "28P01"}, false},
		{"postgres_database", &pgconn.PgError{Code: "3D000"}, false},
		{"tls_certificate", &net.OpError{Op: "dial", Err: x509.UnknownAuthorityError{}}, false},
		{"tls_record", tls.RecordHeaderError{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isTransientDBStartupError(fmt.Errorf("wrapped: %w", tc.err)))
		})
	}
}

func TestPrimaryDatabaseStartupDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	t.Setenv("SQL_DSN", "probe@tcp("+address+")/probe")
	for _, name := range []string{"budget", "parent_deadline", "cancel"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SQL_STARTUP_TIMEOUT_SECONDS", "1")
			ctx := context.Background()
			want := context.DeadlineExceeded
			if name == "parent_deadline" {
				t.Setenv("SQL_STARTUP_TIMEOUT_SECONDS", "") // Exercise the default budget.
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 150*time.Millisecond)
				defer cancel()
			} else if name == "cancel" {
				t.Setenv("SQL_STARTUP_TIMEOUT_SECONDS", "20")
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				timer := time.AfterFunc(150*time.Millisecond, cancel)
				defer timer.Stop()
				want = context.Canceled
			}
			started := time.Now()
			err := waitForPrimaryDatabase(ctx)
			require.ErrorIs(t, err, want)
			elapsed := time.Since(started)
			assert.Less(t, elapsed, 2*time.Second)
			if name == "budget" {
				assert.GreaterOrEqual(t, elapsed, 900*time.Millisecond)
			}
			t.Logf("%s returned after %s", name, elapsed.Round(time.Millisecond))
		})
	}
}

type startupPersistenceProbe struct {
	ID    int `gorm:"primaryKey"`
	Value string
}

func TestPrimaryDatabaseStartupDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			dsn := "local"
			if dialect != "sqlite" {
				dsn = os.Getenv("TEST_" + strings.ToUpper(dialect) + "_DSN")
				if dsn == "" {
					t.Skip("isolated TEST_" + strings.ToUpper(dialect) + "_DSN is not configured")
				}
			}
			previousPath := common.SQLitePath
			common.SQLitePath = filepath.Join(t.TempDir(), "startup.db")
			t.Cleanup(func() { common.SQLitePath = previousPath })
			t.Setenv("SQL_DSN", dsn)
			t.Setenv("SQL_STARTUP_TIMEOUT_SECONDS", "20")
			require.NoError(t, waitForPrimaryDatabase(context.Background()))
			db, dbType, err := chooseDB("SQL_DSN", false)
			require.NoError(t, err)
			assert.Equal(t, common.DatabaseType(dialect), dbType)
			connection, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, connection.Close()) })
			tables, err := db.Migrator().GetTables()
			require.NoError(t, err)
			require.Empty(t, tables, "use a fresh isolated database; startup readiness must not migrate it")
			versionSQL := "SELECT version()"
			if dialect == "sqlite" {
				versionSQL = "SELECT sqlite_version()"
			}
			var version string
			require.NoError(t, db.Raw(versionSQL).Scan(&version).Error)
			t.Logf("engine=%s version=%s", dialect, version)

			require.NoError(t, db.AutoMigrate(&startupPersistenceProbe{}))
			require.NoError(t, db.Create(&startupPersistenceProbe{ID: 1, Value: "persisted-before-wait"}).Error)
			before, err := db.Migrator().GetTables()
			require.NoError(t, err)
			if dialect != "sqlite" {
				address := ""
				if dialect == "mysql" {
					config, err := mysqldriver.ParseDSN(dsn)
					require.NoError(t, err)
					address = config.Addr
				} else {
					parsed, err := url.Parse(dsn)
					require.NoError(t, err)
					address = parsed.Host
				}
				proxy, ready, accepted, active := delayedStartupProxy(t, address, 0)
				t.Setenv("SQL_DSN", startupDSNAtAddress(t, dialect, dsn, proxy))
				started := time.Now()
				require.NoError(t, waitForPrimaryDatabase(context.Background()))
				require.NoError(t, <-ready)
				assert.GreaterOrEqual(t, time.Since(started), time.Second, "same process must survive the initially refused connection")
				assert.GreaterOrEqual(t, accepted.Load(), int64(1))
				assert.Eventually(t, func() bool { return active.Load() == 0 }, time.Second, 10*time.Millisecond, "readiness must close its independent connection")
				t.Logf("same-process delayed recovery=%s; startup connection closed", time.Since(started).Round(time.Millisecond))
				t.Setenv("SQL_DSN", dsn)
				if dialect == "mysql" {
					t.Run("authentication_disconnect_recovers", func(t *testing.T) {
						proxy, ready, accepted, active := delayedStartupProxy(t, address, 2)
						require.NoError(t, <-ready)
						proxyDSN := startupDSNAtAddress(t, dialect, dsn, proxy)
						probe, err := openPrimaryStartupDatabase(proxyDSN)
						require.NoError(t, err)
						t.Cleanup(func() { assert.NoError(t, probe.Close()) })
						ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
						defer cancel()
						err = probe.PingContext(ctx)
						require.ErrorIs(t, err, mysqldriver.ErrInvalidConn, "real MySQL authentication EOF must exercise the driver sentinel")
						require.True(t, isTransientDBStartupError(err))
						require.NoError(t, probe.Close())
						t.Setenv("SQL_DSN", proxyDSN)
						started := time.Now()
						require.NoError(t, waitForPrimaryDatabase(context.Background()))
						assert.EqualValues(t, 3, accepted.Load(), "one diagnostic drop, one startup drop, then a real successful authentication")
						assert.GreaterOrEqual(t, time.Since(started), time.Second)
						assert.Eventually(t, func() bool { return active.Load() == 0 }, time.Second, 10*time.Millisecond)
						t.Logf("real MySQL ErrInvalidConn verified; authentication disconnect recovery=%s", time.Since(started).Round(time.Millisecond))
					})
				}
			}
			require.NoError(t, waitForPrimaryDatabase(context.Background()))
			after, err := db.Migrator().GetTables()
			require.NoError(t, err)
			assert.ElementsMatch(t, before, after, "readiness must not change schema")
			var count int64
			require.NoError(t, db.Model(&startupPersistenceProbe{}).Count(&count).Error)
			assert.EqualValues(t, 1, count, "repeated readiness must not duplicate writes")
			var row startupPersistenceProbe
			require.NoError(t, db.First(&row, 1).Error)
			assert.Equal(t, "persisted-before-wait", row.Value)
			require.NoError(t, db.Model(&row).Update("value", "persisted-after-wait").Error)
			require.NoError(t, connection.Close())
			reopened, _, err := chooseDB("SQL_DSN", false)
			require.NoError(t, err)
			reopenedConnection, err := reopened.DB()
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, reopenedConnection.Close()) })
			require.NoError(t, reopened.First(&row, 1).Error)
			assert.Equal(t, "persisted-after-wait", row.Value, "existing chooseDB path must retain persisted writes")

			if dialect != "sqlite" {
				for _, failure := range []string{"password", "unknown_database"} {
					t.Run(failure, func(t *testing.T) {
						badDSN, secret := dsn, ""
						if dialect == "mysql" {
							config, err := mysqldriver.ParseDSN(dsn)
							require.NoError(t, err)
							secret = config.Passwd
							if failure == "password" {
								config.Passwd += "-wrong"
							} else {
								config.DBName = "db_startup_does_not_exist"
							}
							badDSN = config.FormatDSN()
						} else {
							parsed, err := url.Parse(dsn)
							require.NoError(t, err)
							secret, _ = parsed.User.Password()
							if failure == "password" {
								parsed.User = url.UserPassword(parsed.User.Username(), secret+"-wrong")
							} else {
								parsed.Path = "/db_startup_does_not_exist"
							}
							badDSN = parsed.String()
						}
						t.Setenv("SQL_DSN", badDSN)
						started := time.Now()
						err := waitForPrimaryDatabase(context.Background())
						require.Error(t, err)
						assert.False(t, isTransientDBStartupError(err))
						if secret != "" {
							assert.False(t, strings.Contains(err.Error(), secret), "returned error must redact credentials")
						}
						assert.Less(t, time.Since(started), 2*time.Second, "permanent errors must not consume the 20-second budget")
						t.Logf("permanent %s rejected after %s", failure, time.Since(started).Round(time.Millisecond))
					})
				}
			}
		})
	}
}

func startupDSNAtAddress(t *testing.T, dialect, dsn, address string) string {
	t.Helper()
	if dialect == "mysql" {
		config, err := mysqldriver.ParseDSN(dsn)
		require.NoError(t, err)
		config.Addr = address
		return config.FormatDSN()
	}
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	parsed.Host = address
	return parsed.String()
}

// The port is initially unbound, then forwards the real database handshake and
// authentication. This is deliberately not a fake database or sqlmock driver.
func delayedStartupProxy(t *testing.T, target string, mysqlAuthDrops int64) (string, <-chan error, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := reservation.Addr().String()
	require.NoError(t, reservation.Close())
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if connection != nil {
		_ = connection.Close()
	}
	require.Error(t, err, "proxy must start with no listener")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ready := make(chan error, 1)
	accepted, active := &atomic.Int64{}, &atomic.Int64{}
	go func() {
		timer := time.NewTimer(200 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			ready <- ctx.Err()
			return
		case <-timer.C:
		}
		listener, err := net.Listen("tcp", address)
		ready <- err
		if err != nil {
			return
		}
		defer listener.Close()
		go func() { <-ctx.Done(); _ = listener.Close() }()
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			dropAuthentication := accepted.Add(1) <= mysqlAuthDrops
			active.Add(1)
			go func() {
				defer active.Add(-1)
				defer client.Close()
				upstream, err := net.DialTimeout("tcp", target, time.Second)
				if err != nil {
					return
				}
				defer upstream.Close()
				if dropAuthentication {
					// Forward the real server greeting and client authentication,
					// then cut the connection before delivering the server's reply.
					if forwardStartupMySQLPacket(client, upstream) == nil {
						_ = forwardStartupMySQLPacket(upstream, client)
					}
					return
				}
				copied := make(chan struct{})
				go func() { _, _ = io.Copy(upstream, client); _ = upstream.Close(); close(copied) }()
				_, _ = io.Copy(client, upstream)
				_ = client.Close()
				<-copied
			}()
		}
	}()
	return address, ready, accepted, active
}

func forwardStartupMySQLPacket(destination io.Writer, source io.Reader) error {
	var header [4]byte
	if _, err := io.ReadFull(source, header[:]); err != nil {
		return err
	}
	if _, err := destination.Write(header[:]); err != nil {
		return err
	}
	length := int64(header[0]) | int64(header[1])<<8 | int64(header[2])<<16
	_, err := io.CopyN(destination, source, length)
	return err
}
