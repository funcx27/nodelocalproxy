package bootstrap

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestLoadOrCreateReadsExistingConfigMap(t *testing.T) {
	clientset := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultConfigMapName, Namespace: DefaultConfigMapNamespace},
		Data: map[string]string{
			DefaultConfigMapKey: "mode: ebpf-transparent\nintercept:\n  address: apiserver.example.com:6443\nbackends: [\"10.0.0.1:6443\"]\n",
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
	clientset := fake.NewSimpleClientset(kubernetesEndpoints())
	opts := Options{InterceptAddress: "apiserver.example.com:6443"}

	cfg, err := loadOrCreate(context.Background(), clientset, opts.withDefaults())
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
	if !strings.Contains(cm.Data[DefaultConfigMapKey], "apiserver.example.com:6443") {
		t.Fatalf("created config missing intercept:\n%s", cm.Data[DefaultConfigMapKey])
	}
}

func TestLoadOrCreateRequiresInterceptWhenConfigMapMissing(t *testing.T) {
	clientset := fake.NewSimpleClientset(kubernetesEndpoints())
	_, err := loadOrCreate(context.Background(), clientset, Options{}.withDefaults())
	if err == nil {
		t.Fatal("expected intercept error")
	}
	if !strings.Contains(err.Error(), "--bootstrap-intercept-address is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func kubernetesEndpoints() *corev1.Endpoints {
	return &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{Name: kubernetesEndpoint, Namespace: kubernetesNamespace},
		Subsets: []corev1.EndpointSubset{
			{
				Addresses: []corev1.EndpointAddress{
					{IP: "10.0.0.2"},
					{IP: "10.0.0.1"},
					{IP: "2001:db8::1"},
				},
				NotReadyAddresses: []corev1.EndpointAddress{{IP: "10.0.0.3"}},
				Ports:             []corev1.EndpointPort{{Name: "https", Port: 6443}},
			},
		},
	}
}
