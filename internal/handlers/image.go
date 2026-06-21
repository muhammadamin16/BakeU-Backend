package handlers

import (
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"bakeu/internal/storage"
)

// ImageHandler serves product images stored in GridFS.
type ImageHandler struct {
	store *storage.Store
}

func NewImageHandler(store *storage.Store) *ImageHandler {
	return &ImageHandler{store: store}
}

// Get handles GET /api/images/:id (public) — streams the image bytes with the
// stored content type.
func (h *ImageHandler) Get(c *gin.Context) {
	contentType, stream, err := h.store.Open(c.Request.Context(), c.Param("id"))
	if errors.Is(err, storage.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "image not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load image"})
		return
	}
	defer stream.Close()

	// Force a safe content type and prevent browser sniffing/inline execution,
	// so a stored file can never be served as an active HTML/JS document.
	c.Header("Content-Type", safeImageContentType(contentType))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Disposition", "inline")
	if _, err := io.Copy(c.Writer, stream); err != nil {
		// Headers/body may already be partially written; nothing reliable left to do.
		return
	}
}
