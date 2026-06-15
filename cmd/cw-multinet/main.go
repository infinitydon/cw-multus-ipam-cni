package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/cni/pkg/version"
	"github.com/containernetworking/plugins/pkg/ipam"
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	pluginName         = "cw-multinet"
	defaultMTU         = 1450
	defaultRouteMetric = 200
	defaultVxlanPort   = 14789
	defaultVethPrefix  = "cwm"
)

var vxlanFloodMAC = net.HardwareAddr{0, 0, 0, 0, 0, 0}

type ipamConf struct {
	Type string `json:"type,omitempty"`
}

type netConf struct {
	types.NetConf
	IPAM *ipamConf `json:"ipam,omitempty"`

	// VNI identifies the virtual L2 segment. Use a distinct VNI for N2, N3,
	// N4, N6, S1-MME, and any other isolated telco plane.
	VNI int `json:"vni,omitempty"`

	// Peers are remote node VTEP IPs. They are programmed as VXLAN flood
	// destinations so ARP, IPv6 ND, broadcast, and unknown unicast can cross
	// nodes over the existing provider network.
	Peers []string `json:"peers,omitempty"`

	BridgeName      string   `json:"bridgeName,omitempty"`
	VxlanName       string   `json:"vxlanName,omitempty"`
	VxlanPort       int      `json:"vxlanPort,omitempty"`
	MTU             int      `json:"mtu,omitempty"`
	UnderlayIface   string   `json:"underlayInterface,omitempty"`
	SourceAddress   string   `json:"sourceAddress,omitempty"`
	HostVethPrefix  string   `json:"hostVethPrefix,omitempty"`
	DisableLearning bool     `json:"disableLearning,omitempty"`
	DisableFDBFlood bool     `json:"disableFDBFlood,omitempty"`
	SkipPeerSelf    bool     `json:"skipPeerSelf,omitempty"`
	Routes          []string `json:"routes,omitempty"`
	UseIPAMRoutes   bool     `json:"useIPAMRoutes,omitempty"`
	RouteMetric     int      `json:"routeMetric,omitempty"`
}

func loadConf(stdin []byte) (*netConf, error) {
	conf := &netConf{}
	if err := json.Unmarshal(stdin, conf); err != nil {
		return nil, fmt.Errorf("parse network config: %w", err)
	}
	if conf.CNIVersion == "" {
		conf.CNIVersion = "0.4.0"
	}
	if conf.IPAM == nil || conf.IPAM.Type == "" {
		return nil, errors.New("ipam.type is required")
	}
	if conf.VNI <= 0 || conf.VNI > 16777215 {
		return nil, errors.New("vni is required and must be between 1 and 16777215")
	}
	if conf.MTU == 0 {
		conf.MTU = defaultMTU
	}
	if conf.VxlanPort == 0 {
		conf.VxlanPort = defaultVxlanPort
	}
	if conf.RouteMetric == 0 {
		conf.RouteMetric = defaultRouteMetric
	}
	if conf.HostVethPrefix == "" {
		conf.HostVethPrefix = defaultVethPrefix
	}
	if conf.BridgeName == "" {
		conf.BridgeName = fmt.Sprintf("br-cwm-%d", conf.VNI)
	}
	if conf.VxlanName == "" {
		conf.VxlanName = fmt.Sprintf("vx-cwm-%d", conf.VNI)
	}
	if len(conf.BridgeName) > 15 {
		return nil, fmt.Errorf("bridgeName %q exceeds Linux 15 character interface limit", conf.BridgeName)
	}
	if len(conf.VxlanName) > 15 {
		return nil, fmt.Errorf("vxlanName %q exceeds Linux 15 character interface limit", conf.VxlanName)
	}
	return conf, nil
}

func main() {
	runtime.LockOSThread()
	skel.PluginMainFuncs(skel.CNIFuncs{
		Add:   cmdAdd,
		Check: cmdCheck,
		Del:   cmdDel,
	}, version.All, fmt.Sprintf("%s CNI plugin", pluginName))
}

func cmdAdd(args *skel.CmdArgs) error {
	conf, err := loadConf(args.StdinData)
	if err != nil {
		return err
	}

	ipamResult, err := ipam.ExecAdd(conf.IPAM.Type, args.StdinData)
	if err != nil {
		return fmt.Errorf("ipam add: %w", err)
	}
	result, err := current.NewResultFromResult(ipamResult)
	if err != nil {
		_ = ipam.ExecDel(conf.IPAM.Type, args.StdinData)
		return fmt.Errorf("convert ipam result: %w", err)
	}
	if len(result.IPs) == 0 {
		_ = ipam.ExecDel(conf.IPAM.Type, args.StdinData)
		return errors.New("ipam returned no IP addresses")
	}

	if err := ensureOverlay(conf); err != nil {
		_ = ipam.ExecDel(conf.IPAM.Type, args.StdinData)
		return err
	}

	containerNS, err := ns.GetNS(args.Netns)
	if err != nil {
		_ = ipam.ExecDel(conf.IPAM.Type, args.StdinData)
		return fmt.Errorf("open netns %q: %w", args.Netns, err)
	}
	defer containerNS.Close()

	hostIfName := hostVethName(conf.HostVethPrefix, args.ContainerID, args.IfName)
	if err := createOverlayVeth(containerNS, args.IfName, hostIfName, conf.BridgeName, conf.MTU); err != nil {
		_ = ipam.ExecDel(conf.IPAM.Type, args.StdinData)
		return err
	}

	if err := containerNS.Do(func(_ ns.NetNS) error {
		link, err := netlink.LinkByName(args.IfName)
		if err != nil {
			return fmt.Errorf("lookup %s: %w", args.IfName, err)
		}
		if err := configureLink(link, result, conf); err != nil {
			return err
		}
		result.Interfaces = []*current.Interface{{
			Name:    args.IfName,
			Mac:     link.Attrs().HardwareAddr.String(),
			Sandbox: args.Netns,
		}}
		for i := range result.IPs {
			if result.IPs[i].Interface == nil {
				idx := 0
				result.IPs[i].Interface = &idx
			}
		}
		return nil
	}); err != nil {
		_ = deleteContainerLink(args.Netns, args.IfName)
		_ = ipam.ExecDel(conf.IPAM.Type, args.StdinData)
		return err
	}

	return types.PrintResult(result, conf.CNIVersion)
}

func cmdCheck(args *skel.CmdArgs) error {
	conf, err := loadConf(args.StdinData)
	if err != nil {
		return err
	}
	if err := ipam.ExecCheck(conf.IPAM.Type, args.StdinData); err != nil {
		return fmt.Errorf("ipam check: %w", err)
	}
	if _, err := netlink.LinkByName(conf.BridgeName); err != nil {
		return fmt.Errorf("bridge %s missing: %w", conf.BridgeName, err)
	}
	if _, err := netlink.LinkByName(conf.VxlanName); err != nil {
		return fmt.Errorf("vxlan %s missing: %w", conf.VxlanName, err)
	}
	containerNS, err := ns.GetNS(args.Netns)
	if err != nil {
		return fmt.Errorf("open netns %q: %w", args.Netns, err)
	}
	defer containerNS.Close()
	return containerNS.Do(func(_ ns.NetNS) error {
		link, err := netlink.LinkByName(args.IfName)
		if err != nil {
			return fmt.Errorf("link %s missing: %w", args.IfName, err)
		}
		if link.Attrs().Flags&net.FlagUp == 0 {
			return fmt.Errorf("link %s is down", args.IfName)
		}
		return nil
	})
}

func cmdDel(args *skel.CmdArgs) error {
	conf, err := loadConf(args.StdinData)
	if err != nil {
		return err
	}

	delErr := deleteContainerLink(args.Netns, args.IfName)
	ipamErr := ipam.ExecDel(conf.IPAM.Type, args.StdinData)
	if delErr != nil {
		return delErr
	}
	if ipamErr != nil {
		return fmt.Errorf("ipam del: %w", ipamErr)
	}
	return nil
}

func ensureOverlay(conf *netConf) error {
	bridge, err := ensureBridge(conf.BridgeName, conf.MTU)
	if err != nil {
		return err
	}
	vxlan, err := ensureVxlan(conf, bridge.Attrs().Index)
	if err != nil {
		return err
	}
	if !conf.DisableFDBFlood {
		if err := programFDBFlood(vxlan.Attrs().Index, conf); err != nil {
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
	return link, nil
}

func ensureVxlan(conf *netConf, bridgeIndex int) (netlink.Link, error) {
	link, err := netlink.LinkByName(conf.VxlanName)
	if err == nil {
		if link.Type() != "vxlan" {
			return nil, fmt.Errorf("link %s already exists with type %s, expected vxlan", conf.VxlanName, link.Type())
		}
		if err := enslaveAndRaise(link, bridgeIndex, conf.MTU); err != nil {
			return nil, err
		}
		return link, nil
	}

	vxlan := &netlink.Vxlan{
		LinkAttrs: netlink.LinkAttrs{Name: conf.VxlanName, MTU: conf.MTU},
		VxlanId:   conf.VNI,
		Port:      conf.VxlanPort,
		Learning:  !conf.DisableLearning,
	}
	if conf.UnderlayIface != "" {
		underlay, err := netlink.LinkByName(conf.UnderlayIface)
		if err != nil {
			return nil, fmt.Errorf("lookup underlayInterface %s: %w", conf.UnderlayIface, err)
		}
		vxlan.VtepDevIndex = underlay.Attrs().Index
	}
	if conf.SourceAddress != "" {
		src := net.ParseIP(conf.SourceAddress)
		if src == nil {
			return nil, fmt.Errorf("parse sourceAddress %q", conf.SourceAddress)
		}
		vxlan.SrcAddr = src
	}
	if err := netlink.LinkAdd(vxlan); err != nil {
		return nil, fmt.Errorf("add vxlan %s vni %d: %w", conf.VxlanName, conf.VNI, err)
	}
	link, err = netlink.LinkByName(conf.VxlanName)
	if err != nil {
		return nil, fmt.Errorf("lookup created vxlan %s: %w", conf.VxlanName, err)
	}
	if err := enslaveAndRaise(link, bridgeIndex, conf.MTU); err != nil {
		return nil, err
	}
	return link, nil
}

func enslaveAndRaise(link netlink.Link, masterIndex, mtu int) error {
	if err := setMTU(link, mtu); err != nil {
		return err
	}
	if link.Attrs().MasterIndex != masterIndex {
		if err := netlink.LinkSetMasterByIndex(link, masterIndex); err != nil {
			return fmt.Errorf("attach %s to bridge index %d: %w", link.Attrs().Name, masterIndex, err)
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

func programFDBFlood(vxlanIndex int, conf *netConf) error {
	localIPs := map[string]struct{}{}
	if conf.SkipPeerSelf {
		for _, ip := range localInterfaceIPs() {
			localIPs[ip.String()] = struct{}{}
		}
	}
	for _, peer := range conf.Peers {
		peerIP := net.ParseIP(strings.TrimSpace(peer))
		if peerIP == nil {
			return fmt.Errorf("parse peer %q", peer)
		}
		if _, self := localIPs[peerIP.String()]; self {
			continue
		}
		entry := &netlink.Neigh{
			LinkIndex:    vxlanIndex,
			Family:       unix.AF_BRIDGE,
			State:        unix.NUD_PERMANENT,
			Flags:        unix.NTF_SELF,
			IP:           peerIP,
			HardwareAddr: vxlanFloodMAC,
		}
		if err := netlink.NeighAppend(entry); err != nil && !os.IsExist(err) {
			return fmt.Errorf("add vxlan flood fdb peer %s: %w", peerIP.String(), err)
		}
	}
	return nil
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

func createOverlayVeth(containerNS ns.NetNS, ifName, hostIfName, bridgeName string, mtu int) error {
	peerName := peerVethName(hostIfName)
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: hostIfName, MTU: mtu},
		PeerName:  peerName,
		PeerMTU:   uint32(mtu),
	}
	if err := netlink.LinkAdd(veth); err != nil {
		return fmt.Errorf("add veth %s/%s: %w", hostIfName, peerName, err)
	}

	hostLink, err := netlink.LinkByName(hostIfName)
	if err != nil {
		return fmt.Errorf("lookup host veth %s: %w", hostIfName, err)
	}
	bridge, err := netlink.LinkByName(bridgeName)
	if err != nil {
		return fmt.Errorf("lookup bridge %s: %w", bridgeName, err)
	}
	if err := netlink.LinkSetMaster(hostLink, bridge); err != nil {
		return fmt.Errorf("attach %s to %s: %w", hostIfName, bridgeName, err)
	}
	if err := netlink.LinkSetUp(hostLink); err != nil {
		return fmt.Errorf("set %s up: %w", hostIfName, err)
	}

	peer, err := netlink.LinkByName(peerName)
	if err != nil {
		return fmt.Errorf("lookup peer veth %s: %w", peerName, err)
	}
	if err := netlink.LinkSetNsFd(peer, int(containerNS.Fd())); err != nil {
		return fmt.Errorf("move %s to netns: %w", peerName, err)
	}
	return containerNS.Do(func(_ ns.NetNS) error {
		link, err := netlink.LinkByName(peerName)
		if err != nil {
			return fmt.Errorf("lookup container peer %s: %w", peerName, err)
		}
		if err := netlink.LinkSetName(link, ifName); err != nil {
			return fmt.Errorf("rename %s to %s: %w", peerName, ifName, err)
		}
		return nil
	})
}

func configureLink(link netlink.Link, result *current.Result, conf *netConf) error {
	for _, ipConf := range result.IPs {
		if ipConf == nil {
			continue
		}
		addr := &netlink.Addr{IPNet: &ipConf.Address}
		if err := netlink.AddrAdd(link, addr); err != nil {
			return fmt.Errorf("add address %s to %s: %w", ipConf.Address.String(), link.Attrs().Name, err)
		}
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("set %s up: %w", link.Attrs().Name, err)
	}

	if conf.UseIPAMRoutes {
		for _, route := range result.Routes {
			if err := addRoute(link, route.Dst, route.GW, conf.RouteMetric); err != nil {
				return err
			}
		}
	}
	for _, cidr := range conf.Routes {
		_, dst, err := net.ParseCIDR(cidr)
		if err != nil {
			return fmt.Errorf("parse route %q: %w", cidr, err)
		}
		if err := addRoute(link, *dst, nil, conf.RouteMetric); err != nil {
			return err
		}
	}
	return nil
}

func addRoute(link netlink.Link, dst net.IPNet, gw net.IP, metric int) error {
	route := netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       &dst,
		Gw:        gw,
		Priority:  metric,
	}
	if err := netlink.RouteAdd(&route); err != nil && !os.IsExist(err) {
		return fmt.Errorf("add route %s on %s: %w", dst.String(), link.Attrs().Name, err)
	}
	return nil
}

func deleteContainerLink(netnsPath, ifName string) error {
	if netnsPath == "" {
		return nil
	}
	containerNS, err := ns.GetNS(netnsPath)
	if err != nil {
		// DEL must be idempotent; the runtime may remove the netns before CNI DEL.
		return nil
	}
	defer containerNS.Close()
	return containerNS.Do(func(_ ns.NetNS) error {
		link, err := netlink.LinkByName(ifName)
		if err != nil {
			return nil
		}
		if err := netlink.LinkDel(link); err != nil {
			return fmt.Errorf("delete %s: %w", ifName, err)
		}
		return nil
	})
}

func hostVethName(prefix, containerID, ifName string) string {
	sum := sha1.Sum([]byte(containerID + "/" + ifName))
	suffix := hex.EncodeToString(sum[:])[:10]
	name := prefix + suffix
	if len(name) > 15 {
		return name[:15]
	}
	return name
}

func peerVethName(hostIfName string) string {
	if len(hostIfName) >= 15 {
		return hostIfName[:14] + "p"
	}
	return hostIfName + "p"
}
