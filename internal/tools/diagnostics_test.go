package tools

import (
	"context"
	"testing"

	"github.com/scottlz0310/review-raven/internal/autherr"
)

func TestDiagnoseTokenHandlerAuthRequired(t *testing.T) {
	handler := diagnoseTokenHandler()
	// nil request → no token in context → AUTH_REQUIRED
	result, _, err := handler(context.Background(), nil, DiagnoseTokenInput{})
	assertAuthResult(t, result, err, autherr.AUTH_REQUIRED)
}
