package page

import (
	"context"
	"errors"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/uchaloop/pgfx"
)

// ErrParams означает некорректные параметры или переполнение границ страницы.
var ErrParams = errors.New("invalid pagination parameters")

// Params задаёт номер страницы и запрошенный размер.
type Params struct {
	Page  int
	Limit int
}

// Config задаёт минимальный, стандартный и максимальный размеры страницы.
// Нулевые значения соответствуют минимуму 1, размеру по умолчанию 10 и максимуму 100.
type Config struct {
	MinLimit     int
	DefaultLimit int
	MaxLimit     int
}

// Bounds содержит границы SQL LIMIT/OFFSET.
type Bounds struct {
	Limit  int64
	Offset int64
}

// Pager хранит неизменяемые настройки и допускает параллельное использование.
// Нулевое значение не предназначено для использования; создавайте через MakePager.
type Pager struct {
	config Config
}

// MakePager проверяет настройки один раз и сохраняет их копию.
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

// Bounds нормализует параметры и проверяет переполнение до обращения к БД.
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

// Fetch читает список и количество в одном снимке PostgreSQL.
// Callback должны использовать переданный tx и ctx; сначала выполняется list, затем count.
// Библиотека завершает транзакцию; ошибка возвращает нулевой Result, успешный пустой список не nil.
// db заимствован и не закрывается. ctx должен быть ненулевым.
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
