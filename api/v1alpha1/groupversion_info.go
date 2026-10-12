package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const GroupName = "socialweb.computer"

const Version = "v1alpha1"

var GroupVersion = schema.GroupVersion{Group: GroupName, Version: Version}

var SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

var AddToScheme = SchemeBuilder.AddToScheme

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(
		GroupVersion,
		&Cluster{},
		&ClusterList{},
		&Node{},
		&NodeList{},
		&NodeClaim{},
		&NodeClaimList{},
		&AtprotoIdentity{},
		&AtprotoIdentityList{},
		&ClusterLink{},
		&ClusterLinkList{},
		&MarketIdentity{},
		&MarketIdentityList{},
		&ComputeVM{},
		&ComputeVMList{},
		&HostProcess{},
		&HostProcessList{},
		&Postgres{},
		&PostgresList{},
		&OpenBaoServer{},
		&OpenBaoServerList{},
	)
	return nil
}
