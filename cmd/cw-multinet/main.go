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

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/cni/pkg/version"
	"github.com/containernetworking/plugins/pkg/ipam"
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"
)

const (
	pluginName          = "cw-multinet"
	defaultInterface    = "dummy"
	defaultRouteMetric  = 200
	defaultHostVethPref = "cw"
)

type ipamConf struct {
	Type string `json:"type,omitempty"`
}

type netConf struct {
	types.NetConf
	IPAM *ipamConf `json:"ipam,omitempty"`

	// InterfaceType controls the Linux link created in the target netns.
	// "dummy" is the CoreWeave-safe default because it needs no L2 adjacency.
	// "veth" is useful for node-local inspection or future routed/tunnel agents.
	InterfaceType string `json:"interfaceType,omitempty"`
	MTU           int    `json:"mtu,omitempty"`

	// Routes are extra CIDRs to install against the secondary interface.
	// IPAM-provided routes are installed when UseIPAMRoutes is true.
	Routes        []string `json:"routes,omitempty"`
	UseIPAMRoutes bool     `json:"useIPAMRoutes,omitempty"`
	RouteMetric   int      `json:"routeMetric,omitempty"`

	// VethHostPrefix is used only with interfaceType=veth. Linux interface names
	// are limited to IFNAMSIZ, so the generated name is capped at 15 chars.
	VethHostPrefix string `json:"vethHostPrefix,omitempty"`
}

func loadConf(stdin []byte) (*netConf, error) {
	conf := &netConf{}
	if err := json.Unmarshal(stdin, conf); err != nil {
		return nil, fmt.Errorf("parse network config: %w", err)
	}
	if conf.CNIVersion == "" {
		conf.CNIVersion = "0.4.0"
	}
	if conf.InterfaceType == "" {
		conf.InterfaceType = defaultInterface
	}
	if conf.InterfaceType != "dummy" && conf.InterfaceType != "veth" {
		return nil, fmt.Errorf("unsupported interfaceType %q: expected dummy or veth", conf.InterfaceType)
	}
	if conf.RouteMetric == 0 {
		conf.RouteMetric = defaultRouteMetric
	}
	if conf.VethHostPrefix == "" {
		conf.VethHostPrefix = defaultHostVethPref
	}
	if conf.IPAM == nil || conf.IPAM.Type == "" {
		return nil, errors.New("ipam.type is required")
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

	containerNS, err := ns.GetNS(args.Netns)
	if err != nil {
		_ = ipam.ExecDel(conf.IPAM.Type, args.StdinData)
		return fmt.Errorf("open netns %q: %w", args.Netns, err)
	}
	defer containerNS.Close()

	hostIfName := ""
	if conf.InterfaceType == "veth" {
		hostIfName = hostVethName(conf.VethHostPrefix, args.ContainerID, args.IfName)
		if err := createVeth(containerNS, args.IfName, hostIfName, conf.MTU); err != nil {
			_ = ipam.ExecDel(conf.IPAM.Type, args.StdinData)
			return err
		}
	} else {
		if err := containerNS.Do(func(_ ns.NetNS) error {
			return createDummy(args.IfName, conf.MTU)
		}); err != nil {
			_ = ipam.ExecDel(conf.IPAM.Type, args.StdinData)
			return err
		}
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

	if conf.InterfaceType == "veth" {
		if err := addHostRoutes(hostIfName, result); err != nil {
			_ = deleteContainerLink(args.Netns, args.IfName)
			_ = ipam.ExecDel(conf.IPAM.Type, args.StdinData)
			return err
		}
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

func createDummy(ifName string, mtu int) error {
	if _, err := netlink.LinkByName(ifName); err == nil {
		return fmt.Errorf("link %s already exists", ifName)
	}
	link := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: ifName, MTU: mtu}}
	if err := netlink.LinkAdd(link); err != nil {
		return fmt.Errorf("add dummy %s: %w", ifName, err)
	}
	return nil
}

func createVeth(containerNS ns.NetNS, ifName, hostIfName string, mtu int) error {
	peerName := hostIfName + "p"
	if len(peerName) > 15 {
		peerName = peerName[:15]
	}
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: hostIfName, MTU: mtu},
		PeerName:  peerName,
	}
	if err := netlink.LinkAdd(veth); err != nil {
		return fmt.Errorf("add veth %s/%s: %w", hostIfName, peerName, err)
	}

	hostLink, err := netlink.LinkByName(hostIfName)
	if err != nil {
		return fmt.Errorf("lookup host veth %s: %w", hostIfName, err)
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

func addHostRoutes(hostIfName string, result *current.Result) error {
	link, err := netlink.LinkByName(hostIfName)
	if err != nil {
		return fmt.Errorf("lookup host link %s: %w", hostIfName, err)
	}
	for _, ipConf := range result.IPs {
		if ipConf == nil {
			continue
		}
		dst := singleIPCIDR(ipConf.Address.IP)
		route := netlink.Route{LinkIndex: link.Attrs().Index, Dst: dst, Scope: netlink.SCOPE_LINK}
		if err := netlink.RouteAdd(&route); err != nil && !os.IsExist(err) {
			return fmt.Errorf("add host route %s dev %s: %w", dst.String(), hostIfName, err)
		}
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

func singleIPCIDR(ip net.IP) *net.IPNet {
	if ip.To4() != nil {
		return &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)}
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}
}
