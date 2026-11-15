package pgfx

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.uber.org/fx"
)

func TestHealthShutdownConcurrentApplicationCalls(t *testing.T) {
	var shutdowner fx.Shutdowner
	app := fx.New(fx.NopLogger, HealthModule, fx.Populate(&shutdowner))
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := app.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	}()

	health := connectionHealth{shutdowner: shutdowner}
	var wg sync.WaitGroup
	start := make(chan struct{})

	for _, request := range []func() error{
		health.requestShutdown,
		func() error { return shutdowner.Shutdown() },
		func() error { return shutdowner.Shutdown(fx.ExitCode(2)) },
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()

			<-start

			for range 1000 {
				_ = request()
			}
		}()
	}

	close(start)
	wg.Wait()
}

func TestHealthModuleRequiredWhenEnabled(t *testing.T) {
	cfg := makeValidConfig()
	cfg.Health = HealthConfig{
		IsEnabled:    true,
		Interval:     time.Second,
		Timeout:      time.Second,
		FailureAfter: time.Minute,
	}
	app := fx.New(
		fx.NopLogger,
		fx.Supply(cfg),
		MakeModule(),
		fx.Invoke(func(*Connection) {}),
	)
	if !errors.Is(app.Err(), ErrHealthModuleRequired) {
		t.Fatalf("missing health module: %v", app.Err())
	}
}

func TestHealthShutdownExitCodeAndScope(t *testing.T) {
	var root, nested fx.Shutdowner
	app := fx.New(
		fx.NopLogger,
		HealthModule,
		fx.Populate(&root),
		fx.Module("consumer", fx.Invoke(func(s fx.Shutdowner) { nested = s })),
	)
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := app.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	}()

	if root != nested {
		t.Fatal("consumers do not share shutdowner")
	}

	signal := app.Wait()
	if err := (connectionHealth{shutdowner: nested}).requestShutdown(); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-signal:
		if got.ExitCode != 1 {
			t.Fatalf("exit=%d", got.ExitCode)
		}
	case <-time.After(time.Second):
		t.Fatal("missing shutdown signal")
	}
}

func TestHealthModulePreservesShutdownError(t *testing.T) {
	expected := errors.New("shutdown failed")
	base := &failedShutdowner{err: expected}
	s := makeHealthShutdowner(base)
	if err := s.Shutdown(); !errors.Is(err, expected) {
		t.Fatal(err)
	}
}

type failedShutdowner struct{ err error }

func (s *failedShutdowner) Shutdown(...fx.ShutdownOption) error { return s.err }
