package model

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Only readiness is retried. Schema checks and migrations still run once, in
// their existing order, after a real authenticated connection succeeds.
func waitForPrimaryDatabase(parent context.Context) error {
	dsn := os.Getenv("SQL_DSN")
	if dsn == "" || strings.HasPrefix(dsn, "local") || isClickHouseDSN(dsn) {
		return nil
	}
	seconds := 120
	if value := os.Getenv("SQL_STARTUP_TIMEOUT_SECONDS"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 || parsed > 3600 {
			return errors.New("SQL_STARTUP_TIMEOUT_SECONDS must be between 0 and 3600")
		}
		seconds = parsed
	}
	if seconds == 0 {
		return nil
	}
	db, err := openPrimaryStartupDatabase(dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	ctx, cancel := context.WithTimeout(parent, time.Duration(seconds)*time.Second)
	defer cancel()
	waiting := false
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("primary database startup wait ended: %w", err)
		}
		err = db.PingContext(ctx)
		if err == nil {
			if waiting {
				common.SysLog(common.LogText("primary database is ready; continuing startup"))
			}
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("primary database startup wait ended: %w", ctx.Err())
		}
		if !isTransientDBStartupError(err) {
			return err
		}
		if !waiting {
			common.SysLog(common.LogText("primary database is not ready; waiting up to %d seconds for startup", seconds))
			waiting = true
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("primary database startup wait ended: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func openPrimaryStartupDatabase(dsn string) (*sql.DB, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		config, err := pgx.ParseConfig(dsn)
		if err != nil {
			// Parser diagnostics may contain the supplied DSN and its password.
			return nil, errors.New("invalid PostgreSQL SQL_DSN")
		}
		config.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
		return sql.OpenDB(stdlib.GetConnector(*config)), nil
	}
	config, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("invalid MySQL SQL_DSN")
	}
	if config.Net == "tcp" || config.Net == "tcp4" || config.Net == "tcp6" {
		_, port, err := net.SplitHostPort(config.Addr)
		if err != nil {
			return nil, errors.New("invalid MySQL SQL_DSN")
		}
		if _, err := net.LookupPort(config.Net, port); err != nil {
			return nil, errors.New("invalid MySQL SQL_DSN")
		}
	}
	connector, err := mysqldriver.NewConnector(config)
	if err != nil {
		return nil, errors.New("invalid MySQL SQL_DSN")
	}
	return sql.OpenDB(connector), nil
}

func isTransientDBStartupError(err error) bool {
	var mysqlError *mysqldriver.MySQLError
	if errors.As(err, &mysqlError) {
		return mysqlError.Number == 1040 || mysqlError.Number == 1053 || mysqlError.Number == 1203
	}
	var postgresError interface{ SQLState() string }
	if errors.As(err, &postgresError) {
		switch postgresError.SQLState() {
		case "57P01", "57P02", "57P03", "53300":
			return true
		default:
			return false
		}
	}
	var unknownAuthority x509.UnknownAuthorityError
	var invalidCertificate x509.CertificateInvalidError
	var hostnameError x509.HostnameError
	var verificationError *tls.CertificateVerificationError
	var recordError tls.RecordHeaderError
	var addressError *net.AddrError
	var unknownNetwork net.UnknownNetworkError
	if errors.As(err, &unknownAuthority) || errors.As(err, &invalidCertificate) || errors.As(err, &hostnameError) ||
		errors.As(err, &verificationError) || errors.As(err, &recordError) || errors.As(err, &addressError) || errors.As(err, &unknownNetwork) {
		return false
	}
	var networkError *net.OpError
	var dnsError *net.DNSError
	return errors.As(err, &networkError) || errors.As(err, &dnsError) || errors.Is(err, driver.ErrBadConn) || errors.Is(err, mysqldriver.ErrInvalidConn) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
