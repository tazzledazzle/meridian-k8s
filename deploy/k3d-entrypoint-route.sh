#!/bin/sh
# Mounted into the k3s server container as /bin/k3d-entrypoint-route.sh (k3d runs every /bin/k3d-entrypoint-*.sh).
# An internal Docker network gives the container no gateway, and k3s exits with "no default routes found" without one.
# A default route through the interface alone (no next hop) satisfies k3s. It does not open a path out: the network
# is internal, so packets to any outside address still go nowhere (the gate test asserts this).
ip route show default | grep -q . || ip route add default dev eth0
