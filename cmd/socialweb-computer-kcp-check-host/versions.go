package main

import (
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/versions"
)

func pinned(component, detail string) string {
	recorded, err := versions.Get(component)
	if err != nil {
		return detail
	}
	want := strings.TrimSpace(recorded.Version)
	if want == "" || want == "None" {
		return detail
	}
	if strings.Contains(detail, want) {
		return detail + ", which is the version internal/versions records"
	}
	return detail + ". internal/versions records " + want + " as the version this deployment was " +
		"written against, and that is NOT what runs here: nothing downstream compares the two, so " +
		"the difference is one between what a proof measures and what the record describes, and it " +
		"is resolved by reading the code that names the version rather than by trusting either"
}

func pinnedInvariant(component string) (string, error) {
	recorded, err := versions.Get(component)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(recorded.Invariant), nil
}
