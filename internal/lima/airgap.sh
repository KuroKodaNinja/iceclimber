#!/bin/sh
# Air-gap an iceclimber-managed sandbox down to "SSH in, nothing out" (§18.3).
#
# Applied by root at bootstrap time (the sudo-capable lima user runs it; the `agent`
# workload user has no sudo, so it can't flush these rules — the workload-external seam).
#
# Allowed egress: loopback, established/related (so the inbound SSH session's replies flow),
# DNS, DHCP renewal, and 443 to the agent's LLM API (Anthropic's published, stable ingress
# ranges). The API allowance is load-bearing: a Claude agent running IN the box reaches its
# model DIRECTLY (its streaming/TLS control channel must not be MITM'd) — that's the ONE thing
# Popo does NOT bridge. Everything else the agent needs (packages, web, runtimes) has no direct
# route and rides Popo's reverse-tunnel proxy over the (inbound) SSH link (loopback → tunnel).
# Everything else is dropped, for both IPv4 and IPv6.
#
# INPUT is left ACCEPT so the host-initiated SSH session keeps working.
set -eu

# Anthropic API ingress ranges (platform.claude.com/docs/en/api/ip-addresses — "will not change
# without notice"). This is Claude-specific for v1; a different agent would need its API's ranges.
ANTHROPIC_V4="160.79.104.0/23"
ANTHROPIC_V6="2607:6bc0::/48"

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
	iptables -A OUTPUT -p tcp -d "$ANTHROPIC_V4" --dport 443 -j ACCEPT  # the agent's LLM API
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
	ip6tables -A OUTPUT -p tcp -d "$ANTHROPIC_V6" --dport 443 -j ACCEPT  # the agent's LLM API
	ip6tables -P OUTPUT DROP

	echo "iceclimber-airgap: air-gap applied (egress: DNS + 443 to the agent's LLM API only)"
}

case "$action" in
	up)   up ;;
	down) down ;;
	*) echo "usage: airgap.sh [up|down]" >&2; exit 2 ;;
esac
