package procgroup

import (
	"context"
	"errors"
)

type Component string

const (
	ComponentPostgres Component = "embedded-postgres"

	ComponentOpenBao Component = "openbao"

	ComponentApiserverFront Component = "apiserver-front"

	ComponentKine Component = "kine"

	ComponentK3s Component = "k3s-server"

	ComponentCloudControllerManager Component = "socialweb-cloud-controller-manager"

	ComponentExternalSecretsWebhook Component = "external-secrets-webhook"

	ComponentExternalSecrets Component = "external-secrets"

	ComponentApiserverLink Component = "apiserver-link"

	ComponentOpenBaoLink Component = "openbao-link"
)

var DefaultOrder = []Component{
	ComponentPostgres, ComponentKine, ComponentK3s, ComponentCloudControllerManager,
}

var StackOrder = []Component{
	ComponentPostgres, ComponentOpenBao, ComponentApiserverFront, ComponentKine, ComponentK3s,
	ComponentCloudControllerManager, ComponentExternalSecretsWebhook, ComponentExternalSecrets,
}

type Spec struct {
	Argv []string

	Env []string

	EnvFiles []string

	Log string

	Listen string
}

type Member interface {
	Output() string
}

type Step struct {
	Name    Component
	Slot    string
	Spec    Spec
	Prepare func(ctx context.Context, spec Spec) (Spec, error)
	Ready   func(ctx context.Context, m Member) error
}

var ErrPostgresUnavailable = errors.New(
	"procgroup: kine's and openbao's store is this cluster's own Postgres object, and it is not Ready. " +
		"The vault's data and its unseal material live in that store, so a vault started without " +
		"it would come up on a store that is not its own: refusing to place kine or openbao")
