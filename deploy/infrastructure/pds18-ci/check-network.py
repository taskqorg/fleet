#!/usr/bin/env python3
"""Run inside the guest before registering the runner; no browser or credentials."""
import socket
import urllib.request

# Check QEMU's host alias as well as direct host/private/metadata destinations.
for host, port in [
    ("10.0.2.2", 22), ("155.103.4.2", 22), ("155.103.4.1", 22),
    ("127.0.0.1", 22222), ("169.254.169.254", 80),
    ("192.168.42.1", 22), ("23.239.109.234", 22),
]:
    try:
        connection = socket.create_connection((host, port), timeout=2)
    except OSError:
        continue
    connection.close()
    raise SystemExit(f"Unexpected reachable protected endpoint: {host}:{port}")

with urllib.request.urlopen("https://api.github.com/repos/taskqorg/fleet", timeout=15) as response:
    assert response.status == 200
print("Protected connection probes rejected; public GitHub DNS/TLS accepted.")
