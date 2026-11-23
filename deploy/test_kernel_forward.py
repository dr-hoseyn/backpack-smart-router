import runpy
import unittest
from pathlib import Path
from unittest.mock import patch


forward = runpy.run_path(str(Path(__file__).with_name("kernel-forward.py")))
eligible = forward["healthy_backends"]


class KernelHealthTest(unittest.TestCase):
    def test_open_tcp_does_not_override_a_failed_tls_probe(self):
        config = {
            "backends": ["10.0.0.11", "10.0.0.12"], "backend_port": 8443,
            "control_group": "kernel-tcp", "tls_server_name": "tunnel.example.net",
        }
        reports = [
            {"tcp": f"{address}:8443", "group": "kernel-tcp", "state": "healthy", "enabled": True, "drain": False}
            for address in config["backends"]
        ]
        probes = []
        tls_available = {"10.0.0.12"}

        def probe(_, hostname, port, address):
            probes.append((hostname, port, address))
            return address in tls_available

        with patch.dict(eligible.__globals__, proxy_status=lambda _: {"backends": reports}, tls_reachable=probe):
            missed = {}
            self.assertEqual(list(eligible(config, object(), missed)), ["10.0.0.12"])
            self.assertEqual(list(eligible(config)), config["backends"])
            tls_available.clear()
            self.assertEqual(list(eligible(config, object(), missed)), ["10.0.0.12"])
            self.assertEqual(list(eligible(config, object(), missed)), [])
        self.assertCountEqual(probes, [(config["tls_server_name"], 8443, ip) for ip in config["backends"]] * 3)
