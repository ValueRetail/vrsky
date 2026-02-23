# VRSky Converter Functions Reference

## Overview

This guide documents all available functions that can be used in converter transformations. Functions are organized by category and include examples for each.

## Function Categories

1. **String Functions** - String manipulation and transformation
2. **Type Conversion Functions** - Type conversion and parsing
3. **Numeric Functions** - Numeric operations and calculations
4. **Date/Time Functions** - Timestamp and date operations
5. **ID Generation Functions** - Generate unique identifiers
6. **Lookup Functions** - Query external data sources
7. **Caching Functions** - Cache and retrieve values
8. **WASM Plugin Functions** - Custom WebAssembly functions

## String Functions

### upper()
Converts a string to uppercase.

**Signature**: `upper(input: string) → string`

**Example**:
```yaml
- source: customer.name
  function: upper()
  target: customer_name_upper
```

**Input/Output**:
```
Input:  "john doe"
Output: "JOHN DOE"
```

### lower()
Converts a string to lowercase.

**Signature**: `lower(input: string) → string`

**Example**:
```yaml
- source: customer.email
  function: lower()
  target: email_normalized
```

**Input/Output**:
```
Input:  "John@EXAMPLE.COM"
Output: "john@example.com"
```

### substring(start, end)
Extracts a substring from a string.

**Signature**: `substring(input: string, start: int, end: int) → string`

**Example**:
```yaml
- source: order.id
  function: substring(0, 8)
  target: order_prefix
```

**Input/Output**:
```
Input:  "ORD-2026-001-XYZ"
Output: "ORD-2026"
```

### concat(...parts)
Concatenates multiple strings.

**Signature**: `concat(part1: string, part2: string, ...) → string`

**Example**:
```yaml
- function: concat(customer.first_name, " ", customer.last_name)
  target: full_name
```

**Input/Output**:
```
Input:  first_name="John", last_name="Doe"
Output: "John Doe"
```

### split(delimiter)
Splits a string by delimiter into an array.

**Signature**: `split(input: string, delimiter: string) → array[string]`

**Example**:
```yaml
- source: tags
  function: split(",")
  target: tag_array
```

**Input/Output**:
```
Input:  "tag1,tag2,tag3"
Output: ["tag1", "tag2", "tag3"]
```

### join(delimiter)
Joins array elements with a delimiter.

**Signature**: `join(input: array[string], delimiter: string) → string`

**Example**:
```yaml
- source: order.items
  function: join(", ")
  target: items_string
```

**Input/Output**:
```
Input:  ["apple", "banana", "orange"]
Output: "apple, banana, orange"
```

### contains(substring)
Checks if a string contains a substring.

**Signature**: `contains(input: string, substring: string) → boolean`

**Example**:
```yaml
- source: email
  function: contains("@")
  target: is_valid_email
```

**Input/Output**:
```
Input:  "john@example.com"
Output: true
```

### startswith(prefix)
Checks if a string starts with a prefix.

**Signature**: `startswith(input: string, prefix: string) → boolean`

**Example**:
```yaml
- source: order.id
  function: startswith("ORD-")
  target: is_order
```

**Input/Output**:
```
Input:  "ORD-2026-001"
Output: true
```

### endswith(suffix)
Checks if a string ends with a suffix.

**Signature**: `endswith(input: string, suffix: string) → boolean`

**Example**:
```yaml
- source: filename
  function: endswith(".pdf")
  target: is_pdf
```

**Input/Output**:
```
Input:  "document.pdf"
Output: true
```

### trim()
Removes leading and trailing whitespace.

**Signature**: `trim(input: string) → string`

**Example**:
```yaml
- source: customer.name
  function: trim()
  target: name_trimmed
```

**Input/Output**:
```
Input:  "  John Doe  "
Output: "John Doe"
```

### replace(search, replacement)
Replaces all occurrences of a substring.

**Signature**: `replace(input: string, search: string, replacement: string) → string`

**Example**:
```yaml
- source: address
  function: replace(",", ";")
  target: address_normalized
```

**Input/Output**:
```
Input:  "123 Main St, New York, NY"
Output: "123 Main St; New York; NY"
```

### length()
Returns the length of a string or array.

**Signature**: `length(input: string | array) → int`

**Example**:
```yaml
- source: product_name
  function: length()
  target: name_length
```

**Input/Output**:
```
Input:  "MacBook Pro"
Output: 11
```

---

## Type Conversion Functions

### toString()
Converts a value to a string.

**Signature**: `toString(input: any) → string`

**Example**:
```yaml
- source: quantity
  function: toString()
  target: qty_string
  type: string
```

**Input/Output**:
```
Input:  123
Output: "123"
```

### toNumber()
Converts a value to a number (int or float).

**Signature**: `toNumber(input: string | number) → number`

**Example**:
```yaml
- source: price_text
  function: toNumber()
  target: price
  type: float
```

**Input/Output**:
```
Input:  "99.99"
Output: 99.99
```

### toBoolean()
Converts a value to a boolean.

**Signature**: `toBoolean(input: any) → boolean`

**Example**:
```yaml
- source: is_active
  function: toBoolean()
  target: active
  type: boolean
```

**Input/Output**:
```
Input:  "true", 1, true, "yes"
Output: true
Input:  "false", 0, false, "no"
Output: false
```

### parseJSON()
Parses a JSON string into an object.

**Signature**: `parseJSON(input: string) → object`

**Example**:
```yaml
- source: metadata_json
  function: parseJSON()
  target: metadata
  type: object
```

**Input/Output**:
```
Input:  '{"key": "value", "count": 42}'
Output: {"key": "value", "count": 42}
```

### stringifyJSON()
Converts an object to a JSON string.

**Signature**: `stringifyJSON(input: object) → string`

**Example**:
```yaml
- source: metadata_object
  function: stringifyJSON()
  target: metadata_json
  type: string
```

**Input/Output**:
```
Input:  {"key": "value", "count": 42}
Output: '{"key":"value","count":42}'
```

---

## Numeric Functions

### add(value)
Adds a number to another number.

**Signature**: `add(input: number, value: number) → number`

**Example**:
```yaml
- source: order.total
  function: add(tax_amount)
  target: total_with_tax
```

### subtract(value)
Subtracts a number from another number.

**Signature**: `subtract(input: number, value: number) → number`

**Example**:
```yaml
- source: price
  function: subtract(discount)
  target: final_price
```

### multiply(factor)
Multiplies a number by a factor.

**Signature**: `multiply(input: number, factor: number) → number`

**Example**:
```yaml
- source: quantity
  function: multiply(unit_price)
  target: line_total
```

### divide(divisor)
Divides a number by a divisor.

**Signature**: `divide(input: number, divisor: number) → number`

**Example**:
```yaml
- source: total_price
  function: divide(quantity)
  target: unit_price
```

### round(decimals)
Rounds a number to a specified number of decimal places.

**Signature**: `round(input: number, decimals: int) → number`

**Example**:
```yaml
- source: calculated_price
  function: round(2)
  target: final_price
```

**Input/Output**:
```
Input:  99.9876
Output: 99.99
```

### floor()
Rounds down to the nearest integer.

**Signature**: `floor(input: number) → int`

**Example**:
```yaml
- source: quantity_precise
  function: floor()
  target: quantity
```

### ceil()
Rounds up to the nearest integer.

**Signature**: `ceil(input: number) → int`

**Example**:
```yaml
- source: calculated_units
  function: ceil()
  target: units_needed
```

### abs()
Returns the absolute value.

**Signature**: `abs(input: number) → number`

**Example**:
```yaml
- source: amount_difference
  function: abs()
  target: variance
```

---

## Date/Time Functions

### now()
Returns the current timestamp in RFC3339Nano format.

**Signature**: `now() → string`

**Example**:
```yaml
- function: now()
  target: created_at
  type: string
```

**Output**:
```
"2026-02-23T12:30:45.123456789Z"
```

### today()
Returns today's date in YYYY-MM-DD format.

**Signature**: `today() → string`

**Example**:
```yaml
- function: today()
  target: processing_date
  type: string
```

**Output**:
```
"2026-02-23"
```

### timestamp()
Returns the current Unix timestamp in seconds.

**Signature**: `timestamp() → int`

**Example**:
```yaml
- function: timestamp()
  target: unix_timestamp
  type: int
```

---

## ID Generation Functions

### uuid()
Generates a UUID v4 (random unique identifier).

**Signature**: `uuid() → string`

**Example**:
```yaml
- function: uuid()
  target: request_id
  type: string
```

**Output**:
```
"550e8400-e29b-41d4-a716-446655440000"
```

### random(min, max)
Generates a random integer between min and max (inclusive).

**Signature**: `random(min: int, max: int) → int`

**Example**:
```yaml
- function: random(1, 100)
  target: random_number
```

### env(VAR_NAME)
Reads an environment variable.

**Signature**: `env(variable_name: string) → string`

**Example**:
```yaml
- function: env("TENANT_ID")
  target: tenant_id
```

---

## Lookup Functions

Lookup functions query external data sources and can be cached.

### lookup_postgres(query, parameters)
Executes a PostgreSQL query and returns results.

**Signature**: `lookup_postgres(query: string, params: array) → object`

**Features**:
- Connection pooling (default 25 connections)
- Automatic retry on connection failure
- Result caching with TTL
- Transaction support

**Example**:
```yaml
- function: lookup_postgres("SELECT * FROM customers WHERE id = $1", [customer.id])
  target: customer_data
  type: object
```

**Configuration**:
```bash
export POSTGRES_URL=postgres://user:pass@db:5432/dbname
export POSTGRES_MAX_CONNECTIONS=25
export POSTGRES_CONNECTION_TIMEOUT=5
```

**Caching**:
```bash
export CACHE_TTL=300  # Cache results for 5 minutes
```

### lookup_http(url, method, headers)
Makes an HTTP request to an external API.

**Signature**: `lookup_http(url: string, method: string, headers?: object) → object`

**Features**:
- HTTP/HTTPS support
- Custom headers
- Automatic retry on timeout
- Result caching
- Timeout configuration

**Example**:
```yaml
- function: lookup_http("https://api.example.com/customer/" + customer.id, "GET", {"Authorization": "Bearer token"})
  target: customer_api_data
  type: object
```

**Configuration**:
```bash
export REQUEST_TIMEOUT=30
export CACHE_TTL=300
```

**Supported Methods**: GET, POST, PUT, DELETE, PATCH

### lookup_composite(backends)
Chains multiple lookup backends with fallback strategy.

**Signature**: `lookup_composite(backends: array[{type, config}]) → object`

**Features**:
- Fallback to next backend on failure
- Automatic retry with exponential backoff
- Error recovery
- Configurable per-backend timeouts

**Example**:
```yaml
- function: lookup_composite([
    {type: "postgres", query: "SELECT * FROM cache WHERE id = $1"},
    {type: "http", url: "https://api.example.com/customer/$1"},
  ])
  target: customer_data
  type: object
```

**Backends**:
- `postgres` - PostgreSQL query
- `http` - HTTP API call
- `functions` - Mock/test backend

---

## Caching Functions

### cache_get(key)
Retrieves a value from the cache.

**Signature**: `cache_get(key: string) → any | null`

**Example**:
```yaml
- function: cache_get("customer_" + customer.id)
  target: cached_customer
  condition: cached_customer != null
```

### cache_set(key, value, ttl)
Stores a value in the cache with TTL.

**Signature**: `cache_set(key: string, value: any, ttl: int) → boolean`

**Example**:
```yaml
- function: cache_set("customer_" + customer.id, lookup_result, 300)
  target: cache_result
```

**Cache Configuration**:
```bash
export CACHE_TTL=300  # Default 5 minutes
```

---

## WASM Plugin Functions

Custom WebAssembly modules can be deployed as transformation functions.

### Deploying a WASM Plugin

1. **Write WASM module** (Rust/C/etc.):
```rust
#[no_mangle]
pub extern "C" fn transform(input_ptr: *const u8, input_len: usize) -> *const u8 {
    // Custom transformation logic
    // Return transformed data pointer
}
```

2. **Build to WASM**:
```bash
cargo build --target wasm32-wasi --release
```

3. **Deploy to Kubernetes**:
```bash
kubectl cp target/wasm32-wasi/release/my_function.wasm \
  pod:/app/wasm-modules/my_function.wasm -n vrsky-platform
```

4. **Use in transformation**:
```yaml
- function: wasm:my_function(customer.id)
  target: enriched_data
  type: object
```

### WASM Configuration

```bash
export WASM_MODULE_DIR=/app/wasm-modules
export WASM_MEMORY_PAGES=256      # 16MB per module
export WASM_SANDBOX_ENABLED=true
export WASM_TIMEOUT=10             # 10 second timeout
```

---

## Function Usage in Transformations

### Basic Usage

```yaml
transformations:
- source: customer.name
  function: upper()
  target: customer_name_upper
```

### With Conditions

```yaml
transformations:
- source: order.total
  function: multiply(1.1)  # Add 10% markup
  target: final_price
  condition: order.status == "pending"
```

### Chained Functions

```yaml
transformations:
- source: customer.email
  function: lower()
  function2: trim()
  target: email_normalized
```

### With Lookup

```yaml
transformations:
- function: lookup_postgres("SELECT * FROM customers WHERE id = $1", [customer.id])
  target: customer_details
  condition: customer.id != null
```

---

## Performance Tips

### 1. Cache Expensive Operations
```yaml
- function: lookup_http("https://api.expensive.com/data")
  target: data
  # Results cached for CACHE_TTL seconds
```

### 2. Use Conditions to Skip Unnecessary Lookups
```yaml
- function: lookup_postgres("SELECT * FROM big_table ...")
  target: data
  condition: always_need_this == true  # Only if really needed
```

### 3. Batch Lookup Requests
```yaml
# Instead of multiple lookups:
# Bad:
- function: lookup_postgres("SELECT * FROM users WHERE id = $1", [id1])
- function: lookup_postgres("SELECT * FROM users WHERE id = $1", [id2])
- function: lookup_postgres("SELECT * FROM users WHERE id = $1", [id3])

# Good:
- function: lookup_postgres("SELECT * FROM users WHERE id IN ($1, $2, $3)", [id1, id2, id3])
```

### 4. Use Connection Pooling
```bash
export POSTGRES_MAX_CONNECTIONS=50  # Pre-allocate connections
```

---

## Error Handling

Functions can fail and are handled per the error strategy:

- `ValidationError` - Schema validation failed
- `LookupError` - External lookup failed
- `TypeMismatchError` - Type conversion failed
- `TimeoutError` - Function exceeded timeout

See Configuration Reference for error handling strategies.

---

## Built-in Function Reference Table

| Function | Category | Returns | Notes |
|----------|----------|---------|-------|
| upper() | String | string | Convert to uppercase |
| lower() | String | string | Convert to lowercase |
| substring(start, end) | String | string | Extract substring |
| concat(...) | String | string | Join strings |
| split(delim) | String | array | Split by delimiter |
| join(delim) | String | string | Join array |
| contains(str) | String | boolean | Check containment |
| startswith(prefix) | String | boolean | Check prefix |
| endswith(suffix) | String | boolean | Check suffix |
| trim() | String | string | Remove whitespace |
| replace(search, repl) | String | string | Replace string |
| length() | String | int | Get length |
| toString() | Type Conversion | string | Convert to string |
| toNumber() | Type Conversion | number | Convert to number |
| toBoolean() | Type Conversion | boolean | Convert to boolean |
| parseJSON() | Type Conversion | object | Parse JSON string |
| stringifyJSON() | Type Conversion | string | Stringify object |
| add(n) | Numeric | number | Add numbers |
| subtract(n) | Numeric | number | Subtract numbers |
| multiply(n) | Numeric | number | Multiply numbers |
| divide(n) | Numeric | number | Divide numbers |
| round(decimals) | Numeric | number | Round number |
| floor() | Numeric | int | Round down |
| ceil() | Numeric | int | Round up |
| abs() | Numeric | number | Absolute value |
| now() | Date/Time | string | Current timestamp |
| today() | Date/Time | string | Today's date |
| timestamp() | Date/Time | int | Unix timestamp |
| uuid() | ID Generation | string | Generate UUID |
| random(min, max) | ID Generation | int | Random number |
| env(name) | ID Generation | string | Read environment |
| lookup_postgres(...) | Lookup | object | Query database |
| lookup_http(...) | Lookup | object | HTTP request |
| lookup_composite(...) | Lookup | object | Fallback chain |
| cache_get(key) | Caching | any | Get cached value |
| cache_set(key, val, ttl) | Caching | boolean | Cache value |
| wasm:* | WASM Plugin | any | Custom WASM |

---

## References

- Implementation Guide: `CONVERTER_IMPLEMENTATION_GUIDE.md`
- Configuration Reference: `CONVERTER_CONFIGURATION_REFERENCE.md`
- Kubernetes Deployment: `../infrastructure/kubernetes/converter/README.md`
