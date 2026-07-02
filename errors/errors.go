// Package errors 提供结构化的错误类型，用于 LLM API 测试中的错误分类和处理。
//
// 设计目标:
// - 将所有可能的错误映射到有限的错误码，便于上层做错误处理决策
// - 每个错误附带中文描述（用户可见）和解决建议（可操作）
// - 根据错误的严重程度和可恢复性分类
//
// 错误分类层级:
// 1. 网络层: DNS 解析失败 / 连接拒绝 / 超时 / 未知
// 2. 鉴权层: Key 无效 / 过期 / 额度用尽 / 频率超限
// 3. 服务端: 服务器错误 / 过载 / 不可用
// 4. 请求层: 参数错误 / 模型不存在 / 上下文超长
// 5. 未知: 无法归类的错误
package errors

import (
	"fmt"
	"net/http"
	"strings"
)

// ErrorCode 表示 LLM API 测试的错误类型码
// 用于程序化判断错误类型，不建议直接展示给用户
type ErrorCode string

const (
	// ─── 网络错误（前缀 network_）───────────────────
	ErrNetworkTimeout       ErrorCode = "network_timeout"       // 连接超时
	ErrNetworkDNS           ErrorCode = "network_dns"           // DNS 解析失败
	ErrNetworkConnRefused   ErrorCode = "network_conn_refused"  // 连接被拒绝
	ErrNetworkUnknown       ErrorCode = "network_unknown"       // 其他网络错误

	// ─── API 鉴权错误（前缀 auth_）───────────────────
	ErrAuthInvalidKey       ErrorCode = "auth_invalid_key"       // API Key 无效
	ErrAuthExpired          ErrorCode = "auth_expired"           // Token 已过期
	ErrAuthQuotaExhausted   ErrorCode = "auth_quota_exhausted"   // 额度已用尽
	ErrAuthRateLimited      ErrorCode = "auth_rate_limited"      // 请求频率超限
	ErrAuthUnknown          ErrorCode = "auth_unknown"           // 未知鉴权错误

	// ─── 服务端错误（前缀 server_）───────────────────
	ErrServerError          ErrorCode = "server_error"            // 服务器内部错误
	ErrServerOverloaded     ErrorCode = "server_overloaded"      // 服务器过载
	ErrServerUnavailable    ErrorCode = "server_unavailable"     // 服务暂不可用

	// ─── 请求错误（前缀 request_）───────────────────
	ErrBadRequest           ErrorCode = "bad_request"             // 请求参数错误
	ErrModelNotFound        ErrorCode = "model_not_found"         // 模型不存在
	ErrContextLength        ErrorCode = "context_length_exceeded" // 上下文长度超限

	// ─── 未知（兜底）────────────────────────────────
	ErrUnknown              ErrorCode = "unknown"                 // 无法归类的错误
)

// APIError 是 LLM API 调用的结构化错误
//
// Code: 程序化错误码，用于上层逻辑判断
// Message: 用户可见的错误消息（中文）
// Detail: 技术详情（可包含 HTTP 响应体或库错误信息）
// StatusCode: HTTP 状态码（0 表示网络层错误）
// Suggestion: 解决建议（可操作的中文描述）
type APIError struct {
	Code       ErrorCode `json:"code"`                 // 错误码
	Message    string    `json:"message"`              // 用户可见的错误消息（中文）
	Detail     string    `json:"detail,omitempty"`     // 技术详情
	StatusCode int       `json:"statusCode,omitempty"` // HTTP 状态码
	Suggestion string    `json:"suggestion,omitempty"` // 解决建议
}

func (e *APIError) Error() string {
	return fmt.Sprintf("[%s] %s: %s", e.Code, e.Message, e.Detail)
}

// NewAPIError 根据 HTTP 响应状态码和错误信息创建结构化错误
//
// 错误判断优先级:
// 1. 网络层错误（err != nil）: DNS/连接/超时
// 2. HTTP 4xx: 鉴权/限流/参数
// 3. HTTP 5xx: 服务端
// 4. 其他状态码: 未知
//
// 参数:
//   - statusCode: HTTP 响应状态码（网络层错误时为 0）
//   - body: HTTP 响应体（用于从响应内容中提取错误详情）
//   - err: Go 网络错误（HTTP 调用成功时为 nil）
func NewAPIError(statusCode int, body []byte, err error) *APIError {
	// 优先检查网络层错误
	// 此时 statusCode 为 0（没有 HTTP 响应），body 为空
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
		// 模型不存在错误
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

	// 兜底: 无法归类的错误
	return &APIError{
		Code:       ErrUnknown,
		Message:    "未知错误",
		Detail:     fmt.Sprintf("status=%d body=%s", statusCode, bodyStr),
		StatusCode: statusCode,
		Suggestion: "请检查配置后重试",
	}
}

// classifyNetworkError 将网络层错误映射为结构化错误
//
// 判断依据是错误消息字符串中包含的关键词:
// - "timeout" → 连接超时（最常见）
// - "connection refused" → 连接被拒绝（端口未开放）
// - "no such host" / "lookup" → DNS 解析失败
// - 其他 → 未知网络错误
//
// 这些关键词来自 Go 标准库的 net 包错误消息。
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
//
// 在 401/403 状态下，根据响应体中的关键词进一步细分:
// - "invalid_api_key" / "InvalidAuthentication" → Key 格式错误
// - "quota" / "exhausted" / "insufficient_quota" / "insufficient balance" → 额度用完
// - "token_expired" / "expired" → Token 过期
// - "rate" / "limit" → 频率超限（部分 API 用 403 而非 429）
// - 其他 → 未知鉴权错误
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
		strings.Contains(bodyStr, "insufficient_quota") || strings.Contains(bodyStr, "insufficient balance") ||
		strings.Contains(bodyStr, "INSUFFICIENT_BALANCE") || strings.Contains(bodyStr, "insufficient_balance") ||
		strings.Contains(strings.ToLower(bodyStr), "insufficient"):
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
