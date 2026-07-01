// Package errors 提供结构化的错误类型，用于 LLM API 测试中的错误分类和处理。
// 复用 token-refresher-gui/errors 的设计模式，适配 LLM 测试场景。
package errors

import (
	"fmt"
	"net/http"
	"strings"
)

// ErrorCode 表示 LLM API 测试的错误类型码
type ErrorCode string

const (
	// ---- 网络错误 ----
	ErrNetworkTimeout       ErrorCode = "network_timeout"
	ErrNetworkDNS           ErrorCode = "network_dns"
	ErrNetworkConnRefused   ErrorCode = "network_conn_refused"
	ErrNetworkUnknown       ErrorCode = "network_unknown"

	// ---- API 鉴权错误 ----
	ErrAuthInvalidKey       ErrorCode = "auth_invalid_key"
	ErrAuthExpired          ErrorCode = "auth_expired"
	ErrAuthQuotaExhausted   ErrorCode = "auth_quota_exhausted"
	ErrAuthRateLimited      ErrorCode = "auth_rate_limited"
	ErrAuthUnknown          ErrorCode = "auth_unknown"

	// ---- 服务端错误 ----
	ErrServerError          ErrorCode = "server_error"
	ErrServerOverloaded     ErrorCode = "server_overloaded"
	ErrServerUnavailable    ErrorCode = "server_unavailable"

	// ---- 请求错误 ----
	ErrBadRequest           ErrorCode = "bad_request"
	ErrModelNotFound        ErrorCode = "model_not_found"
	ErrContextLength        ErrorCode = "context_length_exceeded"

	// ---- 未知 ----
	ErrUnknown              ErrorCode = "unknown"
)

// APIError 是 LLM API 调用的结构化错误
type APIError struct {
	Code       ErrorCode `json:"code"`
	Message    string    `json:"message"`    // 用户可见的错误消息（中文）
	Detail     string    `json:"detail,omitempty"` // 技术详情
	StatusCode int       `json:"statusCode,omitempty"`
	Suggestion string    `json:"suggestion,omitempty"` // 解决建议
}

func (e *APIError) Error() string {
	return fmt.Sprintf("[%s] %s: %s", e.Code, e.Message, e.Detail)
}

// NewAPIError 根据 HTTP 响应状态码和错误信息创建结构化错误
func NewAPIError(statusCode int, body []byte, err error) *APIError {
	// 优先检查网络层错误
	if err != nil {
		if e := classifyNetworkError(err); e != nil {
			return e
		}
	}

	// 根据 HTTP 状态码分类
	bodyStr := string(body)
	switch {
	case statusCode == http.StatusTooManyRequests:
		return &APIError{
			Code:       ErrAuthRateLimited,
			Message:    "请求频率超限",
			Detail:     bodyStr,
			StatusCode: statusCode,
			Suggestion: "请降低请求频率或稍后重试",
		}
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		return classifyAuthError(bodyStr, statusCode)
	case statusCode == http.StatusBadRequest:
		if strings.Contains(bodyStr, "context_length") || strings.Contains(bodyStr, "maximum context") {
			return &APIError{
				Code:       ErrContextLength,
				Message:    "上下文长度超限",
				Detail:     bodyStr,
				StatusCode: statusCode,
				Suggestion: "请减少消息长度或增加 max_tokens",
			}
		}
		if strings.Contains(bodyStr, "model_not_found") || strings.Contains(bodyStr, "not found") {
			return &APIError{
				Code:       ErrModelNotFound,
				Message:    "模型不存在",
				Detail:     bodyStr,
				StatusCode: statusCode,
				Suggestion: "请检查模型名称是否正确",
			}
		}
		return &APIError{
			Code:       ErrBadRequest,
			Message:    "请求参数错误",
			Detail:     bodyStr,
			StatusCode: statusCode,
			Suggestion: "请检查请求参数",
		}
	case statusCode == http.StatusNotFound:
		return &APIError{
			Code:       ErrModelNotFound,
			Message:    "端点不存在",
			Detail:     bodyStr,
			StatusCode: statusCode,
			Suggestion: "请检查 API 地址和路径是否正确",
		}
	case statusCode == http.StatusServiceUnavailable:
		return &APIError{
			Code:       ErrServerUnavailable,
			Message:    "服务暂不可用",
			Detail:     bodyStr,
			StatusCode: statusCode,
			Suggestion: "服务暂时不可用，请稍后重试",
		}
	case statusCode >= 500:
		return &APIError{
			Code:       ErrServerError,
			Message:    "服务器错误",
			Detail:     fmt.Sprintf("服务器返回 %d", statusCode),
			StatusCode: statusCode,
			Suggestion: "API 服务端异常，建议稍后重试",
		}
	}

	return &APIError{
		Code:       ErrUnknown,
		Message:    "未知错误",
		Detail:     fmt.Sprintf("status=%d body=%s", statusCode, bodyStr),
		StatusCode: statusCode,
		Suggestion: "请检查配置后重试",
	}
}

// classifyNetworkError 将网络层错误映射为结构化错误
func classifyNetworkError(err error) *APIError {
	errMsg := err.Error()
	switch {
	case strings.Contains(errMsg, "timeout"):
		return &APIError{
			Code:       ErrNetworkTimeout,
			Message:    "连接超时",
			Detail:     errMsg,
			Suggestion: "网络连接超时，请检查网络或代理设置",
		}
	case strings.Contains(errMsg, "connection refused"):
		return &APIError{
			Code:       ErrNetworkConnRefused,
			Message:    "连接被拒绝",
			Detail:     errMsg,
			Suggestion: "连接被拒绝，请检查 API 地址和端口是否正确",
		}
	case strings.Contains(errMsg, "no such host") || strings.Contains(errMsg, "lookup"):
		return &APIError{
			Code:       ErrNetworkDNS,
			Message:    "DNS 解析失败",
			Detail:     errMsg,
			Suggestion: "DNS 解析失败，请检查网络连接或 API 地址",
		}
	default:
		return &APIError{
			Code:       ErrNetworkUnknown,
			Message:    "网络错误",
			Detail:     errMsg,
			Suggestion: "请检查网络连接后重试",
		}
	}
}

// classifyAuthError 根据响应体内容分类鉴权错误
func classifyAuthError(bodyStr string, statusCode int) *APIError {
	switch {
	case strings.Contains(bodyStr, "invalid_api_key") || strings.Contains(bodyStr, "InvalidAuthentication"):
		return &APIError{
			Code:       ErrAuthInvalidKey,
			Message:    "API Key 无效",
			Detail:     "服务器拒绝该 API Key",
			StatusCode: statusCode,
			Suggestion: "请检查 API Key 是否正确",
		}
	case strings.Contains(bodyStr, "quota") || strings.Contains(bodyStr, "exhausted") ||
		strings.Contains(bodyStr, "insufficient_quota") || strings.Contains(bodyStr, "insufficient balance"):
		return &APIError{
			Code:       ErrAuthQuotaExhausted,
			Message:    "额度已用尽",
			Detail:     bodyStr,
			StatusCode: statusCode,
			Suggestion: "账户余额或配额不足，请充值或更换 API Key",
		}
	case strings.Contains(bodyStr, "token_expired") || strings.Contains(bodyStr, "expired"):
		return &APIError{
			Code:       ErrAuthExpired,
			Message:    "Token 已过期",
			Detail:     bodyStr,
			StatusCode: statusCode,
			Suggestion: "请重新获取 API Key",
		}
	case strings.Contains(bodyStr, "rate") || strings.Contains(bodyStr, "limit"):
		return &APIError{
			Code:       ErrAuthRateLimited,
			Message:    "请求频率超限",
			Detail:     bodyStr,
			StatusCode: statusCode,
			Suggestion: "请降低请求频率或稍后重试",
		}
	default:
		return &APIError{
			Code:       ErrAuthUnknown,
			Message:    "鉴权失败",
			Detail:     bodyStr,
			StatusCode: statusCode,
			Suggestion: "请检查 API Key 和权限配置",
		}
	}
}