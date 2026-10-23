package main

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/ThomasCrouzet/icloud-mcp/internal/security"
)

const maxHTTPMCPBodyBytes = 1 << 20

// serveHTTP exposes the MCP Streamable HTTP transport at /mcp. It is
// intentionally stateless and non-streaming: this makes it safe behind a
// load-balanced private Service without sticky sessions or retained client
// state. Browser CORS is intentionally absent.
func serveHTTP(mcpServer *server.MCPServer, addr, bearerToken string, errLogger *log.Logger, redactor *security.Redactor) error {
	if strings.TrimSpace(bearerToken) == "" {
		return errors.New("HTTP bearer token cannot be empty")
	}

	transport := server.NewStreamableHTTPServer(
		mcpServer,
		server.WithStateLess(true),
		server.WithDisableStreaming(true),
	)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validBearerToken(r.Header.Get("Authorization"), bearerToken) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="icloud-mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, maxHTTPMCPBodyBytes)
		}
		transport.ServeHTTP(w, r)
	})

	// WriteTimeout is deliberately unset: a future MCP client may use a long
	// lived response. Tool execution itself remains bounded by toolTimeout.
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    8 << 10,
		ErrorLog:          errLogger,
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("HTTP MCP listen: %w", err)
	}
	defer func() { _ = ln.Close() }()
	err = srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	if err != nil {
		return errors.New(redactor.Redact(fmt.Sprintf("HTTP MCP server: %v", err)))
	}
	return nil
}

func validBearerToken(authorization, token string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(authorization, prefix) {
		return false
	}
	provided := strings.TrimPrefix(authorization, prefix)
	if len(provided) != len(token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}
