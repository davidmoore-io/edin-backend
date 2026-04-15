package httpapi

import "net/http"

// RegisterCopilotRoutes adds Copilot API routes to the mux.
func (s *Server) RegisterCopilotRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/copilot/chat/ws", s.handleCopilotChatWebSocket)
	// More routes added by later stories
}
