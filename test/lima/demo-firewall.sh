#!/bin/sh
# Air-gap the demo sandbox down to *only* the Claude API.
#
# Run inside the demo VM as root (the demo harness does:
#   limactl shell iceclimber-demo -- sudo sh < test/lima/demo-firewall.sh [up|down]
#
# This is what makes the demo meaningful: with it applied, the agent inside the
# VM cannot reach PyPI, GitHub, or any data URL directly — it MUST bridge through
# Popo over the (inbound) SSH link — yet Claude Code itself stays reachable.
#
# Allowed egress: loopback, established/related (so Popo's inbound SSH replies
# flow), DNS, DHCP renewal, and 443 to Anthropic's published, stable ingress
# ranges. Everything else is dropped, for both IPv4 and IPv6.
#
#   Anthropic inbound ranges (platform.claude.com/docs/en/api/ip-addresses,
#   "will not change without notice"):
#     IPv4  160.79.104.0/23
#     IPv6  2607:6bc0::/48
#
# INPUT is left ACCEPT so the host-initiated SSH session keeps working.
set -eu

ANTHROPIC_V4="160.79.104.0/23"
ANTHROPIC_V6="2607:6bc0::/48"

action="${1:-up}"

down() {
	# Restore open egress (used to reset between runs).
	iptables  -P OUTPUT ACCEPT; iptables  -F OUTPUT
	ip6tables -P OUTPUT ACCEPT; ip6tables -F OUTPUT
	echo "demo-firewall: egress restored (open)"
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
	iptables -A OUTPUT -p tcp -d "$ANTHROPIC_V4" --dport 443 -j ACCEPT
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
	ip6tables -A OUTPUT -p tcp -d "$ANTHROPIC_V6" --dport 443 -j ACCEPT
	ip6tables -P OUTPUT DROP

	echo "demo-firewall: air-gap applied (egress: DNS + 443 to Anthropic only)"
}

case "$action" in
	up)   up ;;
	down) down ;;
	*) echo "usage: demo-firewall.sh [up|down]" >&2; exit 2 ;;
esac
