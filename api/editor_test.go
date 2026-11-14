package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/TwiN/gatus/v5/config"
)

func TestConfigurationEditorAPI(t *testing.T) {
	t.Setenv("GATUS_CONFIG_EDITOR", "true")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "endpoints:\n  - name: example\n    url: https://example.com\n    conditions: ['[STATUS] == 200']\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfiguration(path)
	if err != nil {
		t.Fatal(err)
	}
	api := New(cfg)
	request := func(method, route, body, origin string) *http.Response {
		t.Helper()
		req := httptest.NewRequest(method, route, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res, err := api.Router().Test(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	res := request("GET", "/api/v1/config/editor", "", "")
	if res.StatusCode != 200 || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("read: %d", res.StatusCode)
	}
	var snapshot config.EditorSnapshot
	if err := json.NewDecoder(res.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	draft, _ := json.Marshal(map[string]string{"file": "config.yaml", "content": content, "revision": snapshot.Revision})
	for _, route := range []string{"/api/v1/config/editor/validate", "/api/v1/config/editor"} {
		method := "POST"
		if route == "/api/v1/config/editor" {
			method = "PUT"
		}
		if got := request(method, route, string(draft), "http://example.com").StatusCode; got != 200 {
			t.Fatalf("%s: %d", route, got)
		}
		if got := request(method, route, string(draft), "https://attacker.example").StatusCode; got != 403 {
			t.Fatalf("cross-origin: %d", got)
		}
	}
	if got := request("GET", "/api/v1/config/editor/schema", "", "").StatusCode; got != 200 {
		t.Fatalf("schema: %d", got)
	}
	// A refresh of the SPA route must serve the application as well.
	if got := request("GET", "/configuration", "", "").StatusCode; got != 200 {
		t.Fatalf("SPA: %d", got)
	}
	t.Setenv("GATUS_CONFIG_EDITOR", "false")
	if got := request("GET", "/api/v1/config/editor", "", "").StatusCode; got != 404 {
		t.Fatalf("disabled: %d", got)
	}
	// Registration after the security middleware is essential for raw secrets.
	t.Setenv("GATUS_CONFIG_EDITOR", "true")
	securedContent := content + "security:\n  basic:\n    username: test\n    password-bcrypt-base64: JDJhJDEwJGFhYWFhYWFhYWFhYWFhYWFhYWFhYXUuZkRxLkxudFZNMGR4QzVnYzgzLnBMUjIuN2s5S3U=\n"
	if err := os.WriteFile(path, []byte(securedContent), 0600); err != nil {
		t.Fatal(err)
	}
	secured, err := config.LoadConfiguration(path)
	if err != nil {
		t.Fatal(err)
	}
	protected := New(secured)
	for _, route := range []string{"/api/v1/config/editor", "/api/v1/config/editor/schema", "/api/v1/config/editor/validate"} {
		req := httptest.NewRequest("GET", route, http.NoBody)
		if route == "/api/v1/config/editor/validate" {
			req.Method = "POST"
		}
		response, err := protected.Router().Test(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != 401 {
			t.Fatalf("unprotected %s: %d", route, response.StatusCode)
		}
	}
}
