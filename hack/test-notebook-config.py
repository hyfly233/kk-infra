"""Local contract tests for the actual ConfigMap Python; no Hub/K8s required."""
import asyncio
import hashlib
import os
from pathlib import Path
import sys
from types import ModuleType, SimpleNamespace
import unittest


class Config(SimpleNamespace):
    def __getattr__(self, name):
        child = Config()
        setattr(self, name, child)
        return child


class Response:
    def __init__(self, status, data):
        self.status_code, self.data = status, data

    def raise_for_status(self):
        if self.status_code >= 400:
            raise ValueError("HTTP failure")

    def json(self):
        return self.data


class NotebookConfigTest(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        text = (Path(__file__).resolve().parents[1] / "deployments/k8s/20-jupyterhub.yaml").read_text()
        block = text.split("  jupyterhub_config.py: |\n", 1)[1].split("\n---", 1)[0]
        source = "\n".join(line[4:] for line in block.splitlines())
        auth = ModuleType("jupyterhub.auth")
        auth.Authenticator = object
        sys.modules["jupyterhub.auth"] = auth
        requests = ModuleType("requests")
        requests.RequestException = RuntimeError
        sys.modules["requests"] = requests
        os.environ.update(PLATFORM_API_TOKEN="hub-secret", PLATFORM_HUB_TOKEN="service-secret", PLATFORM_INTROSPECTION_URL="http://platform/internal/notebooks/introspect")
        self.scope = {"c": Config()}
        exec(compile(source, "jupyterhub_config.py", "exec"), self.scope)
        self.name = "nb-" + hashlib.sha256(b"tenant-a\x00u1").hexdigest()[:32]
        self.identity = {"active": True, "tenantId": "tenant-a", "sub": "u1", "hubUsername": self.name, "role": "developer"}
        self.requests = requests

    async def test_service_identity_and_owner_validation(self):
        def post(url, **kwargs):
            self.assertEqual(kwargs["headers"]["Authorization"], "Bearer service-secret")
            self.assertEqual(kwargs["json"], {"token": "grant", "spawn": True})
            return Response(200, self.identity)
        self.requests.post = post
        await self.scope["platform_identity"]("grant", spawn=True)
        self.identity["hubUsername"] = "another-user"
        with self.assertRaises(ValueError):
            await self.scope["platform_identity"]("grant", spawn=True)

    async def test_spawn_renders_queue_group_and_restricted_service_account(self):
        async def identity(token, spawn=False):
            self.assertEqual(token, "grant")
            self.assertTrue(spawn)
            return self.identity
        calls = []
        async def kube(method, path, body=None):
            calls.append((method, path, body))
            if "queues" in path:
                return Response(200, {"metadata": {"labels": {"carrot.ai/tenant-id": "tenant-a"}}})
            return Response(404 if method == "GET" else 201, {})
        self.scope.update(platform_identity=identity, kubernetes_request=kube)
        spawner = SimpleNamespace(name="workspace", user=SimpleNamespace(name=self.name), user_options={"platform_spawn_token": "grant", "namespace": "tenant-b"})
        await self.scope["configure_tenant_workspace"](spawner)
        self.assertEqual(spawner.namespace, "tenant-tenant-a")
        self.assertEqual(spawner.service_account, "tenant-notebook")
        self.assertFalse(spawner.automount_service_account_token)
        self.assertEqual(spawner.scheduler_name, "volcano")
        self.assertEqual(spawner.user_options, {})
        self.assertEqual(calls[-1][2]["spec"]["queue"], "tenant-tenant-a")
        self.assertEqual(spawner.extra_annotations["scheduling.k8s.io/group-name"], self.name + "-workspace")
        self.identity["role"] = "viewer"
        spawner.user_options = {"platform_spawn_token": "grant"}
        with self.assertRaises(ValueError):
            await self.scope["configure_tenant_workspace"](spawner)
        self.assertEqual(len(calls), 3)


if __name__ == "__main__":
    unittest.main()
