We had an outage yesterday and I need a short incident summary from `app.log`.

How to find the outage: it is a run of at least 5 `ERROR` lines where each `ERROR` is no more than 60 seconds after the previous `ERROR`. There is exactly one such run in the log. Scattered single errors and smaller clusters are normal noise, so don't count them.

Write `incident.md` containing these four lines, exactly in this `key: value` format:

```text
start: <timestamp of the first ERROR in the outage, as written in the log>
end: <timestamp of the last ERROR in the outage, as written in the log>
errors: <number of ERROR lines in the outage>
top_service: <the service with the most ERROR lines in the outage>
```

You can add a short explanation below those lines if you like. Don't modify `app.log`.
