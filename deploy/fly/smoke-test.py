#!/usr/bin/env python3
"""Offline container integration check. Uses only disposable synthetic state."""
import json
import subprocess
import time
import tomllib
import urllib.error
import urllib.request
import uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def docker(*args, check=True):
    return subprocess.run(["docker", *args], capture_output=True, text=True, check=check)


def main():
    cfg = tomllib.loads((ROOT / "fly.toml").read_text())
    env = cfg["env"] | {
        "GARMIN_MCP_LOGIN_ALLOWED_EMAILS": "pending-setup@example.invalid",
        "HTTPS_PROXY": "http://127.0.0.1:9",  # Block anonymous catalog traffic too.
        "GARMIN_MCP_OAUTH_CLIENTS": json.dumps([{
            "id": "deployment-check", "redirect-uris": ["http://127.0.0.1:8765/callback"],
            "scopes": ["garmin:read"], "resources": [cfg["env"]["GARMIN_MCP_PUBLIC_URL"]],
            "public": True,
        }]),
    }
    name = "garmin-fly-check-" + uuid.uuid4().hex[:8]
    volume = name + "-data"
    origin = ""

    def request(path, headers=None):
        req = urllib.request.Request(origin + path, headers=headers or {})
        try:
            with urllib.request.urlopen(req, timeout=3) as response:
                return response.status, response.headers, response.read()
        except urllib.error.HTTPError as error:
            return error.code, error.headers, error.read()

    def start():
        nonlocal origin
        args = ["run", "-d", "--name", name, "--read-only", "--tmpfs",
                "/tmp:rw,noexec,nosuid,size=16m", "-p", "127.0.0.1::8080",
                "-v", volume + ":/data"]
        for key, value in env.items():
            args += ["-e", key + "=" + value]
        docker(*args, "garmin-mcp:fly-check", *cfg["processes"]["app"].split())
        origin = "http://" + docker("port", name, "8080").stdout.strip()
        for _ in range(100):
            try:
                if request("/readyz")[0] == 200:
                    return
            except (OSError, urllib.error.URLError):
                pass
            time.sleep(0.2)
        logs = docker("logs", name)
        raise RuntimeError("Readiness failed: " + logs.stdout + logs.stderr)

    try:
        docker("volume", "create", volume)
        start()
        assert request("/livez")[0] == 200
        status, headers, _ = request("/mcp")
        assert status == 401 and "resource_metadata=" in headers.get("WWW-Authenticate", "")
        meta = json.loads(request("/.well-known/oauth-authorization-server")[2])
        assert meta["code_challenge_methods_supported"] == ["S256"]
        assert set(meta["scopes_supported"]) == {"garmin:read"}
        assert "registration_endpoint" not in meta
        resource = json.loads(request("/.well-known/oauth-protected-resource")[2])
        assert resource["resource"] == env["GARMIN_MCP_PUBLIC_URL"]
        assert request("/mcp", {"Origin": "https://untrusted.invalid"})[0] == 403
        uid = docker("exec", name, "sh", "-c", "awk '/^Uid:/{print $2}' /proc/1/status")
        assert uid.stdout.strip() == "65532"
        for path in ["/data/garmin/garmin.db", "/data/garmin/keys/key-v1.json"]:
            mode = docker("exec", name, "stat", "-c", "%u:%g:%a", path)
            assert mode.stdout.strip() == "65532:65532:600"
        before = docker("exec", name, "sha256sum", "/data/garmin/keys/key-v1.json").stdout
        docker("stop", name)
        docker("rm", name)
        start()
        after = docker("exec", name, "sha256sum", "/data/garmin/keys/key-v1.json").stdout
        assert before == after, "Encryption key changed across container replacement"
        missing = docker("run", "--rm", "--read-only", "garmin-mcp:fly-check", check=False)
        assert missing.returncode != 0 and "mounted volume" in missing.stderr
        # A healthy volume must not make missing login/client configuration acceptable.
        unset = docker("run", "--rm", "--read-only", "-v", volume + ":/data",
                       "garmin-mcp:fly-check", check=False)
        assert unset.returncode != 0 and "login allowlist" in unset.stderr
        print("PASS: probes, OAuth metadata, 401, Origin guard, nonroot UID, private state,")
        print("key persistence, missing-volume refusal, and missing-allowlist refusal.")
    finally:
        docker("rm", "-f", name, check=False)
        docker("volume", "rm", volume, check=False)


if __name__ == "__main__":
    main()
