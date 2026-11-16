package pgfx

func transactionValue[T any](run func(func(*Tx) error) error, fn func(*Tx) (T, error)) (T, error) {
	var value T

	err := run(func(tx *Tx) error {
		var err error
		value, err = fn(tx)

		return err
	})
	if err != nil {
		var zero T

		return zero, err
	}

	return value, nil
}
