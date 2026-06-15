# KubeVirt With cw-multinet

This folder contains a minimal KubeVirt validation setup for running two VMs with a cw-multinet secondary interface.

References:

- KubeVirt installation guide: https://kubevirt.io/user-guide/cluster_admin/installation/
- KubeVirt release notes: https://kubevirt.io/user-guide/release_notes/
- KubeVirt interfaces and networks: https://kubevirt.io/user-guide/network/interfaces_and_networks/
- KubeVirt cloud-init startup scripts and networkData: https://kubevirt.io/user-guide/user_workloads/startup_scripts/

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

For the 2026-06-15 validation cluster, `stable.txt` resolved to `v1.8.3`. That release line is aligned to Kubernetes `v1.35`; the test cluster was Kubernetes `v1.36.1`, so treat this as a forward-compatibility smoke test until KubeVirt publishes a release line aligned to Kubernetes `v1.36`.

## Create Two VMs On cw-multinet

The demo manifest creates:

- Namespace `kubevirt-cw-test`
- NAD `vm-cw-net`
- VM `cwm-vm-a` pinned to `g46cd14`
- VM `cwm-vm-b` pinned to `g80b396`

Update the two `nodeSelector` hostnames in `vm-cw-multinet-demo.yaml` if your worker node names differ.

Apply:

```sh
kubectl apply -f kubevirt/vm-cw-multinet-demo.yaml
kubectl -n kubevirt-cw-test wait vmi cwm-vm-a cwm-vm-b --for=condition=Ready --timeout=300s
kubectl -n kubevirt-cw-test get vm,vmi,pods -o wide
```

Check the cw-multinet-assigned guest-facing IPs:

```sh
kubectl -n kubevirt-cw-test get vmi cwm-vm-a cwm-vm-b -o json \
  | jq -r '.items[] | .metadata.name as $n | .status.interfaces[]? |
    [$n,.name,.ipAddress,((.ipAddresses//[])|join(",")),.mac] | @tsv'
```

Expected shape:

```text
cwm-vm-a  default  10.8.x.x       10.8.x.x       <mac>
cwm-vm-a  cwmnet   10.254.50.16   10.254.50.16   <mac>
cwm-vm-b  default  10.8.x.x       10.8.x.x       <mac>
cwm-vm-b  cwmnet   10.254.50.17   10.254.50.17   <mac>
```

The KubeVirt bridge binding attaches the Multus/cw-multinet interface to the guest. The demo uses CirrOS because it boots quickly. In the live test, CirrOS exposed `eth1` but did not automatically apply the cloud-init `networkData` for that secondary interface, so the test configured the guest IP manually from the console.

Download matching `virtctl`:

```sh
export RELEASE="$(curl -fsSL https://storage.googleapis.com/kubevirt-prow/release/kubevirt/kubevirt/stable.txt)"
curl -fL -o /tmp/virtctl "https://github.com/kubevirt/kubevirt/releases/download/${RELEASE}/virtctl-${RELEASE}-linux-amd64"
chmod +x /tmp/virtctl
```

Console into each VM:

```sh
/tmp/virtctl -n kubevirt-cw-test console cwm-vm-a
/tmp/virtctl -n kubevirt-cw-test console cwm-vm-b
```

CirrOS login:

```text
username: cirros
password: gocubsgo
```

Configure the secondary interface inside the guests using the IPs shown in VMI status:

```sh
# cwm-vm-a
sudo ifconfig eth1 10.254.50.16 netmask 255.255.255.0 up

# cwm-vm-b
sudo ifconfig eth1 10.254.50.17 netmask 255.255.255.0 up
```

Test from VM A:

```sh
ping -c 5 -W 2 10.254.50.17
```

Test from VM B:

```sh
ping -c 5 -W 2 10.254.50.16
```

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

