---
type: Documentation
title: gmmff Wiki Quickstart
description: Entry point for the gmmff peer-to-peer file transfer system wiki. Provides high-level overview and navigation to key sections.
tags: [quickstart, overview, navigation]
verified:
  - by: openwiki/0.7.1
    at: 2026-10-09T14:54:52.045Z
sources:
  - id: openwiki-source-8037e2358a2c4f9b2c722a11
    resource: repo://AGENTS.md
  - id: openwiki-source-23775c3de52f3ab95a13cb8b
    resource: repo://README.md
generated: { by: "openwiki/0.7.1", at: "2026-10-09T14:54:52.045Z" }
---

# gmmff Wiki

**gmmff** (pronounced *gimph*) is a brutally simple, cryptographically sound peer-to-peer file and message transfer system.

gmmff consists of two parts: a **signaling server** that brokers the initial connection, and a **CLI client** that handles the actual transfer. The server never sees file contents — once two (or more) peers are connected, all data flows directly between them over an encrypted WebRTC data channel.

## Key Sections

- [Architecture Overview](./architecture/overview.md) - System components, data flow, and security model
- [Key Workflows](./workflows/key-workflows.md) - Common operations like file transfer, messaging, and scheduling
- [Domain Concepts](./domain-concepts/overview.md) - Core concepts like sessions, slots, PAKE, and WebRTC
- [Operations & Runbook](./operations/runbook.md) - Deployment, configuration, and maintenance procedures
- [Testing Guidance](./testing/guidance.md) - How to run tests and contribute
- [Integration Points](./integrations/overview.md) - How gmmff integrates with external systems
- [Source Map](./source-map.md) - Direct mapping of wiki topics to source code locations

## Getting Started

See the [official README](../README.md) for installation and quick start guides, including Docker Compose and local Go + Redis/Valkey setups.
