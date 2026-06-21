package handlers

import (
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

// newUploadContext builds a gin context whose request carries a single
// multipart file field with the given raw bytes.
func newUploadContext(t *testing.T, field, filename string, data []byte) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	w.Close()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/upload", &body)
	c.Request.Header.Set("Content-Type", w.FormDataContentType())
	return c, rec
}

func TestUploadImageRejectsNonImage(t *testing.T) {
	// HTML disguised as an image — the XSS payload we must refuse. The store is
	// never reached because validation fails first, so nil is safe here.
	html := []byte("<html><script>steal(document.cookie)</script></html>")
	c, rec := newUploadContext(t, "image", "evil.png", html)

	url, ok := uploadImageFile(c, nil, "image")
	if ok {
		t.Fatal("expected non-image upload to be rejected")
	}
	if url != "" {
		t.Fatalf("expected empty url, got %q", url)
	}
	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestSafeImageContentType(t *testing.T) {
	if got := safeImageContentType("image/png"); got != "image/png" {
		t.Fatalf("png should pass through, got %q", got)
	}
	if got := safeImageContentType("text/html"); got != "application/octet-stream" {
		t.Fatalf("html should be neutralized, got %q", got)
	}
}
