# Research Notes

## Constraints

CoreWeave Kubernetes Service currently lists Cilium as the supported CNI for new clusters and Multus as the secondary-interface mechanism used for high-throughput workloads. The sample cluster also has Multus and Cilium installed, with a two-node CoreWeave data plane.

The important dataplane constraint is that standard Multus secondary networks such as ipvlan and macvlan normally depend on L2 adjacency or ARP behavior on the secondary network. On managed overlays where the provider does not expose L2 reachability, that model is brittle.

## Design Decision

`cw-multinet` intentionally separates address allocation from link creation:

1. Delegate ADD, DEL, and CHECK to the configured IPAM plugin.
2. Create a pod-local secondary interface.
3. Assign the IPAM result addresses to that interface.
4. Return a normal CNI result so Multus can annotate pod network status.

The default link type is `dummy`. This creates bindable N2, N3, N4, N6, S1-MME, and similar interfaces without requiring ARP, a VLAN, a secondary NIC, or changes to CoreWeave's managed Cilium installation.

## IPAM Support

The plugin delegates to any executable IPAM plugin available in `CNI_PATH`. The examples cover:

- `static`
- `host-local`
- `whereabouts`

Whereabouts is the recommended dynamic allocator when addresses must be unique across nodes. `host-local` is only node-local, so each node can allocate the same address unless ranges are partitioned per node.

## Reachability Boundary

The default `dummy` mode is correct for applications that need stable interface names and IPs to bind sockets, advertise configured addresses, or satisfy NF configuration requirements.

It does not create cross-node L2 or cross-node secondary-IP routing by itself. True secondary-IP-to-secondary-IP connectivity over a provider overlay needs an additional routed/tunneled node agent that discovers pod secondary IPs and encapsulates traffic over node or primary-pod reachability.

`interfaceType: "veth"` is included as a foundation for that next step: it places one side of the link in the pod and one side on the host, with host /32 routes back to the pod secondary IPs.
