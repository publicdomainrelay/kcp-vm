package v1alpha1

import (
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
	out.Timestamps = copyPtr(in.Timestamps)
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
