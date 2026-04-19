package api

// events.go — SSE hub para push de eventos en tiempo real al frontend.
// No usa gorilla/websocket: usa Server-Sent Events (EventSource nativo en browsers).
// Ventajas: reconexión automática, sin handshake, funciona detrás de proxies HTTP/1.1.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// EventType identifica el tipo de evento SSE.
type EventType string

const (
	EvAgentUpdate   EventType = "agent_update"
	EvAlert         EventType = "alert"
	EvCommandResult EventType = "command_result"
	EvTicketUpdate  EventType = "ticket_update"
	EvHeartbeat     EventType = "heartbeat"
)

// SSEEvent es el payload enviado a través del stream.
type SSEEvent struct {
	Type    EventType `json:"type"`
	Payload any       `json:"payload"`
	TS      time.Time `json:"ts"`
}

// Hub gestiona subscriptores SSE y distribuye eventos a todos.
type Hub struct {
	mu      sync.RWMutex
	clients map[chan []byte]struct{}
	bus     chan []byte
}

// NewHub crea e inicia el hub en background.
func NewHub() *Hub {
	h := &Hub{
		clients: make(map[chan []byte]struct{}),
		bus:     make(chan []byte, 512),
	}
	go h.run()
	return h
}

func (h *Hub) run() {
	for msg := range h.bus {
		h.mu.RLock()
		for ch := range h.clients {
			select {
			case ch <- msg:
			default:
				// cliente lento — drop, no bloquear el broadcast
			}
		}
		h.mu.RUnlock()
	}
}

// Subscribe registra un canal nuevo y devuelve el canal del suscriptor.
func (h *Hub) Subscribe() chan []byte {
	ch := make(chan []byte, 32)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	slog.Debug("SSE client subscribed", "total", len(h.clients))
	return ch
}

// Unsubscribe elimina el canal del hub.
func (h *Hub) Unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
	close(ch)
}

// Broadcast serializa y envía un evento a todos los clientes suscritos.
func (h *Hub) Broadcast(evType EventType, payload any) {
	evt := SSEEvent{Type: evType, Payload: payload, TS: time.Now().UTC()}
	data, err := json.Marshal(evt)
	if err != nil {
		slog.Error("hub: marshal event", "error", err)
		return
	}
	select {
	case h.bus <- data:
	default:
		slog.Warn("hub: bus full, dropping event", "type", evType)
	}
}

// ClientCount devuelve el número de clientes SSE conectados.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// ─── HTTP Handler ──────────────────────────────────────────────────────────

// handleSSE upgrades the connection to an SSE stream.
// No requiere auth especial — la API key se valida vía authMiddleware en la ruta padre.
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // deshabilitar buffering de nginx
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)

	// Comentario inicial para confirmar conexión al cliente.
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	ch := s.deps.Hub.Subscribe()
	defer s.deps.Hub.Unsubscribe(ch)

	// Keep-alive ping cada 25 s (evita timeout de proxies).
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()

		case <-ping.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()

		case <-r.Context().Done():
			return
		}
	}
}
