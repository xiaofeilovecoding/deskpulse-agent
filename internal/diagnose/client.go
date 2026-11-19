package diagnose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/settings"
)

const (
	defaultBaseURL       = "https://api.deepseek.com"
	defaultTimeout       = 60 * time.Second
	maxRequestBytes      = 96 << 10
	maxResponseBytes     = 256 << 10
	maxChatResponseBytes = 64 << 10
)

// APIError is a fixed-code, user-readable error. It never contains a key,
// request body or full model response.
type APIError struct {
	Code    string
	Message string
	Status  int
}

func (e *APIError) Error() string { return e.Message }

// CodeOf returns the stable error code for APIError values.
func CodeOf(err error) string {
	var api *APIError
	if errors.As(err, &api) {
		return api.Code
	}
	if err == nil {
		return ""
	}
	return "internal_error"
}

var (
	ErrNoKey     = &APIError{Code: "deepseek_not_configured", Message: "未配置 DeepSeek API Key，采集、历史与导出仍可用"}
	ErrBusy      = &APIError{Code: "analysis_busy", Message: "已有诊断正在进行，请等待完成或先取消"}
	ErrAuth      = &APIError{Code: "deepseek_auth_failed", Message: "DeepSeek 认证失败，请检查 API Key"}
	ErrRate      = &APIError{Code: "deepseek_rate_limited", Message: "DeepSeek 请求受限或配额不足，请稍后重试"}
	ErrTimeout   = &APIError{Code: "deepseek_timeout", Message: "DeepSeek 请求超时，请稍后重试"}
	ErrNetwork   = &APIError{Code: "deepseek_network_error", Message: "无法连接 DeepSeek，请检查网络"}
	ErrInvalid   = &APIError{Code: "deepseek_invalid_response", Message: "DeepSeek 返回了无效或空白内容"}
	ErrTruncated = &APIError{Code: "deepseek_truncated", Message: "DeepSeek 输出被截断，请重试或缩小窗口"}
	ErrRedirect  = &APIError{Code: "deepseek_redirect_refused", Message: "DeepSeek 返回重定向，已停止发送凭据"}
)

var errCrossHostRedirect = errors.New("cross-host redirect refused")

// Client sends bounded, non-streaming requests to one model endpoint.
type Client struct {
	BaseURL  string
	Provider settings.ModelProvider
	HTTP     *http.Client
	MaxReq   int64
	MaxResp  int64
}

// NewClient builds a client with a redirect policy that refuses to carry the
// Authorization header to another host. The key is only ever placed in the
// Authorization header, never in a URL or log line.
func NewClient(baseURL string, timeout time.Duration) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	host := ""
	if u, err := url.Parse(baseURL); err == nil {
		host = u.Host
	}
	hc := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			if host != "" && !strings.EqualFold(req.URL.Host, host) {
				return errCrossHostRedirect
			}
			if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
				return errCrossHostRedirect
			}
			return nil
		},
	}
	return &Client{BaseURL: baseURL, Provider: settings.DeepSeek, HTTP: hc, MaxReq: maxRequestBytes, MaxResp: maxResponseBytes}
}

type chatRequest struct {
	Model          string            `json:"model"`
	Messages       []chatMessage     `json:"messages"`
	Temperature    float64           `json:"temperature,omitempty"`
	MaxTokens      int               `json:"max_tokens"`
	ResponseFormat map[string]string `json:"response_format"`
	Thinking       map[string]string `json:"thinking,omitempty"`
	Stream         bool              `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatEnvelope struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Chat sends one JSON-mode completion request and returns the raw content.
func (c *Client) Chat(ctx context.Context, key, model, system, user string) ([]byte, error) {
	content, err := c.complete(ctx, key, model, system, user)
	if err != nil {
		return nil, c.providerError(err)
	}
	if key != "" && containsModelCredential(content, key) {
		return nil, c.providerError(ErrInvalid)
	}
	return content, nil
}

func containsModelCredential(content []byte, key string) bool {
	if bytes.Contains(content, []byte(key)) {
		return true
	}
	encoded, err := json.Marshal(key)
	if err == nil && len(encoded) > 2 && bytes.Contains(content, encoded[1:len(encoded)-1]) {
		return true
	}
	var value any
	return json.Unmarshal(content, &value) == nil && containsCredentialText(value, key)
}

func containsCredentialText(value any, key string) bool {
	switch data := value.(type) {
	case string:
		return strings.Contains(data, key)
	case []any:
		for _, child := range data {
			if containsCredentialText(child, key) {
				return true
			}
		}
	case map[string]any:
		for _, child := range data {
			if containsCredentialText(child, key) {
				return true
			}
		}
	}
	return false
}

func (c *Client) complete(ctx context.Context, key, model, system, user string) ([]byte, error) {
	if strings.TrimSpace(key) == "" {
		return nil, ErrNoKey
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = "deepseek-flash"
	}
	route, reqBody := c.requestBody(model, system, user)
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, &APIError{Code: "internal_error", Message: "无法构建诊断请求"}
	}
	maxReq := c.MaxReq
	if maxReq <= 0 {
		maxReq = maxRequestBytes
	}
	if int64(len(data)) > maxReq {
		return nil, &APIError{Code: "request_too_large", Message: "诊断摘要超过发送上限"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+route, bytes.NewReader(data))
	if err != nil {
		return nil, &APIError{Code: "internal_error", Message: "无法构建诊断请求"}
	}
	c.authorize(req, key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "DeskPulse Agent/0.1")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, classifyTransport(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, classifyStatus(resp.StatusCode)
	}
	limit := c.MaxResp
	if limit <= 0 || limit > maxChatResponseBytes {
		limit = maxChatResponseBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, classifyTransport(err)
	}
	if int64(len(body)) > limit {
		return nil, ErrTruncated
	}
	switch c.Provider {
	case settings.Responses:
		return decodeResponses(body)
	case settings.Messages:
		return decodeMessages(body)
	}
	var env chatEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, ErrInvalid
	}
	if len(env.Choices) == 0 {
		return nil, ErrInvalid
	}
	choice := env.Choices[0]
	if strings.EqualFold(choice.FinishReason, "length") {
		return nil, ErrTruncated
	}
	return textOutput(choice.Message.Content)
}

// TestConnection performs a lightweight authenticated GET /models.
func (c *Client) TestConnection(ctx context.Context, key string) error {
	return c.providerError(c.testConnection(ctx, key))
}

func (c *Client) testConnection(ctx context.Context, key string) error {
	if strings.TrimSpace(key) == "" {
		return ErrNoKey
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/models", nil)
	if err != nil {
		return ErrInvalid
	}
	c.authorize(req, key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "DeskPulse Agent/0.1")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return classifyTransport(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return classifyStatus(resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return nil
}

func classifyStatus(status int) error {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &APIError{Code: ErrAuth.Code, Message: ErrAuth.Message, Status: status}
	case status == http.StatusTooManyRequests:
		return &APIError{Code: ErrRate.Code, Message: ErrRate.Message, Status: status}
	case status >= 500:
		return &APIError{Code: "deepseek_upstream_error", Message: fmt.Sprintf("DeepSeek 服务暂时不可用（HTTP %d）", status), Status: status}
	default:
		return &APIError{Code: "deepseek_request_failed", Message: fmt.Sprintf("DeepSeek 请求失败（HTTP %d）", status), Status: status}
	}
}

func classifyTransport(err error) error {
	if errors.Is(err, errCrossHostRedirect) {
		return ErrRedirect
	}
	if errors.Is(err, context.Canceled) {
		return &APIError{Code: "canceled", Message: "诊断已取消"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrTimeout
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return ErrTimeout
	}
	return ErrNetwork
}
