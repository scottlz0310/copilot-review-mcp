package ghclient

import (
	"errors"
	"net/http"
	"testing"

	"github.com/google/go-github/v85/github"
)

func TestIsAuthError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "REST 401 error response",
			err: &github.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusUnauthorized},
			},
			want: true,
		},
		{
			name: "REST non-401 error response",
			err: &github.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusForbidden},
			},
			want: false,
		},
		{
			name: "githubv4 plain 401 error string",
			err:  errors.New("non-200 OK status code: 401 Unauthorized body: {\"message\":\"Bad credentials\"}"),
			want: true,
		},
		{
			name: "nearby non-401 error string",
			err:  errors.New("non-200 OK status code: 403 Forbidden body: {\"message\":\"Resource not accessible\"}"),
			want: false,
		},
		{
			name: "generic non-auth error",
			err:  errors.New("network timeout"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAuthError(tt.err); got != tt.want {
				t.Fatalf("IsAuthError() = %v, want %v", got, tt.want)
			}
		})
	}
}
