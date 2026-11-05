// Package server Mock 推理后端：模拟 OpenAI 兼容 Chat Completions。
// 用于本地 MVP 闭环验证（流式 + TTFT 模拟），生产环境由真实 vLLM 替代。
package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// OpenAIModel 模型元数据
type OpenAIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ChatRequest OpenAI Chat 请求
type ChatRequest struct {
	Model         string         `json:"model"`
	Messages      []Message      `json:"messages"`
	Stream        bool           `json:"stream"`
	StreamOptions *StreamOptions `json:"stream_options,omitempty"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// Message 消息
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatResponse 非流式响应
type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice 选项
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage token 统计
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Chunk 流式响应块
type Chunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []ChunkChoice `json:"choices"`
}

// ChunkChoice 流式块选项
type ChunkChoice struct {
	Index        int     `json:"index"`
	Delta        Message `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

// Server Mock 推理服务
type Server struct {
	logger     *slog.Logger
	model      string
	ttftDelay  time.Duration // 模拟首 Token 延迟
	extraDelay time.Duration // 每 Token 间隔
}

// NewServer 创建 Mock 服务
func NewServer(logger *slog.Logger, model string) *Server {
	return &Server{
		logger:     logger,
		model:      model,
		ttftDelay:  180 * time.Millisecond, // 模拟 180ms TTFT
		extraDelay: 30 * time.Millisecond,
	}
}

// Handler 路由
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("GET /health", s.handleHealth)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{
		"object": "list",
		"data": []OpenAIModel{
			{ID: s.model, Object: "model", Created: time.Now().Unix(), OwnedBy: "carrot"},
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体解析失败: " + err.Error()})
		return
	}
	// Mock 服务不校验模型名：验证链路用，任意模型名都响应
	// （生产环境由真实 vLLM 做模型名校验）
	// 取最后一条用户消息作为回复内容
	content := "你好，我是 " + s.model + " 模型。"
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" && req.Messages[i].Content != "" {
			content = "回复: " + truncate(req.Messages[i].Content, 50)
			break
		}
	}

	if req.Stream {
		s.handleStream(w, req, content)
		return
	}
	s.handleNonStream(w, req, content)
}

// handleNonStream 非流式响应
func (s *Server) handleNonStream(w http.ResponseWriter, req ChatRequest, content string) {
	time.Sleep(s.ttftDelay + s.extraDelay)
	now := time.Now().Unix()
	resp := ChatResponse{
		ID:      "chatcmpl-mock-1",
		Object:  "chat.completion",
		Created: now,
		Model:   req.Model,
		Choices: []Choice{{
			Index:        0,
			Message:      Message{Role: "assistant", Content: content},
			FinishReason: "stop",
		}},
		Usage: Usage{PromptTokens: countTokens(req), CompletionTokens: 12, TotalTokens: countTokens(req) + 12},
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleStream 流式 SSE 响应
func (s *Server) handleStream(w http.ResponseWriter, req ChatRequest, content string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "不支持流式"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	now := time.Now().Unix()
	// 模拟 TTFT：首块延迟
	time.Sleep(s.ttftDelay)
	chunks := splitRunes(content, 6)
	for i, ch := range chunks {
		var fr *string
		if i == len(chunks)-1 {
			stop := "stop"
			fr = &stop
		}
		chunk := Chunk{
			ID:      "chatcmpl-mock-stream-1",
			Object:  "chat.completion.chunk",
			Created: now,
			Model:   req.Model,
			Choices: []ChunkChoice{{
				Index:        0,
				Delta:        Message{Role: "assistant", Content: ch},
				FinishReason: fr,
			}},
		}
		data, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
		time.Sleep(s.extraDelay)
	}
	if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
		usage := map[string]interface{}{
			"id": "chatcmpl-mock-stream-1", "object": "chat.completion.chunk", "created": now, "model": req.Model,
			"choices": []interface{}{}, "usage": Usage{PromptTokens: countTokens(req), CompletionTokens: 12, TotalTokens: countTokens(req) + 12},
		}
		data, _ := json.Marshal(usage)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// countTokens 粗略估算输入 token
func countTokens(req ChatRequest) int {
	n := 0
	for _, m := range req.Messages {
		n += len(m.Content)/3 + 2
	}
	return n
}

func splitRunes(s string, n int) []string {
	runes := []rune(s)
	var out []string
	for i := 0; i < len(runes); i += n {
		end := i + n
		if end > len(runes) {
			end = len(runes)
		}
		out = append(out, string(runes[i:end]))
	}
	return out
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "..."
}

// 确保 strings 引用
var _ = strings.TrimSpace
