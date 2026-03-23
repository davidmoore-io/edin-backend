package assistant

import "context"

type contextKey string

const (
	ctxSessionID contextKey = "assistant/session_id"
	ctxUserID    contextKey = "assistant/user_id"
)

// WithContext attaches conversational metadata to the provided context.
func WithContext(ctx context.Context, sessionID, userID string) context.Context {
	if sessionID != "" {
		ctx = context.WithValue(ctx, ctxSessionID, sessionID)
	}
	if userID != "" {
		ctx = context.WithValue(ctx, ctxUserID, userID)
	}
	return ctx
}

// SessionIDFromContext extracts the session identifier if present.
func SessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(ctxSessionID).(string); ok {
		return v
	}
	return ""
}

// UserIDFromContext extracts the user identifier if present.
func UserIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(ctxUserID).(string); ok {
		return v
	}
	return ""
}
