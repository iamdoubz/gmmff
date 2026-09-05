---
type: Documentation
title: gmmff Wiki Quickstart
description: Entry point for the gmmff peer-to-peer file transfer system wiki. Provides high-level overview and navigation to key sections.
verified:
  - by: openwiki/0.5.0
    at: 2026-09-05T11:33:48.919Z
sources:
  - id: openwiki-source-e8e61d605125cac4d909755e
    resource: repo://docs/ARCHITECTURE.md
  - id: openwiki-source-23775c3de52f3ab95a13cb8b
    resource: repo://README.md
generated: { by: "openwiki/0.5.0", at: "2026-09-05T11:33:48.919Z" }
---
# gmmff Wiki

**gmmff** (pronounced *gimph*) is a brutally simple, cryptographically sound peer-to-peer file and message transfer system.

## Overview

gmmff consists of two main components:
- **Signaling server**: Brokers initial connections between peers (WebSocket server)
- **CLI client**: Handles actual file/message transfer over encrypted WebRTC data channels

The server never sees file contents—once peers connect, data flows directly between them over encrypted WebRTC data channels.

## Key Sections

- [Architecture Overview](/openwiki/architecture/overview.md) - System components, data flow, and security model
- [Key Workflows](/openwiki/workflows/key-workflows.md) - Common operations like file transfer, messaging, and scheduling
- [Domain Concepts](/openwiki/domain-concepts/overview.md) - Core concepts like sessions, slots, PAKE, and WebRTC
- [Operations & Runbook](/openwiki/operations/runbook.md) - Deployment, configuration, and maintenance procedures
- [Testing Guidance](/openwiki/testing/guidance.md) - How to run tests and contribute
- [Integration Points](/openwiki/integrations/overview.md) - How gmmff integrates with external systems
- [Source Map](/openwiki/source-map.md) - Direct mapping of wiki topics to source code locations

## Getting Started

<!-- openwiki: broken internal link [docs/INSTALL.md] file "docs/INSTALL.md" does not exist. Fix the href or restore the target, then delete this comment. -->
<!-- openwiki: broken internal link [docs/BUILD.md] file "docs/BUILD.md" does not exist. Fix the href or restore the target, then delete this comment. -->
See the [installation guide](docs/INSTALL.md) for installing `gmmff` and the [build guide](docs/BUILD.md) for building from source.

For quick starts, see the [README](/README.md) which includes Docker Compose and local setup instructions.

## Contributing

<!-- openwiki: broken internal link [docs/BUILD.md] file "docs/BUILD.md" does not exist. Fix the href or restore the target, then delete this comment. -->
See the [Testing Guidance](/openwiki/testing/guidance.md) for how to run tests and the [Build Guide](docs/BUILD.md) for development setup.
