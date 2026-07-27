package bootstrap

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestLoadOrCreateReadsExistingConfigMap(t *testing.T) {
	clientset := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultConfigMapName, Namespace: DefaultConfigMapNamespace},
		Data: map[string]string{
			DefaultConfigMapKey: "mode: ebpf-transparent\nbackends: [\"10.0.0.1:6443\"]\n",
		},
	})

	cfg, err := loadOrCreate(context.Background(), clientset, Options{}.withDefaults())
	if err != nil {
		t.Fatalf("loadOrCreate: %v", err)
	}
	if got, want := cfg.Backends[0], "10.0.0.1:6443"; got != want {
		t.Fatalf("backend: got %q want %q", got, want)
	}
}

func TestLoadOrCreateCreatesMissingConfigMapFromEndpoints(t *testing.T) {
	clientset := fake.NewSimpleClientset(kubernetesEndpointSlice())

	cfg, err := loadOrCreate(context.Background(), clientset, Options{}.withDefaults())
	if err != nil {
		t.Fatalf("loadOrCreate: %v", err)
	}
	if got, want := cfg.Backends, []string{"10.0.0.1:6443", "10.0.0.2:6443"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("backends: got %v want %v", got, want)
	}

	cm, err := clientset.CoreV1().ConfigMaps(DefaultConfigMapNamespace).Get(context.Background(), DefaultConfigMapName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get created configmap: %v", err)
	}
	if strings.Contains(cm.Data[DefaultConfigMapKey], "intercept:") {
		t.Fatalf("created config should not contain intercept:\n%s", cm.Data[DefaultConfigMapKey])
	}
}

func kubernetesEndpointSlice() *discoveryv1.EndpointSlice {
	ready := true
	notReady := false
	name := "https"
	port := int32(6443)
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kubernetesEndpoint,
			Namespace: kubernetesNamespace,
			Labels:    map[string]string{discoveryv1.LabelServiceName: kubernetesService},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Ports:       []discoveryv1.EndpointPort{{Name: &name, Port: &port}},
		Endpoints: []discoveryv1.Endpoint{
			{
				Addresses:  []string{"10.0.0.2", "10.0.0.1", "2001:db8::1"},
				Conditions: discoveryv1.EndpointConditions{Ready: &ready},
			},
			{
				Addresses:  []string{"10.0.0.3"},
				Conditions: discoveryv1.EndpointConditions{Ready: &notReady},
			},
		},
	}
}
