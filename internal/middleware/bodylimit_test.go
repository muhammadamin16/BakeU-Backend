package middleware

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

func TestBodyLimitRejectsOversizedBody(t *testing.T) {
	r := gin.New()
	r.POST("/x", BodyLimit(10), func(c *gin.Context) {
		if _, err := io.ReadAll(c.Request.Body); err != nil {
			c.Status(413)
			return
		}
		c.Status(200)
	})

	// Under the limit succeeds.
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/x", strings.NewReader("tiny")))
	if rec.Code != 200 {
		t.Fatalf("small body should pass, got %d", rec.Code)
	}

	// Over the limit is refused.
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/x", strings.NewReader(strings.Repeat("a", 100))))
	if rec.Code != 413 {
		t.Fatalf("oversized body should be rejected, got %d", rec.Code)
	}
}
