package errors

import (
	"net/http"
	"testing"
)

func TestNewAPIError_Network(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantMsg string
		wantSug string
	}{
		{"timeout", errStr("timeout"), "连接超时", "网络连接超时"},
		{"connection refused", errStr("connection refused"), "连接被拒绝", "连接被拒绝"},
		{"no such host", errStr("no such host"), "DNS 解析失败", "DNS 解析失败"},
		{"dns lookup", errStr("lookup xxx.com"), "DNS 解析失败", "DNS 解析失败"},
		{"unknown network", errStr("broken pipe"), "网络错误", "请检查网络连接"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewAPIError(0, nil, tt.err)
			if e.Message != tt.wantMsg {
				t.Errorf("Message = %q, want %q", e.Message, tt.wantMsg)
			}
			if e.Code != ErrNetworkTimeout && e.Code != ErrNetworkConnRefused &&
				e.Code != ErrNetworkDNS && e.Code != ErrNetworkUnknown {
				t.Errorf("Code 未识别: %v", e.Code)
			}
		})
	}
}

func TestNewAPIError_HTTP4xx(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		body        string
		wantCode    ErrorCode
		wantMessage string
	}{
		{"401 空响应体", http.StatusUnauthorized, "", ErrAuthUnknown, "鉴权失败"},
		{"403 空响应体", http.StatusForbidden, "", ErrAuthUnknown, "鉴权失败"},
		{"429", http.StatusTooManyRequests, "", ErrAuthRateLimited, "请求频率超限"},
		{"400 context_length", http.StatusBadRequest, "context_length_exceeded", ErrContextLength, "上下文长度超限"},
		{"400 model_not_found", http.StatusBadRequest, "model_not_found", ErrModelNotFound, "模型不存在"},
		{"400 generic", http.StatusBadRequest, "bad params", ErrBadRequest, "请求参数错误"},
		{"404", http.StatusNotFound, "", ErrModelNotFound, "端点不存在"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewAPIError(tt.statusCode, []byte(tt.body), nil)
			if e.Code != tt.wantCode {
				t.Errorf("Code = %v, want %v", e.Code, tt.wantCode)
			}
			if e.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", e.Message, tt.wantMessage)
			}
		})
	}
}

func TestNewAPIError_HTTP5xx(t *testing.T) {
	e := NewAPIError(http.StatusInternalServerError, []byte("internal error"), nil)
	if e.Code != ErrServerError {
		t.Errorf("Code = %v, want ErrServerError", e.Code)
	}
	if e.Message != "服务器错误" {
		t.Errorf("Message = %q", e.Message)
	}
}

func TestNewAPIError_503(t *testing.T) {
	e := NewAPIError(http.StatusServiceUnavailable, nil, nil)
	if e.Code != ErrServerUnavailable {
		t.Errorf("Code = %v, want ErrServerUnavailable", e.Code)
	}
}

func TestNewAPIError_QuotaExhausted(t *testing.T) {
	tests := []struct {
		body string
	}{
		{"quota exceeded"},
		{"insufficient_quota"},
		{"insufficient balance"},
	}
	for _, tt := range tests {
		t.Run(tt.body[:10], func(t *testing.T) {
			e := NewAPIError(http.StatusForbidden, []byte(tt.body), nil)
			if e.Code != ErrAuthQuotaExhausted {
				t.Errorf("Code = %v, want ErrAuthQuotaExhausted", e.Code)
			}
			if e.Message != "额度已用尽" {
				t.Errorf("Message = %q", e.Message)
			}
		})
	}
}

func TestNewAPIError_Unknown(t *testing.T) {
	e := NewAPIError(http.StatusTeapot, []byte("teapot"), nil)
	if e.Code != ErrUnknown {
		t.Errorf("Code = %v, want ErrUnknown", e.Code)
	}
}

func TestAPIError_Error(t *testing.T) {
	e := &APIError{Code: ErrNetworkTimeout, Message: "连接超时", Detail: "timeout after 30s"}
	errStr := e.Error()
	if errStr == "" {
		t.Error("Error() 返回空字符串")
	}
}

func errStr(s string) error { return &testErr{s} }

type testErr struct{ s string }

func (e *testErr) Error() string { return e.s }