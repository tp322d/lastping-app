package mcptools

import (
	"context"
	"fmt"
)

type ctxKey struct{}

// ContextWithClient returns a new context carrying c.
func ContextWithClient(ctx context.Context, c *APIClient) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

// clientFromContext retrieves the *APIClient stored in ctx.
// Returns an error if no client has been seeded.
func clientFromContext(ctx context.Context) (*APIClient, error) {
	c, ok := ctx.Value(ctxKey{}).(*APIClient)
	if !ok || c == nil {
		return nil, fmt.Errorf("no LastPing API client in context: LASTPING_API_KEY may not be set")
	}
	return c, nil
}
