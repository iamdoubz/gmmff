---
type: Documentation
title: gmmff Wiki Quickstart
description: Entry point for the gmmff peer-to-peer file transfer system wiki. Provides high-level overview and navigation to key sections.
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

See the [official README](/README.md) for installation and quick start guides.

## Contributing

See [CONTRIBUTING](docs/CONTRIBUTING.md) for development setup, testing, and contribution guidelines.

*Last updated: 2026-07-20*