# Getting started

Welcome to VRSky. This guide takes you from creating an account to seeing your
first integration move data. No coding required.

## A few concepts first

You'll meet these terms throughout the app:

- **Workspace** — your isolated tenant. Everything you build (pipelines,
  connections, members) lives inside a workspace and is private to it.
- **Pipeline / connection** — a flow that moves data from a **source**, through
  an optional **filter** or **converter**, to a **destination**.
- **Connector** — the building block for each step (for example, an HTTP source
  or a database destination).

## Create your account

You sign up on the **Register** screen, which has two steps.

**Step 1 — your details**

1. Enter your **email** and a **password**.
2. Re-enter the password to **confirm** it.
3. Enter your **full name**.

!!! note "Password requirements"
    Your password must be at least **8 characters** and include an **uppercase
    letter**, a **lowercase letter**, and a **digit**.

**Step 2 — your first workspace**

1. Give your first workspace a **name** (for example, your team or company).
2. Finish registration.

![Screenshot: the Register screen showing the two-step sign-up form with email, password and workspace name fields](../img/register.png)

When registration is complete, head to the Login screen to sign in.

## Log in

On the **Login** screen, enter your **email** and **password**, then sign in.

![Screenshot: the login screen with email and password fields](../img/login.png)

### Signing in with SSO

If your workspace has single sign-on configured, you can sign in through your
company's identity provider instead:

1. Type your **workspace slug**.
2. Choose **Sign in with SSO**.
3. You're redirected to your identity provider to authenticate.

!!! note
    The SSO option only works if an administrator has set up SSO for your
    workspace. If you don't have it, just use email and password.

## Your first pipeline: the Get started wizard

The first time you log in and have no pipelines yet, VRSky drops you on the
**Get started wizard**. You can also reach it any time at `/welcome`.

The wizard walks you through a working integration in a few clicks:

1. **Pick a template** that matches what you want to do.
2. **Fill in a couple of fields** (such as a URL or a name).
3. Choose **Deploy** to launch the pipeline.
4. Choose **Send a sample event** to push test data through it and confirm it
   works.

![Screenshot: the Get started wizard showing template choices and the Deploy button](../img/get-started-wizard.png)

!!! note "Want the full walkthrough?"
    For a step-by-step tour that explains each part of a pipeline in detail,
    see [Build your first pipeline](../tutorials/first-pipeline.md).

## Where to go next

- Learn how to switch workspaces, invite teammates, and manage your account in
  [Workspaces, members & your account](workspaces-and-members.md).
- Explore usage, alerts, and the audit log in
  [Settings: usage, notifications, audit](settings.md).
