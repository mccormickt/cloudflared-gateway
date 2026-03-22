package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func (in *CloudflareAccessPolicy) DeepCopyObject() runtime.Object {
	return in.DeepCopy()
}

func (in *CloudflareAccessPolicy) DeepCopy() *CloudflareAccessPolicy {
	if in == nil {
		return nil
	}
	out := new(CloudflareAccessPolicy)
	in.DeepCopyInto(out)
	return out
}

func (in *CloudflareAccessPolicy) DeepCopyInto(out *CloudflareAccessPolicy) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *CloudflareAccessPolicySpec) DeepCopyInto(out *CloudflareAccessPolicySpec) {
	*out = *in
	if in.TargetRefs != nil {
		out.TargetRefs = make([]gwapiv1.LocalPolicyTargetReference, len(in.TargetRefs))
		copy(out.TargetRefs, in.TargetRefs)
	}
	if in.AudTag != nil {
		out.AudTag = make([]string, len(in.AudTag))
		copy(out.AudTag, in.AudTag)
	}
}

func (in *CloudflareAccessPolicyStatus) DeepCopyInto(out *CloudflareAccessPolicyStatus) {
	*out = *in
	if in.Conditions != nil {
		in, out := &in.Conditions, &out.Conditions
		*out = make([]metav1.Condition, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

func (in *CloudflareAccessPolicyList) DeepCopyObject() runtime.Object {
	return in.DeepCopy()
}

func (in *CloudflareAccessPolicyList) DeepCopy() *CloudflareAccessPolicyList {
	if in == nil {
		return nil
	}
	out := new(CloudflareAccessPolicyList)
	in.DeepCopyInto(out)
	return out
}

func (in *CloudflareAccessPolicyList) DeepCopyInto(out *CloudflareAccessPolicyList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]CloudflareAccessPolicy, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}
