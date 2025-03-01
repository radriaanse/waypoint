// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package server

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/handlers"
	"github.com/hashicorp/go-hclog"
)

// httpLogHandler returns an http.Handler that uses the request-scoped and
// annotated logger to write access logs.
func httpLogHandler(handler http.Handler, log hclog.Logger) http.Handler {
	return handlers.CustomLoggingHandler(nil, handler, func(_ io.Writer, params handlers.LogFormatterParams) {
		req := params.Request

		// Extract the Client IP honoring the X-Forwarded-For header set by
		// proxies.
		clientIP, _, err := net.SplitHostPort(req.RemoteAddr)
		if err != nil {
			clientIP = req.RemoteAddr
		}
		if forwardedFor := req.Header.Get("X-Forwarded-For"); forwardedFor != "" {
			clientIP = forwardedFor
		}

		// Extract the URL scheme honoring the X-Forwarded-Proto header set by
		// proxies.
		var forwarded bool
		scheme := req.URL.Scheme
		if forwardedProto := req.Header.Get("X-Forwarded-Proto"); forwardedProto != "" {
			forwarded = true
			scheme = forwardedProto
		}

		// Log the kube health check probe at a trace level while all other
		// requests are at the info level.
		logFunc := log.Info
		if strings.HasPrefix(strings.ToLower(req.UserAgent()), "kube-probe") {
			logFunc = log.Trace
		}

		logFunc(
			fmt.Sprintf("HTTP request: %s %s", req.Method, req.URL.Path),
			"date", params.TimeStamp.Format(time.RFC3339Nano),
			"http.host", req.Host,
			"http.method", req.Method,
			"http.request_path", req.URL.Path,
			"http.remote_addr", clientIP,
			"http.response_size", params.Size,
			"http.scheme", scheme,
			"http.scheme_forwarded", forwarded,
			"http.tls", req.TLS != nil,
			"http.status_code", params.StatusCode,
			"http.useragent", req.UserAgent(),
			"http.version", req.Proto,
		)
	})
}
