package flywheel

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDoctorLocalServed tests that a served model returns ok true.
func TestDoctorLocalServed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "m1"},
			},
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	Init(dir, false)
	InitLocal(dir, "m1", server.URL+"/v1")

	class, ok := localEndpointClass(dir, "flywheel-local/m1", &http.Client{})
	if !ok {
		t.Errorf("localEndpointClass returned ok=false, want true")
	}
	if class != "" {
		t.Errorf("localEndpointClass returned class=%q, want empty", class)
	}
}

// TestDoctorLocalNotPulled tests that a missing model returns ClassLocalMissing.
func TestDoctorLocalNotPulled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "m1"},
			},
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	Init(dir, false)
	InitLocal(dir, "m1", server.URL+"/v1")

	class, ok := localEndpointClass(dir, "flywheel-local/m2", &http.Client{})
	if ok {
		t.Errorf("localEndpointClass returned ok=true, want false")
	}
	if class != ClassLocalMissing {
		t.Errorf("localEndpointClass returned class=%q, want %q", class, ClassLocalMissing)
	}
}

// TestDoctorLocalDown tests that a closed server returns ClassLocalDown.
func TestDoctorLocalDown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	url := server.URL
	server.Close()

	dir := t.TempDir()
	Init(dir, false)
	InitLocal(dir, "m1", url+"/v1")

	class, ok := localEndpointClass(dir, "flywheel-local/m1", &http.Client{})
	if ok {
		t.Errorf("localEndpointClass returned ok=true, want false")
	}
	if class != ClassLocalDown {
		t.Errorf("localEndpointClass returned class=%q, want %q", class, ClassLocalDown)
	}
}

// TestDoctorLocalNon200 tests that a 500 response returns ClassLocalDown.
func TestDoctorLocalNon200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	dir := t.TempDir()
	Init(dir, false)
	InitLocal(dir, "m1", server.URL+"/v1")

	class, ok := localEndpointClass(dir, "flywheel-local/m1", &http.Client{})
	if ok {
		t.Errorf("localEndpointClass returned ok=true, want false")
	}
	if class != ClassLocalDown {
		t.Errorf("localEndpointClass returned class=%q, want %q", class, ClassLocalDown)
	}
}

// TestDoctorLocalOtherModelsUnchecked tests that non-local models are not checked.
func TestDoctorLocalOtherModelsUnchecked(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server handler was called, but non-local models should not trigger a request")
	}))
	defer server.Close()

	dir := t.TempDir()
	Init(dir, false)
	InitLocal(dir, "m1", server.URL+"/v1")

	class, ok := localEndpointClass(dir, "openrouter/x", &http.Client{})
	if !ok {
		t.Errorf("localEndpointClass returned ok=false for non-local model, want true")
	}
	if class != "" {
		t.Errorf("localEndpointClass returned class=%q for non-local model, want empty", class)
	}
}

// TestDoctorLocalUnknownWorker tests that DoctorWorker returns an error for unknown workers.
func TestDoctorLocalUnknownWorker(t *testing.T) {
	dir := t.TempDir()
	Init(dir, false)

	_, err := DoctorWorker(dir, "nope")
	if err == nil {
		t.Errorf("DoctorWorker returned nil error for unknown worker")
	}
	if !strings.Contains(err.Error(), `"nope"`) || !errors.Is(err, ErrUnknownWorker) {
		t.Errorf("DoctorWorker error %q: want it to name \"nope\" and wrap ErrUnknownWorker", err.Error())
	}
}
