package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"bakeu/internal/config"
)

func TestBrowserSessionFlow(t *testing.T) {
	uri := os.Getenv("BAKEU_TEST_MONGO_URI")
	if !strings.HasPrefix(uri, "mongodb://127.0.0.1:") {
		t.Skip("set BAKEU_TEST_MONGO_URI to a disposable loopback MongoDB")
	}
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx)
	db := client.Database("bakeu_browser_test_" + bson.NewObjectID().Hex())
	defer db.Drop(ctx)
	const origin = "https://shop.example.com"
	cfg := config.Config{JWTSecret: "test-only-secret", RefreshSessions: true, BrowserOrigins: []string{origin}}
	r := New(db, cfg)

	call := func(method, path, requestOrigin, mode, body, bearer string, cookie *http.Cookie, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", requestOrigin)
		req.Header.Set("X-BakeU-Session", mode)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s: got %d, want %d, body %s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}
	read := func(w *httptest.ResponseRecorder) (string, *http.Cookie) {
		t.Helper()
		var response struct {
			Token        string `json:"token"`
			RefreshToken string `json:"refreshToken"`
		}
		cookies := w.Result().Cookies()
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Token == "" ||
			response.RefreshToken != "" || len(cookies) != 1 {
			t.Fatal("expected bearer token in JSON and a single refresh cookie")
		}
		cookie := cookies[0]
		if !cookie.HttpOnly || !cookie.Secure || cookie.Domain != "" || cookie.Path != "/" ||
			cookie.MaxAge <= 0 || cookie.SameSite != http.SameSiteStrictMode {
			t.Fatalf("unexpected cookie attributes for %s", cookie.Name)
		}
		return response.Token, cookie
	}
	preflight := call("OPTIONS", "/api/auth/refresh", origin, "", "", "", nil, 204)
	if preflight.Header().Get("Access-Control-Allow-Origin") != origin ||
		preflight.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatal("trusted origin did not receive credentialed CORS")
	}
	body := `{"email":"buyer@example.com","password":"test-password","role":"customer"}`
	call("POST", "/api/auth/signup", "https://other.example.com", "browser", body, "", nil, 403)
	_, signupCookie := read(call("POST", "/api/auth/signup", origin, "browser", body, "", nil, 201))
	call("POST", "/api/auth/logout", origin, "browser", "", "", signupCookie, 204)
	_, cookie := read(call("POST", "/api/auth/login", origin, "browser", body, "", nil, 200))
	call("POST", "/api/auth/refresh", "https://other.example.com", "browser", "", "", cookie, 403)
	call("POST", "/api/auth/refresh", "", "browser", "", "", cookie, 403)
	call("POST", "/api/auth/refresh", origin, "", "{}", "", cookie, 400)
	r = New(db, cfg)
	token, next := read(call("POST", "/api/auth/refresh", origin, "browser", "", "", cookie, 200))
	if next.Value == cookie.Value {
		t.Fatal("refresh cookie did not rotate")
	}
	call("GET", "/api/me", origin, "", "", token, nil, 200)
	logout := call("POST", "/api/auth/logout", origin, "browser", "", "", next, 204)
	cleared := logout.Result().Cookies()
	if len(cleared) != 1 || cleared[0].MaxAge >= 0 {
		t.Fatal("logout did not clear the refresh cookie")
	}
	call("GET", "/api/me", origin, "", "", token, nil, 401)
	call("POST", "/api/auth/refresh", origin, "browser", "", "", next, 401)
}
