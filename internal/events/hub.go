// Package events avisa a quienes esperan cambios. La fuente de verdad es la tabla
// de eventos: el Hub solo despierta a los lectores para que vuelvan a consultarla,
// de modo que un aviso perdido no pierde datos.
package events

import "sync"

// Hub es seguro para uso concurrente.
type Hub struct {
	mu sync.Mutex
	ch chan struct{}
}

// NewHub devuelve un Hub listo para usar.
func NewHub() *Hub {
	return &Hub{ch: make(chan struct{})}
}

// Wait devuelve un canal que se cierra con el próximo aviso. Hay que pedirlo
// antes de consultar la tabla, para no perder un aviso entre medias.
func (h *Hub) Wait() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ch
}

// Notify despierta a todos los que esperan.
func (h *Hub) Notify() {
	h.mu.Lock()
	defer h.mu.Unlock()
	close(h.ch)
	h.ch = make(chan struct{})
}
