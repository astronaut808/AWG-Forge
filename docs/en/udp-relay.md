# Transparent UDP relay

The client connects to the relay endpoint. The relay forwards encrypted datagrams to an existing tunnel on the exit server. The tunnel decrypts the traffic and uses its configured egress, either direct or through WARP. One relay can serve multiple clients on the same tunnel.

The first release supports Ubuntu 24.04 LTS and 26.04 LTS on x86_64 relay hosts. CI runs the UDP relay smoke test on both versions. Installation and removal were also verified in a VM running Ubuntu 24.04.5 LTS x86_64. Support for relay hosts on CentOS Stream and RHEL is deferred. The exit server keeps the existing AWG-Forge platform support.

## Setup

1. In the panel, open **Tunnels**, select a tunnel, and open **UDP relays**. Choose **Add relay** and enter its public address. The panel fills in the tunnel port and configured endpoint. You can enter a different DNS name or IPv4 address reachable from the relay. AWG-Forge uses the tunnel's listen port as the destination port. Leave the name blank to use the public address.
2. Copy the install command and run it on the relay host. It downloads `install.sh` from `master` and selects the relay image for the panel's release with `--version`. The installer offers to install Docker Engine, Compose v2 and missing host tools. It reuses a working Docker installation. Enable `net.ipv4.ip_forward` on the relay host. Allow inbound UDP on the relay port in your provider firewall and allow the relay to reach the tunnel's UDP port.
3. When creating or editing a client, choose a relay in **Connect via**. **Direct** uses the tunnel endpoint. Export the configuration and import it on the device. After changing the relay address or port, distribute updated configurations to affected clients.

Use an IPv4 DNS record for the relay hostname. The installer takes the local address and interface from the route to the exit server. Inbound UDP must reach that address and interface. For a fixed IPv4 destination, run the installer again if the route changes. For a DNS destination, the relay resolves IPv4 locally at startup and refreshes it every minute. It keeps the current address while it remains in the DNS answer, and retains the current rules if resolution temporarily fails.

## Operations

The install command from the panel downloads `install.sh` to the current directory on the relay host as `awg-forge-install.sh`. Reuse that file, or download the script from `master` to run maintenance commands.

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/install.sh -o awg-forge-install.sh
```

To find the relay ID in the panel, open **Tunnels**, select the tunnel, and go to **UDP relays**. Click **Installation** next to the relay. Copy the value after `--id` from **Install command** or **Removal command** and use it in place of `0123456789abcdef` below.

```bash
sudo bash ./awg-forge-install.sh relay status --id 0123456789abcdef
```

To remove the relay after reassigning its clients, run the following command on the relay host.

```bash
sudo bash ./awg-forge-install.sh relay remove --id 0123456789abcdef
```

The container starts with Docker and restores its own rules. The status command checks the container and host rules. The panel checks the public endpoint at startup and repeats the check about once a minute. The exit server initiates an AWG handshake through the relay using a temporary peer without tunnel routes. The panel shows the result and the time of the last successful check. This verifies the round trip between the servers. To verify a particular client network or Internet egress, connect from that network and test traffic.

If the panel shows no response, generate traffic through the relay and run the following command while the UDP flows are active. It checks the local setup and looks for an unanswered forwarded flow and a separate unanswered flow from a different source IP to the same translated UDP port in the same conntrack zone. This indicates a possible mismatch. The panel handshake and a client traffic test are the checks for recovery. The command requires v0.21.2 or later on the relay host.

```bash
sudo bash ./awg-forge-install.sh relay diagnose --id 0123456789abcdef
```

For an exit with multiple IPv4 addresses, upgrade the **exit server** to v0.21.2 or later. Its bundled userspace runtime retains the receiving address when Linux UDP GRO is enabled, so it can reply from that address. As a temporary recovery or after rolling the exit back to v0.21.1, you can set the relay destination to the exit's primary IPv4 if the tunnel listens there. Run the updated install command and verify an AWG handshake and client traffic in both directions.

After changing a relay, run the updated install command on the relay host. A destination address change requires reinstalling the relay even when client configs stay current. To remove a relay, reassign its clients, run `relay remove` on the relay host, and delete the relay in the panel. Remove the tunnel's relays before changing its listen port or deleting the tunnel.

The relay uses a separate Compose project. The container manages its own iptables-nft rules, and the Linux kernel forwards packets. Allocate a free UDP port. Check that existing host firewall rules and any third-party firewall chains allow the traffic.

For a manual rollback to v0.20.0, stop the exit server and save a separate copy of its v4 data directory. Restore the v3 backup made before upgrading, then start v0.20.0. Starting v0.20.0 on the v4 data directory can discard relay settings and client assignments. You can also install v0.20.0 with a fresh data directory and recreate the tunnels and clients. Reimport direct client configurations and remove the relay from its host if it is no longer needed. See [backup and restore](diagnostics.md#encrypted-backup--restore).

If installation is interrupted, rerun the install command. The installer checks the running container and either keeps the new configuration or restores the previous one. Relay forwarding rules are appended to `FORWARD`, so existing firewall rules take precedence.

The relay records its active IPv4 addresses and UDP ports on the host before installing DNAT. Removal uses that record to clear matching conntrack entries even after a firewall reload. For legacy installations without this record, cleanup can recover the tuple from surviving managed rules.
