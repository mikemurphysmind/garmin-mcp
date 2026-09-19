#!/usr/bin/env python3
"""Run interactively in your terminal; send configuration directly to Fly secrets."""
import getpass
import hashlib
import json
import subprocess
import sys
import tomllib
from pathlib import Path
from urllib.parse import urlsplit


def main():
    if not sys.stdin.isatty():
        raise SystemExit("Run this helper interactively in your own terminal.")
    root = Path(__file__).resolve().parents[2]
    config = tomllib.loads((root / "fly.toml").read_text())
    app = config["app"]
    resource = config["env"]["GARMIN_MCP_PUBLIC_URL"]
    print(f"Configure ChatGPT OAuth for {app}. No input is written to a file.")
    email = getpass.getpass("Garmin email to allow (hidden): ").strip()
    if (email.count("@") != 1 or "." not in email.split("@")[-1]
            or any(c.isspace() or ord(c) < 32 for c in email)):
        raise SystemExit("Enter one valid email address.")
    redirect = input("Exact ChatGPT OAuth redirect URL: ").strip()
    uri = urlsplit(redirect)
    if (uri.scheme != "https" or uri.netloc != "chatgpt.com"
            or uri.fragment or uri.query or "*" in redirect
            or any(c.isspace() or ord(c) < 32 for c in redirect)
            or not (uri.path.startswith("/connector/oauth/")
                    or uri.path == "/connector_platform_oauth_redirect")):
        raise SystemExit("Copy the exact ChatGPT callback URL; wildcards are not accepted.")
    secret = getpass.getpass("OAuth client secret from your password manager (hidden, 32+ characters): ")
    if len(secret) < 32:
        raise SystemExit("Use a randomly generated secret of at least 32 characters.")
    clients = [{"id": "chatgpt", "name": "ChatGPT", "redirect-uris": [redirect],
                "scopes": ["garmin:read", "offline_access"], "resources": [resource],
                "public": False,
                "secret-hash": hashlib.sha256(secret.encode()).hexdigest()}]
    payload = ("GARMIN_MCP_LOGIN_ALLOWED_EMAILS=" + email + "\n"
               + "GARMIN_MCP_OAUTH_CLIENTS=" + json.dumps(clients, separators=(",", ":")) + "\n")
    subprocess.run(["fly", "secrets", "import", "--stage", "--app", app],
                   input=payload, text=True, check=True, cwd=root)
    print("Staged. Use client ID chatgpt and the same secret in ChatGPT.")
    print("Apply with: fly deploy --remote-only --ha=false")


if __name__ == "__main__":
    main()
