package middleware

import (
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
)

func TestCORSRejectsForeignOriginsBeforeAnyAuthenticatedMutation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	called := false
	r := gin.New()
	r.Use(CORS([]string{"https://bakeu.test"}))
	r.POST("/api/profile", func(c *gin.Context) { called = true; c.Status(204) })
	for _, method := range []string{"OPTIONS", "POST"} {
		req := httptest.NewRequest(method, "/api/profile", nil)
		req.Header.Set("Origin", "https://other.test")
		req.Header.Set("Authorization", "Bearer not-a-proof")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 403 || called || w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("foreign origin received API access")
		}
	}
	req := httptest.NewRequest("POST", "/api/profile", nil)
	req.Header.Set("Origin", "https://bakeu.test")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 204 || !called || w.Header().Get("Access-Control-Allow-Origin") != "https://bakeu.test" {
		t.Fatal("configured browser origin was denied")
	}
}
