package events

import (
	"sync"
	"testing"
	"time"
)

func TestNotifyWakesWaiters(t *testing.T) {
	t.Parallel()

	h := NewHub()
	first, second := h.Wait(), h.Wait()

	select {
	case <-first:
		t.Fatal("no debería despertar antes del aviso")
	default:
	}

	h.Notify()
	for _, ch := range []<-chan struct{}{first, second} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatal("el aviso no despertó a quien esperaba")
		}
	}
}

func TestWaitAfterNotifyWaitsForTheNextOne(t *testing.T) {
	t.Parallel()

	h := NewHub()
	h.Notify()
	select {
	case <-h.Wait():
		t.Fatal("un aviso anterior no debe despertar una espera nueva")
	default:
	}
}

func TestConcurrentUse(t *testing.T) {
	t.Parallel()

	h := NewHub()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				_ = h.Wait()
				h.Notify()
			}
		})
	}
	wg.Wait()
}
