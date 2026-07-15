# Backend Development Guidelines

> Best practices for backend development in this project.

---

## Overview

Go 1.23+ backend with PostgreSQL, GORM, and `crypto/ssh`. Hub service provides REST + WebSocket APIs for AI clients to execute commands on remote servers without exposing SSH credentials.

---

## Guidelines Index

| Guide | Description | Status |
|-------|-------------|--------|
| [Directory Structure](./directory-structure.md) | Go module layout: `cmd/`, `internal/{handler,service,model,repository,pkg}/` | ✅ |
| [Database Guidelines](./database-guidelines.md) | PostgreSQL + GORM conventions, migration patterns, table schemas | ✅ |
| [Error Handling](./error-handling.md) | AppError types, handler-service-repo error chain, JSON error envelope | ✅ |
| [Scope Registration](./scopes.md) | API key scope system: validScopes, routeScopes, jwtOnlyRoutes, and how to add a new scope | ✅ |
| [Quality Guidelines](./quality-guidelines.md) | golangci-lint config, testing requirements, forbidden patterns | ✅ |
| [Logging Guidelines](./logging-guidelines.md) | slog structured logging, levels, sensitive data redaction | ✅ |

---

## How to Fill These Guidelines

For each guideline file:

1. Document your project's **actual conventions** (not ideals)
2. Include **code examples** from your codebase
3. List **forbidden patterns** and why
4. Add **common mistakes** your team has made

The goal is to help AI assistants and new team members understand how YOUR project works.

---

## Tech Stack Summary

| Component | Choice |
|-----------|--------|
| Language | Go 1.23+ |
| HTTP Router | Chi |
| Database | PostgreSQL 16+ + TimescaleDB |
| ORM | GORM v2 |
| Migrations | golang-migrate |
| SSH | `crypto/ssh` (stdlib) |
| Auth | JWT (user) + API Key (AI client) |
| Encryption | AES-256-GCM (credential at rest) |
| Logging | `log/slog` JSON structured |
| AI Integration | REST + WebSocket (channel 2) + OpenCode Skill wrapper |

---

**Language**: English (code), English (docs).
