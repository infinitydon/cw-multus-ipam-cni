package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	defaultVXLANPrefix = "vx-cwm-"
	defaultSyncPeriod  = 15 * time.Second
)

var floodMAC = net.HardwareAddr{0, 0, 0, 0, 0, 0}

type agentConfig struct {
	VXLANPrefix string
	SyncPeriod  time.Duration
	NodeName    string
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	cfg := loadAgentConfig()
	client, err := newKubeClient()
	if err != nil {
		log.Fatalf("create kubernetes client: %v", err)
	}

	log.Printf("starting cw-multinet-agent vxlanPrefix=%s syncPeriod=%s nodeName=%s", cfg.VXLANPrefix, cfg.SyncPeriod, cfg.NodeName)
	if err := run(context.Background(), client, cfg); err != nil {
		log.Fatalf("agent stopped: %v", err)
	}
}

func loadAgentConfig() agentConfig {
	cfg := agentConfig{
		VXLANPrefix: envOrDefault("VXLAN_PREFIX", defaultVXLANPrefix),
		SyncPeriod:  defaultSyncPeriod,
		NodeName:    os.Getenv("NODE_NAME"),
	}
	if raw := os.Getenv("SYNC_PERIOD"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			log.Fatalf("parse SYNC_PERIOD %q: %v", raw, err)
		}
		cfg.SyncPeriod = parsed
	}
	return cfg
}

func run(ctx context.Context, client kubernetes.Interface, cfg agentConfig) error {
	ticker := time.NewTicker(cfg.SyncPeriod)
	defer ticker.Stop()

	for {
		if err := reconcile(ctx, client, cfg); err != nil {
			log.Printf("reconcile failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func reconcile(ctx context.Context, client kubernetes.Interface, cfg agentConfig) error {
	desiredPeers, err := desiredNodeIPs(ctx, client, cfg.NodeName)
	if err != nil {
		return err
	}
	vxlans, err := discoverVXLANs(cfg.VXLANPrefix)
	if err != nil {
		return err
	}
	for _, vxlan := range vxlans {
		if err := reconcileFDB(vxlan, desiredPeers); err != nil {
			return err
		}
	}
	log.Printf("reconciled peers=%d vxlanLinks=%d", len(desiredPeers), len(vxlans))
	return nil
}

func desiredNodeIPs(ctx context.Context, client kubernetes.Interface, selfNodeName string) (map[string]net.IP, error) {
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	peers := map[string]net.IP{}
	for _, node := range nodes.Items {
		if node.Name == selfNodeName {
			continue
		}
		if !nodeReady(node) {
			continue
		}
		for _, addr := range node.Status.Addresses {
			if addr.Type != corev1.NodeInternalIP {
				continue
			}
			ip := net.ParseIP(addr.Address)
			if ip == nil {
				continue
			}
			peers[ip.String()] = ip
		}
	}

	for _, ip := range localInterfaceIPs() {
		delete(peers, ip.String())
	}
	return peers, nil
}

func nodeReady(node corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func discoverVXLANs(prefix string) ([]netlink.Link, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	var vxlans []netlink.Link
	for _, link := range links {
		if link.Type() == "vxlan" && strings.HasPrefix(link.Attrs().Name, prefix) {
			vxlans = append(vxlans, link)
		}
	}
	return vxlans, nil
}

func reconcileFDB(vxlan netlink.Link, desiredPeers map[string]net.IP) error {
	existing, err := netlink.NeighList(vxlan.Attrs().Index, unix.AF_BRIDGE)
	if err != nil {
		return fmt.Errorf("list fdb for %s: %w", vxlan.Attrs().Name, err)
	}

	seen := map[string]struct{}{}
	for _, entry := range existing {
		if !isManagedFloodEntry(entry) {
			continue
		}
		key := entry.IP.String()
		if _, desired := desiredPeers[key]; !desired {
			stale := entry
			if err := netlink.NeighDel(&stale); err != nil && !isNotFound(err) {
				return fmt.Errorf("delete stale fdb %s on %s: %w", key, vxlan.Attrs().Name, err)
			}
			log.Printf("deleted stale fdb peer=%s link=%s", key, vxlan.Attrs().Name)
			continue
		}
		seen[key] = struct{}{}
	}

	for key, ip := range desiredPeers {
		if _, ok := seen[key]; ok {
			continue
		}
		entry := &netlink.Neigh{
			LinkIndex:    vxlan.Attrs().Index,
			Family:       unix.AF_BRIDGE,
			State:        unix.NUD_PERMANENT,
			Flags:        unix.NTF_SELF,
			IP:           ip,
			HardwareAddr: floodMAC,
		}
		if err := netlink.NeighAppend(entry); err != nil && !os.IsExist(err) {
			return fmt.Errorf("add fdb peer %s on %s: %w", key, vxlan.Attrs().Name, err)
		}
		log.Printf("added fdb peer=%s link=%s", key, vxlan.Attrs().Name)
	}
	return nil
}

func isManagedFloodEntry(entry netlink.Neigh) bool {
	return entry.Family == unix.AF_BRIDGE &&
		entry.IP != nil &&
		entry.HardwareAddr.String() == floodMAC.String() &&
		entry.Flags&unix.NTF_SELF != 0
}

func localInterfaceIPs() []net.IP {
	var ips []net.IP
	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			switch value := addr.(type) {
			case *net.IPNet:
				ips = append(ips, value.IP)
			case *net.IPAddr:
				ips = append(ips, value.IP)
			}
		}
	}
	return ips
}

func newKubeClient() (kubernetes.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			home, homeErr := os.UserHomeDir()
			if homeErr == nil {
				kubeconfig = filepath.Join(home, ".kube", "config")
			}
		}
		if kubeconfig == "" {
			return nil, err
		}
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, err
		}
	}
	return kubernetes.NewForConfig(cfg)
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func isNotFound(err error) bool {
	return errors.Is(err, os.ErrNotExist) || strings.Contains(strings.ToLower(err.Error()), "no such")
}
