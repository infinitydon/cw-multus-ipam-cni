# cw-multinet Comprehensive Validation - 2026-06-15

## Environment

- Cluster: two-node CoreWeave Kubernetes cluster
- Kubernetes: `v1.36.1`
- Nodes:
  - `g46cd14` - `10.176.244.201`
  - `g80b396` - `10.176.243.1`
- cw-multinet image: `ghcr.io/infinitydon/cw-multus-ipam-cni:fb58098`
- cw-multinet DaemonSet: `2/2` ready
- Auto-VNI allocator Lease holder during most tests: `g46cd14`
- Whereabouts installed for this validation from upstream release `v0.9.4`, pinned to `ghcr.io/k8snetworkplumbingwg/whereabouts:v0.9.4`.

## Scope

The validation covered:

- Auto-VNI allocation with config preservation.
- Explicit VNI operation.
- Inter-node L2 overlay traffic.
- Intra-node bridge traffic.
- `static`, `host-local`, and `whereabouts` IPAM.
- TCP throughput over cw-multinet.
- Leader pod restart resilience.
- Duplicate VNI conflict detection.
- Stale bridge/VXLAN cleanup after NAD and pod deletion.
- Whereabouts allocation release.

## IPAM And Connectivity Matrix

| Test | Network | VNI | Placement | IPAM | Result |
| --- | --- | ---: | --- | --- | --- |
| Static auto-VNI | `static-auto` | `10000` | cross-node | `static` with Multus `ips` capability | Pass |
| Explicit static | `static-explicit` | `12101` | same-node | `static` with Multus `ips` capability | Pass |
| Host-local | `hostlocal-net` | `12102` | same-node | `host-local` | Pass |
| Whereabouts | `whereabouts-net` | `10001` | cross-node | `whereabouts` | Pass |

Assigned secondary addresses:

| Pod | Interface | Address |
| --- | --- | --- |
| `static-server` | `netstatic` | `10.254.20.10/24` |
| `static-client` | `netstatic` | `10.254.20.20/24` |
| `explicit-a` | `netexp` | `10.254.21.10/24` |
| `explicit-b` | `netexp` | `10.254.21.20/24` |
| `hostlocal-a` | `nethl` | `10.254.22.10/24` |
| `hostlocal-b` | `nethl` | `10.254.22.11/24` |
| `whereabouts-a` | `netwb` | `10.254.23.17/24` |
| `whereabouts-b` | `netwb` | `10.254.23.16/24` |

Ping results:

| Test | Direction | Result | RTT |
| --- | --- | --- | --- |
| Static auto inter-node | `10.254.20.20 -> 10.254.20.10` | `5/5`, `0%` loss | avg `0.318 ms` |
| Explicit static intra-node | `10.254.21.20 -> 10.254.21.10` | `5/5`, `0%` loss | avg `0.035 ms` |
| Host-local same-node | `10.254.22.11 -> 10.254.22.10` | `5/5`, `0%` loss | avg `0.035 ms` |
| Whereabouts inter-node | `10.254.23.16 -> 10.254.23.17` | `5/5`, `0%` loss | avg `0.340 ms` |

Throughput results:

| Test | Direction | Duration | Result |
| --- | --- | ---: | --- |
| Static auto inter-node TCP | `10.254.20.20 -> 10.254.20.10` | 10s | `22.9 GBytes`, `19.7 Gbits/sec` receiver |
| Whereabouts inter-node TCP | `10.254.23.16 -> 10.254.23.17` | 10s | `28.5 GBytes`, `24.5 Gbits/sec` receiver |

## Leader Restart Resilience

The allocator leader pod on `g46cd14` was deleted:

```text
oldLeader=g46cd14 leaderPod=cw-multinet-cni-w46zh
pod "cw-multinet-cni-w46zh" deleted
daemon set "cw-multinet-cni" successfully rolled out
currentLeader=g46cd14
```

Connectivity remained healthy after the leader pod restart:

| Test | Result | RTT |
| --- | --- | --- |
| Static inter-node ping | `5/5`, `0%` loss | avg `0.322 ms` |
| Whereabouts inter-node ping | `5/5`, `0%` loss | avg `0.322 ms` |

## Duplicate VNI Conflict Detection

Two NADs were created with `vni: 12222`. Both agents reported the conflict:

```text
vni conflict: cw-multinet-conflict/conflict-a and cw-multinet-conflict/conflict-b both use vni 12222
```

Result: pass. The agents rejected the ambiguous overlay ownership instead of silently reconciling two networks onto the same VNI.

## Cleanup

After deleting the comprehensive test namespace, both agents removed stale local host devices. Observed cleanup included:

```text
deleted stale vxlan=vx-cwm-10000 vni=10000
deleted stale bridge=br-cwm-10000 vni=10000
deleted stale vxlan=vx-cwm-10001 vni=10001
deleted stale bridge=br-cwm-10001 vni=10001
deleted stale vxlan=vx-cwm-12101 vni=12101
deleted stale bridge=br-cwm-12101 vni=12101
deleted stale vxlan=vx-cwm-12102 vni=12102
deleted stale bridge=br-cwm-12102 vni=12102
```

Whereabouts retained the `IPPool` object, but released the allocations:

```yaml
apiVersion: whereabouts.cni.cncf.io/v1alpha1
kind: IPPool
metadata:
  name: 10.254.23.0-24
  namespace: kube-system
spec:
  allocations: {}
  range: 10.254.23.0/24
```

Result: pass.

