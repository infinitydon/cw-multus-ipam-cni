# KubeVirt cw-multinet Test Results - 2026-06-15

## Environment

- Cluster: two-node CoreWeave Kubernetes cluster
- Kubernetes: `v1.36.1`
- KubeVirt release installed: `v1.8.3`
- KubeVirt status: `Available=True`
- cw-multinet image: `ghcr.io/infinitydon/cw-multus-ipam-cni:fb58098`
- Whereabouts release installed: `v0.9.4`

KubeVirt required a placement adjustment because the default operator manifests initially targeted control-plane/master nodes, and this managed cluster exposes only worker nodes. The following patch was applied:

```sh
kubectl -n kubevirt patch kubevirt kubevirt --type=merge \
  -p '{"spec":{"infra":{"nodePlacement":{}},"workloads":{"nodePlacement":{}}}}'
```

The already-created install job was deleted after the patch so the operator recreated it with the corrected placement.

## KubeVirt Components

```text
virt-api          2/2 Running
virt-controller   2/2 Running
virt-handler      2/2 Running
virt-operator     2/2 Running
```

## VM Deployment

Applied manifest shape:

- Namespace: `kubevirt-cw-test`
- NAD: `vm-cw-net`
- IPAM: `whereabouts`, range `10.254.50.0/24`, exclude `10.254.50.0/28`
- Auto-VNI allocation: `vni=10000`
- VM A: `cwm-vm-a`, node `g46cd14`
- VM B: `cwm-vm-b`, node `g80b396`

VM status:

```text
virtualmachine.kubevirt.io/cwm-vm-a   Running   Ready=True
virtualmachine.kubevirt.io/cwm-vm-b   Running   Ready=True
```

VMI interfaces:

```text
cwm-vm-a  default  10.8.0.72    10.8.0.72      a6:d8:33:60:3c:c7
cwm-vm-a  cwmnet   10.254.50.16 10.254.50.16   52:e6:02:38:1d:95
cwm-vm-b  default  10.8.0.217   10.8.0.217     72:df:16:7d:9b:fd
cwm-vm-b  cwmnet   10.254.50.17 10.254.50.17   7e:7e:70:55:d1:88
```

Multus network-status on the launcher pods also showed the cw-multinet attachments:

```text
virt-launcher-cwm-vm-a: kubevirt-cw-test/vm-cw-net, IP 10.254.50.16
virt-launcher-cwm-vm-b: kubevirt-cw-test/vm-cw-net, IP 10.254.50.17
```

## Guest Validation

CirrOS exposed the secondary NIC as `eth1`, but did not automatically apply the cloud-init `networkData` for that interface. The live test configured the guest interface manually from `virtctl console`:

```sh
# cwm-vm-a
sudo ifconfig eth1 10.254.50.16 netmask 255.255.255.0 up

# cwm-vm-b
sudo ifconfig eth1 10.254.50.17 netmask 255.255.255.0 up
```

VM B to VM A:

```text
PING 10.254.50.16 (10.254.50.16): 56 data bytes
64 bytes from 10.254.50.16: seq=0 ttl=64 time=0.732 ms
64 bytes from 10.254.50.16: seq=1 ttl=64 time=0.400 ms
64 bytes from 10.254.50.16: seq=2 ttl=64 time=0.418 ms
64 bytes from 10.254.50.16: seq=3 ttl=64 time=0.425 ms
64 bytes from 10.254.50.16: seq=4 ttl=64 time=0.457 ms

5 packets transmitted, 5 packets received, 0% packet loss
round-trip min/avg/max = 0.400/0.486/0.732 ms
```

VM A to VM B:

```text
PING 10.254.50.17 (10.254.50.17): 56 data bytes
64 bytes from 10.254.50.17: seq=0 ttl=64 time=0.455 ms
64 bytes from 10.254.50.17: seq=1 ttl=64 time=0.385 ms
64 bytes from 10.254.50.17: seq=2 ttl=64 time=1.630 ms
64 bytes from 10.254.50.17: seq=3 ttl=64 time=0.417 ms
64 bytes from 10.254.50.17: seq=4 ttl=64 time=0.468 ms

5 packets transmitted, 5 packets received, 0% packet loss
round-trip min/avg/max = 0.385/0.671/1.630 ms
```

Result: pass. Two KubeVirt VMs on different worker nodes used cw-multinet as a secondary L2 network and successfully exchanged traffic over their secondary VM interfaces.

## Cleanup

The `kubevirt-cw-test` namespace was deleted after the test. Both node agents removed the stale host devices:

```text
deleted stale vxlan=vx-cwm-10000 vni=10000
deleted stale bridge=br-cwm-10000 vni=10000
```

The Whereabouts pool remained as a CR, but all VM allocations were released:

```yaml
apiVersion: whereabouts.cni.cncf.io/v1alpha1
kind: IPPool
metadata:
  name: 10.254.50.0-24
  namespace: kube-system
spec:
  allocations: {}
  range: 10.254.50.0/24
```
