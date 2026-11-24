package handlers

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"bakeu/internal/models"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestPhoneRegistrationDraftValidation(t *testing.T) {
	lat, lng := 41.31, 69.28
	valid := phoneStartRequest{Purpose: "register", Role: models.RoleSeller, FirstName: "  Али  ", LastName: "  Каримов ", MarketName: " Bakery ", Address: " Tashkent street 1 ", Latitude: &lat, Longitude: &lng}
	u, m, err := valid.registrationDraft("+998901234567")
	if err != nil || u.Name != "Али Каримов" || u.FirstName != "Али" || u.LastName != "Каримов" || m.Name != "Bakery" || m.Address != "Tashkent street 1" || *m.Latitude != lat || u.PasswordHash != "" || u.Email != "" {
		t.Fatalf("bad draft: %v", err)
	}
	// No fake address or unused password hash is stored for phone-only users.
	raw, _ := bson.Marshal(u)
	var doc bson.M
	_ = bson.Unmarshal(raw, &doc)
	if _, ok := doc["email"]; ok {
		t.Fatal("phone-only user stored email")
	}
	if _, ok := doc["passwordHash"]; ok {
		t.Fatal("phone-only user stored password hash")
	}
	for _, change := range []func(*phoneStartRequest){
		func(r *phoneStartRequest) { r.FirstName = " " },
		func(r *phoneStartRequest) { r.LastName = "" },
		func(r *phoneStartRequest) { r.FirstName = strings.Repeat("Я", 81) },
		func(r *phoneStartRequest) { r.Role = "admin" },
		func(r *phoneStartRequest) { r.MarketName = "x" },
		func(r *phoneStartRequest) { r.Address = "" },
		func(r *phoneStartRequest) { r.Latitude = nil },
		func(r *phoneStartRequest) { r.Longitude = nil },
		func(r *phoneStartRequest) { n := 91.0; r.Latitude = &n },
		func(r *phoneStartRequest) { n := 181.0; r.Longitude = &n },
		func(r *phoneStartRequest) { n := math.NaN(); r.Latitude = &n },
		func(r *phoneStartRequest) { n := math.Inf(1); r.Longitude = &n },
	} {
		r := valid
		change(&r)
		if _, _, err := r.registrationDraft("+998901234567"); err == nil {
			t.Fatal("invalid registration accepted")
		}
	}
	customer := valid
	customer.Role = models.RoleCustomer
	customer.Address = "seller-only"
	customer.Latitude = nil
	customer.Longitude = nil
	u, m, err = customer.registrationDraft("+998901234567")
	if err != nil || m != nil || u.Address != "" {
		t.Fatal("customer inherited seller-only data")
	}
	login := phoneStartRequest{Purpose: "login"}
	if u, m, err := login.registrationDraft("+998901234567"); err != nil || u != nil || m != nil {
		t.Fatal("login required signup data")
	}
}

func TestPhoneRequestDoesNotAcceptCredentialsOrIdentity(t *testing.T) {
	var req phoneStartRequest
	if err := json.Unmarshal([]byte(`{"purpose":"register","firstName":"Ali","lastName":"Karimov","role":"customer","email":"ignored@example.invalid","password":"ignored-password","uid":"forged","phoneVerifiedAt":"2026-01-01"}`), &req); err != nil {
		t.Fatal(err)
	}
	u, _, err := req.registrationDraft("+998901234567")
	if err != nil || u.Email != "" || u.PasswordHash != "" || u.UID != "" || u.PhoneVerified() {
		t.Fatal("client controlled credentials or verification")
	}
}
