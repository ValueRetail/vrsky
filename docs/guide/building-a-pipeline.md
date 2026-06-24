# Building a pipeline

The pipeline builder is the canvas you land on at **Home** (`/`). You assemble a
pipeline by dropping nodes onto the canvas, connecting them, configuring each
one, and clicking **Deploy**.

![Screenshot: the pipeline builder — component palette on the left, canvas in the middle, an Input node connected to an Output node](../img/builder-overview.png)

## 1. Add nodes from the palette

The **component palette** on the left has four kinds of node:

| Kind | Color | Role |
|------|-------|------|
| **Input** | blue | the **source** — where data comes from |
| **Filter** | orange | keep or drop messages by a rule |
| **Converter** | pink | reshape / map the payload |
| **Output** | green | the **destination** — where data goes |

Drag a block onto the canvas (or click it). Nodes are auto-named — *Input 1*,
*Output 1*, and so on. A minimal pipeline is one **Input** connected to one
**Output**; filters and converters are optional and sit in between.

## 2. Connect the nodes

Drag from a node's output dot to the next node's input dot to draw a
connection. Data flows along the arrows, left to right. To remove a node or a
connection, select it and press delete (or use its right-click menu).

## 3. Configure each node

Select a node to open the **Property panel** on the right. What you see depends
on the node's connector type — pick the connector (e.g. *HTTP*, *Database*,
*Salesforce*) and fill its fields.

- Credentials are entered as masked fields and stored encrypted — see
  [Credentials, secrets & OAuth](credentials-and-oauth.md).
- For sources, two helpers speed you up:
  - **Discover** reads the source's schema (database tables/columns, Salesforce
    objects, CSV headers) so you can map fields without guessing.
  - **Test connection** checks the source/destination is reachable *before* you
    deploy.
- Full field-by-field reference for every connector is in the
  [Connectors](../connectors/index.md) section — open the page for the connector
  you're using.

![Screenshot: the Property panel for an HTTP output node showing URL, method, and auth fields](../img/property-panel.png)

## 4. (Optional) filter and convert

- A **Filter** node passes messages that match its rule and drops the rest.
- A **Converter** node maps input fields to output fields (and can transform
  values), so the destination gets exactly the shape it expects. Use
  **Discover** on the source first to populate the field list.

## 5. Deploy

Click **Deploy** (top-right). VRSky validates the pipeline (everything connected,
required fields filled), encrypts any credentials you entered, creates the
connection, and starts it running. You'll get the connection's details — and for
a webhook source, its public URL.

!!! note "Multiple canvases"
    The tabs at the top let you keep several pipelines side by side in a
    workspace. Double-click a tab to rename it; use **+** to add one. Your
    in-progress canvas is saved in your browser until you deploy.

Once deployed, head to [Deploying & monitoring](deploying-and-monitoring.md) to
watch it run, or the [first-pipeline tutorial](../tutorials/first-pipeline.md)
for a guided end-to-end example.
