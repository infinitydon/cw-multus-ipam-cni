# KubeVirt With cw-multinet And Whereabouts

This folder contains a working KubeVirt validation setup for two VMs with a cw-multinet secondary interface and Whereabouts IPAM.

The example uses a Fedora cloud containerDisk instead of CirrOS because the VM needs cloud-init `networkData` support to request DHCP on the secondary interface automatically. CirrOS is useful for a quick boot smoke test, but it did not apply the secondary NIC cloud-init network data in validation.

## Install KubeVirt

Install the latest stable KubeVirt operator and custom resource:

```sh
export KUBECONFIG=/path/to/kubeconfig
export RELEASE="$(curl -fsSL https://storage.googleapis.com/kubevirt-prow/release/kubevirt/kubevirt/stable.txt)"

kubectl apply -f "https://github.com/kubevirt/kubevirt/releases/download/${RELEASE}/kubevirt-operator.yaml"
kubectl apply -f "https://github.com/kubevirt/kubevirt/releases/download/${RELEASE}/kubevirt-cr.yaml"
```

On managed Kubernetes clusters where control-plane nodes are not schedulable or not visible, remove KubeVirt's default control-plane placement by patching the KubeVirt CR:

```sh
kubectl -n kubevirt patch kubevirt kubevirt --type=merge \
  -p '{"spec":{"infra":{"nodePlacement":{}},"workloads":{"nodePlacement":{}}}}'
```

If `virt-operator` itself is pending because of control-plane affinity, remove the Deployment affinity once:

```sh
kubectl -n kubevirt patch deployment virt-operator --type=json \
  -p='[{"op":"remove","path":"/spec/template/spec/affinity"}]'
```

Wait for KubeVirt:

```sh
kubectl -n kubevirt wait kv kubevirt --for=condition=Available --timeout=600s
kubectl -n kubevirt get pods -o wide
```

For the 2026-06-15 validation cluster, `stable.txt` resolved to `v1.8.3`.

## Create Two VMs On cw-multinet

The demo manifest creates:

- Namespace `kubevirt-cw-test`
- NAD `vm-cw-net`
- VM `cwm-vm-a` pinned to `g46cd14`
- VM `cwm-vm-b` pinned to `g80b396`
- A shared Whereabouts pool `10.254.57.0/24`
- An automatically allocated cw-multinet VNI

Update the two `nodeSelector` hostnames in `vm-cw-multinet-demo.yaml` if your worker node names differ.

The manifest sets fixed guest MAC addresses and cloud-init `networkData` so Fedora requests DHCP on both `eth0` and `eth1`. With KubeVirt bridge binding, the DHCP server gives the guest the IP that Whereabouts assigned to the pod-side Multus attachment.

Apply:

```sh
kubectl apply -f kubevirt/vm-cw-multinet-demo.yaml
kubectl -n kubevirt-cw-test wait vmi cwm-vm-a cwm-vm-b --for=condition=Ready --timeout=600s
kubectl -n kubevirt-cw-test get vm,vmi,pods -o wide
```

Check the Multus/Whereabouts allocation events:

```sh
kubectl -n kubevirt-cw-test get events --sort-by=.lastTimestamp \
  | grep 'AddedInterface'
```

Expected shape on a fresh pool:

```text
Add pod... [10.254.57.16/24] from kubevirt-cw-test/vm-cw-net
Add pod... [10.254.57.17/24] from kubevirt-cw-test/vm-cw-net
```

The manifest writes a boot-time validation report to each VM serial log. The report prints guest NIC addresses and pings the peer VM secondary IP. The baked-in ping targets assume the first two fresh-pool allocations are `.16` and `.17`; if the pool is reused, update the two `runcmd` ping targets to match the actual Whereabouts allocations.

Read the serial logs from the launcher pods:

```sh
POD_A="$(kubectl -n kubevirt-cw-test get pod -l kubevirt.io/domain=cwm-vm-a -o jsonpath='{.items[0].metadata.name}')"
kubectl -n kubevirt-cw-test exec "$POD_A" -c compute -- \
  sh -c 'find /var/run/kubevirt-private -name virt-serial0-log -exec tail -220 {} \;'

POD_B="$(kubectl -n kubevirt-cw-test get pod -l kubevirt.io/domain=cwm-vm-b -o jsonpath='{.items[0].metadata.name}')"
kubectl -n kubevirt-cw-test exec "$POD_B" -c compute -- \
  sh -c 'find /var/run/kubevirt-private -name virt-serial0-log -exec tail -220 {} \;'
```

Expected guest evidence:

```text
eth1: 10.254.57.16 ...
CW-MULTINET-REPORT-START cwm-vm-a
3 packets transmitted, 3 received, 0% packet loss
CW-MULTINET-REPORT-END cwm-vm-a

eth1: 10.254.57.17 ...
CW-MULTINET-REPORT-START cwm-vm-b
3 packets transmitted, 3 received, 0% packet loss
CW-MULTINET-REPORT-END cwm-vm-b
```

The demo password is `fedora` for the `fedora` user. It is intentionally simple for console validation and must not be reused in production manifests.

## Cleanup

Delete only the demo VMs and network:

```sh
kubectl delete namespace kubevirt-cw-test
```

Delete KubeVirt itself:

```sh
kubectl delete kubevirt kubevirt -n kubevirt
kubectl delete -f "https://github.com/kubevirt/kubevirt/releases/download/${RELEASE}/kubevirt-operator.yaml"
```
