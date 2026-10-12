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
	{Kind: "HostProcess", Plural: "hostprocesses", Revision: 3, Export: "hostprocesses", Object: HostProcess{}, PrinterColumns: []PrinterColumn{
		{Name: "Placement", Type: "string", JSONPath: ".spec.placement"},
		{Name: "Phase", Type: "string", JSONPath: ".status.phase"},
		{Name: "Backend", Type: "string", JSONPath: ".status.runtime.backend"},
		{Name: "Unit", Type: "string", JSONPath: ".spec.unit"},
		{Name: "PID", Type: "integer", JSONPath: ".status.runtime.pid"},
		{Name: "Age", Type: "date", JSONPath: ".metadata.creationTimestamp"},
	}},
	{Kind: "Postgres", Plural: "postgreses", Revision: 1, Export: "hostprocesses", Object: Postgres{}, PrinterColumns: []PrinterColumn{
		{Name: "Phase", Type: "string", JSONPath: ".status.phase"},
		{Name: "Version", Type: "string", JSONPath: ".status.version"},
		{Name: "Socket", Type: "string", JSONPath: ".status.socketDir"},
		{Name: "PID", Type: "integer", JSONPath: ".status.runtime.pid"},
		{Name: "Age", Type: "date", JSONPath: ".metadata.creationTimestamp"},
	}},
	{Kind: "OpenBaoServer", Plural: "openbaoservers", Revision: 3, Export: "hostprocesses", Object: OpenBaoServer{}, PrinterColumns: []PrinterColumn{
		{Name: "Phase", Type: "string", JSONPath: ".status.phase"},
		{Name: "Address", Type: "string", JSONPath: ".status.address"},
		{Name: "Initialized", Type: "boolean", JSONPath: ".status.initialized"},
		{Name: "Sealed", Type: "boolean", JSONPath: ".status.sealed"},
		{Name: "Instance", Type: "string", JSONPath: ".status.runtime.instance"},
		{Name: "Age", Type: "date", JSONPath: ".metadata.creationTimestamp"},
	}},
	{Kind: "MarketNetwork", Plural: "marketnetworks", Revision: 2, Export: "applications", Object: MarketNetwork{}, PrinterColumns: []PrinterColumn{
		{Name: "Profile", Type: "string", JSONPath: ".spec.profile"},
		{Name: "Phase", Type: "string", JSONPath: ".status.phase"},
		{Name: "PLC", Type: "string", JSONPath: ".status.network.plcDirectoryUrl"},
		{Name: "Age", Type: "date", JSONPath: ".metadata.creationTimestamp"},
	}},
	{Kind: "MarketBidder", Plural: "marketbidders", Revision: 2, Export: "applications", Object: MarketBidder{}, PrinterColumns: []PrinterColumn{
		{Name: "Profile", Type: "string", JSONPath: ".spec.profile"},
		{Name: "Phase", Type: "string", JSONPath: ".status.phase"},
		{Name: "DID", Type: "string", JSONPath: ".status.did"},
		{Name: "Age", Type: "date", JSONPath: ".metadata.creationTimestamp"},
	}},
	{Kind: "MarketRequester", Plural: "marketrequesters", Revision: 1, Export: "applications", Object: MarketRequester{}, PrinterColumns: []PrinterColumn{
		{Name: "Profile", Type: "string", JSONPath: ".spec.profile"},
		{Name: "Phase", Type: "string", JSONPath: ".status.phase"},
		{Name: "DID", Type: "string", JSONPath: ".status.did"},
		{Name: "Age", Type: "date", JSONPath: ".metadata.creationTimestamp"},
	}},
	{Kind: "Gateway", Plural: "gateways", Revision: 2, Export: "applications", Object: Gateway{}, PrinterColumns: []PrinterColumn{
		{Name: "Profile", Type: "string", JSONPath: ".spec.profile"},
		{Name: "Phase", Type: "string", JSONPath: ".status.phase"},
		{Name: "Age", Type: "date", JSONPath: ".metadata.creationTimestamp"},
	}},
}
