package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	defaultVXLANPrefix  = "vx-cwm-"
	defaultBridgePrefix = "br-cwm-"
	defaultSyncPeriod   = 15 * time.Second
	defaultVNIStart     = 10000
	defaultVNIEnd       = 16777215
	defaultLeaseName    = "cw-multinet-vni-allocator"
)

var floodMAC = net.HardwareAddr{0, 0, 0, 0, 0, 0}

type agentConfig struct {
	VXLANPrefix             string
	BridgePrefix            string
	SyncPeriod              time.Duration
	NodeName                string
	DisableBridgeNetfilter  bool
	PrewarmNADs             bool
	AutoAllocateVNI         bool
	AllocatorLeaderElection bool
	AllocatorLeaseNamespace string
	AllocatorLeaseName      string
	VNIStart                int
	VNIEnd                  int
}

type overlayDecl struct {
	VNI             int
	MTU             int
	VXLANPort       int
	BridgeName      string
	VXLANName       string
	UnderlayIface   string
	SourceAddress   string
	DisableLearning bool
}

type localOverlay struct {
	VNI        int
	BridgeName string
	VXLANName  string
	VXLAN      netlink.Link
	Bridge     netlink.Link
}

type nadConfig struct {
	Type            string `json:"type,omitempty"`
	VNI             int    `json:"vni,omitempty"`
	MTU             int    `json:"mtu,omitempty"`
	VXLANPort       int    `json:"vxlanPort,omitempty"`
	BridgeName      string `json:"bridgeName,omitempty"`
	VXLANName       string `json:"vxlanName,omitempty"`
	UnderlayIface   string `json:"underlayInterface,omitempty"`
	SourceAddress   string `json:"sourceAddress,omitempty"`
	DisableLearning bool   `json:"disableLearning,omitempty"`
}

type nadOverlay struct {
	Namespace string
	Name      string
	Config    nadConfig
	RawConfig string
}

var nadGVR = schema.GroupVersionResource{
	Group:    "k8s.cni.cncf.io",
	Version:  "v1",
	Resource: "network-attachment-definitions",
}

var errNotCWMultinet = errors.New("not a cw-multinet NAD")

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	cfg := loadAgentConfig()
	restCfg, err := kubeRESTConfig()
	if err != nil {
		log.Fatalf("create kubernetes rest config: %v", err)
	}
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		log.Fatalf("create kubernetes clientset: %v", err)
	}
	dynClient, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		log.Fatalf("create dynamic client: %v", err)
	}

	log.Printf("starting cw-multinet-agent vxlanPrefix=%s bridgePrefix=%s syncPeriod=%s nodeName=%s disableBridgeNetfilter=%t prewarmNADs=%t autoAllocateVNI=%t vniRange=%d-%d", cfg.VXLANPrefix, cfg.BridgePrefix, cfg.SyncPeriod, cfg.NodeName, cfg.DisableBridgeNetfilter, cfg.PrewarmNADs, cfg.AutoAllocateVNI, cfg.VNIStart, cfg.VNIEnd)
	if err := run(context.Background(), client, dynClient, cfg); err != nil {
		log.Fatalf("agent stopped: %v", err)
	}
}

func loadAgentConfig() agentConfig {
	cfg := agentConfig{
		VXLANPrefix:             envOrDefault("VXLAN_PREFIX", defaultVXLANPrefix),
		BridgePrefix:            envOrDefault("BRIDGE_PREFIX", defaultBridgePrefix),
		SyncPeriod:              defaultSyncPeriod,
		NodeName:                os.Getenv("NODE_NAME"),
		DisableBridgeNetfilter:  envBoolOrDefault("DISABLE_BRIDGE_NETFILTER", true),
		PrewarmNADs:             envBoolOrDefault("PREWARM_NADS", false),
		AutoAllocateVNI:         envBoolOrDefault("AUTO_ALLOCATE_VNI", true),
		AllocatorLeaderElection: envBoolOrDefault("ALLOCATOR_LEADER_ELECTION", true),
		AllocatorLeaseNamespace: os.Getenv("POD_NAMESPACE"),
		AllocatorLeaseName:      envOrDefault("ALLOCATOR_LEASE_NAME", defaultLeaseName),
		VNIStart:                envIntOrDefault("VNI_RANGE_START", defaultVNIStart),
		VNIEnd:                  envIntOrDefault("VNI_RANGE_END", defaultVNIEnd),
	}
	if raw := os.Getenv("SYNC_PERIOD"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			log.Fatalf("parse SYNC_PERIOD %q: %v", raw, err)
		}
		cfg.SyncPeriod = parsed
	}
	if cfg.VNIStart <= 0 || cfg.VNIStart > 16777215 {
		log.Fatalf("VNI_RANGE_START must be between 1 and 16777215, got %d", cfg.VNIStart)
	}
	if cfg.VNIEnd <= 0 || cfg.VNIEnd > 16777215 || cfg.VNIEnd < cfg.VNIStart {
		log.Fatalf("VNI_RANGE_END must be between VNI_RANGE_START and 16777215, got %d", cfg.VNIEnd)
	}
	if cfg.AutoAllocateVNI && cfg.AllocatorLeaderElection && cfg.AllocatorLeaseNamespace == "" {
		log.Fatalf("POD_NAMESPACE is required when AUTO_ALLOCATE_VNI=true and ALLOCATOR_LEADER_ELECTION=true")
	}
	return cfg
}

func run(ctx context.Context, client kubernetes.Interface, dynClient dynamic.Interface, cfg agentConfig) error {
	trigger := make(chan string, 1)
	triggerReconcile := func(reason string) {
		select {
		case trigger <- reason:
		default:
		}
	}

	var nadInformer cache.SharedIndexInformer
	nodeFactory := informers.NewSharedInformerFactory(client, 0)
	nodeInformer := nodeFactory.Core().V1().Nodes().Informer()
	if _, err := nodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { triggerReconcile("node add") },
		UpdateFunc: func(any, any) { triggerReconcile("node update") },
		DeleteFunc: func(any) { triggerReconcile("node delete") },
	}); err != nil {
		return fmt.Errorf("register node informer handler: %w", err)
	}

	nadFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(dynClient, 0, metav1.NamespaceAll, nil)
	nadInformer = nadFactory.ForResource(nadGVR).Informer()
	if _, err := nadInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { triggerReconcile("nad add") },
		UpdateFunc: func(any, any) { triggerReconcile("nad update") },
		DeleteFunc: func(any) { triggerReconcile("nad delete") },
	}); err != nil {
		return fmt.Errorf("register nad informer handler: %w", err)
	}

	startInformers(ctx, nodeFactory, nadFactory)
	go subscribeLinkEvents(ctx, cfg, triggerReconcile)
	var allocatorLeader atomic.Bool
	if cfg.AutoAllocateVNI {
		if cfg.AllocatorLeaderElection {
			startAllocatorLeaderElection(ctx, client, cfg, &allocatorLeader, triggerReconcile)
		} else {
			allocatorLeader.Store(true)
		}
	}

	ticker := time.NewTicker(cfg.SyncPeriod)
	defer ticker.Stop()
	triggerReconcile("startup")

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case reason := <-trigger:
			if err := reconcile(ctx, client, dynClient, nadInformer, cfg, &allocatorLeader); err != nil {
				log.Printf("reconcile failed reason=%q: %v", reason, err)
			}
		case <-ticker.C:
			if err := reconcile(ctx, client, dynClient, nadInformer, cfg, &allocatorLeader); err != nil {
				log.Printf("reconcile failed reason=%q: %v", "periodic", err)
			}
		}
	}
}

func startInformers(ctx context.Context, nodeFactory informers.SharedInformerFactory, nadFactory dynamicinformer.DynamicSharedInformerFactory) {
	nodeFactory.Start(ctx.Done())
	nadFactory.Start(ctx.Done())
	cache.WaitForCacheSync(ctx.Done(), nodeFactory.Core().V1().Nodes().Informer().HasSynced)
	for gvr, ok := range nadFactory.WaitForCacheSync(ctx.Done()) {
		if !ok {
			log.Printf("informer cache sync failed gvr=%s", gvr.String())
		}
	}
}

func subscribeLinkEvents(ctx context.Context, cfg agentConfig, trigger func(string)) {
	updates := make(chan netlink.LinkUpdate, 32)
	done := make(chan struct{})
	defer close(done)
	if err := netlink.LinkSubscribe(updates, done); err != nil {
		log.Printf("netlink link subscribe failed: %v", err)
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case update := <-updates:
			if update.Link == nil || update.Link.Type() != "vxlan" {
				continue
			}
			if strings.HasPrefix(update.Link.Attrs().Name, cfg.VXLANPrefix) {
				trigger("netlink vxlan " + update.Link.Attrs().Name)
			}
		}
	}
}

func reconcile(ctx context.Context, client kubernetes.Interface, dynClient dynamic.Interface, nadInformer cache.SharedIndexInformer, cfg agentConfig, allocatorLeader *atomic.Bool) error {
	if cfg.DisableBridgeNetfilter {
		if err := disableBridgeNetfilter(); err != nil {
			return err
		}
	}
	overlays := nadOverlays(nadInformer)
	localOverlays, err := discoverLocalOverlays(cfg)
	if err != nil {
		return err
	}
	if err := validateAssignedVNIs(overlays); err != nil {
		return err
	}
	if cfg.AutoAllocateVNI && allocatorLeader.Load() {
		if changed, err := allocateMissingVNIs(ctx, dynClient, overlays, localOverlays, cfg); err != nil {
			return err
		} else if changed {
			return nil
		}
	}
	desiredPeers, err := desiredNodeIPs(ctx, client, cfg.NodeName)
	if err != nil {
		return err
	}
	if cfg.PrewarmNADs {
		decls := declaredOverlays(overlays, cfg)
		if err := ensureDeclaredOverlays(decls); err != nil {
			return err
		}
	}
	if err := garbageCollectOverlays(overlays, cfg); err != nil {
		return err
	}
	vxlans, err := discoverVXLANs(cfg.VXLANPrefix)
	if err != nil {
		return err
	}
	for _, vxlan := range vxlans {
		if err := ensureBridgeAttachment(vxlan, cfg); err != nil {
			return err
		}
		if err := reconcileFDB(vxlan, desiredPeers); err != nil {
			return err
		}
	}
	return nil
}

func startAllocatorLeaderElection(ctx context.Context, client kubernetes.Interface, cfg agentConfig, leader *atomic.Bool, trigger func(string)) {
	identity := cfg.NodeName
	if identity == "" {
		hostname, err := os.Hostname()
		if err == nil && hostname != "" {
			identity = hostname
		} else {
			identity = "unknown"
		}
	}

	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      cfg.AllocatorLeaseName,
			Namespace: cfg.AllocatorLeaseNamespace,
		},
		Client: client.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: identity,
		},
	}

	go func() {
		leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
			Lock:            lock,
			LeaseDuration:   30 * time.Second,
			RenewDeadline:   20 * time.Second,
			RetryPeriod:     5 * time.Second,
			ReleaseOnCancel: true,
			Callbacks: leaderelection.LeaderCallbacks{
				OnStartedLeading: func(leaderCtx context.Context) {
					leader.Store(true)
					log.Printf("became auto-vni allocator leader identity=%s lease=%s/%s", identity, cfg.AllocatorLeaseNamespace, cfg.AllocatorLeaseName)
					trigger("allocator leader acquired")
					<-leaderCtx.Done()
					leader.Store(false)
				},
				OnStoppedLeading: func() {
					leader.Store(false)
					log.Printf("stopped auto-vni allocator leadership identity=%s lease=%s/%s", identity, cfg.AllocatorLeaseNamespace, cfg.AllocatorLeaseName)
					trigger("allocator leader lost")
				},
				OnNewLeader: func(current string) {
					log.Printf("auto-vni allocator leader=%s", current)
				},
			},
		})
	}()
}

func nadOverlays(informer cache.SharedIndexInformer) []nadOverlay {
	if informer == nil {
		return nil
	}
	var overlays []nadOverlay
	for _, item := range informer.GetStore().List() {
		obj, ok := item.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		rawConfig, ok, err := unstructured.NestedString(obj.Object, "spec", "config")
		if err != nil || !ok || strings.TrimSpace(rawConfig) == "" {
			continue
		}
		nad, err := parseNADConfig(rawConfig)
		if err != nil {
			if errors.Is(err, errNotCWMultinet) {
				continue
			}
			log.Printf("skip nad=%s/%s: %v", obj.GetNamespace(), obj.GetName(), err)
			continue
		}
		overlays = append(overlays, nadOverlay{
			Namespace: obj.GetNamespace(),
			Name:      obj.GetName(),
			Config:    nad,
			RawConfig: rawConfig,
		})
	}
	sort.Slice(overlays, func(i, j int) bool {
		left := overlays[i].Namespace + "/" + overlays[i].Name
		right := overlays[j].Namespace + "/" + overlays[j].Name
		return left < right
	})
	return overlays
}

func declaredOverlays(overlays []nadOverlay, cfg agentConfig) []overlayDecl {
	seen := map[string]overlayDecl{}
	for _, overlay := range overlays {
		if overlay.Config.VNI <= 0 {
			continue
		}
		decl, err := overlayDeclFromConfig(overlay.Config, cfg)
		if err != nil {
			log.Printf("skip nad=%s/%s: %v", overlay.Namespace, overlay.Name, err)
			continue
		}
		seen[decl.VXLANName] = decl
	}
	decls := make([]overlayDecl, 0, len(seen))
	for _, decl := range seen {
		decls = append(decls, decl)
	}
	return decls
}

func validateAssignedVNIs(overlays []nadOverlay) error {
	used := map[int]string{}
	for _, overlay := range overlays {
		vni := overlay.Config.VNI
		if vni <= 0 {
			continue
		}
		owner := fmt.Sprintf("%s/%s", overlay.Namespace, overlay.Name)
		if other, exists := used[vni]; exists && other != owner {
			return fmt.Errorf("vni conflict: %s and %s both use vni %d", other, owner, vni)
		}
		used[vni] = owner
	}
	return nil
}

func parseNADConfig(raw string) (nadConfig, error) {
	nad := nadConfig{}
	if err := json.Unmarshal([]byte(raw), &nad); err != nil {
		return nadConfig{}, fmt.Errorf("parse config: %w", err)
	}
	if nad.Type != "cw-multinet" {
		return nadConfig{}, errNotCWMultinet
	}
	if nad.VNI < 0 || nad.VNI > 16777215 {
		return nadConfig{}, fmt.Errorf("vni must be between 1 and 16777215 when set")
	}
	return nad, nil
}

func overlayDeclFromConfig(nad nadConfig, cfg agentConfig) (overlayDecl, error) {
	if nad.VNI <= 0 || nad.VNI > 16777215 {
		return overlayDecl{}, fmt.Errorf("vni is required and must be between 1 and 16777215")
	}
	decl := overlayDecl{
		VNI:             nad.VNI,
		MTU:             nad.MTU,
		VXLANPort:       nad.VXLANPort,
		BridgeName:      nad.BridgeName,
		VXLANName:       nad.VXLANName,
		UnderlayIface:   nad.UnderlayIface,
		SourceAddress:   nad.SourceAddress,
		DisableLearning: nad.DisableLearning,
	}
	if decl.MTU == 0 {
		decl.MTU = 1450
	}
	if decl.VXLANPort == 0 {
		decl.VXLANPort = 14789
	}
	if decl.BridgeName == "" {
		decl.BridgeName = fmt.Sprintf("%s%d", cfg.BridgePrefix, decl.VNI)
	}
	if decl.VXLANName == "" {
		decl.VXLANName = fmt.Sprintf("%s%d", cfg.VXLANPrefix, decl.VNI)
	}
	if len(decl.BridgeName) > 15 {
		return overlayDecl{}, fmt.Errorf("bridgeName %q exceeds Linux 15 character interface limit", decl.BridgeName)
	}
	if len(decl.VXLANName) > 15 {
		return overlayDecl{}, fmt.Errorf("vxlanName %q exceeds Linux 15 character interface limit", decl.VXLANName)
	}
	return decl, nil
}

func allocateMissingVNIs(ctx context.Context, dynClient dynamic.Interface, overlays []nadOverlay, localOverlays []localOverlay, cfg agentConfig) (bool, error) {
	used := map[int]string{}
	changed := false

	for _, overlay := range overlays {
		vni := overlay.Config.VNI
		if vni <= 0 {
			continue
		}
		owner := fmt.Sprintf("%s/%s", overlay.Namespace, overlay.Name)
		if other, exists := used[vni]; exists && other != owner {
			return false, fmt.Errorf("vni conflict: %s and %s both use vni %d", other, owner, vni)
		}
		used[vni] = owner
	}
	for _, overlay := range localOverlays {
		if _, exists := used[overlay.VNI]; !exists {
			used[overlay.VNI] = "local-active/" + overlay.VXLANName
		}
	}

	for _, overlay := range overlays {
		if overlay.Config.VNI > 0 {
			continue
		}
		vni, err := nextFreeVNI(used, cfg)
		if err != nil {
			return changed, err
		}
		owner := fmt.Sprintf("%s/%s", overlay.Namespace, overlay.Name)
		updated := overlay.Config
		updated.VNI = vni
		if err := patchNADVNI(ctx, dynClient, overlay, updated); err != nil {
			return changed, err
		}
		used[vni] = owner
		changed = true
		log.Printf("allocated vni=%d nad=%s", vni, owner)
	}
	return changed, nil
}

func desiredOverlayVNIs(overlays []nadOverlay) map[int]struct{} {
	desired := map[int]struct{}{}
	for _, overlay := range overlays {
		if overlay.Config.VNI > 0 {
			desired[overlay.Config.VNI] = struct{}{}
		}
	}
	return desired
}

func discoverLocalOverlays(cfg agentConfig) ([]localOverlay, error) {
	vxlans, err := discoverVXLANs(cfg.VXLANPrefix)
	if err != nil {
		return nil, err
	}
	overlays := make([]localOverlay, 0, len(vxlans))
	for _, vxlan := range vxlans {
		suffix := strings.TrimPrefix(vxlan.Attrs().Name, cfg.VXLANPrefix)
		vni, err := strconv.Atoi(suffix)
		if err != nil || vni <= 0 || vni > 16777215 {
			continue
		}
		bridgeName := cfg.BridgePrefix + suffix
		bridge, err := netlink.LinkByName(bridgeName)
		if err != nil {
			bridge = nil
		}
		overlays = append(overlays, localOverlay{
			VNI:        vni,
			BridgeName: bridgeName,
			VXLANName:  vxlan.Attrs().Name,
			VXLAN:      vxlan,
			Bridge:     bridge,
		})
	}
	return overlays, nil
}

func garbageCollectOverlays(overlays []nadOverlay, cfg agentConfig) error {
	desired := desiredOverlayVNIs(overlays)
	localOverlays, err := discoverLocalOverlays(cfg)
	if err != nil {
		return err
	}
	for _, overlay := range localOverlays {
		if _, ok := desired[overlay.VNI]; ok {
			continue
		}
		if overlay.Bridge == nil {
			continue
		}
		empty, err := bridgeHasNoPodPorts(overlay.Bridge, overlay.VXLANName)
		if err != nil {
			return err
		}
		if !empty {
			continue
		}
		if overlay.VXLAN != nil {
			if err := netlink.LinkDel(overlay.VXLAN); err != nil && !isNotFound(err) {
				return fmt.Errorf("delete stale vxlan %s: %w", overlay.VXLANName, err)
			}
			log.Printf("deleted stale vxlan=%s vni=%d", overlay.VXLANName, overlay.VNI)
		}
		refetchedBridge, err := netlink.LinkByName(overlay.BridgeName)
		if err != nil {
			if isNotFound(err) {
				continue
			}
			return fmt.Errorf("lookup stale bridge %s: %w", overlay.BridgeName, err)
		}
		empty, err = bridgeHasNoPodPorts(refetchedBridge, "")
		if err != nil {
			return err
		}
		if !empty {
			continue
		}
		if err := netlink.LinkDel(refetchedBridge); err != nil && !isNotFound(err) {
			return fmt.Errorf("delete stale bridge %s: %w", overlay.BridgeName, err)
		}
		log.Printf("deleted stale bridge=%s vni=%d", overlay.BridgeName, overlay.VNI)
	}
	return nil
}

func bridgeHasNoPodPorts(bridge netlink.Link, ignoreLinkName string) (bool, error) {
	links, err := linkList()
	if err != nil {
		return false, fmt.Errorf("list links for bridge %s cleanup: %w", bridge.Attrs().Name, err)
	}
	for _, link := range links {
		if link.Attrs().MasterIndex != bridge.Attrs().Index {
			continue
		}
		if ignoreLinkName != "" && link.Attrs().Name == ignoreLinkName {
			continue
		}
		return false, nil
	}
	return true, nil
}

func linkList() ([]netlink.Link, error) {
	var (
		links []netlink.Link
		err   error
	)
	for attempt := 1; attempt <= 3; attempt++ {
		links, err = netlink.LinkList()
		if err == nil {
			return links, nil
		}
		if !isTransientNetlinkDumpError(err) {
			return links, err
		}
		time.Sleep(time.Duration(attempt) * 50 * time.Millisecond)
	}
	return links, err
}

func isTransientNetlinkDumpError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "incomplete or inconsistent") ||
		strings.Contains(msg, "dump was interrupted")
}

func nextFreeVNI(used map[int]string, cfg agentConfig) (int, error) {
	for vni := cfg.VNIStart; vni <= cfg.VNIEnd; vni++ {
		if _, exists := used[vni]; !exists {
			return vni, nil
		}
	}
	return 0, fmt.Errorf("no free VNI in range %d-%d", cfg.VNIStart, cfg.VNIEnd)
}

func patchNADVNI(ctx context.Context, dynClient dynamic.Interface, overlay nadOverlay, updated nadConfig) error {
	var config map[string]any
	if err := json.Unmarshal([]byte(overlay.RawConfig), &config); err != nil {
		return fmt.Errorf("parse original NAD config %s/%s: %w", overlay.Namespace, overlay.Name, err)
	}
	config["vni"] = updated.VNI
	updatedConfig, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal updated NAD config %s/%s: %w", overlay.Namespace, overlay.Name, err)
	}
	patch := map[string]any{
		"spec": map[string]any{
			"config": string(updatedConfig),
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal patch for NAD %s/%s: %w", overlay.Namespace, overlay.Name, err)
	}
	_, err = dynClient.Resource(nadGVR).Namespace(overlay.Namespace).Patch(ctx, overlay.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patch NAD %s/%s with allocated vni: %w", overlay.Namespace, overlay.Name, err)
	}
	return nil
}

func ensureDeclaredOverlays(decls []overlayDecl) error {
	for _, decl := range decls {
		bridge, err := ensureBridge(decl.BridgeName, decl.MTU)
		if err != nil {
			return err
		}
		if _, err := ensureVxlan(decl, bridge); err != nil {
			return err
		}
	}
	return nil
}

func ensureBridge(name string, mtu int) (netlink.Link, error) {
	link, err := netlink.LinkByName(name)
	if err == nil {
		if link.Type() != "bridge" {
			return nil, fmt.Errorf("link %s already exists with type %s, expected bridge", name, link.Type())
		}
		if err := setMTU(link, mtu); err != nil {
			return nil, err
		}
		if err := netlink.LinkSetUp(link); err != nil {
			return nil, fmt.Errorf("set bridge %s up: %w", name, err)
		}
		return link, nil
	}

	bridge := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: name, MTU: mtu}}
	if err := netlink.LinkAdd(bridge); err != nil {
		return nil, fmt.Errorf("add bridge %s: %w", name, err)
	}
	link, err = netlink.LinkByName(name)
	if err != nil {
		return nil, fmt.Errorf("lookup created bridge %s: %w", name, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return nil, fmt.Errorf("set bridge %s up: %w", name, err)
	}
	log.Printf("created bridge=%s", name)
	return link, nil
}

func ensureVxlan(decl overlayDecl, bridge netlink.Link) (netlink.Link, error) {
	link, err := netlink.LinkByName(decl.VXLANName)
	if err == nil {
		if link.Type() != "vxlan" {
			return nil, fmt.Errorf("link %s already exists with type %s, expected vxlan", decl.VXLANName, link.Type())
		}
		if err := enslaveAndRaise(link, bridge, decl.MTU); err != nil {
			return nil, err
		}
		return link, nil
	}

	vxlan := &netlink.Vxlan{
		LinkAttrs: netlink.LinkAttrs{Name: decl.VXLANName, MTU: decl.MTU},
		VxlanId:   decl.VNI,
		Port:      decl.VXLANPort,
		Learning:  !decl.DisableLearning,
	}
	if decl.UnderlayIface != "" {
		underlay, err := netlink.LinkByName(decl.UnderlayIface)
		if err != nil {
			return nil, fmt.Errorf("lookup underlayInterface %s: %w", decl.UnderlayIface, err)
		}
		vxlan.VtepDevIndex = underlay.Attrs().Index
	}
	if decl.SourceAddress != "" {
		src := net.ParseIP(decl.SourceAddress)
		if src == nil {
			return nil, fmt.Errorf("parse sourceAddress %q", decl.SourceAddress)
		}
		vxlan.SrcAddr = src
	}
	if err := netlink.LinkAdd(vxlan); err != nil {
		return nil, fmt.Errorf("add vxlan %s vni %d: %w", decl.VXLANName, decl.VNI, err)
	}
	link, err = netlink.LinkByName(decl.VXLANName)
	if err != nil {
		return nil, fmt.Errorf("lookup created vxlan %s: %w", decl.VXLANName, err)
	}
	if err := enslaveAndRaise(link, bridge, decl.MTU); err != nil {
		return nil, err
	}
	log.Printf("created vxlan=%s vni=%d bridge=%s", decl.VXLANName, decl.VNI, bridge.Attrs().Name)
	return link, nil
}

func enslaveAndRaise(link, bridge netlink.Link, mtu int) error {
	if err := setMTU(link, mtu); err != nil {
		return err
	}
	if link.Attrs().MasterIndex != bridge.Attrs().Index {
		if err := netlink.LinkSetMaster(link, bridge); err != nil {
			return fmt.Errorf("attach %s to bridge %s: %w", link.Attrs().Name, bridge.Attrs().Name, err)
		}
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("set %s up: %w", link.Attrs().Name, err)
	}
	return nil
}

func setMTU(link netlink.Link, mtu int) error {
	if mtu > 0 && link.Attrs().MTU != mtu {
		if err := netlink.LinkSetMTU(link, mtu); err != nil {
			return fmt.Errorf("set %s mtu %d: %w", link.Attrs().Name, mtu, err)
		}
	}
	return nil
}

func disableBridgeNetfilter() error {
	for _, path := range []string{
		"/proc/sys/net/bridge/bridge-nf-call-iptables",
		"/proc/sys/net/bridge/bridge-nf-call-ip6tables",
	} {
		current, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read %s: %w", path, err)
		}
		if strings.TrimSpace(string(current)) == "0" {
			continue
		}
		if err := os.WriteFile(path, []byte("0\n"), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		log.Printf("set %s=0", path)
	}
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
	links, err := linkList()
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

func ensureBridgeAttachment(vxlan netlink.Link, cfg agentConfig) error {
	suffix := strings.TrimPrefix(vxlan.Attrs().Name, cfg.VXLANPrefix)
	bridgeName := cfg.BridgePrefix + suffix
	bridge, err := netlink.LinkByName(bridgeName)
	if err != nil {
		return fmt.Errorf("lookup bridge %s for %s: %w", bridgeName, vxlan.Attrs().Name, err)
	}
	if vxlan.Attrs().MasterIndex == bridge.Attrs().Index {
		return nil
	}
	if err := netlink.LinkSetMaster(vxlan, bridge); err != nil {
		return fmt.Errorf("attach %s to %s: %w", vxlan.Attrs().Name, bridgeName, err)
	}
	refetched, err := netlink.LinkByName(vxlan.Attrs().Name)
	if err != nil {
		return fmt.Errorf("lookup attached vxlan %s: %w", vxlan.Attrs().Name, err)
	}
	if refetched.Attrs().MasterIndex != bridge.Attrs().Index {
		return fmt.Errorf("attach %s to %s did not persist", vxlan.Attrs().Name, bridgeName)
	}
	log.Printf("attached vxlan=%s bridge=%s", vxlan.Attrs().Name, bridgeName)
	return nil
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

func kubeRESTConfig() (*rest.Config, error) {
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
	return cfg, nil
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envBoolOrDefault(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		log.Fatalf("parse %s %q: %v", key, value, err)
	}
	return parsed
}

func envIntOrDefault(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		log.Fatalf("parse %s %q: %v", key, value, err)
	}
	return parsed
}

func isNotFound(err error) bool {
	return errors.Is(err, os.ErrNotExist) || strings.Contains(strings.ToLower(err.Error()), "no such")
}
