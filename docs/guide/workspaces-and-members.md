# Workspaces, members & your account

A **workspace** is your isolated space in VRSky. Pipelines and connections
belong to one workspace and are never shared with another. This guide covers
switching between workspaces, managing the people in them, and looking after
your own account.

## Switching and managing workspaces

The **workspace switcher** is the dropdown in the top header.

- **Switch context** — pick another workspace from the dropdown. Your pipelines
  and connections always reflect the workspace you currently have selected.
- **Create a workspace** — choose the **+** button. A modal opens; give the new
  workspace a name and create it.
- **Delete a workspace** — choose the **trash** button to delete the workspace
  you're currently in. A confirmation dialog appears before anything is removed.

![Screenshot: the top header workspace switcher dropdown with the plus and trash buttons](../img/workspace-switcher.png)

!!! warning "Deleting a workspace is permanent"
    Deleting a workspace removes **all of its data**, including every pipeline
    and connection. The trash button only appears when you belong to more than
    one workspace, so you can't accidentally delete your only one. Make sure
    you've selected the right workspace before confirming.

## Members and roles

Open the members list at **Settings → Members** (`/settings/users`). Here you
can see everyone in the workspace and what they can do.

### Roles

Roles range from least to most privilege. Each role includes everything the one
before it can do, plus more:

| Role | What they can do |
|------|------------------|
| **Viewer** | Read-only access. |
| **Editor** | Build and edit pipelines, manage connections, and use the API key. |
| **Admin** | Everything an editor can, plus manage OAuth providers, notifications, and members. |
| **Owner** | Everything an admin can, plus all settings, rotating the API key, and deleting the workspace. |

![Screenshot: the Members page listing members with their role dropdowns](../img/members.png)

### Managing members (owners)

If you're an **owner**, you can:

- **Change a member's role** using the role dropdown next to their name.
- **Remove a member** from the workspace.

!!! note "The last owner is protected"
    A workspace must always keep at least one owner, so the **last owner can't
    be removed**. Promote someone else to owner first if you need to step down.

## Your account

Open your account from the **user menu** at the top-right (your name or the gear
icon). From here you can:

- **Log out** of VRSky.
- **Delete your account** — a destructive action that asks you to confirm first.

![Screenshot: the top-right user menu showing Logout and Delete account options](../img/user-menu.png)

!!! note "Changing your password"
    There's no password field in the account menu. To change your password, use
    the **forgot password** reset flow from the Login screen — request a reset
    and follow the link you receive.

!!! warning "Deleting your account can't be undone"
    Deleting your account is permanent. If you're the last owner of a workspace,
    sort out ownership before you delete your account.

## Where to go next

- New here? Start with [Getting started](getting-started.md).
- Manage usage, alerts, and the audit log in
  [Settings: usage, notifications, audit](settings.md).
