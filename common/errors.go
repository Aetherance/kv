package common

import "errors"

// KV Engine

var ErrEngineEmptyKey = errors.New("kv engine: empty key")
var ErrEngineKeyNotFound = errors.New("kv engine: did not found the key from storage")

// Server

var ErrServerAlreadyStarted = errors.New("Server: has been started")
var ErrServerStopWhileServerDidNotRun = errors.New("Server: server is not running but Stop() called")

// Coordinator

var ErrCoordErrUnknownOp = errors.New("Coord: Unknown Op")
