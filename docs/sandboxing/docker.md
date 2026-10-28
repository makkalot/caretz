---
title: "Docker, system bus, and GPUs"
weight: 50
description: "Reach the Docker daemon, systemd, and GPU devices."
---

# Docker, System Bus, and GPUs

```sh
caretz --allow-docker -p "build the image and run the smoke tests in a container"
```

Access to the Docker daemon is effectively control of the host, so enable it
only for tasks you trust.

## Docker

On Linux, `--allow-docker` passes through an existing `DOCKER_HOST`, or points
the client at `unix:///run/docker.sock` when that socket is available. A custom
daemon socket must also be visible through an appropriate root. On macOS,
Caretz permits Unix-socket access for an existing `DOCKER_HOST` and the standard
Docker Desktop, OrbStack, Colima, Rancher Desktop, and `/var/run` socket paths.
SSH Docker contexts require unrestricted networking on macOS. Session execution
carries the Docker daemon socket and `DOCKER_HOST` into its persistent
supervisor only when `--allow-docker` is also configured.

## System Bus and Devices

Linux one-shot commands still see the host's `/run` (and `/var/run`), so daemon
clients such as `tailscale` find their sockets. Sockets that would hand a
command control of the host stay hidden: the user and system D-Bus buses,
user systemd, Podman, and containerd always, and `/run/docker.sock` and
rootless `/run/user/*/docker.sock` unless `--allow-docker` is set.

`"allowSystemBus": true` lets commands query the system bus only when Caretz
doesn't run as root: polkit still authorizes a regular user's state changes,
such as restarting a service, while root's calls bypass it, so root keeps the
bus hidden. Linux commands get a minimal `/dev` without GPU device nodes;
`"allowGPU": true` binds the host's NVIDIA, Jetson, DRM, and ROCm device nodes
into the sandbox, and device permissions still decide who may open them.

The built-in [`ops` profile](../profiles/ops.md) turns on the system bus and
GPU access for machine maintenance.
