package pgfx

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/uchaloop/secret/v2"
)

func TestConfigEveryFieldSupportsEnvironmentLoading(t *testing.T) {
	secretType := reflect.TypeOf(secret.Secret{})

	var walk func(reflect.Type, string)
	walk = func(typ reflect.Type, path string) {
		for i := range typ.NumField() {
			field := typ.Field(i)
			fieldPath := field.Name
			if path != "" {
				fieldPath = path + "." + field.Name
			}

			if field.Type.Kind() == reflect.Struct && field.Type != secretType {
				if field.Tag.Get("envPrefix") == "" {
					t.Errorf("%s has no envPrefix tag", fieldPath)
				}
				walk(field.Type, fieldPath)
				continue
			}
			if field.Tag.Get("env") == "" {
				t.Errorf("%s has no env tag", fieldPath)
			}
		}
	}

	walk(reflect.TypeOf(Config{}), "")
}

func TestResolveEndpoint(t *testing.T) {
	const dp uint16 = 5432

	tests := []struct {
		name     string
		endpoint string
		wantHost string
		wantPort uint16
		wantErr  bool
	}{
		{name: "host only", endpoint: "db", wantHost: "db", wantPort: dp},
		{name: "host:port", endpoint: "db:6543", wantHost: "db", wantPort: 6543},
		{name: "ipv4 with port", endpoint: "192.168.1.1:6000", wantHost: "192.168.1.1", wantPort: 6000},
		{name: "bare ipv6", endpoint: "::1", wantHost: "::1", wantPort: dp},
		{name: "bracketed ipv6 with port", endpoint: "[::1]:6543", wantHost: "::1", wantPort: 6543},
		{name: "empty errors", endpoint: "", wantErr: true},
		{name: "bad port errors", endpoint: "db:x", wantErr: true},
		{name: "missing host errors", endpoint: ":5432", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port, err := resolveEndpoint(tt.endpoint)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got host=%q port=%d", host, port)
				}

				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if host != tt.wantHost || port != tt.wantPort {
				t.Fatalf("got host=%q port=%d, want host=%q port=%d", host, port, tt.wantHost, tt.wantPort)
			}
		})
	}
}

func TestConfigValidate(t *testing.T) {
	// User and Password are optional; Host and Database are checked here as well
	// as by their notEmpty tags, so a Config built in Go is covered too.
	valid := Config{Host: "db:5432", Database: "app"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	// Each case is otherwise valid but flips one invalid field, so the test
	// isolates that invariant.
	cases := map[string]Config{
		"missing host":     {Database: "app"},
		"missing database": {Host: "db"},
		"bad host":         {Host: "db:x", Database: "app"},
		"min exceeds max":  {Host: "db", Database: "app", Pool: PoolConfig{MaxConns: 2, MinConns: 3}},
		"negative max":     {Host: "db", Database: "app", Pool: PoolConfig{MaxConns: -1}},
		"negative connect": {Host: "db", Database: "app", Timeouts: TimeoutConfig{Connect: -1}},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if err := cfg.Validate(); err == nil {
				t.Fatalf("expected error for %q", name)
			}
		})
	}
}

// TestConfigValidateReportsEveryProblem keeps the accumulating style: a
// deployment is fixed in a config map and rolled out, so one report per rollout
// is the difference between one round trip and three.
func TestConfigValidateReportsEveryProblem(t *testing.T) {
	cfg := Config{
		Host:     "db:x",
		Database: "app",
		Pool:     PoolConfig{MaxConns: -1},
		Timeouts: TimeoutConfig{Connect: -1},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected the three problems to be reported")
	}

	for _, want := range []string{"invalid host", "pool.max_conns", "timeouts.connect"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the report is missing %q:\n%v", want, err)
		}
	}
}

// TestConfigRequiresItsVariables pins what moved out of Validate: the env tags
// are what make a deployment supply a host and a database.
func TestConfigRequiresItsVariables(t *testing.T) {
	tags := map[string]string{"Host": "HOST,notEmpty", "Database": "DATABASE,notEmpty"}

	configType := reflect.TypeFor[Config]()
	for field, want := range tags {
		declared, ok := configType.FieldByName(field)
		if !ok {
			t.Fatalf("no field %s", field)
		}
		if got := declared.Tag.Get("env"); got != want {
			t.Errorf("%s env tag = %q, want %q", field, got, want)
		}
	}
}

func TestPoolConfigSetsConnectionAndKeepsPasswordOutOfDSN(t *testing.T) {
	cfg := Config{
		Host:     "db:6543",
		Database: "app",
		User:     "orders",
		Password: secret.New("s3cr3t"),
		AppName:  "orders-service",
		TLS:      TLSConfig{Mode: "disable"}, // avoid cert I/O
		Pool:     PoolConfig{MaxConns: 20},
	}

	poolCfg, err := cfg.poolConfig(makeOptions())
	if err != nil {
		t.Fatalf("poolConfig: %v", err)
	}

	if poolCfg.ConnConfig.Host != "db" || poolCfg.ConnConfig.Port != 6543 {
		t.Errorf("host/port = %q/%d, want db/6543", poolCfg.ConnConfig.Host, poolCfg.ConnConfig.Port)
	}
	if poolCfg.ConnConfig.Database != "app" || poolCfg.ConnConfig.User != "orders" {
		t.Errorf("database/user = %q/%q", poolCfg.ConnConfig.Database, poolCfg.ConnConfig.User)
	}
	if poolCfg.ConnConfig.Password != "s3cr3t" {
		t.Errorf("password not set on ConnConfig: %q", poolCfg.ConnConfig.Password)
	}
	if poolCfg.MaxConns != 20 {
		t.Errorf("max_conns = %d, want 20", poolCfg.MaxConns)
	}
}

func TestPoolConfigKeepsLibpqDefaultsWhenUserPasswordEmpty(t *testing.T) {
	// With no User/Password, poolConfig must not override the values ParseConfig
	// derives from libpq's environment - so peer auth and .pgpass keep working.
	t.Setenv("PGUSER", "pguser")
	t.Setenv("PGPASSWORD", "pgpass")

	cfg := Config{Host: "db", Database: "app", TLS: TLSConfig{Mode: "disable"}}

	poolCfg, err := cfg.poolConfig(makeOptions())
	if err != nil {
		t.Fatalf("poolConfig: %v", err)
	}

	if poolCfg.ConnConfig.User != "pguser" {
		t.Errorf("user = %q, want pguser (libpq default kept)", poolCfg.ConnConfig.User)
	}
	if poolCfg.ConnConfig.Password != "pgpass" {
		t.Error("password from PGPASSWORD was overwritten")
	}
}

func TestPoolConfigAppliesEveryPoolLimit(t *testing.T) {
	want := PoolConfig{
		MaxConns:              20,
		MinConns:              3,
		MinIdleConns:          2,
		MaxConnLifetime:       30 * time.Minute,
		MaxConnLifetimeJitter: 5 * time.Minute,
		MaxConnIdleTime:       10 * time.Minute,
		HealthPeriod:          45 * time.Second,
	}
	cfg := Config{
		Host:     "db",
		Database: "app",
		TLS:      TLSConfig{Mode: "disable"},
		Pool:     want,
	}

	poolCfg, err := cfg.poolConfig(makeOptions())
	if err != nil {
		t.Fatalf("poolConfig: %v", err)
	}

	if poolCfg.MaxConns != want.MaxConns {
		t.Errorf("MaxConns = %d, want %d", poolCfg.MaxConns, want.MaxConns)
	}
	if poolCfg.MinConns != want.MinConns {
		t.Errorf("MinConns = %d, want %d", poolCfg.MinConns, want.MinConns)
	}
	if poolCfg.MinIdleConns != want.MinIdleConns {
		t.Errorf("MinIdleConns = %d, want %d", poolCfg.MinIdleConns, want.MinIdleConns)
	}
	if poolCfg.MaxConnLifetime != want.MaxConnLifetime {
		t.Errorf(
			"MaxConnLifetime = %v, want %v",
			poolCfg.MaxConnLifetime,
			want.MaxConnLifetime,
		)
	}
	if poolCfg.MaxConnLifetimeJitter != want.MaxConnLifetimeJitter {
		t.Errorf(
			"MaxConnLifetimeJitter = %v, want %v",
			poolCfg.MaxConnLifetimeJitter,
			want.MaxConnLifetimeJitter,
		)
	}
	if poolCfg.MaxConnIdleTime != want.MaxConnIdleTime {
		t.Errorf(
			"MaxConnIdleTime = %v, want %v",
			poolCfg.MaxConnIdleTime,
			want.MaxConnIdleTime,
		)
	}
	if poolCfg.HealthCheckPeriod != want.HealthPeriod {
		t.Errorf(
			"HealthCheckPeriod = %v, want %v",
			poolCfg.HealthCheckPeriod,
			want.HealthPeriod,
		)
	}
}

func TestPoolConfigZeroPoolLimitsKeepPgxDefaults(t *testing.T) {
	cfg := Config{
		Host:     "db",
		Database: "app",
		TLS:      TLSConfig{Mode: "disable"},
	}

	poolCfg, err := cfg.poolConfig(makeOptions())
	if err != nil {
		t.Fatalf("poolConfig: %v", err)
	}

	if poolCfg.MaxConns <= 0 {
		t.Fatalf("MaxConns = %d, want a positive pgxpool default", poolCfg.MaxConns)
	}
	if poolCfg.MaxConnLifetime <= 0 {
		t.Fatalf(
			"MaxConnLifetime = %v, want a positive pgxpool default",
			poolCfg.MaxConnLifetime,
		)
	}
	if poolCfg.MaxConnIdleTime <= 0 {
		t.Fatalf(
			"MaxConnIdleTime = %v, want a positive pgxpool default",
			poolCfg.MaxConnIdleTime,
		)
	}
	if poolCfg.HealthCheckPeriod <= 0 {
		t.Fatalf(
			"HealthCheckPeriod = %v, want a positive pgxpool default",
			poolCfg.HealthCheckPeriod,
		)
	}
}

func TestConfigMasksPassword(t *testing.T) {
	cfg := Config{Password: secret.New("hunter2")}
	if out := fmt.Sprintf("%+v", cfg); strings.Contains(out, "hunter2") {
		t.Fatalf("Config %%+v leaks the password: %s", out)
	}
}
