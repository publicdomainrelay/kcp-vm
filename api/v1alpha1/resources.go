package v1alpha1

//go:generate go run ../../cmd/socialweb-apigen -root ../..

type PrinterColumn struct {
	Name string

	Type string

	JSONPath string
}

type APIResource struct {
	Kind string

	Plural string

	Revision int

	Export string

	Object any

	PrinterColumns []PrinterColumn
}

var APIResources = []APIResource{
	{Kind: "Cluster", Plural: "clusters", Revision: 1, Export: "clusters", Object: Cluster{}},
	{Kind: "Node", Plural: "nodes", Revision: 1, Export: "clusters", Object: Node{}},
	{Kind: "NodeClaim", Plural: "nodeclaims", Revision: 5, Export: "clusters", Object: NodeClaim{}},
	{Kind: "ClusterLink", Plural: "clusterlinks", Revision: 1, Export: "clusters", Object: ClusterLink{}},
	{Kind: "AtprotoIdentity", Plural: "atprotoidentities", Revision: 1, Export: "atprotoidentities", Object: AtprotoIdentity{}},
	{Kind: "MarketIdentity", Plural: "marketidentities", Revision: 1, Export: "marketidentities", Object: MarketIdentity{}},
	{Kind: "ComputeVM", Plural: "computevms", Revision: 1, Export: "computevms", Object: ComputeVM{}, PrinterColumns: []PrinterColumn{
		{Name: "Phase", Type: "string", JSONPath: ".status.phase"},
		{Name: "GuestIP", Type: "string", JSONPath: ".status.network.guestIP"},
		{Name: "Requester", Type: "string", JSONPath: ".spec.requester.did"},
		{Name: "Age", Type: "date", JSONPath: ".metadata.creationTimestamp"},
	}},
}
