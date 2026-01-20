package common

import "errors"

// KV Engine

var ErrEngineEmptyKey = errors.New("kv engine: empty key")
var ErrEngineKeyNotFound = errors.New("kv engine: did not found the key from storage")
