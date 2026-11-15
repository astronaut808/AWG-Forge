# Transparent UDP relay

The client connects to the relay endpoint. The relay forwards encrypted datagrams to an existing tunnel on the exit server. The tunnel decrypts the traffic and uses its configured egress, either direct or through WARP. One relay can serve multiple clients on the same tunnel.

The first release supports Ubuntu 24.04 LTS x86_64 as the relay host. Installation, removal, and the UDP relay smoke test were verified on Ubuntu 24.04.5 LTS x86_64. CentOS Stream and RHEL relay hosts are outside the initial support scope. The exit server keeps the existing AWG-Forge platform support.

## Setup

1. In the panel, open **Tunnels**, select a tunnel, and open **UDP relays**. Choose **Add relay** and enter its public address. The panel fills in the tunnel port and configured endpoint. You can enter a different DNS name or IPv4 address reachable from the relay. AWG-Forge uses the tunnel's listen port as the destination port. Leave the name blank to use the public address.
2. Copy the install command and run it on the relay host. It downloads the installer for a specific release and runs `install.sh relay`. The installer offers to install Docker Engine, Compose v2 and missing host tools. It reuses a working Docker installation. Enable `net.ipv4.ip_forward` on the relay host. Allow inbound UDP on the relay port in your provider firewall and allow the relay to reach the tunnel's UDP port.
3. When creating or editing a client, choose a relay in **Connect via**. **Direct** uses the tunnel endpoint. Export the configuration and import it on the device. After changing the relay address or port, distribute updated configurations to affected clients.

Use an IPv4 DNS record for the relay hostname. The installer takes the local address and interface from the route to the exit server. Inbound UDP must reach that address and interface. For a fixed IPv4 destination, run the installer again if the route changes. For a DNS destination, the relay resolves IPv4 locally at startup and refreshes it every minute. It keeps the current address while it remains in the DNS answer, and retains the current rules if resolution temporarily fails.

## Operations

```bash
sudo bash ./awg-forge-install.sh relay status --id 0123456789abcdef
sudo bash ./awg-forge-install.sh relay remove --id 0123456789abcdef
```

The container starts with Docker and restores its own rules. The status command checks the container and host rules. The panel checks the public endpoint at startup and repeats the check about once a minute. The exit server initiates an AWG handshake through the relay using a temporary peer without tunnel routes. The panel shows the result and the time of the last successful check. This verifies the round trip between the servers. To verify a particular client network or Internet egress, connect from that network and test traffic.

After changing a relay, run the updated install command on the relay host. A destination address change requires reinstalling the relay even when client configs stay current. To remove a relay, reassign its clients, run `relay remove` on the relay host, and delete the relay in the panel. Remove the tunnel's relays before changing its listen port or deleting the tunnel.

The relay uses a separate Compose project. The container manages its own iptables-nft rules, and the Linux kernel forwards packets. Allocate a free UDP port. Check that existing host firewall rules and any third-party firewall chains allow the traffic.

Keep an encrypted v3 backup from before the upgrade if you may need to roll back to v0.20.0. v0.20.0 rejects v4 backups even after you remove relay assignments. Restoring the v3 backup returns the exit server to its saved configuration. Reimport direct client configurations and remove the relay from its host if it is no longer needed. See [backup and restore](diagnostics.md#encrypted-backup--restore).

If installation is interrupted, rerun the install command. The installer checks the running container and either keeps the new configuration or restores the previous one. Relay forwarding rules are appended to `FORWARD`, so existing firewall rules take precedence.

The relay records its active IPv4 addresses and UDP ports on the host before installing DNAT. Removal uses that record to clear matching conntrack entries even after a firewall reload. For legacy installations without this record, cleanup can recover the tuple from surviving managed rules.
