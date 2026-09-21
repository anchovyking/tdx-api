package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

// statusRecorder 包装 ResponseWriter 以捕获状态码和响应字节数
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Flush 透传（部分流式响应需要）
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// accessLogHandler 包装 mux，为每个请求打印一行访问日志到控制台
// 格式: [访问] 200 12ms 192.168.0.100 GET /api/kline?code=000001 (1.2KB)
func accessLogHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		// 完整路径（含 query），便于第三方排查调了哪个接口、传了什么参数
		full := r.URL.Path
		if r.URL.RawQuery != "" {
			full += "?" + r.URL.RawQuery
		}
		log.Printf("[访问] %d %s %s %s %s (%s)",
			rec.status,
			time.Since(start).Round(time.Millisecond),
			clientIP(r),
			r.Method,
			full,
			humanSize(rec.bytes),
		)
	})
}

// clientIP 取真实客户端IP：优先 X-Forwarded-For / X-Real-IP（反代场景），其次 RemoteAddr
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// humanSize 人类可读的字节数：1.2KB / 3.4MB / 512B
func humanSize(n int) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1fMB", float64(n)/1024/1024)
	case n >= 1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	default:
		return fmt.Sprintf("%dB", n)
	}
}
