package settings

import (
	"errors"
	"net/url"
	"path"
	"strings"
	"unicode"
)

type ModelProvider string

const (
	DeepSeek  ModelProvider = "deepseek"
	ChatAPI   ModelProvider = "chat_completions"
	Responses ModelProvider = "responses"
	Messages  ModelProvider = "messages"
)

// ModelProfile is non-secret. A credential belongs to the selected protocol
// and exact normalized base URL. Models at the same endpoint share a key.
type ModelProfile struct {
	Provider ModelProvider `json:"provider"`
	BaseURL  string        `json:"base_url,omitempty"`
	Model    string        `json:"model"`
}

func (p ModelProvider) Label() string {
	switch p {
	case DeepSeek:
		return "DeepSeek"
	case ChatAPI:
		return "Chat Completions"
	case Responses:
		return "OpenAI Responses"
	case Messages:
		return "Anthropic Messages"
	}
	return ""
}

func (p ModelProvider) DefaultURL() string {
	switch p {
	case DeepSeek:
		return "https://api.deepseek.com"
	case ChatAPI, Responses:
		return "https://api.openai.com/v1"
	case Messages:
		return "https://api.anthropic.com/v1"
	}
	return ""
}

func (p ModelProfile) Normalize() (ModelProfile, error) {
	if p.Provider == "" {
		p.Provider = DeepSeek
	}
	if p.Provider.DefaultURL() == "" {
		return ModelProfile{}, errors.New("未知的模型协议")
	}
	p.Model = strings.TrimSpace(p.Model)
	if p.Model == "" && p.Provider == DeepSeek {
		p.Model = DefaultModel
	}
	if err := ValidateModel(p.Model); err != nil {
		return ModelProfile{}, err
	}
	base := strings.TrimSpace(p.BaseURL)
	if base == "" {
		base = p.Provider.DefaultURL()
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		strings.ContainsAny(base, "?#\\") || u.RawPath != "" ||
		strings.ContainsAny(u.Path, "%\\") || strings.IndexFunc(u.Path, unicode.IsSpace) >= 0 {
		return ModelProfile{}, errors.New("模型接口须使用 HTTPS 基础地址，不能含凭据、查询参数或片段")
	}
	clean := strings.TrimRight(u.Path, "/")
	if clean != "" && path.Clean(clean) != clean {
		return ModelProfile{}, errors.New("模型接口路径无效")
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = clean
	p.BaseURL = u.String()
	return p, nil
}

func (p ModelProfile) CredentialScope() string {
	return string(p.Provider) + "\n" + p.BaseURL
}
