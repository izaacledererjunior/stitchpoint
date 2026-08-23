package safego

import (
	"sync"
	"testing"
)

// TestRecover_StopsPanicFromPropagating has no explicit assertion because
// the assertion IS the test surviving: an unrecovered panic in a goroutine
// crashes the whole test binary, so reaching wg.Wait()'s return proves
// Recover contained it.
func TestRecover_StopsPanicFromPropagating(_ *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		defer Recover("test")
		panic("boom")
	}()

	wg.Wait()
}

func TestRecover_NoPanicIsANoOp(_ *testing.T) {
	func() {
		defer Recover("test")
	}()
}
