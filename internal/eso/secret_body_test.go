package eso

import (
	"encoding/json"
	"testing"
)

func TestTheClientCertificateSecretBodyCarriesItsTypeMeta(t *testing.T) {
	body, err := clientSecretBody(testOptions())
	if err != nil {
		t.Fatalf("clientSecretBody: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("the body is not JSON: %v", err)
	}
	if decoded["apiVersion"] != "v1" || decoded["kind"] != "Secret" {
		t.Fatalf("the server-side-apply body carries apiVersion=%v kind=%v, and a typed object "+
			"marshalled without them is refused by the dynamic client with \"invalid object type: "+
			" /, Kind=\" before it ever reaches the apiserver", decoded["apiVersion"], decoded["kind"])
	}
	data, ok := decoded["data"].(map[string]any)
	if !ok {
		t.Fatalf("the body carries no data: %v", decoded)
	}
	for _, key := range []string{CertKey, KeyKey, CAKey} {
		if _, ok := data[key]; !ok {
			t.Fatalf("the body carries no %s: %v", key, data)
		}
	}
}
