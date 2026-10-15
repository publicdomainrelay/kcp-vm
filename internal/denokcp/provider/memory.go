package provider

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
)

const MinPodMemory = 64 << 20

func parseMemory(memory string) (uint64, error) {
	if memory == "" {
		return 0, nil
	}
	q, err := resource.ParseQuantity(memory)
	if err != nil {
		return 0, err
	}
	n := q.Value()
	if n < MinPodMemory {
		return 0, fmt.Errorf("%q is below the %d byte minimum of a deno process", memory, MinPodMemory)
	}
	return uint64(n), nil
}
