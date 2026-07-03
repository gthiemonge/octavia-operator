# Using Octavia with a Third-Party SDN

This document explains how to deploy Octavia when using a third-party SDN
(Software Defined Networking) solution instead of OVN. In this scenario, the
operator cannot create the Neutron management network resources automatically
because it assumes OVN-specific constructs (provider networks with flat segments
mapped to a physical bridge). Instead, you must pre-create the Neutron resources
and configure the operator to discover them.

## Prerequisites

Before proceeding, ensure you have:

- A working OpenStack deployment with your third-party SDN.
- Neutron configured and operational with your SDN backend.
- The `octavia` service tenant (or whichever tenant Octavia is configured to
  use).
- Access to `openstack` CLI commands with admin privileges.
- Familiarity with the management network architecture described in
  [MANAGEMENT_NETWORK.md](MANAGEMENT_NETWORK.md).

## Overview

The Octavia management network provides connectivity between the amphora
controller pods running in the OpenShift control plane and the amphora load
balancer VMs running in the compute plane. The architecture requires:

1. A **tenant network** (`lb-mgmt-net`) where amphora VMs are attached.
2. A **mechanism** for controller pods to reach the tenant network (replaces the
   provider network + router used with OVN).
3. **Security groups** to control traffic to/from amphorae and controllers.
4. **Routes in the controller pods** so they can reach amphora VMs.
5. **Predictable IPs** on the pod-side network for health manager heartbeats.

With a third-party SDN, the provider network approach may not apply. You need to
establish equivalent connectivity using whatever mechanism your SDN supports
(e.g. VXLAN tunnels, BGP peering, direct L2 extension, etc.).

## Step 1: Plan Your IP Addressing

You need two IP ranges that can be routed to each other:

| Network | Purpose | Example CIDR |
|---|---|---|
| Pod-side network | Controller pods communicate on this network via the NAD | `172.23.0.0/24` |
| Tenant network | Amphora VMs are attached to this network | `172.24.0.0/16` |

The pod-side network CIDR is shared between multiple consumers. Plan the
allocation carefully:

```
Pod IPs (whereabouts):       172.23.0.30  - 172.23.0.70   (NAD range)
Neutron allocation pool:     172.23.0.71  - 172.23.0.96   (25 IPs, reserved by operator)
Predictable IPs:             172.23.0.97  - 172.23.0.122  (25 IPs, reserved by operator)
Router gateway:              172.23.0.150                  (if using a Neutron router)
```

> **Important**: The operator always reserves 50 IPs on the pod-side network
> starting after `range_end`: 25 for Neutron and 25 for predictable IPs. Your
> pod-side CIDR must be large enough to accommodate all three ranges plus the
> router gateway.

## Step 2: Create the Network Attachment Definition

Even with a third-party SDN, the operator reads the NAD to determine subnet
CIDRs and the gateway IP. The NAD also provides pod-side connectivity.

How you achieve L2 connectivity to the bridge depends on your SDN. The NAD
itself can use any CNI plugin that provides the interface to your pods. The
example below uses a Linux bridge, but adapt it to your environment:

```yaml
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  labels:
    osp/net: octavia
  name: octavia
  namespace: openstack
spec:
  config: |
    {
      "cniVersion": "0.3.1",
      "name": "octavia",
      "type": "bridge",
      "bridge": "octbr",
      "ipam": {
        "type": "whereabouts",
        "range": "172.23.0.0/24",
        "range_start": "172.23.0.30",
        "range_end": "172.23.0.70",
        "routes": [
           {
             "dst": "172.24.0.0/16",
             "gw" : "172.23.0.150"
           }
         ]
      }
    }
```

The fields the operator reads from this NAD:

| Field | Used For |
|---|---|
| `range` | Provider subnet CIDR (and base for IP pool calculations) |
| `range_end` | Start of Neutron + predictable IP pools (range_end + 1) |
| `routes[0].dst` | Tenant subnet CIDR |
| `routes[0].gw` | Router gateway IP (used as `MGMT_GATEWAY` env var in pods) |

> **If your SDN does not use a Neutron router**: You still need the `routes`
> entry in the NAD because the operator uses `dst` to determine the tenant
> subnet CIDR and `gw` to configure the pod routes. Set `gw` to the IP address
> of whatever device routes traffic between the pod network and the tenant
> network in your SDN (e.g. a gateway appliance, tunnel endpoint, etc.).
> Alternatively, omit the `routes` from the NAD and set
> `spec.lbMgmtNetwork.lbMgmtRouterGateway` in the Octavia CR to this gateway IP.

### How the NAD drives pod environment variables

Understanding the data flow is important for troubleshooting. The `MGMT_CIDR`,
`MGMT_GATEWAY`, and `MGMT_CIDRn` environment variables injected into amphora
controller pods are **not** read directly from the NAD. They go through a
multi-step pipeline:

1. **NAD parsing**: The operator reads the NAD and extracts network parameters
   (see table above). This always happens, regardless of `manageLbMgmtNetworks`.

2. **Neutron resource lookup/creation**: Depending on the mode:
   - `manageLbMgmtNetworks: true` (default): The operator creates the Neutron
     resources and returns a `NetworkProvisioningSummary` containing the tenant
     subnet CIDR, the router gateway IP, and any extra AZ CIDRs.
   - `manageLbMgmtNetworks: false`: The operator queries Neutron for existing
     resources by name. The gateway IP (`ManagementSubnetGateway`) is read from
     the `octavia-link-router`'s first `external_fixed_ips` entry. The tenant
     CIDR (`ManagementSubnetCIDR`) is read from the `lb-mgmt-subnet`'s CIDR.
     Extra AZ CIDRs are read from `lb-mgmt-<az>-subnet` subnets.

3. **Daemonset spec**: The values from the `NetworkProvisioningSummary` are
   written into the `OctaviaAmphoraController` spec fields:
   - `OctaviaProviderSubnetCIDR` <- `ManagementSubnetCIDR`
   - `OctaviaProviderSubnetGateway` <- `ManagementSubnetGateway`
   - `OctaviaProviderSubnetExtraCIDRs` <- `ManagementSubnetExtraCIDRs`

4. **Pod environment variables**: The daemonset template maps these spec fields
   to container environment variables:
   - `MGMT_CIDR` <- `OctaviaProviderSubnetCIDR` (the tenant subnet CIDR,
     e.g. `172.24.0.0/16`)
   - `MGMT_GATEWAY` <- `OctaviaProviderSubnetGateway` (the router gateway IP
     on the provider network, e.g. `172.23.0.150`)
   - `MGMT_CIDR0`, `MGMT_CIDR1`, ... <- `OctaviaProviderSubnetExtraCIDRs`
     (sorted alphabetically for stability)

5. **Init container**: At pod startup, the init script uses these environment
   variables to add routes via a Python script (`octavia_mgmt_subnet_route.py`):
   ```
   ip route add 172.24.0.0/16 via 172.23.0.150 dev octavia
   ```

The key takeaway for third-party SDN users: **in unmanaged mode
(`manageLbMgmtNetworks: false`), `MGMT_CIDR` and `MGMT_GATEWAY` are sourced
exclusively from Neutron**, not from the NAD:

- `MGMT_CIDR` comes from the `lb-mgmt-subnet` CIDR in Neutron. If this subnet
  does not exist, `MGMT_CIDR` is empty and **no routes are added** (the init
  script skips route setup when `MGMT_CIDR` is empty).
- `MGMT_GATEWAY` comes from the `octavia-link-router`'s first
  `external_fixed_ips` entry. If no router named `octavia-link-router` exists,
  `MGMT_GATEWAY` is empty and the route addition will fail even if `MGMT_CIDR`
  is set.

This means that in unmanaged mode, you **must** create both the `lb-mgmt-subnet`
and the `octavia-link-router` with a valid external gateway IP for pod routing to
work. The NAD `routes[0].gw` value is only used as a gateway source in managed
mode (`manageLbMgmtNetworks: true`).

## Step 3: Create the Neutron Management Network Resources

Set `manageLbMgmtNetworks: false` in the Octavia CR so the operator discovers
pre-existing resources instead of trying to create them. The operator looks up
resources by the exact names listed below, in the service tenant.

### 3.1: Create the Tenant Network and Subnet

```sh
# Get the service tenant ID
SERVICE_PROJECT_ID=$(openstack project show service -f value -c id)

# Create the tenant network
openstack network create \
  --project $SERVICE_PROJECT_ID \
  lb-mgmt-net

# Create the tenant subnet
# The CIDR must match the "dst" field from the NAD routes
openstack subnet create \
  --project $SERVICE_PROJECT_ID \
  --network lb-mgmt-net \
  --subnet-range 172.24.0.0/16 \
  --allocation-pool start=172.24.0.5,end=172.24.255.254 \
  --no-gateway \
  lb-mgmt-subnet
```

> **Naming is critical**: The operator queries Neutron for networks and subnets
> by name and tenant ID. The names **must** be exactly `lb-mgmt-net` and
> `lb-mgmt-subnet`.

### 3.2: Create Security Groups

The operator looks up the security group `lb-mgmt-sec-grp` in the service
tenant to get its ID for `amp_secgroup_list` in the Octavia configuration.

```sh
# Management security group (applied to amphorae)
openstack security group create \
  --project $SERVICE_PROJECT_ID \
  lb-mgmt-sec-grp

# SSH access (controller -> amphora)
openstack security group rule create --protocol tcp --dst-port 22 \
  --project $SERVICE_PROJECT_ID lb-mgmt-sec-grp
openstack security group rule create --protocol tcp --dst-port 22 \
  --ethertype IPv6 --project $SERVICE_PROJECT_ID lb-mgmt-sec-grp

# Amphora agent (controller -> amphora, TLS on port 9443)
openstack security group rule create --protocol tcp --dst-port 9443 \
  --project $SERVICE_PROJECT_ID lb-mgmt-sec-grp
openstack security group rule create --protocol tcp --dst-port 9443 \
  --ethertype IPv6 --project $SERVICE_PROJECT_ID lb-mgmt-sec-grp

# Health manager security group (applied to health manager ports)
openstack security group create \
  --project $SERVICE_PROJECT_ID \
  lb-health-mgr-sec-grp

# Heartbeat (amphora -> health manager, UDP 5555)
openstack security group rule create --protocol udp --dst-port 5555 \
  --project $SERVICE_PROJECT_ID lb-health-mgr-sec-grp
openstack security group rule create --protocol udp --dst-port 5555 \
  --ethertype IPv6 --project $SERVICE_PROJECT_ID lb-health-mgr-sec-grp

# Log offloading (amphora -> rsyslog, UDP/TCP 514)
openstack security group rule create --protocol udp --dst-port 514 \
  --project $SERVICE_PROJECT_ID lb-health-mgr-sec-grp
openstack security group rule create --protocol udp --dst-port 514 \
  --ethertype IPv6 --project $SERVICE_PROJECT_ID lb-health-mgr-sec-grp
openstack security group rule create --protocol tcp --dst-port 514 \
  --project $SERVICE_PROJECT_ID lb-health-mgr-sec-grp
openstack security group rule create --protocol tcp --dst-port 514 \
  --ethertype IPv6 --project $SERVICE_PROJECT_ID lb-health-mgr-sec-grp
```

### 3.3: Establish Routing Between Pod and Tenant Networks

This is where your third-party SDN comes in. You must ensure bidirectional IP
connectivity between the pod-side network (`172.23.0.0/24`) and the tenant
network (`172.24.0.0/16`).

**What the operator does with OVN (for reference):**

1. Creates a Neutron provider network (`octavia-provider-net`) mapped to the
   `octbr` bridge.
2. Creates a Neutron router (`octavia-link-router`) connecting the provider
   network to the tenant network, with SNAT disabled.
3. Sets host routes on `lb-mgmt-subnet` so amphorae route to the provider
   network via the router.

**What you need to replicate with your SDN:**

The exact mechanism depends on your SDN, but the requirements are:

- **Pod -> Amphora**: Pods on `172.23.0.0/24` must be able to reach VMs on
  `172.24.0.0/16`. The operator configures a route inside each pod:
  `172.24.0.0/16 via <gateway>`, where `<gateway>` is the `gw` value from the
  NAD. Your SDN must ensure that this gateway actually forwards traffic to the
  tenant network.

- **Amphora -> Pod**: Amphora VMs on `172.24.0.0/16` must be able to reach the
  controller pods on `172.23.0.0/24`. This typically requires a host route on
  the tenant subnet or a default route in the amphora VMs. If using a Neutron
  router, set a host route:

  ```sh
  openstack subnet set \
    --host-route destination=172.23.0.0/24,gateway=<tenant-side-router-ip> \
    lb-mgmt-subnet
  ```

- **No SNAT**: The routing must preserve source IP addresses. The amphora
  controllers communicate with amphorae using their tenant network IPs directly,
  and the health managers must receive heartbeats with the amphora's real source
  IP.

**Common approaches with third-party SDNs:**

| Approach | Description |
|---|---|
| SDN-managed routing | Configure your SDN to route between the pod network and the tenant network natively (e.g. via BGP, VXLAN, etc.) |
| Neutron router (if supported) | Create a Neutron router similar to the OVN approach, connecting a provider-like network to lb-mgmt-net |
| L2 bridging | Extend the tenant network directly to the pods (eliminates the need for routing but may have scalability implications) |
| Gateway appliance | Use a dedicated VM or appliance that bridges both networks |

Regardless of the approach, the `gw` field in the NAD must point to a reachable
next-hop that forwards traffic to the tenant network.

### 3.4: (Optional) Create a Neutron Router

If your SDN supports Neutron routers and you want the operator to discover the
gateway IP from the router (used in unmanaged mode), create one:

```sh
openstack router create octavia-link-router

# Set the external gateway (adapt to your SDN's provider network)
openstack router set --external-gateway <your-provider-network> \
  --fixed-ip ip-address=172.23.0.150 \
  --disable-snat \
  octavia-link-router

# Add the tenant subnet interface
openstack router add subnet octavia-link-router lb-mgmt-subnet
```

The operator looks for a router named `octavia-link-router` and reads the
gateway IP from `external_gateway_info.external_fixed_ips[0].ip_address`.

If you do **not** create a router, the operator falls back to using the `gw`
value from the NAD routes as the gateway IP. This is fine as long as your SDN
handles the actual routing.

## Step 4: Configure the Octavia CR

```yaml
apiVersion: core.openstack.org/v1beta1
kind: OpenStackControlPlane
metadata:
  name: controlplane
  namespace: openstack
spec:
  octavia:
    enabled: true
    template:
      lbMgmtNetwork:
        manageLbMgmtNetworks: false
      octaviaHousekeeping:
        networkAttachments:
          - octavia
      octaviaHealthManager:
        networkAttachments:
          - octavia
      octaviaWorker:
        networkAttachments:
          - octavia
```

Key setting: `manageLbMgmtNetworks: false` tells the operator to look up
existing Neutron resources by name instead of creating them.

## Summary of Required Resources

| Resource | Name | Tenant | Required |
|---|---|---|---|
| Network | `lb-mgmt-net` | service | Yes |
| Subnet | `lb-mgmt-subnet` | service | Yes |
| Security Group | `lb-mgmt-sec-grp` | service | Yes |
| Security Group | `lb-health-mgr-sec-grp` | service | Recommended |
| Router | `octavia-link-router` | any | Optional (if NAD has routes) |
| NAD | `octavia` | openstack (k8s) | Yes |
| Routing | Pod network <-> Tenant network | N/A | Yes (SDN-specific) |
