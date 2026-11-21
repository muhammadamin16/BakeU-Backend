package handlers

import (
	"bakeu/internal/middleware"
	"bakeu/internal/repository"
	"errors"
	"github.com/gin-gonic/gin"
)

type FavoritesHandler struct {
	repo     *repository.FavoritesRepo
	products *repository.ProductRepo
	markets  *repository.MarketRepo
}

func NewFavoritesHandler(r *repository.FavoritesRepo, p *repository.ProductRepo, m *repository.MarketRepo) *FavoritesHandler {
	return &FavoritesHandler{r, p, m}
}
func (h *FavoritesHandler) Get(c *gin.Context) {
	out, err := h.repo.Get(c.Request.Context(), middleware.UID(c))
	if err != nil {
		orderError(c, err)
		return
	}
	c.JSON(200, out)
}
func (h *FavoritesHandler) Set(c *gin.Context) {
	id := c.Param("id")
	if id == "" || len(id) > 128 {
		c.JSON(400, gin.H{"error": "invalid product"})
		return
	}
	if c.Request.Method == "PUT" {
		p, err := h.products.Get(c.Request.Context(), id)
		if errors.Is(err, repository.ErrNotFound) || err == nil && !p.Available {
			c.JSON(404, gin.H{"error": "Товар недоступен."})
			return
		}
		if err != nil {
			orderError(c, err)
			return
		}
		approved, err := h.markets.IsApprovedSeller(c.Request.Context(), p.SellerID)
		if err != nil {
			orderError(c, err)
			return
		}
		if !approved {
			c.JSON(404, gin.H{"error": "Товар недоступен."})
			return
		}
	}
	out, err := h.repo.Change(c.Request.Context(), middleware.UID(c), nil, func(f *repository.Favorites) error {
		found := false
		ids := []string{}
		for _, item := range f.ProductIDs {
			if item == id {
				found = true
				if c.Request.Method == "DELETE" {
					continue
				}
			}
			ids = append(ids, item)
		}
		if c.Request.Method == "PUT" && !found {
			if len(ids) >= 500 {
				return repository.ErrConflict
			}
			ids = append(ids, id)
		}
		f.ProductIDs = ids
		return nil
	})
	if err != nil {
		orderError(c, err)
		return
	}
	c.JSON(200, out)
}
func (h *FavoritesHandler) Clear(c *gin.Context) {
	revision, exists := c.GetQuery("revision")
	if !exists {
		c.JSON(400, gin.H{"error": "Обновите избранное перед очисткой."})
		return
	}
	out, err := h.repo.Change(c.Request.Context(), middleware.UID(c), &revision, func(f *repository.Favorites) error { f.ProductIDs = []string{}; return nil })
	if err != nil {
		orderError(c, err)
		return
	}
	c.JSON(200, out)
}
