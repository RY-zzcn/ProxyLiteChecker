package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtractSourceProxyLinesParsesJSON(t *testing.T) {
	lines := extractSourceProxyLines([]byte(`{"proxies":[{"ip":"1.2.3.4","port":8080,"protocol":"http"},{"host":"5.6.7.8","port":1080,"type":"socks5"}]}`), sourceOption{DefaultProtocol: "auto", Parser: "json"})
	if len(lines) != 2 {
		t.Fatalf("expected two parsed proxies, got %d (%#v)", len(lines), lines)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "http://1.2.3.4:8080") || !strings.Contains(joined, "socks5://5.6.7.8:1080") {
		t.Fatalf("unexpected parsed JSON proxies: %q", joined)
	}
}

func TestSourceCatalogMigrationCRUDAndSelection(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	items, err := st.ListSources()
	if err != nil {
		t.Fatalf("list seeded sources: %v", err)
	}
	if len(items) != len(builtinSources()) {
		t.Fatalf("expected %d seeded sources, got %d", len(builtinSources()), len(items))
	}
	for _, item := range items {
		if !item.Builtin || !item.Enabled {
			t.Fatalf("unexpected seeded source: %#v", item)
		}
	}

	builtin := items[0]
	builtin.URL = "https://example.com/updated.txt"
	if err := st.UpdateSource(builtin); err != nil {
		t.Fatalf("update built-in source: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("repeat ensure schema: %v", err)
	}
	items, err = st.ListSources()
	if err != nil || items[0].URL != builtin.URL {
		t.Fatalf("built-in override was not preserved: items=%#v err=%v", items, err)
	}

	custom := sourceOption{
		ID: "custom_example", Name: "Custom Example", URL: "https://example.com/proxies.json",
		DefaultProtocol: "auto", Parser: "json", Enabled: true,
	}
	if err := st.CreateSource(custom); err != nil {
		t.Fatalf("create custom source: %v", err)
	}
	if err := st.CreateSource(custom); err == nil {
		t.Fatal("expected duplicate source ID to fail")
	}
	srv := &server{store: st}
	selected := srv.selectedSources([]string{custom.ID})
	if len(selected) != 1 || selected[0].ID != custom.ID {
		t.Fatalf("custom source was not selectable: %#v", selected)
	}
	custom.Enabled = false
	if err := st.UpdateSource(custom); err != nil {
		t.Fatalf("disable custom source: %v", err)
	}
	if selected := srv.selectedSources([]string{custom.ID}); len(selected) != 0 {
		t.Fatalf("disabled source remained selectable: %#v", selected)
	}
	if err := st.DeleteSource(custom.ID); err != nil {
		t.Fatalf("delete custom source: %v", err)
	}
	if err := st.DeleteSource(custom.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected missing source error, got %v", err)
	}
}

func TestSourceCatalogAPIAndHealthError(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if err := st.EnsureSettingsSchema(); err != nil {
		t.Fatalf("ensure settings schema: %v", err)
	}
	srv := &server{store: st}

	createBody := bytes.NewBufferString(`{"id":"custom_api","name":"API Source","url":"https://example.com/list.txt","default_protocol":"http","parser":"plain","enabled":true}`)
	createResponse := httptest.NewRecorder()
	srv.handleCreateSource(createResponse, httptest.NewRequest(http.MethodPost, "/api/sources", createBody))
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create source status=%d body=%s", createResponse.Code, createResponse.Body.String())
	}

	updateRequest := httptest.NewRequest(http.MethodPatch, "/api/sources/custom_api", bytes.NewBufferString(`{"url":"https://example.com/updated.txt","enabled":false}`))
	updateRequest.SetPathValue("source_id", "custom_api")
	updateResponse := httptest.NewRecorder()
	srv.handleUpdateSource(updateResponse, updateRequest)
	if updateResponse.Code != http.StatusOK {
		t.Fatalf("update source status=%d body=%s", updateResponse.Code, updateResponse.Body.String())
	}

	fetchErr := errors.New("x509: certificate signed by unknown authority")
	if err := st.RecordSourceFetch("custom_api", 0, 0, 0, fetchErr); err != nil {
		t.Fatalf("record source failure: %v", err)
	}
	payload := srv.sourcesPayload()
	var found map[string]any
	for _, item := range payload {
		if item["id"] == "custom_api" {
			found = item
			break
		}
	}
	if found == nil || found["last_error"] != fetchErr.Error() || found["enabled"] != false {
		raw, _ := json.Marshal(found)
		t.Fatalf("unexpected source payload: %s", raw)
	}

	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/sources/custom_api", nil)
	deleteRequest.SetPathValue("source_id", "custom_api")
	deleteResponse := httptest.NewRecorder()
	srv.handleDeleteSource(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusOK {
		t.Fatalf("delete source status=%d body=%s", deleteResponse.Code, deleteResponse.Body.String())
	}
}
