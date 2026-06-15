package main

import (
	"context"
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
)

func TestAllocateMissingVNIsPreservesNADConfig(t *testing.T) {
	rawConfig := `{
  "cniVersion": "0.4.0",
  "name": "auto-net",
  "type": "cw-multinet",
  "mtu": 1450,
  "vxlanPort": 14789,
  "capabilities": {"ips": true},
  "ipam": {"type": "static", "addresses": []}
}`
	overlay := nadOverlay{
		Namespace: "default",
		Name:      "auto-net",
		Config: nadConfig{
			Type:      "cw-multinet",
			MTU:       1450,
			VXLANPort: 14789,
		},
		RawConfig: rawConfig,
	}
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), nadObject("default", "auto-net", rawConfig))

	changed, err := allocateMissingVNIs(context.Background(), client, []nadOverlay{overlay}, []localOverlay{{VNI: 10000, VXLANName: "vx-cwm-10000"}}, agentConfig{
		VNIStart: 10000,
		VNIEnd:   10002,
	})
	if err != nil {
		t.Fatalf("allocateMissingVNIs returned error: %v", err)
	}
	if !changed {
		t.Fatal("allocateMissingVNIs returned changed=false")
	}

	updated, err := client.Resource(nadGVR).Namespace("default").Get(context.Background(), "auto-net", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get updated NAD: %v", err)
	}
	updatedConfig, ok, err := unstructured.NestedString(updated.Object, "spec", "config")
	if err != nil || !ok {
		t.Fatalf("read updated config ok=%t err=%v", ok, err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(updatedConfig), &parsed); err != nil {
		t.Fatalf("parse updated config: %v", err)
	}
	if got := int(parsed["vni"].(float64)); got != 10001 {
		t.Fatalf("allocated vni=%d, want 10001", got)
	}
	if got := parsed["name"]; got != "auto-net" {
		t.Fatalf("name=%v, want auto-net", got)
	}
	ipam, ok := parsed["ipam"].(map[string]any)
	if !ok {
		t.Fatalf("ipam was not preserved: %#v", parsed["ipam"])
	}
	if got := ipam["type"]; got != "static" {
		t.Fatalf("ipam.type=%v, want static", got)
	}
}

func TestValidateAssignedVNIsRejectsConflicts(t *testing.T) {
	err := validateAssignedVNIs([]nadOverlay{
		{Namespace: "ns-a", Name: "net-a", Config: nadConfig{VNI: 12000}},
		{Namespace: "ns-b", Name: "net-b", Config: nadConfig{VNI: 12000}},
	})
	if err == nil {
		t.Fatal("validateAssignedVNIs returned nil for duplicate VNI")
	}
}

func nadObject(namespace, name, rawConfig string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "k8s.cni.cncf.io/v1",
			"kind":       "NetworkAttachmentDefinition",
			"metadata": map[string]any{
				"namespace": namespace,
				"name":      name,
			},
			"spec": map[string]any{
				"config": rawConfig,
			},
		},
	}
}
