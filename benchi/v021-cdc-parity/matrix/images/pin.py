#!/usr/bin/env python3
"""Resolves, once per run, the digests of the quay.io Debezium images and
writes images/PINS.json (aws/launch.sh, before the kit is packed).

quay.io rebuilds debezium/connect:3.7.0.Final and debezium/server:3.7.0.Final
(and server-native) on refreshed base images and deletes the superseded
manifests, so a digest written into the repository stops resolving within a
day (the shakedown, 2026-10-28). The version is pinned in the repository (the
tag); the digest is pinned per run, here, and recorded in environment.txt.

    pin.py OUT_JSON
"""
import json
import sys
import urllib.request

REPOS = {"connect": ("debezium/connect", "3.7.0.Final"), "dbzserver": ("debezium/server", "3.7.0.Final"),
         "dbzserver-native": ("debezium/server-native", "3.7.0.Final")}
ACCEPT = ", ".join(["application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json",
                    "application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json"])


def digest(repo, tag):
    tok = json.load(urllib.request.urlopen(f"https://quay.io/v2/auth?service=quay.io&scope=repository:{repo}:pull", timeout=20))["token"]
    req = urllib.request.Request(f"https://quay.io/v2/{repo}/manifests/{tag}", method="HEAD",
                                 headers={"Authorization": f"Bearer {tok}", "Accept": ACCEPT})
    with urllib.request.urlopen(req, timeout=20) as r:
        d = r.headers.get("Docker-Content-Digest")
    if not d or not d.startswith("sha256:"):
        sys.exit(f"pin.py: no digest for {repo}:{tag}")
    return f"quay.io/{repo}:{tag}@{d}"


def main():
    pins = {k: digest(*v) for k, v in REPOS.items()}
    with open(sys.argv[1], "w") as f:
        json.dump(pins, f, indent=1)
        f.write("\n")
    for k, v in pins.items():
        print(k, v)


if __name__ == "__main__":
    main()
