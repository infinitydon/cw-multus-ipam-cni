# cw-multinet

`cw-multinet` is a small CNI plugin for Multus secondary pod IPs on CoreWeave-like Kubernetes clusters where the primary network is a managed Cilium overlay and secondary L2 reachability is not available.

The plugin delegates IP allocation to standard IPAM plugins, then creates a pod-local secondary interface and assigns the returned addresses. This lets telco-style workloads expose predictable interfaces such as `n2`, `n3`, `n4`, `n6`, and `s1-mme` without relying on macvlan, ipvlan L2 mode, VLANs, or Cilium customization.

## What It Does

- Works as a Multus delegate CNI.
- Supports any CNI IPAM executable in `CNI_PATH`, including `static`, `host-local`, and `whereabouts`.
- Creates `dummy` secondary interfaces by default, which do not need L2 adjacency.
- Supports optional `veth` mode for node-local host visibility and future routed/tunnel agents.
- Implements CNI `ADD`, `DEL`, and `CHECK`.

## What It Does Not Do

The default mode does not make secondary IPs routable across nodes. That is deliberate: a managed overlay that does not expose L2 cannot make macvlan/ipvlan-style secondary networks work by configuration alone. Use this plugin when the NF needs secondary interface names and bindable IPs. Add a node route/tunnel agent if the NF must exchange traffic directly between secondary IPs across nodes.

## Build

```sh
make build
```

The module currently requires Go 1.24.2 or newer.

## Install

Build and publish the image, then replace the image in `deploy/install-daemonset.yaml`:

```sh
make image IMAGE=your-registry/cw-multus-ipam-cni:latest
docker push your-registry/cw-multus-ipam-cni:latest
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
      "interfaceType": "dummy",
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
| `interfaceType` | `dummy` | `dummy` for no-L2 pod-local IPs, or `veth` for pod/host paired links. |
| `ipam` | required | Any CNI IPAM config. |
| `mtu` | kernel default | Optional MTU for created link. |
| `routes` | `[]` | Optional extra CIDRs to route through the secondary interface. |
| `useIPAMRoutes` | `false` | Install routes returned by the IPAM plugin. Keep false for bind-only dummy interfaces. |
| `routeMetric` | `200` | Metric used for plugin-managed routes. |
| `vethHostPrefix` | `cw` | Host-side veth name prefix in veth mode. |

## Sample Cluster Notes

The provided kubeconfig points to a two-node CoreWeave cluster with:

- Kubernetes `v1.36.1`
- Cilium DaemonSet in `cw-cilium-system`
- Multus thick plugin in `kube-system`
- Existing telco NADs in `core5g` and `open5gs-ai-eval`
- Kube-OVN currently installed as an additional component

No cluster changes were applied while creating this repository.
