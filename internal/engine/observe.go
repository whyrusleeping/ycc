package engine

import "sync/atomic"

// turnObserver, when set, is told the final outcome (after retries) of every
// model turn by logical model name, so the daemon health monitor can react to
// real provider failures and skip probing models that are visibly working.
var turnObserver atomic.Pointer[func(model string, err error)]

// SetTurnObserver installs fn as the model-turn observer (nil removes it).
func SetTurnObserver(fn func(model string, err error)) {
	if fn == nil {
		turnObserver.Store(nil)
		return
	}
	turnObserver.Store(&fn)
}

func observeTurn(model string, err error) {
	if model == "" {
		return
	}
	if fn := turnObserver.Load(); fn != nil {
		(*fn)(model, err)
	}
}
