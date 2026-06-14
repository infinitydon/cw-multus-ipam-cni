# Research Notes

## Constraints

CoreWeave Kubernetes Service currently lists Cilium as the supported CNI for new clusters and Multus as the secondary-interface mechanism used for high-throughput workloads. The sample cluster also has Multus and Cilium installed, with a two-node CoreWeave data plane.

The important dataplane constraint is that standard Multus secondary networks such as ipvlan and macvlan normally depend on L2 adjacency or ARP behavior on the secondary network. On managed overlays where the provider does not expose L2 reachability, that model is brittle unless the secondary network creates its own overlay.

## Design Decision

`cw-multinet` intentionally separates address allocation from link creation:

1. Delegate ADD, DEL, and CHECK to the configured IPAM plugin.
2. Ensure a per-network Linux bridge and VXLAN device on the host.
3. Program remote node VTEPs as VXLAN flood destinations.
4. Create a veth pair for each pod, attach the host side to the bridge, and move the pod side into the pod namespace.
5. Assign the IPAM result addresses to the pod interface.
6. Return a normal CNI result so Multus can annotate pod network status.

This creates real N2, N3, N4, N6, S1-MME, and similar virtual NICs. Intra-node traffic is switched by the bridge; inter-node L2 traffic is encapsulated in VXLAN over the existing provider-routable node network.

## IPAM Support

The plugin delegates to any executable IPAM plugin available in `CNI_PATH`. The examples cover:

- `static`
- `host-local`
- `whereabouts`

Whereabouts is the recommended dynamic allocator when addresses must be unique across nodes. `host-local` is only node-local, so each node can allocate the same address unless ranges are partitioned per node.

## Reachability Boundary

The plugin creates an L2 overlay, but it still needs a way to know remote VTEP addresses. The current implementation accepts an explicit `peers` list in each NAD. A production follow-up should add a small node watcher or generated ConfigMap workflow so peer membership updates automatically as nodes scale.

All nodes must be able to send UDP VXLAN traffic to each peer on `vxlanPort`, default `4789`.
