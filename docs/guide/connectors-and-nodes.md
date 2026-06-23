# Connectors & node types

A **pipeline** in VRSky is a graph of **nodes** joined left to right. Data
enters on the left, flows through the middle, and leaves on the right. You build
a pipeline by dragging nodes from the component palette onto the canvas and
connecting them.

## The four node kinds

In the builder's component palette, nodes appear as colored blocks. There are
four kinds:

| Node kind | What it does |
|-----------|--------------|
| **Input** | Where data comes *from* (a source / consumer). |
| **Filter** | Keeps or drops messages by a rule. |
| **Converter** | Transforms or reshapes the payload (field mapping). |
| **Output** | Where data goes *to* (a destination / producer). |

### Input

An Input is the start of your pipeline. It reads data from a source, such as:

- a webhook or HTTP request,
- a REST API you poll on a schedule,
- a database,
- a file or CSV,
- an SFTP server,
- cloud storage,
- Apache Kafka or RabbitMQ,
- Salesforce,
- or another workspace.

### Filter

A Filter sits between nodes and decides, message by message, whether to keep it
or drop it based on a rule you define.

### Converter

A Converter reshapes the payload — for example mapping fields from the source
format into the shape your destination expects.

### Output

An Output is the end of your pipeline. It writes data to a destination such as
HTTP, a database, a file, SFTP, cloud storage, Kafka, RabbitMQ, or Salesforce.

!!! note "Filters and converters"
    For more on building rules and field mappings, see
    [Filters & converters](../connectors/filters-converters.md).

## Configuring a node

Select a node on the canvas and edit its settings in the **Property panel** on
the right.

![Screenshot: a selected node with its Property panel open on the right](../img/property-panel.png)

Every connector has its own set of fields. The complete reference for each one —
every field plus an example — lives in the **Connectors** section. Open the page
for the connector you're using:

- Start with the [Connectors overview](../connectors/index.md).
- Then open the page for your specific connector (for example
  [HTTP & webhooks](../connectors/http.md) or
  [Salesforce](../connectors/salesforce.md)).

## Two helpers in input editors

When you configure an Input, two buttons help you set it up correctly before you
deploy:

- **Discover** — reads the source's schema so you don't have to type field names
  by hand. It pulls database tables and columns, Salesforce objects, or CSV
  headers, which makes mapping in a downstream Converter much easier.
- **Test connection** — checks that the source (or destination) is reachable
  before you deploy the pipeline.

!!! note "Test before you deploy"
    Running **Test connection** on both ends of your pipeline catches bad hosts
    or credentials early. See
    [Troubleshooting](troubleshooting.md) if a test fails.

## Putting it together

A typical pipeline reads from an Input, optionally passes through a Filter and a
Converter, and writes to an Output:

1. Drag an **Input** onto the canvas and configure its source.
2. (Optional) Add a **Filter** to drop messages you don't want.
3. (Optional) Add a **Converter** to map fields.
4. Drag an **Output** onto the canvas and configure its destination.
5. Connect the nodes left to right, then deploy.
