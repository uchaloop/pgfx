package page

import (
	"context"
	"errors"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/uchaloop/pgfx"
)

// ErrParams reports invalid pagination parameters or offset overflow.
var ErrParams = errors.New("invalid pagination parameters")

// Params specifies the page number and requested size.
type Params struct {
	Page  int
	Limit int
}

// Config specifies the minimum, default and maximum page sizes.
// Zero fields use defaults of 1, 10 and 100, respectively.
type Config struct {
	MinLimit     int
	DefaultLimit int
	MaxLimit     int
}

// Bounds contains SQL LIMIT/OFFSET values.
type Bounds struct {
	Limit  int64
	Offset int64
}

// Pager stores immutable settings and supports concurrent use.
// Its zero value is not usable; construct it with MakePager.
type Pager struct {
	config Config
}

// MakePager validates settings once and stores a copy.
func MakePager(cfg Config) (*Pager, error) {
	if cfg.MinLimit == 0 {
		cfg.MinLimit = 1
	}

	if cfg.DefaultLimit == 0 {
		cfg.DefaultLimit = 10
	}

	if cfg.MaxLimit == 0 {
		cfg.MaxLimit = 100
	}

	if cfg.MinLimit < 1 || cfg.MinLimit > cfg.DefaultLimit || cfg.DefaultLimit > cfg.MaxLimit {
		return nil, ErrParams
	}

	return &Pager{config: cfg}, nil
}

// Bounds normalizes parameters and checks overflow without accessing the database.
func (p *Pager) Bounds(params Params) (Bounds, error) {
	cfg := p.config
	number, size := int64(params.Page), int64(params.Limit)
	if number < 1 {
		number = 1
	}

	if size < 1 {
		size = int64(cfg.DefaultLimit)
	}

	if size < int64(cfg.MinLimit) {
		size = int64(cfg.MinLimit)
	}

	if size > int64(cfg.MaxLimit) {
		size = int64(cfg.MaxLimit)
	}

	if number-1 > math.MaxInt64/size {
		return Bounds{}, ErrParams
	}

	return Bounds{Limit: size, Offset: (number - 1) * size}, nil
}

// Fetch reads the list and count from one PostgreSQL snapshot.
// Callbacks must use the supplied tx and ctx; list runs before count.
// Fetch owns transaction completion. Errors return a zero Result; a successful empty list is non-nil.
// The borrowed db is not closed. ctx must be non-nil.
func (p *Pager) Fetch[T any](ctx context.Context, db *pgfx.DB, params Params, list func(context.Context, *pgfx.Tx, Bounds) ([]T, error), count func(context.Context, *pgfx.Tx) (int64, error)) (Result[T], error) {
	bounds, err := p.Bounds(params)
	if err != nil {
		return Result[T]{}, err
	}

	if db == nil {
		return Result[T]{}, errors.New("database is nil")
	}

	if list == nil || count == nil {
		return Result[T]{}, errors.New("page callback is nil")
	}

	if err := ctx.Err(); err != nil {
		return Result[T]{}, err
	}

	return db.TransactionValue(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(tx *pgfx.Tx) (Result[T], error) {
			return Fetch(
				ctx,
				func(ctx context.Context) ([]T, error) { return list(ctx, tx, bounds) },
				func(ctx context.Context) (int64, error) { return count(ctx, tx) },
			)
		},
	)
}
