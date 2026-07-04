#!/bin/sh
# Air-gap an iceclimber-managed sandbox down to "SSH in, nothing out" (§18.3).
#
# Applied by root at bootstrap time (the sudo-capable lima user runs it; the `agent`
# workload user has no sudo, so it can't flush these rules — the workload-external seam).
#
# Allowed egress is ONLY: loopback, established/related (so the inbound SSH session's
# replies flow), DNS, and DHCP lease renewal. There are NO Anthropic ranges and no other
# public destinations — an iceclimber sandbox's egress rides Popo's reverse-tunnel proxy
# over the (inbound) SSH link (loopback → tunnel), so the guest needs NO direct internet.
# Everything else is dropped, for both IPv4 and IPv6.
#
# INPUT is left ACCEPT so the host-initiated SSH session keeps working.
set -eu

action="${1:-up}"

down() {
	# Restore open egress (used to reset between runs).
	iptables  -P OUTPUT ACCEPT; iptables  -F OUTPUT
	ip6tables -P OUTPUT ACCEPT; ip6tables -F OUTPUT
	echo "iceclimber-airgap: egress restored (open)"
}

up() {
	# IPv4 -----------------------------------------------------------------
	iptables -P INPUT  ACCEPT
	iptables -P FORWARD ACCEPT
	iptables -F OUTPUT
	iptables -A OUTPUT -o lo -j ACCEPT
	iptables -A OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
	iptables -A OUTPUT -p udp --dport 53 -j ACCEPT
	iptables -A OUTPUT -p tcp --dport 53 -j ACCEPT
	iptables -A OUTPUT -p udp --sport 68 --dport 67 -j ACCEPT   # DHCP renewal
	iptables -P OUTPUT DROP                                      # default-drop last

	# IPv6 -----------------------------------------------------------------
	ip6tables -P INPUT  ACCEPT
	ip6tables -P FORWARD ACCEPT
	ip6tables -F OUTPUT
	ip6tables -A OUTPUT -o lo -j ACCEPT
	ip6tables -A OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
	ip6tables -A OUTPUT -p udp --dport 53 -j ACCEPT
	ip6tables -A OUTPUT -p tcp --dport 53 -j ACCEPT
	ip6tables -A OUTPUT -p ipv6-icmp -j ACCEPT                   # ND/RA, keep v6 sane
	ip6tables -P OUTPUT DROP

	echo "iceclimber-airgap: air-gap applied (egress: loopback + established + DNS only)"
}

case "$action" in
	up)   up ;;
	down) down ;;
	*) echo "usage: airgap.sh [up|down]" >&2; exit 2 ;;
esac
