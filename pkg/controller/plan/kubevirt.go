package plan

import (
	"context"
	libcnd "github.com/konveyor/controller/pkg/condition"
	liberr "github.com/konveyor/controller/pkg/error"
	api "github.com/konveyor/virt-controller/pkg/apis/virt/v1alpha1"
	"github.com/konveyor/virt-controller/pkg/apis/virt/v1alpha1/plan"
	"github.com/konveyor/virt-controller/pkg/apis/virt/v1alpha1/snapshot"
	"github.com/konveyor/virt-controller/pkg/controller/plan/builder"
	cdi "github.com/kubevirt/containerized-data-importer/pkg/apis/core/v1beta1"
	vmio "github.com/kubevirt/vm-import-operator/pkg/apis/v2v/v1beta1"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strconv"
	"strings"
)

const (
	// migration label (value=UID)
	kMigration = "migration"
	// plan label (value=UID)
	kPlan = "plan"
	// VM label (value=vmID)
	kVM = "vmID"
)

//
// Map of VmImport keyed by vmID.
type ImportMap map[string]VmImport

//
// Represents kubevirt.
type KubeVirt struct {
	// Plan.
	*api.Plan
	// Migration.
	*api.Migration
	// Builder
	Builder builder.Builder
	// Secret.
	Secret *core.Secret
	// k8s client.
	Client client.Client
}

//
// Build a ImportMap.
func (r *KubeVirt) ImportMap() (mp ImportMap, err error) {
	list, err := r.ListImports()
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	mp = ImportMap{}
	for _, object := range list {
		mp[object.Labels[kVM]] = object
	}

	return
}

//
// List import CRs.
// Each VmImport represents a VMIO VirtualMachineImport
// with associated DataVolumes.
func (r *KubeVirt) ListImports() ([]VmImport, error) {
	selector := labels.SelectorFromSet(
		map[string]string{
			kMigration: string(r.Plan.Status.Migration.Active),
			kPlan:      string(r.Plan.GetUID()),
		})
	vList := &vmio.VirtualMachineImportList{}
	err := r.Client.List(
		context.TODO(),
		&client.ListOptions{
			Namespace:     r.namespace(),
			LabelSelector: selector,
		},
		vList)
	if err != nil {
		return nil, liberr.Wrap(err)
	}
	list := []VmImport{}
	for i := range vList.Items {
		vmImport := &vList.Items[i]
		list = append(
			list,
			VmImport{
				VirtualMachineImport: vmImport,
			})
	}
	dvList := &cdi.DataVolumeList{}
	err = r.Client.List(
		context.TODO(),
		&client.ListOptions{
			Namespace: r.namespace(),
		},
		dvList)
	if err != nil {
		return nil, liberr.Wrap(err)
	}
	for i := range list {
		vmImport := &list[i]
		for i := range dvList.Items {
			dv := &dvList.Items[i]
			if vmImport.Owner(dv) {
				vmImport.DataVolumes = append(
					vmImport.DataVolumes,
					DataVolume{
						DataVolume: dv,
					})
			}
		}
	}

	return list, nil
}

//
// Create the VMIO CR on the destination.
func (r *KubeVirt) EnsureImport(vmID string) (err error) {
	newImport, err := r.buildImport(vmID)
	if err != nil {
		return
	}
	err = r.ensureObject(newImport)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}

	return
}

//
// Ensure the namespace exists on the destination.
func (r *KubeVirt) EnsureNamespace() (err error) {
	ns := &core.Namespace{
		ObjectMeta: meta.ObjectMeta{
			Name: r.namespace(),
		},
	}
	err = r.Client.Create(context.TODO(), ns)
	if err != nil {
		if errors.IsAlreadyExists(err) {
			err = nil
		}
	}

	return
}

//
// Ensure the VMIO mapping exists on the destination.
func (r *KubeVirt) EnsureMapping() (err error) {
	mapping, err := r.buildMapping()
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	err = r.ensureObject(mapping)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}

	return
}

//
// Ensure the VMIO secret exists on the destination.
func (r *KubeVirt) EnsureSecret(vmID string) (err error) {
	secret, err := r.buildSecret(vmID)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	err = r.ensureObject(secret)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}

	return
}

//
// Build the VMIO CR.
func (r *KubeVirt) buildImport(vmID string) (object *vmio.VirtualMachineImport, err error) {
	source, err := r.buildSource(vmID)
	if err != nil {
		return
	}
	namespace := r.namespace()
	object = &vmio.VirtualMachineImport{
		ObjectMeta: meta.ObjectMeta{
			Namespace: r.namespace(),
			Name:      r.nameForImport(vmID),
			Labels: map[string]string{
				kMigration: string(r.Plan.Status.Migration.Active),
				kPlan:      string(r.Plan.UID),
				kVM:        vmID,
			},
		},
		Spec: vmio.VirtualMachineImportSpec{
			Source: *source,
			ProviderCredentialsSecret: vmio.ObjectIdentifier{
				Namespace: &namespace,
				Name:      r.nameForSecret(vmID),
			},
			ResourceMapping: &vmio.ObjectIdentifier{
				Namespace: &namespace,
				Name:      r.nameForMapping(),
			},
		},
	}

	return
}

//
// Build the ResourceMapping CR.
func (r *KubeVirt) buildMapping() (object *vmio.ResourceMapping, err error) {
	object = &vmio.ResourceMapping{
		ObjectMeta: meta.ObjectMeta{
			Namespace: r.namespace(),
			Name:      r.nameForMapping(),
			Labels: map[string]string{
				kMigration: string(r.Plan.Status.Migration.Active),
				kPlan:      string(r.Plan.UID),
			},
		},
	}
	sn := snapshot.New(r.Migration)
	mp := &plan.Map{}
	err = sn.Get(api.MapSnapshot, mp)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	err = r.Builder.Mapping(mp, object)
	if err != nil {
		err = liberr.Wrap(err)
	}

	return
}

//
// Build the VMIO secret.
func (r *KubeVirt) buildSecret(vmID string) (object *core.Secret, err error) {
	object = &core.Secret{
		ObjectMeta: meta.ObjectMeta{
			Namespace: r.namespace(),
			Name:      r.nameForSecret(vmID),
			Labels: map[string]string{
				kMigration: string(r.Plan.Status.Migration.Active),
				kPlan:      string(r.Plan.UID),
			},
		},
	}
	err = r.Builder.Secret(vmID, r.Secret, object)
	if err != nil {
		err = liberr.Wrap(err)
	}

	return
}

//
// Build the VMIO Source.
func (r *KubeVirt) buildSource(vmID string) (object *vmio.VirtualMachineImportSourceSpec, err error) {
	object = &vmio.VirtualMachineImportSourceSpec{}
	err = r.Builder.Source(vmID, object)
	if err != nil {
		err = liberr.Wrap(err)
	}

	return
}

//
// Generated name for kubevirt VM Import mapping CR.
func (r *KubeVirt) nameForMapping() string {
	uid := string(r.Plan.UID)
	parts := []string{
		"plan",
		r.Plan.Name,
		uid[len(uid)-4:],
	}

	return strings.Join(parts, "-")
}

//
// Generated name for kubevirt VM Import CR secret.
func (r *KubeVirt) nameForSecret(vmID string) string {
	uid := string(r.Plan.UID)
	parts := []string{
		"plan",
		r.Plan.Name,
		vmID,
		uid[len(uid)-4:],
	}

	return strings.Join(parts, "-")
}

//
// Generated name for kubevirt VM Import CR.
func (r *KubeVirt) nameForImport(vmID string) string {
	uid := string(r.Plan.Status.Migration.Active)
	parts := []string{
		"plan",
		r.Plan.Name,
		vmID,
		uid[len(uid)-4:],
	}

	return strings.Join(parts, "-")
}

//
// Represents a CDI DataVolume and add behavior.
type DataVolume struct {
	*cdi.DataVolume
}

//
// Get conditions.
func (r *DataVolume) Conditions() (cnd *libcnd.Conditions) {
	cnd = &libcnd.Conditions{}
	for _, c := range r.Status.Conditions {
		cnd.SetCondition(libcnd.Condition{
			Type:               string(c.Type),
			Status:             string(c.Status),
			Reason:             c.Reason,
			Message:            c.Message,
			LastTransitionTime: c.LastTransitionTime,
		})
	}

	return
}

//
// Convert the Status.Progress into a
// percentage (float).
func (r *DataVolume) PercentComplete() (pct float64) {
	s := string(r.Status.Progress)
	if strings.HasSuffix(s, "%") {
		s = s[:len(s)-1]
		n, err := strconv.ParseFloat(s, 64)
		if err == nil {
			pct = n / 100
		}
	}

	return
}

//
// Represents VMIO VirtualMachineImport with associated DataVolumes.
type VmImport struct {
	*vmio.VirtualMachineImport
	DataVolumes []DataVolume
}

//
// Determine if `this` VMIO VirtualMachineImport is the
// owner of the CDI DataVolume.
func (r *VmImport) Owner(dv *cdi.DataVolume) bool {
	for _, ref := range r.Status.DataVolumes {
		if dv.Name == ref.Name {
			return true
		}
	}

	return false
}

//
// Get conditions.
func (r *VmImport) Conditions() (cnd *libcnd.Conditions) {
	cnd = &libcnd.Conditions{}
	for _, c := range r.Status.Conditions {
		newCnd := libcnd.Condition{
			Type:   string(c.Type),
			Status: string(c.Status),
		}
		if c.Reason != nil {
			newCnd.Reason = *c.Reason
		}
		if c.Message != nil {
			newCnd.Message = *c.Message
		}
		if c.LastTransitionTime != nil {
			newCnd.LastTransitionTime = *c.LastTransitionTime
		}
		cnd.SetCondition(newCnd)
	}

	return
}

//
// Convert the progress annotation into an int64.
func (r *VmImport) PercentComplete() (pct float64) {
	name := "vmimport.v2v.kubevirt.io/progress"
	if meta.HasAnnotation(r.ObjectMeta, name) {
		n, err := strconv.ParseFloat(r.Annotations[name], 64)
		if err != err {
			return
		}
		pct = n / 100
	}

	return
}

//
// Get the target namespace.
// Default to `plan` namespace when not specified
// in the plan spec.
func (r *KubeVirt) namespace() (ns string) {
	ns = r.Plan.Spec.TargetNamespace
	if ns == "" {
		ns = r.Plan.Namespace
	}

	return
}

//
// Ensure resource.
// Resource is created/updated as needed.
func (r *KubeVirt) ensureObject(object runtime.Object) (err error) {
	retry := 3
	defer func() {
		err = liberr.Wrap(err)
	}()
	for {
		err = r.Client.Create(context.TODO(), object)
		if errors.IsAlreadyExists(err) && retry > 0 {
			retry--
			err = r.deleteObject(object)
			if err != nil {
				break
			}
		} else {
			break
		}
	}

	return
}

//
// Delete a resource.
func (r *KubeVirt) deleteObject(object runtime.Object) (err error) {
	err = r.Client.Delete(context.TODO(), object)
	if !errors.IsNotFound(err) {
		err = liberr.Wrap(err)
	} else {
		err = nil
	}

	return
}