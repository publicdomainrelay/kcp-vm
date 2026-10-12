package statuspatch

import (
	"encoding/json"
	"fmt"
)

func Merge(status map[string]any) ([]byte, error) {
	body, err := json.Marshal(map[string]any{"status": status})
	if err != nil {
		return nil, fmt.Errorf("statuspatch: encode status: %w", err)
	}
	return body, nil
}

func WithResourceVersion(body []byte, version string) ([]byte, error) {
	if version == "" {
		return body, nil
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("statuspatch: decode patch to stamp a resource version: %w", err)
	}
	obj["metadata"] = map[string]any{"resourceVersion": version}
	stamped, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("statuspatch: encode patch with a resource version: %w", err)
	}
	return stamped, nil
}

func FinalizerRemove(current []string, dropped string) ([]byte, error) {
	remaining := make([]string, 0, len(current))
	for _, finalizer := range current {
		if finalizer != dropped {
			remaining = append(remaining, finalizer)
		}
	}
	if len(remaining) == len(current) {
		return nil, nil
	}
	return json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/finalizers", "value": current},
		{"op": "add", "path": "/metadata/finalizers", "value": remaining},
	})
}

func FinalizerAdd(finalizers []string) ([]byte, error) {
	body, err := json.Marshal([]map[string]any{
		{"op": "add", "path": "/metadata/finalizers", "value": finalizers},
	})
	if err != nil {
		return nil, fmt.Errorf("statuspatch: encode the finalizer patch: %w", err)
	}
	return body, nil
}

type Metadata struct {
	Finalizers []string `json:"finalizers"`
}

func FinalizersOf(body []byte) ([]string, error) {
	var obj struct {
		Metadata Metadata `json:"metadata"`
	}
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("statuspatch: decode object metadata: %w", err)
	}
	return obj.Metadata.Finalizers, nil
}
