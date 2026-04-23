package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Event type constants used by the SSE hub.
const (
	EvAgentUpdate   = "agent_update"
	EvHeartbeat     = "heartbeat"
	EvAlert         = "alert"
	EvCommandResult = "command_result"
	EvTicketUpdate  = "ticket_update"
)

// Event is sent to all SSE subscribers.
type Event struct {
	Type    string `json:"type"`
	Payload any    `json:"payload"`
	TS      string `json:"ts"`
}

// Hub manages SSE client connections and broadcasts events.
type Hub struct {
	mu      sync.RWMutex
	clients map[chan Event]struct{}
}

// NewHub creates a new SSE hub.
func NewHub() *Hub {
	return &Hub{clients: make(map[chan Event]struct{})}
}

// Broadcast sends an event to all connected SSE clients.
func (h *Hub) Broadcast(evType string, payload any) {
	ev := Event{
		Type:    evType,
		Payload: payload,
		TS:      time.Now().UTC().Format(time.RFC3339),
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.clients {
		select {
		case ch <- ev:
		default:
			// Drop if client is slow
		}
	}
}

func (h *Hub) subscribe() chan Event {
	ch := make(chan Event, 32)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) unsubscribe(ch chan Event) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
	close(ch)
}

// ConnectedClients returns the number of active SSE connections.
func (h *Hub) ConnectedClients() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// handleSSE streams server-sent events to the client.
func (h *Hub) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	ch := h.subscribe()
	defer h.unsubscribe(ch)

	slog.Debug("SSE client connected", "remote", r.RemoteAddr)

	// Send initial connection event
	fmt.Fprintf(w, "data: {\"type\":\"connected\",\"ts\":%q}\n\n", time.Now().UTC().Format(time.RFC3339))
	flusher.Flush()

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()

	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-ping.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			slog.Debug("SSE client disconnected", "remote", r.RemoteAddr)
			return
		}
	}
}
