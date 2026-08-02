package log

// Stub logging helpers. They intentionally do nothing for now; replace with a
// real logger implementation later.

func Debugf(format string, args ...interface{}) {}
func Infof(format string, args ...interface{})  {}
func Warnf(format string, args ...interface{})  {}
func Errorf(format string, args ...interface{}) {}
func Fatalf(format string, args ...interface{}) {}
func Fatal(args ...interface{})                 {}
