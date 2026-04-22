package httpapi

import "net/http"

// RegisterCopilotRoutes adds Copilot API routes to the mux.
//
// The WebSocket route lives at /api/copilot/chat/ws and authenticates inside
// the handler via the first-message nonce frame — it is intentionally NOT
// wrapped in withCommanderAuth.
//
// The session management endpoints, however, use standard cookie/Bearer auth.
// They MUST sit under the same path prefix as the commander session cookie
// (Path=/api/commander) or the browser will not attach the cookie on
// same-origin fetches. Hence they are mounted under /api/commander/chat/
// rather than /api/copilot/chat/. The commander namespace also honours Bearer
// auth, so the Flutter desktop client continues to work via its JWT.
func (s *Server) RegisterCopilotRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/copilot/chat/ws", s.handleCopilotChatWebSocket)

	mux.Handle("GET /api/commander/chat/sessions",
		s.withCommanderAuth(s.handleCopilotChatSessions))
	mux.Handle("POST /api/commander/chat/sessions/{id}/activate",
		s.withCommanderAuth(s.handleCopilotChatActivateSession))
}
