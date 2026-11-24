package handlers

import (
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"bakeu/internal/models"
)

// Phone registration has no email/password fields. Legacy credentials remain
// on existing users, never copied or rewritten by this endpoint.
type phoneStartRequest struct {
	Purpose    string      `json:"purpose"`
	Phone      string      `json:"phone"`
	FirstName  string      `json:"firstName"`
	LastName   string      `json:"lastName"`
	Role       models.Role `json:"role"`
	MarketName string      `json:"marketName"`
	Address    string      `json:"address"`
	Latitude   *float64    `json:"lat"`
	Longitude  *float64    `json:"lng"`
}

func validText(value string, min, max int) bool {
	n := utf8.RuneCountInString(value)
	return n >= min && n <= max && !strings.ContainsAny(value, "\x00\r\n")
}

func (req phoneStartRequest) registrationDraft(phone string) (*models.User, *models.Market, error) {
	if req.Purpose == "login" {
		return nil, nil, nil
	}
	first, last := strings.TrimSpace(req.FirstName), strings.TrimSpace(req.LastName)
	if !req.Role.Valid() || !validText(first, 1, 80) || !validText(last, 1, 80) {
		return nil, nil, errors.New("Укажите имя, фамилию и тип аккаунта.")
	}
	draft := &models.User{FirstName: first, LastName: last, Name: first + " " + last, Phone: phone, Role: req.Role}
	if req.Role == models.RoleCustomer {
		return draft, nil, nil
	}
	name, address := strings.TrimSpace(req.MarketName), strings.TrimSpace(req.Address)
	if !validText(name, 2, 150) || !validText(address, 3, 250) {
		return nil, nil, errors.New("Укажите название магазина и подробный адрес.")
	}
	if req.Latitude == nil || req.Longitude == nil || math.IsNaN(*req.Latitude) || math.IsNaN(*req.Longitude) || math.IsInf(*req.Latitude, 0) || math.IsInf(*req.Longitude, 0) || math.Abs(*req.Latitude) > 90 || math.Abs(*req.Longitude) > 180 {
		return nil, nil, errors.New("Выберите точку магазина на карте.")
	}
	draft.Address = address
	return draft, &models.Market{Name: name, Address: address, Latitude: req.Latitude, Longitude: req.Longitude}, nil
}
