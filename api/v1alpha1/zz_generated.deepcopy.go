package v1alpha1

import (
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func (in *Cluster) DeepCopyInto(out *Cluster) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
	in.Status.DeepCopyInto(&out.Status)
}

func (in *Cluster) DeepCopy() *Cluster {
	if in == nil {
		return nil
	}
	out := new(Cluster)
	in.DeepCopyInto(out)
	return out
}

func (in *Cluster) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *ClusterList) DeepCopyInto(out *ClusterList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]Cluster, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *ClusterList) DeepCopy() *ClusterList {
	if in == nil {
		return nil
	}
	out := new(ClusterList)
	in.DeepCopyInto(out)
	return out
}

func (in *ClusterList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *ClusterStatus) DeepCopyInto(out *ClusterStatus) {
	*out = *in
	if in.NodeExpiry != nil {
		v := *in.NodeExpiry
		out.NodeExpiry = &v
	}
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
}

func (in *Node) DeepCopyInto(out *Node) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
	in.Status.DeepCopyInto(&out.Status)
}

func (in *Node) DeepCopy() *Node {
	if in == nil {
		return nil
	}
	out := new(Node)
	in.DeepCopyInto(out)
	return out
}

func (in *Node) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *NodeList) DeepCopyInto(out *NodeList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]Node, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *NodeList) DeepCopy() *NodeList {
	if in == nil {
		return nil
	}
	out := new(NodeList)
	in.DeepCopyInto(out)
	return out
}

func (in *NodeList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *NodeStatus) DeepCopyInto(out *NodeStatus) {
	*out = *in
	if in.NodeExpiry != nil {
		v := *in.NodeExpiry
		out.NodeExpiry = &v
	}
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
}

func (in *NodeClaim) DeepCopyInto(out *NodeClaim) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *NodeClaim) DeepCopy() *NodeClaim {
	if in == nil {
		return nil
	}
	out := new(NodeClaim)
	in.DeepCopyInto(out)
	return out
}

func (in *NodeClaim) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *NodeClaimList) DeepCopyInto(out *NodeClaimList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]NodeClaim, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *NodeClaimList) DeepCopy() *NodeClaimList {
	if in == nil {
		return nil
	}
	out := new(NodeClaimList)
	in.DeepCopyInto(out)
	return out
}

func (in *NodeClaimList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *NodeClaimSpec) DeepCopyInto(out *NodeClaimSpec) {
	*out = *in
	if in.DenyBidderDids != nil {
		out.DenyBidderDids = make([]string, len(in.DenyBidderDids))
		copy(out.DenyBidderDids, in.DenyBidderDids)
	}
}

func (in *NodeClaimStatus) DeepCopyInto(out *NodeClaimStatus) {
	*out = *in
	in.Contract.DeepCopyInto(&out.Contract)
	out.Grant = copyPtr(in.Grant)
	out.Secrets = copyPtr(in.Secrets)
}

func (in *NodeClaimContract) DeepCopyInto(out *NodeClaimContract) {
	*out = *in
	out.Version = copyPtr(in.Version)
	out.Payload = copyPtr(in.Payload)
	out.RFP = copyPtr(in.RFP)
	out.Policy = copyPtr(in.Policy)
	out.Bid = copyPtr(in.Bid)
	out.Winner = copyPtr(in.Winner)
	out.AcceptPending = copyPtr(in.AcceptPending)
	out.Accept = copyPtr(in.Accept)
	out.Receipt = copyPtr(in.Receipt)
	out.ReceiptOk = copyPtr(in.ReceiptOk)
	out.Bids = copyPtr(in.Bids)
	if in.Release != nil {
		out.Release = new(ContractRelease)
		in.Release.DeepCopyInto(out.Release)
	}
	if in.Termination != nil {
		out.Termination = new(ContractTermination)
		in.Termination.DeepCopyInto(out.Termination)
	}
	if in.HostKeys != nil {
		out.HostKeys = new(ContractHostKeys)
		in.HostKeys.DeepCopyInto(out.HostKeys)
	}
	out.Timestamps = copyPtr(in.Timestamps)
}

func (in *ContractHostKeys) DeepCopyInto(out *ContractHostKeys) {
	*out = *in
	if in.Keys != nil {
		out.Keys = make([]ContractHostKey, len(in.Keys))
		copy(out.Keys, in.Keys)
	}
}

func (in *ContractTermination) DeepCopyInto(out *ContractTermination) {
	*out = *in
	out.ExitCode = copyPtr(in.ExitCode)
	out.Request = copyPtr(in.Request)
}

func (in *ContractRelease) DeepCopyInto(out *ContractRelease) {
	*out = *in
	out.Status = copyPtr(in.Status)
}

func copyPtr[T any](in *T) *T {
	if in == nil {
		return nil
	}
	out := new(T)
	*out = *in
	return out
}

func (in *AtprotoIdentity) DeepCopyInto(out *AtprotoIdentity) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
	in.Status.DeepCopyInto(&out.Status)
}

func (in *AtprotoIdentity) DeepCopy() *AtprotoIdentity {
	if in == nil {
		return nil
	}
	out := new(AtprotoIdentity)
	in.DeepCopyInto(out)
	return out
}

func (in *AtprotoIdentity) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *AtprotoIdentityList) DeepCopyInto(out *AtprotoIdentityList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]AtprotoIdentity, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *AtprotoIdentityList) DeepCopy() *AtprotoIdentityList {
	if in == nil {
		return nil
	}
	out := new(AtprotoIdentityList)
	in.DeepCopyInto(out)
	return out
}

func (in *AtprotoIdentityList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *AtprotoIdentityStatus) DeepCopyInto(out *AtprotoIdentityStatus) {
	*out = *in
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
}

func (in *ClusterLink) DeepCopyInto(out *ClusterLink) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
	out.Status = in.Status
}

func (in *ClusterLink) DeepCopy() *ClusterLink {
	if in == nil {
		return nil
	}
	out := new(ClusterLink)
	in.DeepCopyInto(out)
	return out
}

func (in *ClusterLink) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *ClusterLinkList) DeepCopyInto(out *ClusterLinkList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]ClusterLink, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *ClusterLinkList) DeepCopy() *ClusterLinkList {
	if in == nil {
		return nil
	}
	out := new(ClusterLinkList)
	in.DeepCopyInto(out)
	return out
}

func (in *ClusterLinkList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *MarketIdentity) DeepCopyInto(out *MarketIdentity) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *MarketIdentity) DeepCopy() *MarketIdentity {
	if in == nil {
		return nil
	}
	out := new(MarketIdentity)
	in.DeepCopyInto(out)
	return out
}

func (in *MarketIdentity) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *MarketIdentityList) DeepCopyInto(out *MarketIdentityList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]MarketIdentity, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *MarketIdentityList) DeepCopy() *MarketIdentityList {
	if in == nil {
		return nil
	}
	out := new(MarketIdentityList)
	in.DeepCopyInto(out)
	return out
}

func (in *MarketIdentityList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *MarketIdentitySpec) DeepCopyInto(out *MarketIdentitySpec) {
	*out = *in
	if in.ApprovedAssociations != nil {
		out.ApprovedAssociations = append([]string(nil), in.ApprovedAssociations...)
	}
	if in.RejectedAssociations != nil {
		out.RejectedAssociations = append([]string(nil), in.RejectedAssociations...)
	}
	if in.KeysRef != nil {
		ref := *in.KeysRef
		out.KeysRef = &ref
	}
	if in.SessionRef != nil {
		ref := *in.SessionRef
		out.SessionRef = &ref
	}
}

func (in *MarketIdentityStatus) DeepCopyInto(out *MarketIdentityStatus) {
	*out = *in
	if in.PendingAction != nil {
		action := *in.PendingAction
		out.PendingAction = &action
	}
	if in.Associations != nil {
		out.Associations = append([]string(nil), in.Associations...)
	}
	if in.SessionRef != nil {
		ref := *in.SessionRef
		out.SessionRef = &ref
	}
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
}

func (in *ComputeVM) DeepCopyInto(out *ComputeVM) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *ComputeVM) DeepCopy() *ComputeVM {
	if in == nil {
		return nil
	}
	out := new(ComputeVM)
	in.DeepCopyInto(out)
	return out
}

func (in *ComputeVM) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *ComputeVMList) DeepCopyInto(out *ComputeVMList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]ComputeVM, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *ComputeVMList) DeepCopy() *ComputeVMList {
	if in == nil {
		return nil
	}
	out := new(ComputeVMList)
	in.DeepCopyInto(out)
	return out
}

func (in *ComputeVMList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *ComputeVMSpec) DeepCopyInto(out *ComputeVMSpec) {
	*out = *in
	if in.Contract != nil {
		contract := *in.Contract
		out.Contract = &contract
	}
	if in.Image != nil {
		image := *in.Image
		out.Image = &image
	}
}

func (in *ComputeVMStatus) DeepCopyInto(out *ComputeVMStatus) {
	*out = *in
	if in.Network != nil {
		network := *in.Network
		out.Network = &network
	}
	if in.Image != nil {
		image := *in.Image
		out.Image = &image
	}
	if in.Runtime != nil {
		rt := *in.Runtime
		out.Runtime = &rt
	}
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
	if in.Market != nil {
		market := *in.Market
		if in.Market.HostKeys != nil {
			market.HostKeys = append([]ComputeVMHostKey(nil), in.Market.HostKeys...)
		}
		out.Market = &market
	}
}

func (in *HostProcess) DeepCopyInto(out *HostProcess) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *HostProcess) DeepCopy() *HostProcess {
	if in == nil {
		return nil
	}
	out := new(HostProcess)
	in.DeepCopyInto(out)
	return out
}

func (in *HostProcess) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *HostProcessList) DeepCopyInto(out *HostProcessList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]HostProcess, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *HostProcessList) DeepCopy() *HostProcessList {
	if in == nil {
		return nil
	}
	out := new(HostProcessList)
	in.DeepCopyInto(out)
	return out
}

func (in *HostProcessList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *HostProcessSpec) DeepCopyInto(out *HostProcessSpec) {
	*out = *in
	if in.Asset != nil {
		v := *in.Asset
		out.Asset = &v
	}
	out.Argv = append([]string(nil), in.Argv...)
	out.Env = append([]HostProcessEnv(nil), in.Env...)
	out.EnvFiles = append([]string(nil), in.EnvFiles...)
	if in.Output != nil {
		v := *in.Output
		out.Output = &v
	}
	if in.Limits != nil {
		v := *in.Limits
		out.Limits = &v
	}
	if in.Stop != nil {
		v := *in.Stop
		out.Stop = &v
	}
	out.After = append([]string(nil), in.After...)
	out.Wants = append([]string(nil), in.Wants...)
	if in.Readiness != nil {
		v := *in.Readiness
		v.Exec = append([]string(nil), in.Readiness.Exec...)
		out.Readiness = &v
	}
}

func (in *HostProcessStatus) DeepCopyInto(out *HostProcessStatus) {
	*out = *in
	if in.Runtime != nil {
		v := *in.Runtime
		out.Runtime = &v
	}
	if in.LastResult != nil {
		v := *in.LastResult
		out.LastResult = &v
	}
	out.Listeners = append([]HostProcessListener(nil), in.Listeners...)
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
}

func (in *Postgres) DeepCopyInto(out *Postgres) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *Postgres) DeepCopy() *Postgres {
	if in == nil {
		return nil
	}
	out := new(Postgres)
	in.DeepCopyInto(out)
	return out
}

func (in *Postgres) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *PostgresList) DeepCopyInto(out *PostgresList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]Postgres, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *PostgresList) DeepCopy() *PostgresList {
	if in == nil {
		return nil
	}
	out := new(PostgresList)
	in.DeepCopyInto(out)
	return out
}

func (in *PostgresList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *PostgresSpec) DeepCopyInto(out *PostgresSpec) {
	*out = *in
	out.Databases = append([]string(nil), in.Databases...)
	out.Settings = append([]string(nil), in.Settings...)
}

func (in *PostgresStatus) DeepCopyInto(out *PostgresStatus) {
	*out = *in
	if in.Runtime != nil {
		v := *in.Runtime
		out.Runtime = &v
	}
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
}

func (in *OpenBaoServer) DeepCopyInto(out *OpenBaoServer) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *OpenBaoServer) DeepCopy() *OpenBaoServer {
	if in == nil {
		return nil
	}
	out := new(OpenBaoServer)
	in.DeepCopyInto(out)
	return out
}

func (in *OpenBaoServer) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *OpenBaoServerList) DeepCopyInto(out *OpenBaoServerList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]OpenBaoServer, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *OpenBaoServerList) DeepCopy() *OpenBaoServerList {
	if in == nil {
		return nil
	}
	out := new(OpenBaoServerList)
	in.DeepCopyInto(out)
	return out
}

func (in *OpenBaoServerList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *OpenBaoServerSpec) DeepCopyInto(out *OpenBaoServerSpec) {
	b, err := json.Marshal(in)
	if err == nil {
		*out = OpenBaoServerSpec{}
		err = json.Unmarshal(b, out)
	}
	if err != nil {
		*out = *in
	}
}

func (in *OpenBaoServerStatus) DeepCopyInto(out *OpenBaoServerStatus) {
	*out = *in
	if in.Runtime != nil {
		v := *in.Runtime
		out.Runtime = &v
	}
	if in.Refs != nil {
		v := *in.Refs
		out.Refs = &v
	}
	if in.Clients != nil {
		out.Clients = append([]OpenBaoServerClientRefs(nil), in.Clients...)
	}
	if in.Sync != nil {
		v := *in.Sync
		v.Pending = append([]string(nil), in.Sync.Pending...)
		v.Conflicts = append([]string(nil), in.Sync.Conflicts...)
		out.Sync = &v
	}
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
}
