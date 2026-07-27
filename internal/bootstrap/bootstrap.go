package bootstrap

import (
	"context"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/funcx27/nodelocalproxy/internal/config"
)

const (
	DefaultConfigMapNamespace = "kube-system"
	DefaultConfigMapName      = "nodelocalproxy"
	DefaultConfigMapKey       = "config.yaml"

	kubernetesNamespace = "default"
	kubernetesEndpoint  = "kubernetes"
	kubernetesService   = "kubernetes"
	serviceAccountNS    = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
)

type Options struct {
	ConfigMapNamespace string
	ConfigMapName      string
	ConfigMapKey       string
	InterceptAddress   string
}

func LoadOrCreate(ctx context.Context, opts Options) (*config.Config, error) {
	opts = opts.withDefaults()
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("bootstrap requires Kubernetes in-cluster config: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	return loadOrCreate(ctx, clientset, opts)
}

func loadOrCreate(ctx context.Context, clientset kubernetes.Interface, opts Options) (*config.Config, error) {
	cm, err := clientset.CoreV1().ConfigMaps(opts.ConfigMapNamespace).Get(ctx, opts.ConfigMapName, metav1.GetOptions{})
	if err == nil {
		data, ok := cm.Data[opts.ConfigMapKey]
		if !ok {
			return nil, fmt.Errorf("bootstrap configmap %s/%s missing key %q", opts.ConfigMapNamespace, opts.ConfigMapName, opts.ConfigMapKey)
		}
		return config.LoadConfigData([]byte(data))
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("get bootstrap configmap %s/%s: %w", opts.ConfigMapNamespace, opts.ConfigMapName, err)
	}
	if opts.InterceptAddress == "" {
		return nil, fmt.Errorf("--bootstrap-intercept-address is required when configmap %s/%s does not exist", opts.ConfigMapNamespace, opts.ConfigMapName)
	}

	cfgYAML, err := generateConfigYAML(ctx, clientset, opts.InterceptAddress)
	if err != nil {
		return nil, err
	}
	cm = &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      opts.ConfigMapName,
			Namespace: opts.ConfigMapNamespace,
			Annotations: map[string]string{
				"nodelocalproxy.io/generated": "true",
				"nodelocalproxy.io/source":    "default/kubernetes endpointslices",
			},
		},
		Data: map[string]string{opts.ConfigMapKey: cfgYAML},
	}
	if _, err := clientset.CoreV1().ConfigMaps(opts.ConfigMapNamespace).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("create bootstrap configmap %s/%s: %w", opts.ConfigMapNamespace, opts.ConfigMapName, err)
	}
	return config.LoadConfigData([]byte(cfgYAML))
}

func (o Options) withDefaults() Options {
	if o.ConfigMapNamespace == "" {
		o.ConfigMapNamespace = namespaceFromServiceAccount()
		if o.ConfigMapNamespace == "" {
			o.ConfigMapNamespace = DefaultConfigMapNamespace
		}
	}
	if o.ConfigMapName == "" {
		o.ConfigMapName = DefaultConfigMapName
	}
	if o.ConfigMapKey == "" {
		o.ConfigMapKey = DefaultConfigMapKey
	}
	return o
}

func namespaceFromServiceAccount() string {
	data, err := os.ReadFile(serviceAccountNS)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func generateConfigYAML(ctx context.Context, clientset kubernetes.Interface, interceptAddress string) (string, error) {
	slices, err := clientset.DiscoveryV1().EndpointSlices(kubernetesNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + kubernetesService,
	})
	if err != nil {
		return "", fmt.Errorf("list %s/%s endpointslices: %w", kubernetesNamespace, kubernetesEndpoint, err)
	}
	backends, err := backendsFromEndpointSlices(slices.Items)
	if err != nil {
		return "", err
	}
	var doc struct {
		Mode      string           `yaml:"mode"`
		Intercept config.Intercept `yaml:"intercept"`
		Backends  []string         `yaml:"backends"`
	}
	doc.Mode = "ebpf-transparent"
	doc.Intercept.Address = interceptAddress
	doc.Backends = backends
	data, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("marshal bootstrap config: %w", err)
	}
	return string(data), nil
}

func backendsFromEndpointSlices(slices []discoveryv1.EndpointSlice) ([]string, error) {
	seen := make(map[string]struct{})
	for _, slice := range slices {
		port, ok := endpointSlicePort(slice.Ports)
		if !ok {
			continue
		}
		for _, endpoint := range slice.Endpoints {
			if endpoint.Conditions.Ready != nil && !*endpoint.Conditions.Ready {
				continue
			}
			for _, addr := range endpoint.Addresses {
				ip := net.ParseIP(addr).To4()
				if ip == nil {
					continue
				}
				seen[net.JoinHostPort(ip.String(), strconv.Itoa(int(port)))] = struct{}{}
			}
		}
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("no ready IPv4 backends found in %s/%s endpointslices", kubernetesNamespace, kubernetesEndpoint)
	}
	out := make([]string, 0, len(seen))
	for backend := range seen {
		out = append(out, backend)
	}
	sort.Strings(out)
	return out, nil
}

func endpointSlicePort(ports []discoveryv1.EndpointPort) (int32, bool) {
	for _, port := range ports {
		if port.Name != nil && *port.Name == "https" && port.Port != nil && *port.Port > 0 {
			return *port.Port, true
		}
	}
	for _, port := range ports {
		if port.Port != nil && *port.Port == 6443 {
			return *port.Port, true
		}
	}
	for _, port := range ports {
		if port.Port != nil && *port.Port > 0 {
			return *port.Port, true
		}
	}
	return 0, false
}
