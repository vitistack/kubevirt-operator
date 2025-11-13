/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package crdvalidation

import (
	"context"
	"fmt"

	"github.com/vitistack/common/pkg/loggers/vlog"
	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	"github.com/vitistack/kubevirt-operator/pkg/clients"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CheckCRDCompatibility checks if CRD schemas match across clusters
func CheckCRDCompatibility(ctx context.Context, clientMgr clients.ClientManager, configs []vitistackv1alpha1.KubevirtConfig) {
	vlog.Info("Checking CRD schema compatibility across clusters...")

	supervisorClient := clientMgr.GetSupervisorClient()

	// vitistack.io CRDs should match between supervisor and remote clusters
	vitistackCRDs := []string{
		"machines.vitistack.io",
		"networkconfigurations.vitistack.io",
		"kubevirtconfigs.vitistack.io",
	}

	// Get supervisor vitistack.io CRD schemas as baseline
	supervisorSchemas, err := getCRDSchemas(ctx, supervisorClient, vitistackCRDs)
	if err != nil {
		vlog.Warn(fmt.Sprintf("⚠️  Failed to fetch vitistack.io CRD schemas from supervisor cluster: %s", err.Error()))
		return
	}

	vlog.Info(fmt.Sprintf("✅ Supervisor cluster has %d vitistack.io CRDs", len(supervisorSchemas)))

	// Compare each remote cluster's vitistack.io CRDs against supervisor
	for i := range configs {
		config := &configs[i]
		compareRemoteVitistackCRDs(ctx, clientMgr, config, vitistackCRDs, supervisorSchemas)
	}

	// Check kubevirt.io CRD versions across remote clusters
	checkKubevirtCRDVersions(ctx, clientMgr, configs)

	vlog.Info("✅ CRD compatibility check completed")
}

// compareRemoteVitistackCRDs compares vitistack.io CRDs between supervisor and a remote cluster
func compareRemoteVitistackCRDs(ctx context.Context, clientMgr clients.ClientManager, config *vitistackv1alpha1.KubevirtConfig, crdNames []string, supervisorSchemas map[string]*unstructured.Unstructured) {
	vlog.Info(fmt.Sprintf("Comparing vitistack.io CRDs on cluster: %s", config.Name))

	// Get client for this cluster
	remoteClient, err := clientMgr.GetClientForConfig(ctx, config.Name)
	if err != nil {
		vlog.Warn(fmt.Sprintf("⚠️  Cluster '%s': Failed to get client for CRD comparison: %s", config.Name, err.Error()))
		return
	}

	// Get remote vitistack.io CRD schemas
	remoteSchemas, err := getCRDSchemas(ctx, remoteClient, crdNames)
	if err != nil {
		vlog.Warn(fmt.Sprintf("⚠️  Cluster '%s': Failed to fetch vitistack.io CRD schemas: %s", config.Name, err.Error()))
		return
	}

	// Compare schemas
	for _, crdName := range crdNames {
		supervisorCRD, hasSupervisor := supervisorSchemas[crdName]
		remoteCRD, hasRemote := remoteSchemas[crdName]

		if !hasSupervisor && !hasRemote {
			vlog.Warn(fmt.Sprintf("⚠️  Cluster '%s': CRD '%s' not found in either cluster", config.Name, crdName))
			continue
		}

		if !hasRemote {
			vlog.Warn(fmt.Sprintf("⚠️  Cluster '%s': CRD '%s' exists on supervisor but not on remote cluster", config.Name, crdName))
			continue
		}

		if !hasSupervisor {
			vlog.Warn(fmt.Sprintf("⚠️  Cluster '%s': CRD '%s' exists on remote but not on supervisor cluster", config.Name, crdName))
			continue
		}

		// Compare versions
		supervisorVersion := extractCRDVersion(supervisorCRD)
		remoteVersion := extractCRDVersion(remoteCRD)

		if supervisorVersion != remoteVersion {
			vlog.Warn(fmt.Sprintf("⚠️  Cluster '%s': CRD '%s' version mismatch - Supervisor: %s, Remote: %s",
				config.Name, crdName, supervisorVersion, remoteVersion))
		} else {
			vlog.Info(fmt.Sprintf("✅ Cluster '%s': CRD '%s' version matches supervisor: %s",
				config.Name, crdName, supervisorVersion))
		}

		// Compare schema properties (basic check)
		if !compareCRDSchemas(supervisorCRD, remoteCRD) {
			vlog.Warn(fmt.Sprintf("⚠️  Cluster '%s': CRD '%s' schema differs from supervisor cluster", config.Name, crdName))
		}
	}
}

// checkKubevirtCRDVersions checks kubevirt.io CRD version consistency across remote clusters
func checkKubevirtCRDVersions(ctx context.Context, clientMgr clients.ClientManager, configs []vitistackv1alpha1.KubevirtConfig) {
	kubevirtCRDsToCheck := []schema.GroupVersionResource{
		{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"},
		{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachineinstances"},
	}

	var baselineKubevirtVersions map[string]string
	var baselineClusterName string

	// Check each remote cluster for KubeVirt CRDs
	for i := range configs {
		config := &configs[i]

		remoteClient, err := clientMgr.GetClientForConfig(ctx, config.Name)
		if err != nil {
			vlog.Warn(fmt.Sprintf("⚠️  Cluster '%s': Failed to get client for KubeVirt CRD check: %s", config.Name, err.Error()))
			continue
		}

		remoteVersions, err := getCRDVersions(ctx, remoteClient, kubevirtCRDsToCheck)
		if err != nil {
			vlog.Warn(fmt.Sprintf("⚠️  Cluster '%s': Failed to fetch KubeVirt CRD versions: %s", config.Name, err.Error()))
			continue
		}

		// If this is the first cluster, use it as baseline
		if baselineKubevirtVersions == nil {
			baselineKubevirtVersions = remoteVersions
			baselineClusterName = config.Name
			vlog.Info(fmt.Sprintf("✅ Cluster '%s': Using as baseline for KubeVirt CRD versions", config.Name))
			continue
		}

		// Compare with baseline
		compareKubevirtCRDVersions(config.Name, baselineClusterName, baselineKubevirtVersions, remoteVersions)
	}
}

// extractCRDVersion extracts version from a CRD object
func extractCRDVersion(crd *unstructured.Unstructured) string {
	// Try stored versions first
	storedVersions, found, err := unstructured.NestedSlice(crd.Object, "status", "storedVersions")
	if err == nil && found && len(storedVersions) > 0 {
		if version, ok := storedVersions[0].(string); ok {
			return version
		}
	}

	// Fallback to spec.versions
	versions, found, err := unstructured.NestedSlice(crd.Object, "spec", "versions")
	if err == nil && found && len(versions) > 0 {
		if versionMap, ok := versions[0].(map[string]interface{}); ok {
			if version, ok := versionMap["name"].(string); ok {
				return version
			}
		}
	}

	return "unknown"
}

// compareCRDSchemas compares two CRD schemas for compatibility
func compareCRDSchemas(crd1, crd2 *unstructured.Unstructured) bool {
	// Get spec.versions from both CRDs
	versions1, found1, err1 := unstructured.NestedSlice(crd1.Object, "spec", "versions")
	versions2, found2, err2 := unstructured.NestedSlice(crd2.Object, "spec", "versions")

	if err1 != nil || !found1 || err2 != nil || !found2 {
		return true // Can't compare, assume compatible
	}

	if len(versions1) != len(versions2) {
		return false // Different number of versions
	}

	// For now, just check if the number of versions match
	// A more thorough comparison would check actual schema properties
	return true
}

// compareKubevirtCRDVersions compares KubeVirt CRD versions between remote clusters
func compareKubevirtCRDVersions(clusterName, baselineClusterName string, baselineVersions, remoteVersions map[string]string) {
	hasWarnings := false

	for crdName, baselineVersion := range baselineVersions {
		remoteVersion, exists := remoteVersions[crdName]

		if !exists {
			vlog.Warn(fmt.Sprintf("⚠️  Cluster '%s': KubeVirt CRD '%s' not found (exists in '%s' with version %s)",
				clusterName, crdName, baselineClusterName, baselineVersion))
			hasWarnings = true
			continue
		}

		if baselineVersion != remoteVersion {
			vlog.Warn(fmt.Sprintf("⚠️  Cluster '%s': KubeVirt CRD '%s' version mismatch - %s: %s, %s: %s",
				clusterName, crdName, baselineClusterName, baselineVersion, clusterName, remoteVersion))
			hasWarnings = true
		}
	}

	if !hasWarnings {
		vlog.Info(fmt.Sprintf("✅ Cluster '%s': All KubeVirt CRD versions match baseline cluster '%s'", clusterName, baselineClusterName))
	}
}

// getCRDSchemas fetches full CRD objects for schema inspection
func getCRDSchemas(ctx context.Context, k8sClient client.Client, crdNames []string) (map[string]*unstructured.Unstructured, error) {
	crdSchemas := make(map[string]*unstructured.Unstructured)

	crdList, err := fetchCRDList(ctx, k8sClient)
	if err != nil {
		return nil, err
	}

	// Create a map for quick lookup
	crdNameMap := make(map[string]bool)
	for _, name := range crdNames {
		crdNameMap[name] = true
	}

	// Extract CRDs we care about
	for i := range crdList.Items {
		crd := &crdList.Items[i]
		crdName, found, err := unstructured.NestedString(crd.Object, "metadata", "name")
		if err != nil || !found {
			continue
		}

		if crdNameMap[crdName] {
			crdSchemas[crdName] = crd
		}
	}

	return crdSchemas, nil
}

// fetchCRDList retrieves the list of CRDs from the cluster
func fetchCRDList(ctx context.Context, k8sClient client.Client) (*unstructured.UnstructuredList, error) {
	crdGVR := schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1",
		Resource: "customresourcedefinitions",
	}

	crdList := &unstructured.UnstructuredList{}
	crdList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   crdGVR.Group,
		Version: crdGVR.Version,
		Kind:    "CustomResourceDefinitionList",
	})

	err := k8sClient.List(ctx, crdList)
	if err != nil {
		return nil, fmt.Errorf("failed to list CRDs: %w", err)
	}

	return crdList, nil
}

// getCRDVersions fetches CRD versions for specified resources
func getCRDVersions(ctx context.Context, k8sClient client.Client, crds []schema.GroupVersionResource) (map[string]string, error) {
	versions := make(map[string]string)

	crdList, err := fetchCRDList(ctx, k8sClient)
	if err != nil {
		return nil, err
	}

	// Build a map of CRD names to check
	crdNamesToCheck := buildCRDNameMap(crds)

	// Extract versions from CRDs
	for _, crd := range crdList.Items {
		extractCRDVersionFromList(&crd, crdNamesToCheck, versions)
	}

	return versions, nil
}

// buildCRDNameMap builds a map of CRD names from the GVR list
func buildCRDNameMap(crds []schema.GroupVersionResource) map[string]bool {
	crdNamesToCheck := make(map[string]bool)
	for _, crd := range crds {
		crdName := fmt.Sprintf("%s.%s", crd.Resource, crd.Group)
		crdNamesToCheck[crdName] = true
	}
	return crdNamesToCheck
}

// extractCRDVersionFromList extracts version information from a CRD object in a list
func extractCRDVersionFromList(crd *unstructured.Unstructured, crdNamesToCheck map[string]bool, versions map[string]string) {
	crdName, found, err := unstructured.NestedString(crd.Object, "metadata", "name")
	if err != nil || !found {
		return
	}

	// Only check CRDs we care about
	if !crdNamesToCheck[crdName] {
		return
	}

	// Get the stored versions
	storedVersions, found, err := unstructured.NestedSlice(crd.Object, "status", "storedVersions")
	if err != nil || !found || len(storedVersions) == 0 {
		// Try spec.versions instead
		extractVersionFromSpec(crd, crdName, versions)
		return
	}

	// Use the first stored version
	if version, ok := storedVersions[0].(string); ok {
		versions[crdName] = version
	}
}

// extractVersionFromSpec extracts version from spec.versions field
func extractVersionFromSpec(crd *unstructured.Unstructured, crdName string, versions map[string]string) {
	specVersions, foundSpec, errSpec := unstructured.NestedSlice(crd.Object, "spec", "versions")
	if errSpec != nil || !foundSpec || len(specVersions) == 0 {
		return
	}

	// Get the first (or latest) version
	if versionMap, ok := specVersions[0].(map[string]interface{}); ok {
		if version, foundVer := versionMap["name"].(string); foundVer {
			versions[crdName] = version
		}
	}
}
