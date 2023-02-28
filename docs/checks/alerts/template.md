---
layout: default
parent: Checks
grand_parent: Documentation
---

# alerts/template

This check validates templates used in annotations and labels for alerting rules.
See [Prometheus docs](https://prometheus.io/docs/prometheus/latest/configuration/template_reference/)
for details of supported template syntax.

This check will also inspect all alert rules and warn if any of them
uses query return values inside alert labels.
Two alerts are identical if they have identical labels, so using
query value will generate a new unique alert every time it changes.
If alerting rule is using `for` it might prevent it from ever firing
if the value keeps changing before `for` is satisfied, because
Prometheus will consider it to be a new alert and start `for` tracking
from zero.

If you want to include query value in the alert then use annotations
for that. Annotations are not used to compare alerts identity and so
the value of any annotation can change between alert evaluations.

See [this blog post](https://www.robustperception.io/dont-put-the-value-in-alert-labels)
for more details.

## Configuration

This check doesn't have any configuration options.

## How to enable it

This check is enabled by default.

## How to disable it

You can disable this check globally by adding this config block:

```js
checks {
  disabled = ["alerts/template"]
}
```

You can also disable it for all rules inside given file by adding
a comment anywhere in that file. Example:

`# pint file/disable alerts/template`

Or you can disable it per rule by adding a comment to it. Example:

`# pint disable alerts/template`

## How to snooze it

You can disable this check until given time by adding a comment to it. Example:

`# pint snooze $TIMESTAMP alerts/template`

Where `$TIMESTAMP` is either use [RFC3339](https://www.rfc-editor.org/rfc/rfc3339)
formatted  or `YYYY-MM-DD`.
Adding this comment will disable `alerts/template` *until* `$TIMESTAMP`, after that
check will be re-enabled.
