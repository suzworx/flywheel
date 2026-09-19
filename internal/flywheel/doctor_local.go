package flywheel

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Doctor classes for a local model (issue #44).
const (
	ClassLocalDown    = "local endpoint down"
	ClassLocalMissing = "model not pulled"
)

// localEndpointClass checks a "flywheel-local/<model>" model against its
// server before any adapter probe: it reads the flywheel-local provider's
// options.baseURL from .flywheel/opencode-worker.json and GETs <baseURL>/models
// with client. ok is false with ClassLocalDown when the request fails or the
// status is not 2xx, false with ClassLocalMissing when the JSON body's
// "data" list has no entry whose "id" equals <model>, and true (class "")
// when the model is served. A model without the flywheel-local/ prefix, or a
// policy file without the provider, is not checked (ok true).
func localEndpointClass(dir, model string, client *http.Client) (class string, ok bool) {
	// Only check flywheel-local models.
	if !strings.HasPrefix(model, LocalProviderID+"/") {
		return "", true
	}

	modelName := strings.TrimPrefix(model, LocalProviderID+"/")

	// Read the policy file.
	policyPath := filepath.Join(dir, ".flywheel", "opencode-worker.json")
	b, err := os.ReadFile(policyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", true
		}
		return ClassLocalDown, false
	}

	var policy map[string]any
	if err := json.Unmarshal(b, &policy); err != nil {
		return ClassLocalDown, false
	}

	providerMap, ok := policy["provider"].(map[string]any)
	if !ok {
		return "", true
	}

	provider, ok := providerMap[LocalProviderID].(map[string]any)
	if !ok {
		return "", true
	}

	options, ok := provider["options"].(map[string]any)
	if !ok {
		return ClassLocalDown, false
	}

	baseURL, ok := options["baseURL"].(string)
	if !ok {
		return ClassLocalDown, false
	}

	// Trim trailing slash.
	// Only a loopback endpoint is contacted directly: the URL comes from the
	// repository, and doctor on an untrusted checkout must not reach other
	// hosts from the operator's machine (#330 review). Any other host is left
	// to the adapter probe, as before.
	if u, err := url.Parse(baseURL); err != nil || !loopbackHost(u.Hostname()) {
		return "", true
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	modelsURL := baseURL + "/models"

	// GET the models endpoint.
	resp, err := client.Get(modelsURL)
	if err != nil {
		return ClassLocalDown, false
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ClassLocalDown, false
	}

	// Parse the response JSON.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ClassLocalDown, false
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return ClassLocalDown, false
	}

	dataList, ok := result["data"].([]any)
	if !ok {
		return ClassLocalDown, false
	}

	// Check if the model is in the list.
	for _, item := range dataList {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, ok := itemMap["id"].(string)
		if ok && id == modelName {
			return "", true
		}
	}

	return ClassLocalMissing, false
}

// loopbackHost reports whether host is "localhost" or a loopback IP literal.
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
