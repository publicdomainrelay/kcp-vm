package nodefirecracker

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

func LoadGuestDocument(path string) (GuestDocument, error) {
	var doc GuestDocument
	raw, err := os.ReadFile(path)
	if err != nil {
		return doc, fmt.Errorf("nodefirecracker: reading %s, which the image carries so the guest "+
			"knows which modules to load and where its k3s lives: %w", path, err)
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return doc, fmt.Errorf("nodefirecracker: %s: %w", path, err)
	}
	return doc, nil
}
