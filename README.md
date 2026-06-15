# cw-multinet

`cw-multinet` is a Multus delegate CNI that creates real secondary pod NICs on a virtual L2 overlay. It is intended for CoreWeave-like Kubernetes clusters where the primary network is a managed Cilium overlay and the provider does not expose a customizable secondary L2 fabric.

The plugin creates a per-network Linux bridge and VXLAN device on each node, attaches each pod with a veth pair, and delegates address allocation to standard CNI IPAM plugins. A companion node agent watches Kubernetes Nodes and continuously programs VXLAN flood entries for remote node VTEPs. Intra-node traffic is switched locally by the bridge. Inter-node traffic is encapsulated in VXLAN over the existing node network.

This gives telco-style workloads predictable interfaces such as `n2`, `n3`, `n4`, `n6`, and `s1-mme` with L2 behavior suitable for secondary-plane communication.

## What It Does

- Works as a Multus delegate CNI.
- Creates pod veth NICs, not dummy interfaces.
- Creates one host bridge and one VXLAN device per VNI.
- Supports inter-node L2 by VXLAN flooding to dynamically discovered node IPs.
- Supports any CNI IPAM executable in `CNI_PATH`, including `static`, `host-local`, and `whereabouts`.
- Implements CNI `ADD`, `DEL`, and `CHECK`.
- Includes a node lifecycle agent for node joins, removals, and readiness changes.

## Dataplane Shape

```text
pod netns                    host netns                              remote node
---------                    ---------                              -----------
n2@ifX  <--- veth pair --->  cwm... -> br-cwm-3002 -> vx-cwm-3002  ~~ VXLAN ~~  vx-cwm-3002 -> br-cwm-3002 -> peer pods
```

Each telco plane should use a distinct VNI. For example:

- N2: `3002`
- N3: `3003`
- N4: `3004`
- N6: `3006`

## Build

```sh
make build
```

The module currently requires Go 1.24.2 or newer.

## Install

Build and publish the image:

```sh
make image IMAGE=ghcr.io/infinitydon/cw-multus-ipam-cni:latest
docker push ghcr.io/infinitydon/cw-multus-ipam-cni:latest
kubectl apply -f deploy/install-daemonset.yaml
```

The installer copies `/cw-multinet` to `/opt/cni/bin/cw-multinet` on every node.

## Example NAD

```yaml
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: n2-whereabouts
  namespace: core5g
spec:
  config: |
    {
      "cniVersion": "0.4.0",
      "name": "n2-whereabouts",
      "type": "cw-multinet",
      "vni": 3002,
      "ipam": {
        "type": "whereabouts",
        "range": "10.30.2.0/24",
        "exclude": ["10.30.2.0/28"]
      }
    }
```

Attach it with Multus:

```yaml
metadata:
  annotations:
    k8s.v1.cni.cncf.io/networks: |
      [{ "name": "n2-whereabouts", "interface": "n2" }]
```

## Configuration

| Field | Default | Description |
| --- | --- | --- |
| `type` | required | Must be `cw-multinet`. |
| `vni` | required | VXLAN Network Identifier, `1` to `16777215`. |
| `peers` | `[]` | Optional static remote node VTEP IPs programmed by the CNI call. The node agent handles dynamic peers. |
| `bridgeName` | `br-cwm-<vni>` | Host bridge name. Must fit Linux's 15-character interface limit. |
| `vxlanName` | `vx-cwm-<vni>` | Host VXLAN device name. Must fit Linux's 15-character interface limit. |
| `vxlanPort` | `14789` | UDP destination port for VXLAN. CoreWeave filtered UDP/4789 in testing, so the default avoids the well-known VXLAN port. |
| `mtu` | `1450` | MTU for bridge, VXLAN, and pod veth. Adjust for the provider underlay. |
| `underlayInterface` | kernel route | Optional host interface to bind the VXLAN VTEP to. |
| `sourceAddress` | kernel route | Optional local VTEP source IP. |
| `hostVethPrefix` | `cwm` | Prefix for host-side pod veth names. |
| `disableLearning` | `false` | Disable VXLAN source-MAC learning. |
| `disableFDBFlood` | `false` | Do not program peer flood entries. |
| `skipPeerSelf` | `false` | Skip peers that match local interface IPs. |
| `ipam` | required | Any CNI IPAM config. |
| `routes` | `[]` | Optional extra CIDRs to route through the secondary interface. |
| `useIPAMRoutes` | `false` | Install routes returned by the IPAM plugin. |
| `routeMetric` | `200` | Metric used for plugin-managed routes. |

## IPAM Notes

Whereabouts is the recommended dynamic allocator when addresses must be unique across nodes. `host-local` is only node-local, so it can allocate duplicate secondary IPs on different nodes unless ranges are partitioned per node. `static` is useful for deterministic NF interface addresses.

## Operational Notes

All nodes must be able to send UDP VXLAN traffic to all configured peer node IPs on `vxlanPort`.

The `cw-multinet-agent` DaemonSet watches Kubernetes Nodes, extracts Ready node `InternalIP` addresses, and reconciles VXLAN flood FDB entries on every host. New Ready nodes are added as peers; removed or NotReady nodes are deleted as peers.

The agent also disables Linux bridge netfilter by default (`net.bridge.bridge-nf-call-iptables=0` and `net.bridge.bridge-nf-call-ip6tables=0`). This is required for transparent L2 behavior on CoreWeave/Cilium because bridged secondary-plane packets otherwise traverse host iptables/Cilium masquerade rules and can have their source rewritten.

## Sample Cluster Notes

The provided kubeconfig points to a two-node CoreWeave cluster with:

- Kubernetes `v1.36.1`
- Cilium DaemonSet in `cw-cilium-system`
- Multus thick plugin in `kube-system`
- Existing telco NADs in `core5g` and `open5gs-ai-eval`
- Kube-OVN currently installed as an additional component

No cluster changes were applied while creating this repository.
