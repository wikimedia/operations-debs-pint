---
layout: default
parent: Checks
grand_parent: Documentation
---

# alerts/count

This check is used to estimate how many times given alert would fire.
It will run `expr` query from every alert rule against selected Prometheus
servers and report how many unique alerts it would generate.
If `for` is set on alerts it will be used to adjust results.

## Configuration

Syntax:

```js
alerts {
  range      = "1h"
  step       = "1m"
  resolve    = "5m"
}
```

- `range` - query range, how far to look back, `1h` would mean that pint will
  query last 1h of metrics.
  Defaults to `1d`.
- `step` - query resolution, for most accurate result use step equal
  to `scrape_interval`, try to reduce it if that would load too many samples.
  Defaults to `1m`.
- `resolve` - duration after which stale alerts are resolved. Defaults to `5m`.

## How to enable it

This check is not enabled by default as it requires explicit configuration
to work.
To enable it add one or more `prometheus {...}` blocks and a `rule {...}` block
with this checks config.

Example:

```js
prometheus "prod" {
  uri     = "https://prometheus-prod.example.com"
  timeout = "60s"
}

rule {
  alerts {
    range      = "1d"
    step       = "1m"
    resolve    = "5m"
  }
}
```

## How to disable it

You can disable this check globally by adding this config block:

```js
checks {
  disabled = ["alerts/count"]
}
```

You can also disable it for all rules inside given file by adding
a comment anywhere in that file. Example:

`# pint file/disable alerts/count`

Or you can disable it per rule by adding a comment to it. Example:

`# pint disable alerts/count`

If you want to disable only individual instances of this check
you can add a more specific comment.

`# pint disable alerts/count($prometheus)`

Where `$prometheus` is the name of Prometheus server to disable.

Example:

`# pint disable alerts/count(prod)`

## How to snooze it

You can disable this check until given time by adding a comment to it. Example:

`# pint snooze $TIMESTAMP alerts/count`

Where `$TIMESTAMP` is either use [RFC3339](https://www.rfc-editor.org/rfc/rfc3339)
formatted  or `YYYY-MM-DD`.
Adding this comment will disable `alerts/count` *until* `$TIMESTAMP`, after that
check will be re-enabled.
