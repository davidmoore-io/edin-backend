// Package websocket provides WebSocket connections for real-time updates.
package websocket

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Message types for WebSocket communication.
const (
	MessageTypeSystemUpdate = "system_update"
	MessageTypeFullRefresh  = "full_refresh"
	MessageTypePing         = "ping"
)

// Message represents a WebSocket message.
type Message struct {
	Type      string      `json:"type"`
	Data      interface{} `json:"data,omitempty"`
	Timestamp string      `json:"timestamp"`
}

// SystemUpdateData represents an update to a single system.
type SystemUpdateData struct {
	SystemName string `json:"system_name"`
	Source     string `json:"source"` // "ssg-eddn" or "inara"
}

// Hub manages WebSocket connections and broadcasts.
type Hub struct {
	clients    map[*Client]bool
	broadcast  chan Message
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex
}

// Client represents a WebSocket client connection.
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
}

// NewHub creates a new WebSocket hub.
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan Message, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

// Run starts the hub's main loop.
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mu.Unlock()

		case message := <-h.broadcast:
			data, err := json.Marshal(message)
			if err != nil {
				continue
			}
			h.mu.RLock()
			for client := range h.clients {
				select {
				case client.send <- data:
				default:
					// Client buffer full, close connection
					close(client.send)
					delete(h.clients, client)
				}
			}
			h.mu.RUnlock()
		}
	}
}

// BroadcastSystemUpdate broadcasts a system update to all clients.
func (h *Hub) BroadcastSystemUpdate(systemName, source string) {
	h.broadcast <- Message{
		Type: MessageTypeSystemUpdate,
		Data: SystemUpdateData{
			SystemName: systemName,
			Source:     source,
		},
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}

// BroadcastFullRefresh broadcasts a full refresh notification.
func (h *Hub) BroadcastFullRefresh(source string) {
	h.broadcast <- Message{
		Type: MessageTypeFullRefresh,
		Data: map[string]string{
			"source": source,
		},
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}

// ClientCount returns the number of connected clients.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Register adds a client to the hub.
func (h *Hub) Register(c *Client) {
	h.register <- c
}

// Unregister removes a client from the hub.
func (h *Hub) Unregister(c *Client) {
	h.unregister <- c
}

// NewClient creates a new WebSocket client.
func NewClient(hub *Hub, conn *websocket.Conn) *Client {
	return &Client{
		hub:  hub,
		conn: conn,
		send: make(chan []byte, 256),
	}
}

// WritePump pumps messages from the hub to the websocket connection.
func (c *Client) WritePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// ReadPump pumps messages from the websocket connection to the hub.
func (c *Client) ReadPump() {
	defer func() {
		c.hub.Unregister(c)
		c.conn.Close()
	}()

	c.conn.SetReadLimit(512)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				// Log unexpected close
			}
			break
		}
		// We don't expect messages from clients, but we need to read to detect close
	}
}
