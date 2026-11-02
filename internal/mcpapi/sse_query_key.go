package mcpapi

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// sseEndpointEventPrefix 为 SDK 下发 endpoint 事件的固定开头（见 go-sdk mcp.writeEvent）。
const sseEndpointEventPrefix = "event: endpoint\ndata: "

// sseWriter 返回 SSE 连接使用的 ResponseWriter。
//
// 客户端以 ?api_key= 鉴权建立 SSE 连接时，SDK 下发的消息地址（.../sse?sessionid=...）
// 不带 api_key，客户端随后 POST 消息会因缺少 API Key 被拒。此时包装 Writer，在 endpoint
// 事件的地址后补上 api_key；以请求头鉴权的连接原样返回。
func sseWriter(c *gin.Context) http.ResponseWriter {
	if c.Request.Method != http.MethodGet {
		return c.Writer
	}
	key := strings.TrimSpace(c.Query("api_key"))
	if key == "" {
		return c.Writer
	}
	return &sseKeyWriter{ResponseWriter: c.Writer, param: "api_key=" + url.QueryEscape(key)}
}

type sseKeyWriter struct {
	http.ResponseWriter
	param string
	done  bool
}

func (w *sseKeyWriter) Write(p []byte) (int, error) {
	if w.done {
		return w.ResponseWriter.Write(p)
	}
	w.done = true
	if !bytes.HasPrefix(p, []byte(sseEndpointEventPrefix)) {
		return w.ResponseWriter.Write(p)
	}
	rest := p[len(sseEndpointEventPrefix):]
	end := bytes.IndexByte(rest, '\n')
	if end < 0 {
		return w.ResponseWriter.Write(p)
	}
	sep := "?"
	if bytes.IndexByte(rest[:end], '?') >= 0 {
		sep = "&"
	}
	out := make([]byte, 0, len(p)+len(sep)+len(w.param))
	out = append(out, p[:len(sseEndpointEventPrefix)+end]...)
	out = append(out, sep...)
	out = append(out, w.param...)
	out = append(out, rest[end:]...)
	if _, err := w.ResponseWriter.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *sseKeyWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *sseKeyWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
