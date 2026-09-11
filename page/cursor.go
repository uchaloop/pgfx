package page

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

const maxCursorBytes = 65536

type cursorValue struct {
	Kind string          `json:"t"`
	Data json.RawMessage `json:"v"`
}
type cursorToken struct {
	Version int           `json:"v"`
	Scope   string        `json:"s"`
	Keys    []cursorValue `json:"k"`
}

// Cursor encodes the ordered key values of the last returned row, not the
// extra look-ahead row. Tokens are versioned and scope-bound, but not signed or
// encrypted; they are not authorization credentials. Always enforce filters.
// Supported keys: integer and floating-point scalars, bool, string, time.Time,
// []byte and UUID represented by [16]byte, plus nil for SQL NULL.
func (s CursorStatement) Cursor(keys []any) (string, error) {
	if s.scope == "" || len(keys) != len(s.Orders) {
		return "", ErrInvalidCursor
	}
	token := cursorToken{Version: 1, Scope: s.scope, Keys: make([]cursorValue, len(keys))}
	for i, key := range keys {
		encoded, err := encodeValue(key)
		if err != nil {
			return "", err
		}
		token.Keys[i] = encoded
	}
	raw, err := json.Marshal(token)
	if err != nil {
		return "", err
	}
	result := base64.RawURLEncoding.EncodeToString(raw)
	if len(result) > maxCursorBytes {
		return "", ErrInvalidCursor
	}
	return result, nil
}

func encodeValue(value any) (cursorValue, error) {
	var kind string
	switch value.(type) {
	case nil:
		kind = "null"
	case int:
		kind = "int"
	case int8:
		kind = "int8"
	case int16:
		kind = "int16"
	case int32:
		kind = "int32"
	case int64:
		kind = "int64"
	case uint:
		kind = "uint"
	case uint8:
		kind = "uint8"
	case uint16:
		kind = "uint16"
	case uint32:
		kind = "uint32"
	case uint64:
		kind = "uint64"
	case float32:
		kind = "float32"
	case float64:
		kind = "float64"
	case bool:
		kind = "bool"
	case string:
		kind = "string"
	case time.Time:
		kind = "time"
	case []byte:
		kind = "bytes"
	case [16]byte:
		kind = "uuid"
	default:
		return cursorValue{}, fmt.Errorf("%w: %T", ErrCursorValue, value)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return cursorValue{}, fmt.Errorf("%w: %v", ErrCursorValue, err)
	}
	return cursorValue{Kind: kind, Data: data}, nil
}

func decodeCursor(text, scope string, count int) ([]any, error) {
	if len(text) > maxCursorBytes {
		return nil, ErrInvalidCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	var token cursorToken
	if json.Unmarshal(raw, &token) != nil || token.Version != 1 || token.Scope != scope || len(token.Keys) != count {
		return nil, ErrInvalidCursor
	}
	keys := make([]any, count)
	for i, key := range token.Keys {
		if key.Kind == "null" {
			if string(key.Data) != "null" {
				return nil, ErrInvalidCursor
			}
			continue
		}
		if string(key.Data) == "null" {
			return nil, ErrInvalidCursor
		}
		keys[i], err = decodeValue(key)
		if err != nil {
			return nil, err
		}
	}
	return keys, nil
}

func decodeValue(key cursorValue) (any, error) {
	switch key.Kind {
	case "int":
		return decodeJSON[int](key.Data)
	case "int8":
		return decodeJSON[int8](key.Data)
	case "int16":
		return decodeJSON[int16](key.Data)
	case "int32":
		return decodeJSON[int32](key.Data)
	case "int64":
		return decodeJSON[int64](key.Data)
	case "uint":
		return decodeJSON[uint](key.Data)
	case "uint8":
		return decodeJSON[uint8](key.Data)
	case "uint16":
		return decodeJSON[uint16](key.Data)
	case "uint32":
		return decodeJSON[uint32](key.Data)
	case "uint64":
		return decodeJSON[uint64](key.Data)
	case "float32":
		return decodeJSON[float32](key.Data)
	case "float64":
		return decodeJSON[float64](key.Data)
	case "bool":
		return decodeJSON[bool](key.Data)
	case "string":
		return decodeJSON[string](key.Data)
	case "time":
		return decodeJSON[time.Time](key.Data)
	case "bytes":
		return decodeJSON[[]byte](key.Data)
	case "uuid":
		return decodeJSON[[16]byte](key.Data)
	default:
		return nil, ErrInvalidCursor
	}
}

func decodeJSON[T any](data json.RawMessage) (T, error) {
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return value, ErrInvalidCursor
	}
	return value, nil
}

// cursorScope rejects accidental reuse with a different query, order or filter.
// It is not a signature; access control always belongs in the base filter.
func cursorScope(sql string, orders []Order, args []any) (string, error) {
	raw, err := json.Marshal(struct {
		SQL    string
		Orders []Order
		Args   []any
	}{sql, orders, args})
	if err != nil {
		return "", fmt.Errorf("%w: filter arguments: %v", ErrCursorValue, err)
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}
