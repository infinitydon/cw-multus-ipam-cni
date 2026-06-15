# KubeVirt cw-multinet Test Results - 2026-06-15

## Environment

- Cluster: two-node CoreWeave Kubernetes cluster
- Kubernetes: `v1.36.1`
- KubeVirt release installed: `v1.8.3`
- KubeVirt status: `Available=True`
- cw-multinet image: `ghcr.io/infinitydon/cw-multus-ipam-cni:fb58098`
- Whereabouts release installed: `v0.9.4`

KubeVirt required a placement adjustment because the default operator manifests initially targeted control-plane/master nodes, and this managed cluster exposes only worker nodes:

```sh
kubectl -n kubevirt patch kubevirt kubevirt --type=merge \
  -p '{"spec":{"infra":{"nodePlacement":{}},"workloads":{"nodePlacement":{}}}}'
```

If `virt-operator` remains pending before it can reconcile the KubeVirt CR, the operator Deployment affinity can also be removed:

```sh
kubectl -n kubevirt patch deployment virt-operator --type=json \
  -p='[{"op":"remove","path":"/spec/template/spec/affinity"}]'
```

## KubeVirt Components

```text
virt-api          2/2 Running
virt-controller   2/2 Running
virt-handler      2/2 Running
virt-operator     2/2 Running
```

## VM Deployment

Applied manifest: `kubevirt/vm-cw-multinet-demo.yaml`

- Namespace: `kubevirt-cw-test`
- NAD: `vm-cw-net`
- IPAM: `whereabouts`, range `10.254.57.0/24`, exclude `10.254.57.0/28`
- Auto-VNI allocation: enabled by omitting `vni`
- VM image: `quay.io/kubevirt/fedora-container-disk-images:35`
- VM A: `cwm-vm-a`, node `g46cd14`
- VM B: `cwm-vm-b`, node `g80b396`

VMI status:

```text
virtualmachineinstance.kubevirt.io/cwm-vm-a   Running   10.8.0.44    g46cd14   Ready=True
virtualmachineinstance.kubevirt.io/cwm-vm-b   Running   10.8.0.155   g80b396   Ready=True
```

Launcher pods:

```text
virt-launcher-cwm-vm-a-mpt5g   3/3 Running   10.8.0.44    g46cd14
virt-launcher-cwm-vm-b-kqq77   3/3 Running   10.8.0.155   g80b396
```

Kubernetes events showed Whereabouts assigning the cw-multinet secondary attachments:

```text
Add pod274c9a51dce [10.254.57.16/24] from kubevirt-cw-test/vm-cw-net
Add pod274c9a51dce [10.254.57.17/24] from kubevirt-cw-test/vm-cw-net
```

## Guest Validation

The Fedora cloud image applied cloud-init `networkData` and requested DHCP on the secondary KubeVirt bridge interface. Inside the guest, `eth1` received the Whereabouts-assigned address.

VM A serial log:

```text
eth0: 10.0.2.2 fe80::ff:fe57:a
eth1: 10.254.57.16 fe80::ff:fe57:1a
CW-MULTINET-REPORT-START cwm-vm-a
PING 10.254.57.17 (10.254.57.17) 56(84) bytes of data.
64 bytes from 10.254.57.17: icmp_seq=1 ttl=64 time=0.903 ms
64 bytes from 10.254.57.17: icmp_seq=2 ttl=64 time=0.509 ms
64 bytes from 10.254.57.17: icmp_seq=3 ttl=64 time=0.479 ms

--- 10.254.57.17 ping statistics ---
3 packets transmitted, 3 received, 0% packet loss, time 2003ms
rtt min/avg/max/mdev = 0.479/0.630/0.903/0.193 ms
CW-MULTINET-REPORT-END cwm-vm-a
```

VM B serial log:

```text
eth0: 10.0.2.2 fe80::ff:fe57:b
eth1: 10.254.57.17 fe80::ff:fe57:1b
CW-MULTINET-REPORT-START cwm-vm-b
PING 10.254.57.16 (10.254.57.16) 56(84) bytes of data.
64 bytes from 10.254.57.16: icmp_seq=1 ttl=64 time=0.388 ms
64 bytes from 10.254.57.16: icmp_seq=2 ttl=64 time=0.426 ms
64 bytes from 10.254.57.16: icmp_seq=3 ttl=64 time=0.466 ms

--- 10.254.57.16 ping statistics ---
3 packets transmitted, 3 received, 0% packet loss, time 2003ms
rtt min/avg/max/mdev = 0.388/0.426/0.466/0.031 ms
CW-MULTINET-REPORT-END cwm-vm-b
```

Result: pass. Two KubeVirt VMs on different worker nodes used cw-multinet as a secondary L2 network, received their secondary guest IPs from Whereabouts through KubeVirt bridge binding, and successfully exchanged traffic over their secondary VM interfaces.

## Notes

- `quay.io/containerdisks/fedora:44` booted the VM domain, but KubeVirt `v1.8.3` rejected VMI status updates because the image produced an empty containerDisk checksum where the CRD expected an `int32`. The KubeVirt Fedora containerDisk image did not have this issue.
- The earlier CirR0S smoke test proved that the secondary interface could be attached, but CirR0S did not apply the cloud-init YAML network data needed to configure `eth1` automatically.
- The demo manifest includes a simple `fedora` password only for serial-console validation. Replace or remove it for any non-demo use.

## Cleanup

The successful validation namespace was deleted after serial log collection:

```sh
kubectl delete namespace kubevirt-cw-test
```

Whereabouts released the two VM allocations:

```yaml
apiVersion: whereabouts.cni.cncf.io/v1alpha1
kind: IPPool
metadata:
  name: 10.254.57.0-24
  namespace: kube-system
spec:
  allocations: {}
  range: 10.254.57.0/24
```
