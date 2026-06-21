package handlers

import (
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"

	"bakeu/internal/storage"
)

// allowedImageTypes is the set of content types accepted for uploaded images.
// Anything else (notably text/html) is rejected so a malicious upload cannot be
// served back as an active document.
var allowedImageTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
	"image/gif":  true,
}

// uploadImageFile reads the multipart file in form field `field`, verifies it is
// actually an image by sniffing its bytes (not trusting the client Content-Type),
// stores it in GridFS, and returns its served URL. It writes the error response
// and returns ok=false on any failure.
func uploadImageFile(c *gin.Context, store *storage.Store, field string) (string, bool) {
	fileHeader, err := c.FormFile(field)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing '" + field + "' file"})
		return "", false
	}
	f, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to open uploaded file"})
		return "", false
	}
	defer f.Close()

	// Sniff the real content type from the leading bytes, then rewind so the
	// full file is still streamed to storage.
	head := make([]byte, 512)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read uploaded file"})
		return "", false
	}
	contentType := http.DetectContentType(head[:n])
	if !allowedImageTypes[contentType] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file must be a JPEG, PNG, WebP or GIF image"})
		return "", false
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read uploaded file"})
		return "", false
	}

	// Sanitized filename for metadata: <unixnano>_<basename>.
	name := fmt.Sprintf("%d_%s", time.Now().UnixNano(), filepath.Base(fileHeader.Filename))
	fileID, err := store.Upload(c.Request.Context(), name, contentType, f)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to upload image"})
		return "", false
	}
	return "/api/images/" + fileID, true
}

// safeImageContentType returns ct if it is an accepted image type, otherwise a
// neutral type that browsers will not render as an active document. Defense in
// depth for any files stored before upload validation existed.
func safeImageContentType(ct string) string {
	if allowedImageTypes[ct] {
		return ct
	}
	return "application/octet-stream"
}
