# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a Go-based HTTP web proxy server for the CSU33032 Advanced Networks course. The proxy listens on port 4000 and forwards HTTP requests to destination servers.

## Build and Run Commands

```bash
# Build the proxy
go build

# Run the proxy
go run .

# Run with specific file
go run main.go
```

## Architecture

The proxy uses a concurrent connection handling model:

- **main.go**: Core proxy server
  - `proxyData` struct holds shared state (blocked sites, cache, logs) protected by a mutex
  - `startProxy()` listens on TCP port 4000 and spawns goroutines for each connection
  - `handleConnection()` parses HTTP requests, extracts the Host header, and forwards requests to the destination server

- **console.go**: Management console (work in progress)
  - Intended for command-line management of the proxy
  - Uses Go templates for HTTP response formatting

## Current State

The proxy is in early development. Core functionality implemented:
- TCP listener on port 4000
- HTTP request parsing and Host header extraction
- Request forwarding to destination servers
- Connection logging

Features defined but not fully implemented:
- Site blocking (`blockedSites` map)
- Response caching (`cache` map)
- Management console
