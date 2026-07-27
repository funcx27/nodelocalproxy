//go:build ebpf

package ebpf

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -cc clang -target bpfel -tags ebpf Nlp nlp.c -- -Iheaders
