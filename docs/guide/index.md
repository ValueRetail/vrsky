# User Guide

This guide explains how to use the VRSky web app end to end — no coding
required. If you're new, start with [Getting started](getting-started.md) and
the [first-pipeline tutorial](../tutorials/first-pipeline.md); then come back
here for the details of each screen.

## What VRSky does

VRSky moves data between systems. You build a **pipeline** (also called a
*connection*): a left-to-right flow from a **source** → optional **filter** /
**converter** → **destination**. You build it visually, deploy it with one
click, and watch data flow through it live.

## Key ideas

| Term | What it means |
|------|---------------|
| **Workspace** | Your isolated area (a "tenant"). Everything — pipelines, secrets, members — belongs to a workspace. You can belong to several and switch between them. |
| **Pipeline / connection** | A graph of **nodes** (source → filter/converter → destination) that, once deployed, runs continuously. |
| **Node** | One step in a pipeline. Four kinds: **Input** (source), **Filter**, **Converter**, **Output** (destination). |
| **Connector** | The integration behind a node — HTTP, database, Salesforce, Kafka, file, etc. |
| **Secret** | A credential you type once; VRSky stores it encrypted and never shows it again. |

## How to use this guide

| If you want to… | Read |
|-----------------|------|
| Sign up and run your first pipeline | [Getting started](getting-started.md) |
| Manage your workspace, teammates, and account | [Workspaces, members & your account](workspaces-and-members.md) |
| Build a pipeline on the canvas | [Building a pipeline](building-a-pipeline.md) |
| Understand the node types | [Connectors & node types](connectors-and-nodes.md) |
| Enter passwords / connect a SaaS account | [Credentials, secrets & OAuth](credentials-and-oauth.md) |
| Deploy and watch a pipeline | [Deploying & monitoring](deploying-and-monitoring.md) |
| Share data with another workspace | [Sharing data between workspaces](data-sharing.md) |
| Check usage, alerts, and the audit log | [Settings](settings.md) |
| Fix a problem | [Troubleshooting](troubleshooting.md) |

!!! tip "Roles matter"
    Some screens and actions are limited by your **role** (viewer, editor,
    admin, owner). If something is missing or greyed out, see
    [roles](workspaces-and-members.md#roles).
