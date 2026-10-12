package v1alpha1

//go:generate go run ../../cmd/socialweb-apigen -root ../..

type APIResource struct {
	Kind string

	Plural string

	Revision int

	Export string

	Object any
}

var APIResources = []APIResource{
	{Kind: "Cluster", Plural: "clusters", Revision: 1, Export: "clusters", Object: Cluster{}},
	{Kind: "Node", Plural: "nodes", Revision: 1, Export: "clusters", Object: Node{}},
	{Kind: "NodeClaim", Plural: "nodeclaims", Revision: 4, Export: "clusters", Object: NodeClaim{}},
	{Kind: "ClusterLink", Plural: "clusterlinks", Revision: 1, Export: "clusters", Object: ClusterLink{}},
	{Kind: "AtprotoIdentity", Plural: "atprotoidentities", Revision: 1, Export: "atprotoidentities", Object: AtprotoIdentity{}},
}
