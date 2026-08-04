package pgfx

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uchaloop/secret/v2"
)

// defaultPostgresPort is the port used when an endpoint omits one.
const defaultPostgresPort uint16 = 5432

// Config is the serializable configuration of a single Postgres connection. It
// carries only plain, serializable values - no tracer, hooks, or other runtime
// dependencies (those are passed to [Make] as [Option] values).
//
// The struct tags let a loader fill it: `koanf` fields come from the file, `env`
// fields from the environment (see confmaker/confx). Every open field supports
// both sources: the file sets a value and the environment can override it.
// Password is env-only (`koanf:"-"`), so putting it in the file is rejected as an
// unknown key. The tags are inert strings, so the Config type itself depends only
// on the standard-library-only github.com/uchaloop/secret/v2 module.
//
// Host and Database are required. User and Password are optional: when empty they
// fall back to libpq's defaults (PGUSER / the OS user, and PGPASSWORD / .pgpass).
type Config struct {
	// Host is the endpoint as "host" or "host:port". A port in the string wins;
	// when omitted, 5432 applies. IPv6 with a port must be bracketed
	// ("[::1]:5433").
	Host string `koanf:"host" env:"HOST"`
	// Database is the required PostgreSQL database name.
	Database string `koanf:"database" env:"DATABASE"`
	// User is the PostgreSQL role. When empty, libpq selects its default.
	User string `koanf:"user" env:"USER"`
	// Password is read from the environment only. When empty, libpq may use
	// PGPASSWORD or .pgpass.
	Password secret.Secret `koanf:"-" env:"PASSWORD"`
	// AppName is reported as application_name in pg_stat_activity.
	AppName string `koanf:"app_name" env:"APP_NAME"`

	// TLS configures libpq transport security.
	TLS TLSConfig `koanf:"tls" envPrefix:"TLS_"`
	// Pool configures pgxpool sizing and connection lifetimes.
	Pool PoolConfig `koanf:"pool" envPrefix:"POOL_"`
	// Timeouts configures connection timeouts.
	Timeouts TimeoutConfig `koanf:"timeouts" envPrefix:"TIMEOUTS_"`
}

// TLSConfig holds the libpq TLS settings.
type TLSConfig struct {
	// Mode is the libpq sslmode (disable/allow/prefer/require/verify-ca/
	// verify-full). Empty leaves the libpq default ("prefer").
	//
	// SECURITY: "prefer"/"allow" fall back to unencrypted and never verify the
	// server certificate; "require" encrypts but still does not verify it. Use
	// "verify-full" with RootCert for MITM protection.
	Mode string `koanf:"mode" env:"MODE"`
	// Cert is the path to the client certificate.
	Cert string `koanf:"cert" env:"CERT"`
	// Key is the path to the client private key.
	Key string `koanf:"key" env:"KEY"`
	// RootCert is the path to the trusted root certificate.
	RootCert string `koanf:"root_cert" env:"ROOT_CERT"`
	// ServerName overrides the TLS server name used for verification.
	ServerName string `koanf:"server_name" env:"SERVER_NAME"`
}

// PoolConfig holds pgxpool sizing and connection-lifecycle settings. Zero values
// leave the pgxpool defaults in place.
type PoolConfig struct {
	// MaxConns is the maximum pool size.
	MaxConns int32 `koanf:"max_conns" env:"MAX_CONNS"`
	// MinConns is the minimum number of connections maintained by the pool.
	MinConns int32 `koanf:"min_conns" env:"MIN_CONNS"`
	// MinIdleConns is the minimum number of idle connections maintained by the
	// pool.
	MinIdleConns int32 `koanf:"min_idle_conns" env:"MIN_IDLE_CONNS"`
	// MaxConnLifetime is the maximum lifetime of a connection.
	MaxConnLifetime time.Duration `koanf:"max_conn_lifetime" env:"MAX_CONN_LIFETIME"`
	// MaxConnLifetimeJitter randomizes connection expiry to avoid synchronized
	// reconnects.
	MaxConnLifetimeJitter time.Duration `koanf:"max_conn_lifetime_jitter" env:"MAX_CONN_LIFETIME_JITTER"`
	// MaxConnIdleTime is the maximum time a connection may remain idle.
	MaxConnIdleTime time.Duration `koanf:"max_conn_idle_time" env:"MAX_CONN_IDLE_TIME"`
	// HealthPeriod controls how often pgxpool checks idle connections.
	HealthPeriod time.Duration `koanf:"health_period" env:"HEALTH_PERIOD"`
}

// TimeoutConfig holds connection timeouts.
type TimeoutConfig struct {
	// Connect bounds establishing a single connection. Zero leaves the pgx
	// default.
	Connect time.Duration `koanf:"connect" env:"CONNECT"`
}

// Validate checks the connection's own invariants. It returns every problem at
// once, not just the first.
func (cfg Config) Validate() error {
	var errs []error

	if len(strings.TrimSpace(cfg.Host)) == 0 {
		errs = append(errs, errors.New("host is required"))
	} else if _, _, err := resolveEndpoint(cfg.Host); err != nil {
		errs = append(errs, fmt.Errorf("invalid host: %w", err))
	}
	if len(cfg.Database) == 0 {
		errs = append(errs, errors.New("database is required"))
	}
	// User and Password are optional: when empty they fall back to libpq's
	// defaults (PGUSER / the OS user, and PGPASSWORD / .pgpass), so peer auth and
	// passwordless connections work. See poolConfig, which only overrides them
	// when set.

	if cfg.Pool.MaxConns < 0 {
		errs = append(errs, errors.New("pool.max_conns must not be negative"))
	}
	if cfg.Pool.MinConns < 0 {
		errs = append(errs, errors.New("pool.min_conns must not be negative"))
	}
	if cfg.Pool.MaxConns > 0 && cfg.Pool.MinConns > cfg.Pool.MaxConns {
		errs = append(errs, errors.New("pool.min_conns must not exceed pool.max_conns"))
	}
	if cfg.Timeouts.Connect < 0 {
		errs = append(errs, errors.New("timeouts.connect must not be negative"))
	}

	return errors.Join(errs...)
}

// resolveEndpoint parses a "host" or "host:port" endpoint. A port in the string
// wins; when absent, 5432 applies. A bracketed IPv6 ("[::1]:5433") carries a
// port; a bare IPv6 ("::1") uses the default port.
func resolveEndpoint(endpoint string) (string, uint16, error) {
	endpoint = strings.TrimSpace(endpoint)
	if len(endpoint) == 0 {
		return "", 0, errors.New("empty host/endpoint")
	}

	if !strings.Contains(endpoint, ":") {
		return endpoint, defaultPostgresPort, nil
	}
	if net.ParseIP(endpoint) != nil {
		return endpoint, defaultPostgresPort, nil
	}

	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", 0, fmt.Errorf("invalid endpoint %q: %w", endpoint, err)
	}
	if len(host) == 0 {
		return "", 0, fmt.Errorf("missing host in %q", endpoint)
	}

	parsed, perr := strconv.ParseUint(port, 10, 16)
	if perr != nil {
		return "", 0, fmt.Errorf("parse port in %q: %w", endpoint, perr)
	}

	return host, uint16(parsed), nil
}

// poolConfig builds a *pgxpool.Config from cfg and the runtime opts. The
// password is set on the connection config directly, never placed in the DSN.
func (cfg Config) poolConfig(opts *options) (*pgxpool.Config, error) {
	host, port, err := resolveEndpoint(cfg.Host)
	if err != nil {
		return nil, err
	}

	parts := []string{
		"host=" + quoteDSN(host),
		"port=" + strconv.FormatUint(uint64(port), 10),
	}
	if len(cfg.TLS.Mode) > 0 {
		parts = append(parts, "sslmode="+quoteDSN(cfg.TLS.Mode))
	}
	if len(cfg.TLS.Cert) > 0 {
		parts = append(parts, "sslcert="+quoteDSN(cfg.TLS.Cert))
	}
	if len(cfg.TLS.Key) > 0 {
		parts = append(parts, "sslkey="+quoteDSN(cfg.TLS.Key))
	}
	if len(cfg.TLS.RootCert) > 0 {
		parts = append(parts, "sslrootcert="+quoteDSN(cfg.TLS.RootCert))
	}

	poolCfg, err := pgxpool.ParseConfig(strings.Join(parts, " "))
	if err != nil {
		return nil, err
	}

	poolCfg.ConnConfig.Host = host
	poolCfg.ConnConfig.Port = port
	poolCfg.ConnConfig.Database = cfg.Database
	// User and Password are optional: override the value ParseConfig derived from
	// libpq's defaults (PGUSER / OS user, PGPASSWORD / .pgpass) only when set, so
	// peer auth and passwordless connections keep working.
	if len(cfg.User) > 0 {
		poolCfg.ConnConfig.User = cfg.User
	}
	if password := cfg.Password.Reveal(); len(password) > 0 {
		poolCfg.ConnConfig.Password = password
	}

	if cfg.Timeouts.Connect > 0 {
		poolCfg.ConnConfig.ConnectTimeout = cfg.Timeouts.Connect
	}
	if len(cfg.TLS.ServerName) > 0 && poolCfg.ConnConfig.TLSConfig != nil {
		poolCfg.ConnConfig.TLSConfig = poolCfg.ConnConfig.TLSConfig.Clone()
		poolCfg.ConnConfig.TLSConfig.ServerName = cfg.TLS.ServerName
	}
	if len(cfg.AppName) > 0 {
		if poolCfg.ConnConfig.RuntimeParams == nil {
			poolCfg.ConnConfig.RuntimeParams = make(map[string]string)
		}

		poolCfg.ConnConfig.RuntimeParams["application_name"] = cfg.AppName
	}

	applyPoolLimits(poolCfg, cfg.Pool)

	if tracer := opts.buildTracer(); tracer != nil {
		poolCfg.ConnConfig.Tracer = tracer
	}

	for _, apply := range opts.poolOptions {
		apply(poolCfg)
	}

	return poolCfg, nil
}

func applyPoolLimits(poolCfg *pgxpool.Config, pool PoolConfig) {
	if pool.MaxConns > 0 {
		poolCfg.MaxConns = pool.MaxConns
	}
	if pool.MinConns > 0 {
		poolCfg.MinConns = pool.MinConns
	}
	if pool.MinIdleConns > 0 {
		poolCfg.MinIdleConns = pool.MinIdleConns
	}
	if pool.MaxConnLifetime > 0 {
		poolCfg.MaxConnLifetime = pool.MaxConnLifetime
	}
	if pool.MaxConnLifetimeJitter > 0 {
		poolCfg.MaxConnLifetimeJitter = pool.MaxConnLifetimeJitter
	}
	if pool.MaxConnIdleTime > 0 {
		poolCfg.MaxConnIdleTime = pool.MaxConnIdleTime
	}
	if pool.HealthPeriod > 0 {
		poolCfg.HealthCheckPeriod = pool.HealthPeriod
	}
}

// quoteDSN wraps a libpq connection-string value in single quotes, escaping
// backslashes and single quotes, so values with spaces or special characters
// (e.g. SSL file paths) survive keyword/value parsing.
func quoteDSN(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `'`, `\'`)

	return "'" + value + "'"
}
