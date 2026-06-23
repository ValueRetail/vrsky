# Credentials, secrets & OAuth

Most connectors need to authenticate to the system they talk to. VRSky handles
this in two ways: **static credentials** you type in, and **OAuth** where you
"Connect your account" to a SaaS provider.

## Static credentials

Credential fields — passwords, API keys, tokens, connection strings, and private
keys — are entered in the node's **Property panel** as masked inputs.

When you deploy the pipeline, VRSky **encrypts each credential** and stores only
a reference to it. The plaintext is never saved and is never shown again.

![Screenshot: a masked credential field in the Property panel](../img/masked-credential.png)

!!! warning "You can't read a secret back"
    Because the plaintext is never stored, you cannot view a credential after
    deploying. To change one, type the new value into the field and deploy
    again. Keep your own copy of any key you might need elsewhere.

!!! note "How encryption works"
    For the full details on how secrets are protected, see the
    [Security whitepaper](../security/whitepaper.md).

## OAuth

Some connectors authenticate to a SaaS by signing you in ("Connect your
account") instead of using a static key. This applies to connectors such as the
HTTP output, the REST API input, and Salesforce.

Setting up OAuth is a two-step process.

### Step 1 — An admin registers an OAuth provider

An admin registers the provider once under **Settings → OAuth providers**
(`/settings/oauth`). They supply:

- the **provider type** — Google, Microsoft 365, Salesforce, HubSpot, Shopify,
  or **Custom**,
- the **client id** and **client secret**, and
- for a **Custom** provider, the **authorization** and **token** URLs.

![Screenshot: the OAuth providers page with a list of registered providers](../img/oauth-providers.png)

!!! note "Registering a provider needs admin rights"
    The OAuth providers page is role-gated. If you don't see it or the action is
    greyed out, ask a workspace admin or owner. See
    [Workspaces, members & your account](workspaces-and-members.md).

### Step 2 — Connect a grant in the node

Once a provider exists, open the node's **Property panel** and:

1. Pick the registered **provider**.
2. Choose **Connect** to start a **grant**. A pop-up window signs you in at the
   provider and links the account.
3. The node now uses that grant to authenticate.

![Screenshot: the Property panel showing a provider picker and a Connect button](../img/oauth-connect-grant.png)

## Connector specifics

Authentication details vary by connector. For the exact fields and OAuth steps,
open the relevant connector page:

- [Salesforce](../connectors/salesforce.md)
- [HTTP & webhooks](../connectors/http.md)

For any connector not covered here, see the relevant connector page in the
[Connectors overview](../connectors/index.md).
