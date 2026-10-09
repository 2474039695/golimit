package limiter

import (
	"errors"
)

var (
	ErrInvalidConfig  = errors.New("limiter: invalid config")
	ErrInvalidRequest = errors.New("limiter: invalid request")
	ErrBackend        = errors.New("limiter: backend failure")
	ErrLocalCapacity  = errors.New("limiter: local bucket capacity reached")
)

// BackendError 表示未取得可信判定：网络失败时脚本可能已经扣减，不能自动重复执行。
type BackendError struct{ Cause error }

func (e *BackendError) Error() string        { return "limiter: backend outcome unknown: " + e.Cause.Error() }
func (e *BackendError) Unwrap() error        { return e.Cause }
func (e *BackendError) Is(target error) bool { return target == ErrBackend }
